//go:build stop_sensor

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// helperKey is the env var that puts the test binary into helper mode.
// When set, the binary becomes a "fake watcher" instead of running
// the test suite.
const helperKey = "HARNESS_STOP_TEST_HELPER"

const (
	helperRespectSIGTERM = "respect_sigterm"
	helperIgnoreSIGTERM  = "ignore_sigterm"
)

// TestMain dispatches into helper mode when the env var is set.
// Standard pattern for spawning the test binary as a fake subprocess.
func TestMain(m *testing.M) {
	switch os.Getenv(helperKey) {
	case helperRespectSIGTERM:
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		os.Exit(0)
	case helperIgnoreSIGTERM:
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func spawnHelper(t *testing.T, mode string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperKey+"="+mode)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	// Reap the helper as soon as it exits so IsPIDAlive (signal-0
	// probe) doesn't keep returning true for the zombie. Without this,
	// stopWatcher's poll loop times out even when SIGTERM killed the
	// helper instantly.
	waited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waited)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waited
	})
	// Give the helper a moment to install its signal handler.
	time.Sleep(150 * time.Millisecond)
	return cmd.Process.Pid
}

func resultFor(t *testing.T, projectRoot string, exists bool) registry.Result {
	t.Helper()
	r := registry.NewRoot(projectRoot)
	state, _, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return registry.Result{
		Root:        r,
		ProjectRoot: projectRoot,
		Source:      registry.SourceWalkUp,
		Exists:      exists,
		State:       state,
	}
}

func TestStop_RegistryFileAbsent_Error(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	exit, sig := runStop(res, []string{"missing"}, false)
	if exit != 1 {
		t.Fatalf("exit: got %d, want 1", exit)
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict: got %v, want \"error\"", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "stop_no_registry" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	if md["registry_exists"] != false {
		t.Errorf("metadata.registry_exists: got %v, want false", md["registry_exists"])
	}
}

func TestStop_NotRunning_ReturnsWarn(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	exit, sig := runStop(res, []string{"missing"}, false)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	if sig["verdict"] != "warn" || sig["metadata"].(map[string]interface{})["kind"] != "not_running" {
		t.Fatalf("got: %+v", sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v, want true", md["registry_exists"])
	}
}

func TestStop_HoldByDependent_RefusesStop(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID: "live", PID: registry.SelfPID(), PGID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{
					{Kind: "manual"},
					{Kind: "sensor", ID: "B", PID: registry.SelfPID()},
				},
			},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	exit, sig := runStop(res, []string{"live"}, false)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "held" {
		t.Fatalf("kind: got %v", md["kind"])
	}
	rs2, _ := registry.Load(r)
	if e := rs2.FindEntry("live"); e == nil {
		t.Fatal("registry entry should still exist")
	}
}

func TestStop_ReapsDeadHolders_WhenFlagSet(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID: "live",
				PID:      3_999_998,
				PGID:     3_999_998,
				HeldBy: []registry.HeldByEntry{
					{Kind: "manual"},
					{Kind: "sensor", ID: "C", PID: 3_999_999},
				},
			},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	_, sig := runStop(res, []string{"live"}, true)
	md := sig["metadata"].(map[string]interface{})
	reaped, _ := md["reaped_holders"].([]interface{})
	if len(reaped) != 1 {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
		_ = filepath.Join // keep import
		t.Fatalf("reaped: got %d", len(reaped))
	}
}

func TestStopWatcher_NormalSIGTERM(t *testing.T) {
	pid := spawnHelper(t, helperRespectSIGTERM)
	killedForcefully, latencyMS := stopWatcher(pid)
	if killedForcefully {
		t.Errorf("killedForcefully: got true, want false")
	}
	if latencyMS < 0 || latencyMS > 500 {
		t.Errorf("latencyMS: got %d, want in [0, 500]", latencyMS)
	}
	if registry.IsPIDAlive(pid) {
		t.Errorf("helper still alive after stopWatcher")
	}
}

func TestStopWatcher_RequiresSIGKILL(t *testing.T) {
	pid := spawnHelper(t, helperIgnoreSIGTERM)
	killedForcefully, latencyMS := stopWatcher(pid)
	if !killedForcefully {
		t.Errorf("killedForcefully: got false, want true")
	}
	if latencyMS < 950 || latencyMS > 1500 {
		t.Errorf("latencyMS: got %d, want in [950, 1500]", latencyMS)
	}
	// Give the parent's Wait goroutine a moment to reap the killed
	// helper so IsPIDAlive (signal-0 probe) stops returning true for
	// the zombie.
	deadline := time.Now().Add(500 * time.Millisecond)
	for registry.IsPIDAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if registry.IsPIDAlive(pid) {
		t.Errorf("helper still alive after stopWatcher")
	}
}

func TestStopWatcher_NonPositivePID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		killedForcefully, latencyMS := stopWatcher(pid)
		if killedForcefully || latencyMS != 0 {
			t.Errorf("pid=%d: got (%v, %d), want (false, 0)", pid, killedForcefully, latencyMS)
		}
	}
}
