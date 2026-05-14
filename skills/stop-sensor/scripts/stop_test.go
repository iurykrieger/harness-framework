//go:build stop_sensor

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
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

// bootstrapFor wraps a registry.Result in a cli.BootstrapResult suitable for
// passing to runStop in tests. The schema validator is loaded so that signal
// validation runs; errors are written to errBuf.
func bootstrapFor(t *testing.T, res registry.Result, errBuf *bytes.Buffer) cli.BootstrapResult {
	t.Helper()
	v, _ := schema.LoadValidator("", errBuf)
	return cli.BootstrapResult{
		Res:       res,
		Validator: v,
		Diagnose:  registry.DiagnoseMetadata(res),
	}
}

func TestStop_RegistryFileAbsent_Error(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	var errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit, sig := runStop(b, []string{"missing"}, false)
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
	var errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit, sig := runStop(b, []string{"missing"}, false)
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
				SensorID: "live", RunID: "1-aa", Blocking: true,
				PID: registry.SelfPID(), PGID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{
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
	var errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit, sig := runStop(b, []string{"live"}, false)
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
				RunID:    "1-bb",
				Blocking: true,
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
	var errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	_, sig := runStop(b, []string{"live"}, true)
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

func TestStop_BlockingFalse_TerminatesRunnerSubprocess(t *testing.T) {
	proj := t.TempDir()
	// Spawn a real, long-running subprocess we'll target. Use Setpgid so
	// kill(-pgid, …) reaches only the subprocess group, not the test
	// binary's group.
	sub := exec.Command("sleep", "30")
	sub.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sub.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	defer func() { _ = sub.Process.Kill(); _, _ = sub.Process.Wait() }()

	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{{
		SensorID:  "alpha",
		RunID:     fmt.Sprintf("%d-runX", sub.Process.Pid),
		Blocking:  false,
		PID:       sub.Process.Pid,
		PGID:      sub.Process.Pid,
		StartedAt: "2026-05-11T00:00:00Z",
	}}}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	res := resultFor(t, proj, true)
	b := bootstrapFor(t, res, new(bytes.Buffer))
	exit, sig := runStop(b, []string{"alpha"}, false)
	if exit != 0 {
		t.Fatalf("exit=%d, sig=%+v", exit, sig)
	}
	// Subprocess should be reaped (Wait returns once it exits).
	done := make(chan struct{})
	go func() { _, _ = sub.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("subprocess pid=%d did not exit after stop", sub.Process.Pid)
	}
	// Entry should be removed.
	after, _ := registry.Load(r)
	if len(after.Entries) != 0 {
		t.Errorf("entry not removed: %+v", after.Entries)
	}
	if sig != nil {
		md, _ := sig["metadata"].(map[string]interface{})
		if md != nil && md["kind"] != "stopped" && md["kind"] != "aggregate" {
			t.Errorf("metadata.kind: got %v, want stopped or aggregate", md["kind"])
		}
	}
}

func TestStop_BlockingPreferred_WhenMixedActives(t *testing.T) {
	// Two entries for "alpha": one blocking:true (real PID), one blocking:false.
	// /stop-sensor alpha (no run-id) must target the blocking:true one.
	proj := t.TempDir()
	blockingSub := exec.Command("sleep", "30")
	blockingSub.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := blockingSub.Start(); err != nil {
		t.Skipf("spawn: %v", err)
	}
	defer func() { _ = blockingSub.Process.Kill(); _, _ = blockingSub.Process.Wait() }()
	nonBlockingSub := exec.Command("sleep", "30")
	nonBlockingSub.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := nonBlockingSub.Start(); err != nil {
		t.Skipf("spawn: %v", err)
	}
	defer func() { _ = nonBlockingSub.Process.Kill(); _, _ = nonBlockingSub.Process.Wait() }()

	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
		{
			SensorID: "alpha", RunID: "nb-1", Blocking: false,
			PID: nonBlockingSub.Process.Pid, PGID: nonBlockingSub.Process.Pid,
			StartedAt: "2026-05-11T00:00:00Z",
		},
		{
			SensorID: "alpha", RunID: "b-1", Blocking: true,
			PID: blockingSub.Process.Pid, PGID: blockingSub.Process.Pid,
			WatcherPID: 1, StartedAt: "2026-05-11T00:00:00Z",
		},
	}}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	res := resultFor(t, proj, true)
	b := bootstrapFor(t, res, new(bytes.Buffer))
	exit, _ := runStop(b, []string{"alpha"}, false)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	after, _ := registry.Load(r)
	var leftBlocking, leftNonBlocking int
	for _, e := range after.Entries {
		if e.Blocking {
			leftBlocking++
		} else {
			leftNonBlocking++
		}
	}
	if leftBlocking != 0 {
		t.Errorf("blocking entry not removed: %+v", after.Entries)
	}
	if leftNonBlocking != 1 {
		t.Errorf("non-blocking entry mistakenly removed: %+v", after.Entries)
	}
}

func TestStop_ReadsRunIDScopedSignalsLog(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)

	// Spawn a real, long-running subprocess so /stop-sensor has something
	// to SIGTERM. Recording stdin keeps go test from leaving zombies.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	runID := fmt.Sprintf("%d-deadbeef", cmd.Process.Pid)
	if err := os.MkdirAll(r.RunDir("svc-x", runID), 0o755); err != nil {
		t.Fatal(err)
	}
	// 3 individuals: 1 fail (severity high), 2 warn (severity low).
	body := strings.Join([]string{
		`{"sensor_id":"svc-x","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"fail","severity":"high","confidence":1.0,"evidence":[{"rationale":"e1"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
		`{"sensor_id":"svc-x","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"warn","severity":"low","confidence":1.0,"evidence":[{"rationale":"e2"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
		`{"sensor_id":"svc-x","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"warn","severity":"low","confidence":1.0,"evidence":[{"rationale":"e3"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(r.SignalsLogRun("svc-x", runID), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rs := registry.RunningSensors{
		Entries: []registry.RunningSensorEntry{{
			SensorID: "svc-x", RunID: runID, Blocking: true,
			PID:        cmd.Process.Pid,
			PGID:       cmd.Process.Pid,
			WatcherPID: 0,
			StartedAt:  "2026-05-14T00:00:00Z",
			Command:    "sleep 60",
			LogDir:     r.RelativeRunDir("svc-x", runID),
			HeldBy:     []registry.HeldByEntry{},
		}},
	}
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	sig := runStopAndDecode(t, root, "svc-x")
	md, _ := sig["metadata"].(map[string]interface{})
	counts, _ := md["counts"].(map[string]interface{})
	if counts["fail"] != float64(1) {
		t.Errorf("counts.fail: got %v, want 1", counts["fail"])
	}
	if counts["warn"] != float64(2) {
		t.Errorf("counts.warn: got %v, want 2", counts["warn"])
	}
	if v, _ := sig["verdict"].(string); v != "fail" {
		t.Errorf("verdict: got %q, want fail", v)
	}
}

// runStopAndDecode invokes the same code path that the CLI runs and
// decodes the aggregate signal as the CLI would (through JSON). This
// normalizes nested map types (map[string]int -> map[string]float64) so
// tests can use the same JSON-shape assertions used elsewhere.
func runStopAndDecode(t *testing.T, projectRoot, sensorID string) map[string]interface{} {
	t.Helper()
	t.Setenv("HARNESS_REGISTRY_ROOT", projectRoot)
	var stdout, stderr bytes.Buffer
	b := cli.Bootstrap("stop-sensor", &stdout, &stderr)
	if b.ExitCode != 0 {
		t.Fatalf("cli.Bootstrap exit=%d stderr=%s", b.ExitCode, stderr.String())
	}
	_, sig := runStop(b, []string{sensorID}, false)
	if sig == nil {
		t.Fatalf("runStop returned nil signal; stderr=%s", stderr.String())
	}
	raw, err := json.Marshal(sig)
	if err != nil {
		t.Fatalf("marshal signal: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal signal: %v (raw=%s)", err, raw)
	}
	return out
}

func TestStop_LegacyFallback(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)

	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	runID := fmt.Sprintf("%d-legacy", cmd.Process.Pid)
	if err := os.MkdirAll(r.SensorDir("svc-legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"sensor_id":"svc-legacy","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"pass","severity":"info","confidence":1.0,"evidence":[{"rationale":"e1"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
		`{"sensor_id":"svc-legacy","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"pass","severity":"info","confidence":1.0,"evidence":[{"rationale":"e2"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(r.LegacySignalsLog("svc-legacy"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rs := registry.RunningSensors{
		Entries: []registry.RunningSensorEntry{{
			SensorID: "svc-legacy", RunID: runID, Blocking: true,
			PID: cmd.Process.Pid, PGID: cmd.Process.Pid, WatcherPID: 0,
			StartedAt: "2026-05-14T00:00:00Z",
			Command:   "sleep 60",
			LogDir:    "",
			HeldBy:    []registry.HeldByEntry{},
		}},
	}
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	sig := runStopAndDecode(t, root, "svc-legacy")
	md, _ := sig["metadata"].(map[string]interface{})
	counts, _ := md["counts"].(map[string]interface{})
	if counts["pass"] != float64(2) {
		t.Errorf("counts.pass: got %v, want 2 (legacy fallback should have read 2 pass signals)", counts["pass"])
	}
}
