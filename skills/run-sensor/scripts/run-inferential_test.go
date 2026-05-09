//go:build run_inferential

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
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas")
}

// writeInferentialSensor writes an inferential sensor fixture to
// <root>/sensors/<id>.json and returns the sensor id.
func writeInferentialSensor(t *testing.T, root, id, command string) string {
	t.Helper()
	s := map[string]interface{}{
		"id": id, "version": "0.1.0",
		"name": id, "description": "fixture",
		"kind": "assertion",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"output": "stream",
		"cost": map[string]interface{}{
			"class":   "expensive",
			"latency": map[string]interface{}{"p50_ms": 1000, "p95_ms": 5000, "timeout_ms": 30000},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 100, "output_avg": 50, "max_output": 256},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "pull-request"}},
		"execution": map[string]interface{}{
			"command":              command,
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "Output JSONL only.",
			"user_prompt_template": "Compare {{a}} to {{b}}.",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 256},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
					map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
				},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "f", "expected_verdict": "warn", "expected_severity": "medium"}},
		},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "tests/cal.jsonl",
			"calibration_size":     120,
			"calibration_date":     "2026-04-15",
		},
	}
	sensorsDir := filepath.Join(root, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	if err := os.WriteFile(filepath.Join(sensorsDir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
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

func TestRunInferential_Pass(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-pass", `printf 'PASS judgment-1\n'`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=foo()",
		"--slot", "b=bar()",
		id,
	}, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) < 2 {
		t.Fatalf("expected >=1 individual + aggregate, got %d", len(lines))
	}
	agg := lines[len(lines)-1]
	if agg["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v", agg["verdict"])
	}
	md := agg["metadata"].(map[string]interface{})
	if md["output_mode"] != "stream" {
		t.Fatalf("aggregate metadata.output_mode=%v want stream", md["output_mode"])
	}
}

func TestRunInferential_CalibrationDowngrade(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	root := t.TempDir()
	// One FAIL line + a HARNESS_AGGREGATE_CONFIDENCE=0.5 line on stdout.
	// Confidence 0.5 < threshold 0.7, so fail -> warn.
	cmd := `printf 'FAIL low-conf\nHARNESS_AGGREGATE_CONFIDENCE=0.5\n'`
	id := writeInferentialSensor(t, root, "infr-calibrate", cmd)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", "--slot", "b=y",
		id,
	}, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	if agg["verdict"] != "warn" {
		t.Fatalf("expected fail->warn downgrade, got %v", agg["verdict"])
	}
	md := agg["metadata"].(map[string]interface{})
	if md["calibration_downgrade"] != true {
		t.Fatalf("expected metadata.calibration_downgrade=true, got %v", md)
	}
	// The HARNESS_AGGREGATE_CONFIDENCE individual should NOT appear in counts.
	counts := md["counts"].(map[string]interface{})
	// Only the FAIL line should be counted: 1 fail.
	if counts["fail"].(float64) != 1 {
		t.Fatalf("counts.fail=%v want 1", counts["fail"])
	}
	if counts["pass"].(float64) != 0 {
		t.Fatalf("counts.pass=%v want 0 (HARNESS_AGGREGATE_CONFIDENCE should not be counted)", counts["pass"])
	}
}

func TestRunInferential_RejectsComputational(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	root := t.TempDir()
	sensorsDir := filepath.Join(root, "sensors")
	_ = os.MkdirAll(sensorsDir, 0o755)
	s := map[string]interface{}{
		"id": "wrong", "version": "0.1.0",
		"name": "x", "description": "x",
		"kind": "assertion",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
		"output": "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 1, "p95_ms": 1, "timeout_ms": 1000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 1},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": "true",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"}},
		},
	}
	b, _ := json.Marshal(s)
	_ = os.WriteFile(filepath.Join(sensorsDir, "wrong.json"), b, 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, "wrong"}, root, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2 (type mismatch), got %d", code)
	}
}

func TestRunInferential_HonoursExitCodeMap(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-ecmap", `printf 'PASS judgment\n'; exit 7`)
	// Patch the sensor on disk to add an exit_code_map that maps 7 -> warn/medium.
	sensorPath := filepath.Join(root, "sensors", id+".json")
	b, _ := os.ReadFile(sensorPath)
	var s map[string]interface{}
	_ = json.Unmarshal(b, &s)
	s["execution"].(map[string]interface{})["exit_code_map"] = []interface{}{
		map[string]interface{}{"exit_code": 7, "verdict": "warn", "severity": "medium"},
	}
	nb, _ := json.Marshal(s)
	_ = os.WriteFile(sensorPath, nb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", "--slot", "b=y",
		id,
	}, root, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	agg := lines[len(lines)-1]
	// Worst-of-two between exit warn (from exit_code_map) and stream pass: warn wins.
	if agg["verdict"] != "warn" {
		t.Fatalf("expected warn (from declared exit_code_map for code 7), got %v", agg["verdict"])
	}
	if agg["severity"] != "medium" {
		t.Fatalf("expected severity=medium, got %v", agg["severity"])
	}
}

func TestRunInferential_MissingRequiredEnvAborts(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-env", `printf "should not run\n"; exit 0`)
	sensorPath := filepath.Join(root, "sensors", id+".json")
	b, _ := os.ReadFile(sensorPath)
	var s map[string]interface{}
	_ = json.Unmarshal(b, &s)
	s["requires"] = map[string]interface{}{
		"env": []interface{}{
			map[string]interface{}{"name": "DETECT_SENSORS_INF_GHOST", "description": "intentionally unset"},
		},
	}
	nb, _ := json.Marshal(s)
	_ = os.WriteFile(sensorPath, nb, 0o644)

	prev := sensor.LookupEnvFn
	sensor.LookupEnvFn = func(name string) (string, bool) {
		if name == "DETECT_SENSORS_INF_GHOST" {
			return "", false
		}
		return os.LookupEnv(name)
	}
	t.Cleanup(func() { sensor.LookupEnvFn = prev })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, "--slot", "a=x", "--slot", "b=y", id}, root, &stdout, &stderr); code != 0 {
		t.Fatalf("expected exit 0 (Signal printed), got %d stderr=%s", code, stderr.String())
	}
	lines := parseJSONL(t, stdout.String())
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 aggregate, got %d", len(lines))
	}
	agg := lines[0]
	if agg["verdict"] != "error" || agg["severity"] != "high" {
		t.Fatalf("expected error/high, got %v/%v", agg["verdict"], agg["severity"])
	}
	rem, _ := agg["remediation"].(map[string]interface{})
	if rem == nil || !strings.Contains(rem["instructions"].(string), "DETECT_SENSORS_INF_GHOST") {
		t.Fatalf("remediation should name missing var: %+v", rem)
	}
}

func TestRunInferential_UnboundSlot(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	root := t.TempDir()
	id := writeInferentialSensor(t, root, "infr-slot", `true`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", // missing 'b'
		id,
	}, root, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for unbound slot, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "b") {
		t.Fatalf("stderr should name unbound slot 'b': %s", stderr.String())
	}
}

func TestRun_InferentialWithComputationalDep(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	sensorsDir := filepath.Join(root, "sensors")
	_ = os.MkdirAll(sensorsDir, 0o755)

	// Setup dep: a kind=setup computational sensor.
	depJSON := testfixtures.ValidSensorSetup()
	depJSON["id"] = "setup-x"
	depExec := depJSON["execution"].(map[string]interface{})
	depExec["command"] = "true"
	depBytes, _ := json.MarshalIndent(depJSON, "", "  ")
	if err := os.WriteFile(filepath.Join(sensorsDir, "setup-x.json"), depBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Inferential requested sensor that depends on the setup.
	infJSON := testfixtures.ValidSensorInferential()
	infJSON["id"] = "inf-with-dep"
	infJSON["depends_on"] = []interface{}{"setup-x"}
	infExec := infJSON["execution"].(map[string]interface{})
	infExec["user_prompt_template"] = "static prompt"
	infExec["command"] = `echo '{"sensor_id":"inf-with-dep","version":"0.1.0","run_id":"r","started_at":"2026-05-08T00:00:00Z","finished_at":"2026-05-08T00:00:01Z","verdict":"pass","severity":"info","confidence":0.9,"evidence":[],"cost_actual":{"latency_ms":100}}'`
	infBytes, _ := json.MarshalIndent(infJSON, "", "  ")
	if err := os.WriteFile(filepath.Join(sensorsDir, "inf-with-dep.json"), infBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, "inf-with-dep"}, root, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 Signals (dep + inf aggregate), got %d:\n%s", len(lines), out.String())
	}
}

func TestRunInferential_BlockingSensorRejected(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	root := t.TempDir()
	sensorsDir := filepath.Join(root, "sensors")
	_ = os.MkdirAll(sensorsDir, 0o755)

	// Blocking sensors: execution.blocking=true, no timeout_ms (schema forbids it), output=stream.
	s := map[string]interface{}{
		"id": "block-inf", "version": "0.1.0",
		"name": "block inf", "description": "fixture",
		"kind": "assertion",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"output": "stream",
		"cost": map[string]interface{}{
			"class": "expensive",
			// No timeout_ms — blocking sensors forbid it per schema.
			"latency": map[string]interface{}{"p50_ms": 1000, "p95_ms": 5000},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 100, "output_avg": 50, "max_output": 256},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command":              "sleep 999",
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "You are a judge.",
			"user_prompt_template": "Evaluate {{x}}.",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 256},
			"blocking":             true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
				},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "f", "expected_verdict": "pass", "expected_severity": "info"}},
		},
		"calibration": map[string]interface{}{
			"confidence_threshold": 0.7,
			"calibration_set":      "tests/cal.jsonl",
			"calibration_size":     10,
			"calibration_date":     "2026-04-15",
		},
	}
	b, _ := json.Marshal(s)
	_ = os.WriteFile(filepath.Join(sensorsDir, "block-inf.json"), b, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, "block-inf"}, root, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for blocking sensor, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "blocking") {
		t.Fatalf("stderr should mention 'blocking': %s", stderr.String())
	}
}
