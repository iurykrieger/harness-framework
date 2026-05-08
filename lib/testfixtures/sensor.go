// Package testfixtures provides shared test data and helpers for the
// harness library's subpackage tests. It is imported only by *_test.go
// files; production code MUST NOT depend on it.
package testfixtures

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ValidSensorComputational returns a minimal computational sensor that passes the schema.
func ValidSensorComputational() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-comp", "version": "0.1.0",
		"name": "smoke", "description": "fixture",
		"kind": "assertion",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
		"output": "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 100, "timeout_ms": 5000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 64},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": "true",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				map[string]interface{}{"exit_code": 1, "verdict": "fail", "severity": "high"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"}},
		},
	}
}

// ValidSensorInferential returns a minimal inferential sensor that passes the schema.
func ValidSensorInferential() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-inf", "version": "0.1.0",
		"name": "smoke inf", "description": "fixture",
		"kind": "assertion",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
		"output": "single",
		"cost": map[string]interface{}{
			"class":   "expensive",
			"latency": map[string]interface{}{"p50_ms": 6000, "p95_ms": 15000, "timeout_ms": 60000},
			"tokens":  map[string]interface{}{"model": "anthropic/claude-sonnet-4-6", "input_avg": 4000, "output_avg": 400, "max_output": 1024},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "pull-request"}},
		"execution": map[string]interface{}{
			"command":              "claude -p {{prompt}}",
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "You are a similarity judge. Output JSON only.",
			"user_prompt_template": "Compare {{a}} to {{b}}.",
			"decoding":             map[string]interface{}{"temperature": 0.0, "max_tokens": 1024},
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
}

// ValidSensorSetup returns a minimal setup sensor (kind=setup) that passes the schema.
func ValidSensorSetup() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-setup", "version": "0.1.0",
		"name": "smoke setup", "description": "fixture: idempotent setup",
		"kind": "setup",
		"type": "computational", "regulation": "behaviour",
		"phase": "on-demand", "determinism": "high",
		"output": "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 100, "timeout_ms": 5000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 64},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "agent-request"}},
		"execution": map[string]interface{}{
			"command": "true",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
	}
}

// WriteTempJSON writes v as JSON to a temp file and returns its path.
func WriteTempJSON(t *testing.T, v interface{}) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
