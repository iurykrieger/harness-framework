package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

// withFakeWatcher installs a watcher.SpawnFn for the duration of one test.
// The fake writes whichever Signal payload the test provides to signals.log
// (or none, for died_silently/timed_out paths) and spawns a real `sleep`
// subprocess as the watcher PID stand-in so SIGTERM on the watcher in
// stopBlockingDep/tearDownFailedSpawn targets a benign target.
func withFakeWatcher(t *testing.T, sigToEmit map[string]interface{}) {
	t.Helper()
	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		if sigToEmit != nil {
			// Stamp the run_id and sensor_id from the spawn opts so the
			// emitted signal matches what the watcher would have emitted.
			s := make(map[string]interface{}, len(sigToEmit))
			for k, v := range sigToEmit {
				s[k] = v
			}
			s["sensor_id"] = opts.SensorID
			s["run_id"] = opts.RunID
			f, err := os.OpenFile(opts.SignalsLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
			if err == nil {
				_ = json.NewEncoder(f).Encode(s)
				_ = f.Close()
			}
		}
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		pid := cmd.Process.Pid
		_ = cmd.Process.Release()
		return pid, nil
	}
	t.Cleanup(func() { watcher.SpawnFn = prev })
}

// writeFastDyingDep writes a blocking sensor whose command exits immediately
// (false). Used for the "died_silently" branch — no signal is ever emitted
// because the subprocess dies before the watcher can react.
func writeFastDyingDep(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
"id": "` + id + `",
"version": "1.0.0",
"name": "Fast-dying blocking dep",
"description": "exits 1 immediately",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"use_cases": ["fake-uc"],
"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":32},"latency":{"p50_ms":10,"p95_ms":50}},
"execution": {
  "command": "false",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^READY$","verdict":"pass","severity":"info"}]}
}
}`
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAttachLiveDep_HealthGateFailed exercises the path where the watcher
// emits an individual signal with verdict=fail during the health gate. The
// dep must cascade, the consumer must NOT run, and the registry must be
// clean afterwards.
func TestAttachLiveDep_HealthGateFailed(t *testing.T) {
	withFakeWatcher(t, map[string]interface{}{
		"version":     "0.0.0",
		"started_at":  "2026-05-14T00:00:00Z",
		"finished_at": "2026-05-14T00:00:00Z",
		"verdict":     "fail",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "watcher saw FATAL"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "individual"},
	})

	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumer(t, root, "uses-tick")

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "uses-tick", root, schemasDir, &out, &errBuf)
	if exit != 1 {
		t.Fatalf("exit=%d, want 1 (cascade); stderr=%s\nstdout=%s", exit, errBuf.String(), out.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	// Find the dep_start_failed line.
	var depFailed map[string]interface{}
	for _, line := range lines {
		var m map[string]interface{}
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		md, _ := m["metadata"].(map[string]interface{})
		if md != nil && md["kind"] == "dep_start_failed" {
			depFailed = m
			break
		}
	}
	if depFailed == nil {
		t.Fatalf("no dep_start_failed in stream:\n%s", out.String())
	}
	if v, _ := depFailed["verdict"].(string); v != "fail" {
		t.Errorf("dep_start_failed verdict = %q, want fail", v)
	}
	md, _ := depFailed["metadata"].(map[string]interface{})
	if md["cause"] != "watcher_reported_failure" {
		t.Errorf("metadata.cause = %v, want watcher_reported_failure", md["cause"])
	}
	if md["observed_signal"] == nil {
		t.Errorf("metadata.observed_signal missing")
	}

	// The last line must be the cascade signal for the consumer.
	var last map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last: %v", err)
	}
	if last["sensor_id"] != "uses-tick" {
		t.Errorf("last sensor_id = %v, want uses-tick", last["sensor_id"])
	}
	lastMd, _ := last["metadata"].(map[string]interface{})
	if lastMd["kind"] != "cascade" {
		t.Errorf("last metadata.kind = %v, want cascade", lastMd["kind"])
	}

	// Registry must be clean.
	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("blocking-tick") != nil {
		t.Error("blocking-tick registry entry not cleaned up after dep_start_failed")
	}
}

// TestAttachLiveDep_HealthGateDiedSilently exercises the path where the
// subprocess dies before the watcher emits any signal. The fake watcher
// writes nothing and spawns a sleep proc; the dep's command is `false`
// which exits immediately so IsPIDAlive(SubprocessPID) flips to false
// during health-gate polling.
func TestAttachLiveDep_HealthGateDiedSilently(t *testing.T) {
	withFakeWatcher(t, nil) // no signal emitted

	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeFastDyingDep(t, root, "fast-dying")
	// Consumer that depends on the fast-dying blocking dep.
	dir := filepath.Join(root, ".harness", "sensors")
	body := []byte(`{
"id": "uses-fast-dying", "version": "1.0.0",
"name":"x","description":"x","determinism":"high","kind":"assertion","type":"computational",
"output":"single","regulation":"behaviour","phase":"on-demand",
"triggers":[{"on":"manual"}],
"use_cases":["fake-uc"],
"requires":[{"kind":"sensor","id":"fast-dying"}],
"cost":{"class":"cheap","compute":{"cpu":"low","memory_mb":32},"latency":{"p50_ms":10,"p95_ms":50,"timeout_ms":2000}},
"execution":{"command":"echo never","exit_code_map":[{"exit_code":0,"verdict":"pass","severity":"info"}]}
}`)
	if err := os.WriteFile(filepath.Join(dir, "uses-fast-dying.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "uses-fast-dying", root, schemasDir, &out, &errBuf)
	if exit != 1 {
		t.Fatalf("exit=%d, want 1 (cascade); stderr=%s\nstdout=%s", exit, errBuf.String(), out.String())
	}

	// Find dep_start_failed with cause=subprocess_died_silently.
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	var depFailed map[string]interface{}
	for _, line := range lines {
		var m map[string]interface{}
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		md, _ := m["metadata"].(map[string]interface{})
		if md != nil && md["kind"] == "dep_start_failed" {
			depFailed = m
			break
		}
	}
	if depFailed == nil {
		t.Fatalf("no dep_start_failed in stream:\n%s", out.String())
	}
	md, _ := depFailed["metadata"].(map[string]interface{})
	if md["cause"] != "subprocess_died_silently" {
		t.Errorf("metadata.cause = %v, want subprocess_died_silently", md["cause"])
	}
	if v, _ := depFailed["verdict"].(string); v != "fail" {
		t.Errorf("dep_start_failed verdict = %q, want fail", v)
	}
}

// TestAttachLiveDep_HealthGateTimedOut exercises the optimistic-proceed
// path: subprocess stays alive, watcher emits no signal, timeout elapses.
// Health gate emits dep_started with metadata.health_gate="timed_out_proceeding"
// and the consumer runs normally.
func TestAttachLiveDep_HealthGateTimedOut(t *testing.T) {
	withFakeWatcher(t, nil)

	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumer(t, root, "uses-tick")

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "uses-tick", root, schemasDir, &out, &errBuf)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s\nstdout=%s", exit, errBuf.String(), out.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	var depStarted map[string]interface{}
	for _, line := range lines {
		var m map[string]interface{}
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		md, _ := m["metadata"].(map[string]interface{})
		if md != nil && md["kind"] == "dep_started" {
			depStarted = m
			break
		}
	}
	if depStarted == nil {
		t.Fatalf("no dep_started in stream:\n%s", out.String())
	}
	md, _ := depStarted["metadata"].(map[string]interface{})
	if md["health_gate"] != "timed_out_proceeding" {
		t.Errorf("metadata.health_gate = %v, want timed_out_proceeding", md["health_gate"])
	}
}

// TestStopBlockingDep_DiedBeforeStop exercises the path where the
// blocking dep starts healthy (fake watcher emits pass), survives the
// consumer's run, but then dies before detach is called. stopBlockingDep
// must observe IsPIDAlive == false and emit verdict=fail with
// metadata.subprocess_state="died_before_stop".
func TestStopBlockingDep_DiedBeforeStop(t *testing.T) {
	withFakeWatcher(t, map[string]interface{}{
		"version":     "0.0.0",
		"started_at":  "2026-05-14T00:00:00Z",
		"finished_at": "2026-05-14T00:00:00Z",
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "ready"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "individual"},
	})

	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")
	v := loadValidator(t)

	var attachOut, attachErr bytes.Buffer
	result, err := orchestrator.AttachLiveDep(context.Background(), dep, root, "test-holder", os.Getpid(), v, &attachOut, &attachErr)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	live := result.Live
	if live.ID == "" {
		t.Fatalf("attach failed: %s", attachOut.String())
	}

	// Kill the subprocess externally to simulate mid-run death. We need
	// the PID; read it from the registry.
	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if entry == nil {
		t.Fatalf("no registry entry post-attach")
	}
	if entry.PGID > 0 {
		// Kill the whole process group to ensure the sh -c and its
		// sleep child both go.
		_ = syscallKill(-entry.PGID, syscall.SIGTERM)
	}
	// Wait for the PID to actually die. Use watcher.IsSubprocessAlive
	// (zombie-aware) so we don't loop until timeout on the inevitable
	// zombie state after the subprocess exits but before init reaps it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !watcher.IsSubprocessAlive(entry.PID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if watcher.IsSubprocessAlive(entry.PID) {
		t.Fatalf("subprocess (pid=%d) did not die from external kill", entry.PID)
	}

	// Now detach. stopBlockingDep should observe the dead PID and emit
	// verdict=fail.
	var detachOut bytes.Buffer
	orchestrator.DetachLiveDep(live, root, "test-holder", v, &detachOut, io.Discard)

	// The last line emitted is the aggregate.
	lines := strings.Split(strings.TrimRight(detachOut.String(), "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("no detach output")
	}
	var agg map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &agg); err != nil {
		t.Fatalf("decode aggregate: %v", err)
	}
	if v, _ := agg["verdict"].(string); v != "fail" {
		t.Errorf("aggregate verdict = %q, want fail (full output:\n%s)", v, detachOut.String())
	}
	md, _ := agg["metadata"].(map[string]interface{})
	if md["subprocess_state"] != "died_before_stop" {
		t.Errorf("metadata.subprocess_state = %v, want died_before_stop", md["subprocess_state"])
	}
}
