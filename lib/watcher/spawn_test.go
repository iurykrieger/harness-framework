package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSpawn_DelegatesToSpawnFn(t *testing.T) {
	called := false
	prev := SpawnFn
	SpawnFn = func(opts SpawnOpts) (int, error) {
		called = true
		if opts.SensorID != "marker" {
			t.Errorf("opts.SensorID = %q, want marker", opts.SensorID)
		}
		return 99999, nil
	}
	t.Cleanup(func() { SpawnFn = prev })

	pid, err := Spawn(SpawnOpts{SensorID: "marker", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !called {
		t.Error("SpawnFn was not invoked")
	}
	if pid != 99999 {
		t.Errorf("pid = %d, want 99999", pid)
	}
}

func TestSpawn_RejectsEmptyPluginRoot(t *testing.T) {
	// Force production code path (no SpawnFn override).
	pid, err := Spawn(SpawnOpts{
		ProjectRoot: t.TempDir(),
		SensorID:    "x",
		RunID:       "r1",
		// PluginRoot intentionally empty
		RawLogPath:     "/dev/null",
		SignalsLogPath: "/dev/null",
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
	})
	if err == nil {
		t.Fatalf("expected error for empty PluginRoot, got pid=%d", pid)
	}
	if !strings.Contains(err.Error(), "plugin root") {
		t.Errorf("err = %v, want one mentioning 'plugin root'", err)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
}

func TestRealSpawn_ArgShape(t *testing.T) {
	_, argsFile, _ := withFakeGo(t, 500, 0, "") // must survive the 100ms early-death probe

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tmp, "watcher.log")

	pid, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake/plugin/root",
		ProjectRoot:    tmp,
		SensorID:       "s",
		RunID:          "r",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: logPath,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if pid <= 0 {
		t.Errorf("pid = %d, want > 0", pid)
	}

	// Wait for the fake go to finish writing args.
	deadline := time.Now().Add(2 * time.Second)
	var args []byte
	for time.Now().Before(deadline) {
		args, _ = os.ReadFile(argsFile)
		if len(args) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got := strings.Split(strings.TrimRight(string(args), "\x1f"), "\x1f")
	want := []string{"-C", "/fake/plugin/root", "run", "-tags=start_watcher", "./skills/start-sensor/scripts"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %#v, want %#v", got, want)
	}
}

func TestRealSpawn_EnvPropagation(t *testing.T) {
	_, _, envFile := withFakeGo(t, 500, 0, "") // must survive the 100ms early-death probe

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644)

	_, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake/plugin/root",
		ProjectRoot:    tmp,
		SensorID:       "sensor-x",
		RunID:          "run-abc",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{"k":"v"}`),
		PatternsJSON:   []byte(`[{"p":1}]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: filepath.Join(tmp, "watcher.log"),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var env []byte
	for time.Now().Before(deadline) {
		env, _ = os.ReadFile(envFile)
		if len(env) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	required := []string{
		"HARNESS_WATCHER_RAW=" + filepath.Join(tmp, "raw.log"),
		"HARNESS_WATCHER_SIGNALS=" + filepath.Join(tmp, "sigs.log"),
		`HARNESS_WATCHER_PATTERNS=[{"p":1}]`,
		`HARNESS_WATCHER_ENVELOPE={"k":"v"}`,
		"HARNESS_WATCHER_SUBPROCESS_PID=" + fmt.Sprintf("%d", os.Getpid()),
		"HARNESS_WATCHER_REGISTRY_ROOT=" + tmp,
		"HARNESS_WATCHER_SENSOR_ID=sensor-x",
		"HARNESS_WATCHER_RUN_ID=run-abc",
		"GOWORK=off",
	}
	for _, want := range required {
		if !strings.Contains(string(env), want) {
			t.Errorf("env missing %q", want)
		}
	}
}

func TestRealSpawn_EarlyDeath(t *testing.T) {
	_, _, _ = withFakeGo(t, 10, 1, "mock compile error: bad syntax")

	// Extend probe window so the shell script (startup ~150ms + 10ms sleep)
	// is reliably caught on macOS even under load. Restore on cleanup.
	prev := earlyDeathTimeout
	earlyDeathTimeout = 3 * time.Second
	t.Cleanup(func() { earlyDeathTimeout = prev })

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644)

	pid, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake",
		ProjectRoot:    tmp,
		SensorID:       "s",
		RunID:          "r",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: filepath.Join(tmp, "watcher.log"),
	})
	if err == nil {
		t.Fatalf("expected error for early-death, got pid=%d", pid)
	}
	if !strings.Contains(err.Error(), "mock compile error") && !strings.Contains(err.Error(), "exited early") {
		t.Errorf("err = %v, want one mentioning 'exited early' or 'mock compile error'", err)
	}
}

func TestRealSpawn_GoMissing(t *testing.T) {
	// Empty PATH — exec.LookPath("go") will fail.
	t.Setenv("PATH", "")

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644)

	pid, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake",
		ProjectRoot:    tmp,
		SensorID:       "s",
		RunID:          "r",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: filepath.Join(tmp, "watcher.log"),
	})
	if err == nil {
		t.Fatalf("expected error when `go` is missing, got pid=%d", pid)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
}

func TestRealSpawn_SuccessPath(t *testing.T) {
	_, _, _ = withFakeGo(t, 500, 0, "") // sleep 500ms — survives 100ms early-death probe

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644)

	pid, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake",
		ProjectRoot:    tmp,
		SensorID:       "s",
		RunID:          "r",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: filepath.Join(tmp, "watcher.log"),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if pid <= 0 {
		t.Errorf("pid = %d, want > 0", pid)
	}

	// Kill the fake-go-as-watcher to avoid orphans.
	_ = syscall.Kill(pid, syscall.SIGTERM)
}
