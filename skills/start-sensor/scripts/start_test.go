//go:build start_sensor

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func writeFixtureSensor(t *testing.T, projectRoot, id string, body map[string]interface{}) string {
	t.Helper()
	dir := filepath.Join(projectRoot, "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body["id"] = id
	data, _ := json.MarshalIndent(body, "", "  ")
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStart_RejectsNonBlocking(t *testing.T) {
	root := t.TempDir()
	writeFixtureSensor(t, root, "not-blocking", map[string]interface{}{
		"version":     "1.0.0",
		"description": "non-blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"output":      "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50, "timeout_ms": 1000},
		},
		"execution": map[string]interface{}{
			"command": "echo hi",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
	})
	exit, _ := runStart(root, []string{"not-blocking"})
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
}

func TestStart_RejectsAlreadyRunning(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "loop", PID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{{Kind: "manual"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeFixtureSensor(t, root, "loop", blockingFixtureBody())
	exit, sig := runStart(root, []string{"loop"})
	if exit != 1 {
		t.Fatalf("expected exit 1, got %d", exit)
	}
	if sig["metadata"].(map[string]interface{})["kind"] != "start_rejected" {
		t.Fatalf("metadata.kind: got %v", sig["metadata"])
	}
}

func blockingFixtureBody() map[string]interface{} {
	return map[string]interface{}{
		"version":     "1.0.0",
		"description": "blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"output":      "stream",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"execution": map[string]interface{}{
			"command":  "while true; do echo TICK; sleep 0.1; done",
			"blocking": true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info", "rationale": "tick"},
				},
			},
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
		},
	}
}
