//go:build run_computational

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func repoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// .../skills/run-sensor/scripts/run-computational_test.go → 3 levels up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas")
}

func writeSensor(t *testing.T, command string) string {
	t.Helper()
	sensor := map[string]interface{}{
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
			"command": command,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				map[string]interface{}{"exit_code": 1, "verdict": "fail", "severity": "high"},
			},
		},
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

func TestRun_HappyPath(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	path := writeSensor(t, "true")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, path}, &stdout, &stderr); code != 0 {
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

func TestRun_MissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

func TestRun_AcceptsAtPrefix(t *testing.T) {
	schemasDir := repoSchemasDir(t)
	full := writeSensor(t, "true")
	cwd, _ := os.Getwd()
	rel, err := filepath.Rel(cwd, full)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, "@" + rel}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
}
