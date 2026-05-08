//go:build run_computational

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// .../skills/run-sensor/scripts/run-computational_test.go → 3 levels up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas")
}

func writeSensor(t *testing.T, output string, exec map[string]interface{}) string {
	t.Helper()
	sensor := map[string]interface{}{
		"id": "comp-test", "version": "0.1.0",
		"name": "comp-test", "description": "fixture",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
		"output": output,
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 100, "timeout_ms": 5000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 64},
		},
		"triggers":  []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": exec,
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"}},
		},
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sensor.json")
	b, _ := json.Marshal(sensor)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func parseJSONL(t *testing.T, s string) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRunComputational_AllPass(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, "stream", map[string]interface{}{
		"command": `printf 'PASS a\nPASS b\n'`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": 1, "verdict": "fail", "severity": "high"},
		},
		"output_parsing": map[string]interface{}{
			"patterns": []interface{}{
				map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
				map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
			},
		},
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) != 3 {
		t.Fatalf("want 2 individuals + 1 aggregate, got %d", len(lines))
	}
	agg := lines[len(lines)-1]
	if agg["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v", agg["verdict"])
	}
	md := agg["metadata"].(map[string]interface{})
	if md["kind"] != "aggregate" {
		t.Fatalf("aggregate metadata.kind=%v", md["kind"])
	}
	if md["output_mode"] != "stream" {
		t.Fatalf("aggregate metadata.output_mode=%v want stream", md["output_mode"])
	}
}

func TestRunComputational_LogStyle_StreamFailEclipsesPassExit(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, "stream", map[string]interface{}{
		"command": `printf 'INFO ok\nERROR something broke\nINFO ok\n'; exit 0`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
		},
		"output_parsing": map[string]interface{}{
			"patterns": []interface{}{
				map[string]interface{}{"regex": "^INFO", "verdict": "pass", "severity": "info"},
				map[string]interface{}{"regex": "^ERROR", "verdict": "fail", "severity": "high"},
			},
		},
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "fail" {
		t.Fatalf("worst-of-two should pick stream fail; got %v", agg["verdict"])
	}
}

func TestRunComputational_FatalNoStream(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, "single", map[string]interface{}{
		"command": `false`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": 1, "verdict": "fail", "severity": "high"},
		},
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 aggregate, got %d", len(lines))
	}
	if lines[0]["verdict"] != "fail" {
		t.Fatalf("aggregate verdict=%v", lines[0]["verdict"])
	}
	md := lines[0]["metadata"].(map[string]interface{})
	if md["output_mode"] != "single" {
		t.Fatalf("aggregate metadata.output_mode=%v want single", md["output_mode"])
	}
}

func TestRunComputational_Timeout(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, "single", map[string]interface{}{
		"command": `sleep 10`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
		},
	})
	// Patch latency.timeout_ms to 200ms.
	b, _ := os.ReadFile(path)
	var s map[string]interface{}
	_ = json.Unmarshal(b, &s)
	s["cost"].(map[string]interface{})["latency"].(map[string]interface{})["timeout_ms"] = 200
	nb, _ := json.Marshal(s)
	_ = os.WriteFile(path, nb, 0o644)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "error" {
		t.Fatalf("expected timeout → error, got %v", agg["verdict"])
	}
	md := agg["metadata"].(map[string]interface{})
	if md["timed_out"] != true {
		t.Fatalf("expected timed_out=true, got %v", md["timed_out"])
	}
}

func TestRunComputational_RejectsInferential(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	// Hand-roll an inferential sensor.
	dir := t.TempDir()
	path := filepath.Join(dir, "sensor.json")
	sensor := map[string]interface{}{
		"id": "wrong-type", "version": "0.1.0",
		"name": "x", "description": "x",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"output": "single",
		"cost": map[string]interface{}{
			"class":   "expensive",
			"latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 1, "timeout_ms": 1},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 1, "output_avg": 1, "max_output": 1},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command":              "true",
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "x",
			"user_prompt_template": "x",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 1},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "f", "expected_verdict": "pass", "expected_severity": "info"}},
		},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "x", "calibration_size": 1,
			"calibration_date": "2026-04-15",
		},
	}
	b, _ := json.Marshal(sensor)
	_ = os.WriteFile(path, b, 0o644)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 (type mismatch), got %d", code)
	}
}

func TestRunComputational_MissingRequiredEnvAborts(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, "single", map[string]interface{}{
		"command": `printf "should not run\n"; exit 0`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
		},
	})
	// Patch the persisted sensor: add requires.env with a definitely-unset name.
	b, _ := os.ReadFile(path)
	var s map[string]interface{}
	_ = json.Unmarshal(b, &s)
	s["requires"] = map[string]interface{}{
		"env": []interface{}{
			map[string]interface{}{"name": "DETECT_SENSORS_TEST_GHOST_VAR", "description": "intentionally unset"},
		},
	}
	nb, _ := json.Marshal(s)
	_ = os.WriteFile(path, nb, 0o644)

	// Override the runtime LookupEnvFn so the test is hermetic regardless of
	// whatever happens to live in the host environment.
	prev := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(name string) (string, bool) {
		if name == "DETECT_SENSORS_TEST_GHOST_VAR" {
			return "", false
		}
		return os.LookupEnv(name)
	}
	t.Cleanup(func() { sensor.LookupEnvFn = prev })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0 (Signal printed), got %d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 aggregate, got %d", len(lines))
	}
	agg := lines[0]
	if agg["verdict"] != "error" {
		t.Fatalf("expected verdict=error, got %v", agg["verdict"])
	}
	if agg["severity"] != "high" {
		t.Fatalf("expected severity=high, got %v", agg["severity"])
	}
	rem, ok := agg["remediation"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected remediation, got %v", agg["remediation"])
	}
	instructions, _ := rem["instructions"].(string)
	if !strings.Contains(instructions, "DETECT_SENSORS_TEST_GHOST_VAR") {
		t.Fatalf("expected remediation to name the missing var, got %q", instructions)
	}
}

func TestRunComputational_RequiredEnvPresentRunsNormally(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, "single", map[string]interface{}{
		"command": `printf "ran with token=$DETECT_SENSORS_TEST_PRESENT\n"; exit 0`,
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
		},
	})
	b, _ := os.ReadFile(path)
	var s map[string]interface{}
	_ = json.Unmarshal(b, &s)
	s["requires"] = map[string]interface{}{
		"env": []interface{}{
			map[string]interface{}{"name": "DETECT_SENSORS_TEST_PRESENT"},
		},
	}
	nb, _ := json.Marshal(s)
	_ = os.WriteFile(path, nb, 0o644)

	t.Setenv("DETECT_SENSORS_TEST_PRESENT", "ok")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "pass" {
		t.Fatalf("expected pass when env is set, got %v", agg["verdict"])
	}
}

func TestRunComputational_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}
