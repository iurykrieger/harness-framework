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

func resultFor(t *testing.T, projectRoot string, exists bool) registry.Result {
	t.Helper()
	r := registry.NewRoot(projectRoot)
	state, _, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return registry.Result{
		Root:        r,
		ProjectRoot: projectRoot,
		Source:      registry.SourceWalkUp,
		Exists:      exists,
		State:       state,
	}
}

func TestStart_RejectsNonBlocking(t *testing.T) {
	root := t.TempDir()
	writeFixtureSensor(t, root, "not-blocking", map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Not blocking fixture",
		"description": "non-blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "on-demand",
		"output":      "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50, "timeout_ms": 1000},
		},
		"triggers": []interface{}{
			map[string]interface{}{"on": "manual"},
		},
		"execution": map[string]interface{}{
			"command": "echo hi",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{
					"fixture":           "sensors/fixtures/not-blocking/pass.txt",
					"expected_verdict":  "pass",
					"expected_severity": "info",
				},
			},
		},
	})
	res := resultFor(t, root, false)
	exit, _ := runStart(res, []string{"not-blocking"})
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
	res := resultFor(t, root, true)
	exit, sig := runStart(res, []string{"loop"})
	if exit != 1 {
		t.Fatalf("expected exit 1, got %d", exit)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "start_rejected" {
		t.Fatalf("metadata.kind: got %v", md["kind"])
	}
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v, want true", md["registry_exists"])
	}
	if md["registry_source"] != "walk_up" {
		t.Errorf("metadata.registry_source: got %v", md["registry_source"])
	}
	wantPath := filepath.Join(root, ".runtime", "sensors", "running_sensors.json")
	if md["registry_path"] != wantPath {
		t.Errorf("metadata.registry_path: got %v, want %v", md["registry_path"], wantPath)
	}
}

func blockingFixtureBody() map[string]interface{} {
	return map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking fixture",
		"description": "blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"output":      "stream",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"triggers": []interface{}{
			map[string]interface{}{"on": "manual"},
		},
		"execution": map[string]interface{}{
			"command":  "while true; do echo TICK; sleep 0.1; done",
			"blocking": true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{
					"fixture":           "sensors/fixtures/loop/pass.txt",
					"expected_verdict":  "pass",
					"expected_severity": "info",
				},
			},
		},
	}
}
