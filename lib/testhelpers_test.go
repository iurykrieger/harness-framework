package lib

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// repoSchemasDir returns the absolute path to schemas/ in the repo root,
// resolved from this test file's own location (independent of cwd).
func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../lib/testhelpers_test.go → 1 level up to repo root.
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), ".."))
	dir := filepath.Join(repoRoot, "schemas")
	if _, err := os.Stat(filepath.Join(dir, "sensor.json")); err != nil {
		t.Fatalf("schemas dir not where expected (%s): %v", dir, err)
	}
	return dir
}

// freezeClock pins NowFn and NewRunIDFn for deterministic Signal output.
// Returns a restore function; defer it.
func freezeClock(t *testing.T) func() {
	t.Helper()
	origNow, origID := NowFn, NewRunIDFn
	frozen := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	NowFn = func() time.Time { return frozen }
	NewRunIDFn = func() string { return "00000000-0000-4000-8000-000000000000" }
	return func() { NowFn, NewRunIDFn = origNow, origID }
}

// validSensorComputational returns a minimal sensor that passes the schema.
func validSensorComputational() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-comp", "version": "0.1.0",
		"name": "smoke", "description": "fixture",
		"type": "computational", "regulation": "maintainability",
		"phase": "on-demand", "determinism": "high",
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

// validSensorInferential returns a minimal inferential sensor that passes
// the pre-Task-3 schema (no `command`, since the inferential branch still
// forbids it). Task 3 adds `command` to both branches and to this fixture.
func validSensorInferential() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-inf", "version": "0.1.0",
		"name": "smoke inf", "description": "fixture",
		"type": "inferential", "regulation": "maintainability",
		"phase": "post-integration", "determinism": "low",
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

// writeTempJSON writes v as JSON to a temp file and returns its path.
func writeTempJSON(t *testing.T, v interface{}) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.json")
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
