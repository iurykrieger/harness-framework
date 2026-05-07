//go:build run_inferential

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas")
}

func writeInferentialSensor(t *testing.T) string {
	t.Helper()
	sensor := map[string]interface{}{
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
			"model":                "anthropic/claude-sonnet-4-6",
			"system_prompt":        "Output JSON only.",
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
	dir := t.TempDir()
	path := filepath.Join(dir, "sensor.json")
	b, _ := json.Marshal(sensor)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mockAnthropic(t *testing.T, replyJSON string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"content": []map[string]interface{}{{"type": "text", "text": replyJSON}},
			"usage":   map[string]interface{}{"input_tokens": 100, "output_tokens": 50},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRun_HappyPath(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeInferentialSensor(t)
	srv := mockAnthropic(t, `{
		"verdict": "pass",
		"severity": "info",
		"confidence": 0.92,
		"evidence": [{"rationale": "ok"}]
	}`)

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	var stdout, stderr bytes.Buffer
	args := []string{
		"--schemas-dir", schemasDir,
		"--api-base", srv.URL,
		"--slot", "a=foo",
		"--slot", "b=bar",
		path,
	}
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &sig); err != nil {
		t.Fatalf("parse: %v\nstdout=%s", err, stdout.String())
	}
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict=%v", sig["verdict"])
	}
}

func TestRun_MissingAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	schemasDir := repoSchemasDir(t)
	path := writeInferentialSensor(t)
	var stdout, stderr bytes.Buffer
	args := []string{"--schemas-dir", schemasDir, "--slot", "a=x", "--slot", "b=y", path}
	if code := run(args, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 for missing API key, got %d", code)
	}
}

func TestRun_BadSlotFormat(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	schemasDir := repoSchemasDir(t)
	path := writeInferentialSensor(t)
	var stdout, stderr bytes.Buffer
	args := []string{"--schemas-dir", schemasDir, "--slot", "no-equals-sign", path}
	if code := run(args, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 for malformed slot, got %d", code)
	}
}

func TestRun_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}
