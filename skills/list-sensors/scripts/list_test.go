//go:build list_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// resultFor builds a registry.Result for a tempdir-backed project.
// exists controls whether running_sensors.json is on disk.
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

func TestList_FileAbsent_Warn(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	var buf bytes.Buffer
	exit := runList(res, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig); err != nil {
		t.Fatal(err)
	}
	if sig["verdict"] != "warn" {
		t.Errorf("verdict: got %v, want \"warn\"", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "list" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	if md["registry_exists"] != false {
		t.Errorf("metadata.registry_exists: got %v", md["registry_exists"])
	}
	if md["registry_source"] != "walk_up" {
		t.Errorf("metadata.registry_source: got %v", md["registry_source"])
	}
	wantPath := filepath.Join(root, ".runtime", "sensors", "running_sensors.json")
	if md["registry_path"] != wantPath {
		t.Errorf("metadata.registry_path: got %v, want %v", md["registry_path"], wantPath)
	}
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 0 {
		t.Errorf("entries: got %d, want 0", len(entries))
	}
	ev, _ := sig["evidence"].([]interface{})
	rationale := ev[0].(map[string]interface{})["rationale"].(string)
	if !strings.Contains(rationale, "registry not found") || !strings.Contains(rationale, "HARNESS_REGISTRY_ROOT") {
		t.Errorf("rationale should mention registry not found and HARNESS_REGISTRY_ROOT, got: %q", rationale)
	}
}

func TestList_FilePresentEmpty_Pass(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	var buf bytes.Buffer
	exit := runList(res, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	if sig["verdict"] != "pass" {
		t.Errorf("verdict: got %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v", md["registry_exists"])
	}
}

func TestList_AnnotatesOrphan(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "alive", PID: registry.SelfPID()},
			{SensorID: "dead", PID: 3_999_999},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	var buf bytes.Buffer
	_ = runList(res, &buf, os.Stderr)
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	entries := sig["metadata"].(map[string]interface{})["entries"].([]interface{})
	var alive, dead map[string]interface{}
	for _, e := range entries {
		em := e.(map[string]interface{})
		switch em["sensor_id"] {
		case "alive":
			alive = em
		case "dead":
			dead = em
		}
	}
	if alive["pid_alive"].(bool) != true {
		t.Errorf("alive pid_alive: got %v", alive["pid_alive"])
	}
	if dead["pid_alive"].(bool) != false {
		t.Errorf("dead pid_alive: got %v", dead["pid_alive"])
	}
	if dead["state"] != "orphan" {
		t.Errorf("dead state: got %v", dead["state"])
	}
}
