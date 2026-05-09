//go:build list_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestList_Empty(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	exit := runList(root, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &sig); err != nil {
		t.Fatal(err)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "list" {
		t.Fatalf("kind: got %v", md["kind"])
	}
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 0 {
		t.Fatalf("entries: got %d, want 0", len(entries))
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
	var buf bytes.Buffer
	_ = runList(root, &buf, os.Stderr)
	var sig map[string]interface{}
	_ = json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &sig)
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
