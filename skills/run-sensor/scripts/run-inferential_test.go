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
)

func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas")
}

func writeInferentialSensor(t *testing.T, command string) string {
	t.Helper()
	sensor := map[string]interface{}{
		"id": "infr-test", "version": "0.1.0",
		"name": "infr-test", "description": "fixture",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
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

func TestRunInferential_Pass(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeInferentialSensor(t, `printf 'PASS judgment-1\n'`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=foo()",
		"--slot", "b=bar()",
		path,
	}, &stdout, &stderr)
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
}

func TestRunInferential_CalibrationDowngrade(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	// One FAIL line + a HARNESS_AGGREGATE_CONFIDENCE=0.5 line on stdout.
	// Confidence 0.5 < threshold 0.7, so fail -> warn.
	cmd := `printf 'FAIL low-conf\nHARNESS_AGGREGATE_CONFIDENCE=0.5\n'`
	path := writeInferentialSensor(t, cmd)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", "--slot", "b=y",
		path,
	}, &stdout, &stderr)
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
	dir := t.TempDir()
	path := filepath.Join(dir, "sensor.json")
	sensor := map[string]interface{}{
		"id": "wrong", "version": "0.1.0",
		"name": "x", "description": "x",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
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
	b, _ := json.Marshal(sensor)
	_ = os.WriteFile(path, b, 0o644)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2 (type mismatch), got %d", code)
	}
}

func TestRunInferential_UnboundSlot(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeInferentialSensor(t, `true`)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--schemas-dir", schemasDir,
		"--slot", "a=x", // missing 'b'
		path,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for unbound slot, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "b") {
		t.Fatalf("stderr should name unbound slot 'b': %s", stderr.String())
	}
}
