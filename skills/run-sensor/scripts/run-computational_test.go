//go:build run_computational

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

// writeSensor writes a sensor fixture to <root>/.harness/sensors/<id>.json and returns
// the sensor id. Tests pass root as projectRoot so ResolveByID can find it.
func writeSensor(t *testing.T, root, id string, mut func(map[string]interface{})) string {
	t.Helper()
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = id
	if mut != nil {
		mut(s)
	}
	sensorsDir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(sensorsDir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRun_NoDeps(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeSensor(t, root, "noop", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, id}, root, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 aggregate Signal, got %d:\n%s", len(lines), out.String())
	}
}

func TestRun_WithDep(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensor(t, root, "dep", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})
	mainID := writeSensor(t, root, "main", func(s map[string]interface{}) {
		s["requires"] = []interface{}{
			map[string]interface{}{"kind": "sensor", "id": "dep"},
		}
		s["execution"].(map[string]interface{})["command"] = "true"
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, mainID}, root, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 Signals (dep + main), got %d", len(lines))
	}
	var lastSig map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &lastSig); err != nil {
		t.Fatal(err)
	}
	if lastSig["sensor_id"] != "main" {
		t.Errorf("last sensor_id = %v, want main", lastSig["sensor_id"])
	}
}

func TestRun_UsageError(t *testing.T) {
	root := t.TempDir()
	var out, errBuf bytes.Buffer
	if code := run([]string{}, root, &out, &errBuf); code != 2 {
		t.Fatalf("expected 2 (no args), got %d", code)
	}
	if code := run([]string{"a", "b"}, root, &out, &errBuf); code != 2 {
		t.Fatalf("expected 2 (extra args), got %d", code)
	}
}

func TestRun_SensorNotFound(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, ".harness", "sensors"), 0o755)
	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schematest.RepoSchemasDir(t), "nonexistent"}, root, &out, &errBuf)
	if code != 2 {
		t.Fatalf("expected 2 when sensor missing, got %d", code)
	}
}

// TestRunComputational_SIGTERMSetsTerminatedExternally spawns the runner
// as a subprocess (via `go run`), waits for it to enter its long-running
// command, and sends SIGTERM. The aggregate Signal on the LAST stdout
// line must carry metadata.terminated_externally=true.
//
// This test exercises the realistic SIGTERM path; the in-process unit
// tests already cover ctx-cancellation via the orchestrator. The
// 200ms sleep before SIGTERM is a known flake risk on slow CI.
func TestRunComputational_SIGTERMSetsTerminatedExternally(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".harness", "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Hand-rolled fixture: minimal valid computational sensor with a
	// long-running command so the runner is still in the command phase
	// when SIGTERM arrives.
	sensorJSON := map[string]interface{}{
		"id":          "sleeper",
		"version":     "0.1.0",
		"name":        "sleeper",
		"description": "fixture",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "maintainability",
		"phase":       "on-demand",
		"determinism": "high",
		"output":      "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 1, "timeout_ms": 60000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 1},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": "sleep 30",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "f", "expected_verdict": "pass", "expected_severity": "info"}},
		},
	}
	b, _ := json.Marshal(sensorJSON)
	if err := os.WriteFile(filepath.Join(proj, ".harness", "sensors", "sleeper.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	// Pre-build the runner binary so we can invoke it directly with
	// cwd=proj (the runner resolves the sensor against its cwd as
	// projectRoot). `go run` would force cwd to be the repo root,
	// defeating the resolution.
	bin := filepath.Join(t.TempDir(), "runner-comp")
	build := exec.Command("go", "build", "-tags=run_computational",
		"-o", bin, "./skills/run-sensor/scripts")
	build.Dir = repoRootForTest(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "--schemas-dir", schematest.RepoSchemasDir(t), "sleeper")
	cmd.Dir = proj
	cmd.Env = append(os.Environ(), "HARNESS_REGISTRY_ROOT="+proj)
	// Set a process group so we can clean up cleanly if the test bails.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Give the runner time to spawn its subprocess and register the
	// signal handler before delivering SIGTERM.
	time.Sleep(500 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	out, _ := io.ReadAll(stdout)
	_ = cmd.Wait()

	trimmed := strings.TrimRight(string(out), "\n")
	if trimmed == "" {
		t.Fatalf("no stdout (subprocess may have been killed before emitting aggregate)")
	}
	lines := strings.Split(trimmed, "\n")
	last := lines[len(lines)-1]
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(last), &sig); err != nil {
		t.Fatalf("parse last line: %v\n%q", err, last)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if md == nil {
		t.Fatalf("aggregate Signal has no metadata: %v", sig)
	}
	if v, _ := md["terminated_externally"].(bool); !v {
		t.Errorf("expected metadata.terminated_externally=true; got metadata=%v", md)
	}
}

func TestRun_BlockingSensorRejected(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeSensor(t, root, "block-me", func(s map[string]interface{}) {
		exec := s["execution"].(map[string]interface{})
		exec["command"] = "sleep 999"
		exec["blocking"] = true
		// Blocking sensors require output=stream and output_parsing.patterns.
		s["output"] = "stream"
		exec["output_parsing"] = map[string]interface{}{
			"patterns": []interface{}{
				map[string]interface{}{"regex": "^READY", "verdict": "pass", "severity": "info"},
			},
		}
		// Blocking sensors forbid cost.latency.timeout_ms per schema.
		latency := s["cost"].(map[string]interface{})["latency"].(map[string]interface{})
		delete(latency, "timeout_ms")
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, id}, root, &out, &errBuf)
	if code != 2 {
		t.Fatalf("expected exit 2 for blocking sensor, got %d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "blocking") {
		t.Fatalf("stderr should mention 'blocking': %s", errBuf.String())
	}
}

// TestRunComputational_AcceptsAbsolutePath verifies that run-computational
// accepts an absolute file path in addition to a bare sensor id.
//
// /heal-sensor's retry-original.go passes absolute paths to the runner.
// sensor.Resolve detects path-shaped inputs via looksLikePath and routes
// through resolvePath instead of the bare-id regex, so this test guards
// against regressions to id-only resolution.
func TestRunComputational_AcceptsAbsolutePath(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeSensor(t, root, "abs-path-sensor", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})
	absPath := filepath.Join(root, ".harness", "sensors", id+".json")

	if !filepath.IsAbs(absPath) {
		t.Fatalf("expected absolute path, got %q", absPath)
	}

	var out, errBuf bytes.Buffer
	// Pass the absolute path. The runner re-derives projectRoot from the
	// sensor's canonical location (.../<projectRoot>/.harness/sensors/<id>.json),
	// so projectRoot passed in here is overridden inside run().
	code := run([]string{"--schemas-dir", schemasDir, absPath}, "" /* projectRoot overridden */, &out, &errBuf)

	// Sentinel for a regression to id-only resolution: exit 2 + "does not match".
	if code == 2 && strings.Contains(errBuf.String(), "does not match") {
		t.Fatalf("runner rejected absolute path via id regex: stderr=%s", errBuf.String())
	}
	if code != 0 {
		t.Fatalf("expected exit 0 for absolute path, got %d stderr=%s", code, errBuf.String())
	}
}
