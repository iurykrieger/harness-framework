//go:build integration

// test/integration_runtime_logs_test.go
//
// End-to-end integration test: two concurrent /run-sensor invocations
// must coexist with distinct run dirs and distinct run_ids (DoD item #6).
package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
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

	sensorsDir := filepath.Join(proj, "sensors")
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
