//go:build integration

// End-to-end integration test: two concurrent /run-sensor invocations
// must coexist with distinct run dirs and distinct run_ids (DoD item #6).
package orchestrator_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

func TestRunSensor_ConcurrentRunsCoexist(t *testing.T) {
	repoDir := repoRoot(t)

	// Place proj under <repoRoot>/.test-tmp/ so the schema walk-up
	// (which climbs from cwd looking for schemas/) can reach
	// <repoRoot>/schemas/. t.TempDir() lands outside the repo tree, so
	// the schema discovery would fail.
	scratch := filepath.Join(repoDir, ".test-tmp")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	proj, err := os.MkdirTemp(scratch, "runtime-logs-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(proj) })

	sensorsDir := filepath.Join(proj, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a minimal valid computational sensor that sleeps briefly so
	// both concurrent runs are alive at the same time (distinct PIDs →
	// distinct run_ids). timeout_ms is required for non-blocking sensors.
	sensorJSON := `{
  "id": "echo",
  "version": "1.0.0",
  "name": "echo fixture",
  "description": "echo fixture for integration test",
  "determinism": "high",
  "kind": "observation",
  "type": "computational",
  "regulation": "behaviour",
  "phase": "on-demand",
  "output": "single",
  "cost": {
    "class": "cheap",
    "compute": {"cpu": "low", "memory_mb": 32},
    "latency": {"p50_ms": 10, "p95_ms": 50, "timeout_ms": 30000}
  },
  "triggers": [{"on": "manual"}],
  "execution": {
    "command": "sleep 1 && echo ok",
    "exit_code_map": [{"exit_code": 0, "verdict": "pass", "severity": "info"}]
  },
  "verification": {
    "golden_cases": [{
      "fixture": "sensors/fixtures/echo/pass.txt",
      "expected_verdict": "pass",
      "expected_severity": "info"
    }]
  }
}`
	if err := os.WriteFile(filepath.Join(sensorsDir, "echo.json"), []byte(sensorJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build the run-computational binary once. Direct exec avoids the
	// cwd ambiguity that go run introduces.
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "run-computational")
	buildCmd := exec.Command("go", "build", "-tags=run_computational", "-o", binPath, "./skills/run-sensor/scripts")
	buildCmd.Dir = repoDir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	runOnce := func() string {
		cmd := exec.Command(binPath, "echo")
		cmd.Dir = proj // runner resolves sensor relative to cwd; registry root = proj
		cmd.Env = append(os.Environ(), "HARNESS_REGISTRY_ROOT="+proj)
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.Errorf("run: exit %d\nstdout: %s\nstderr: %s", ee.ExitCode(), out, ee.Stderr)
			} else {
				t.Errorf("run: %v\nstdout: %s", err, out)
			}
			return ""
		}
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		return lines[len(lines)-1]
	}

	var wg sync.WaitGroup
	var lastA, lastB string
	wg.Add(2)
	go func() { defer wg.Done(); lastA = runOnce() }()
	go func() { defer wg.Done(); lastB = runOnce() }()
	wg.Wait()

	if lastA == "" || lastB == "" {
		t.Fatal("one or both runs failed; see prior errors")
	}

	var sa, sb map[string]interface{}
	if err := json.Unmarshal([]byte(lastA), &sa); err != nil {
		t.Fatalf("parse last line A: %v\n%q", err, lastA)
	}
	if err := json.Unmarshal([]byte(lastB), &sb); err != nil {
		t.Fatalf("parse last line B: %v\n%q", err, lastB)
	}

	ra, _ := sa["run_id"].(string)
	rb, _ := sb["run_id"].(string)
	if ra == "" || rb == "" {
		t.Fatalf("run_id must be non-empty: A=%q B=%q", ra, rb)
	}
	if ra == rb {
		t.Fatalf("run_ids must be distinct: both are %q", ra)
	}

	// Both <run-id>/signals.log files must exist on disk.
	r := registry.NewRoot(proj)
	if _, err := os.Stat(r.SignalsLogRun("echo", ra)); err != nil {
		t.Errorf("signals.log missing for run %s: %v", ra, err)
	}
	if _, err := os.Stat(r.SignalsLogRun("echo", rb)); err != nil {
		t.Errorf("signals.log missing for run %s: %v", rb, err)
	}

	// Registry must be empty after both runs complete (cleaned up by defer).
	rs, loadErr := registry.Load(r)
	if loadErr != nil {
		t.Fatalf("load registry: %v", loadErr)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("registry not cleaned up after runs: %+v", rs.Entries)
	}
}

// TestRunWithDepsRoot_DepSignalsPopulated drives RunWithDepsRoot against a
// root sensor whose blocking dep declares an output_parsing pattern. After
// the orchestrator runs to completion, the dep's run-id-scoped signals.log
// must contain a parsed individual matching the pattern, and the flat path
// (.harness/runtime/<id>/raw.log) must not exist. This is the end-to-end
// regression that would have caught issue #45.
func TestRunWithDepsRoot_DepSignalsPopulated(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()

	// Blocking dep with one matching pattern. Command echoes BOOM continuously
	// so the watcher (which has a non-trivial compile-then-attach latency on
	// cold cache) always has a line to observe after fsnotify registration.
	depBody := []byte(`{
"id": "blocking-boom",
"version": "1.0.0",
"name": "BOOM emitter",
"description": "emits BOOM continuously",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":32},"latency":{"p50_ms":10,"p95_ms":50}},
"execution": {
  "command": "while true; do echo BOOM; sleep 0.1; done",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^BOOM$","verdict":"fail","severity":"high"}]}
}
}`)
	consumerBody := []byte(`{
"id": "uses-boom",
"version": "1.0.0",
"name": "Uses boom",
"description": "consumer",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"blocking-boom"}],
"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":32},"latency":{"p50_ms":10,"p95_ms":50,"timeout_ms":15000}},
"execution": {
  "command": "sleep 2 && echo OK",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)

	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blocking-boom.json"), depBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uses-boom.json"), consumerBody, 0o644); err != nil {
		t.Fatal(err)
	}

	// Opt into the real watcher spawner (orchestrator package's TestMain
	// overrides watcher.SpawnFn with a fake by default).
	prev := watcher.SpawnFn
	watcher.SpawnFn = watcher.RealSpawn
	t.Cleanup(func() { watcher.SpawnFn = prev })

	r := registry.NewRoot(root)

	exit := orchestrator.RunWithDepsRoot(context.Background(), "uses-boom", root, schemasDir, io.Discard, io.Discard)
	if exit != 0 {
		t.Fatalf("RunWithDepsRoot exit=%d", exit)
	}

	// After teardown, the per-run directory still exists with raw.log and
	// signals.log preserved (registry entry is gone, files are not removed).
	depBase := r.SensorDir("blocking-boom")
	subs, err := os.ReadDir(depBase)
	if err != nil {
		t.Fatalf("read dep dir: %v", err)
	}
	var runDir string
	for _, e := range subs {
		if e.IsDir() {
			runDir = filepath.Join(depBase, e.Name())
			break
		}
	}
	if runDir == "" {
		t.Fatalf("no run-id-scoped run dir found under %s; flat layout suspected", depBase)
	}

	sigsData, err := os.ReadFile(filepath.Join(runDir, "signals.log"))
	if err != nil {
		t.Fatalf("read signals.log: %v", err)
	}
	if len(sigsData) == 0 {
		t.Fatalf("signals.log empty; pattern did not fire")
	}
	lines := strings.Split(strings.TrimSpace(string(sigsData)), "\n")
	matched := false
	for _, line := range lines {
		var sig map[string]interface{}
		if err := json.Unmarshal([]byte(line), &sig); err != nil {
			continue
		}
		if v, _ := sig["verdict"].(string); v == "fail" {
			ev, _ := sig["evidence"].([]interface{})
			if len(ev) > 0 {
				first, _ := ev[0].(map[string]interface{})
				// No `rationale` capture in the pattern, so the rationale
				// falls back to the matched line itself ("BOOM").
				if r, _ := first["rationale"].(string); r == "BOOM" {
					matched = true
					break
				}
			}
		}
	}
	if !matched {
		t.Errorf("expected a fail individual with rationale=\"BOOM\"; got: %s", string(sigsData))
	}

	// Flat path must not exist.
	if _, err := os.Stat(r.LegacyRawLog("blocking-boom")); err == nil {
		t.Error("flat raw.log exists at .harness/runtime/blocking-boom/raw.log; expected only run-id-scoped")
	}
}

// repoRoot walks up from the test's cwd until go.mod is found.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("repo root (go.mod) not found from %s", wd)
	return ""
}
