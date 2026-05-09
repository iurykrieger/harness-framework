//go:build tail_sensor

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

func setupRunning(t *testing.T, root, id string, signalsLines []string) {
	t.Helper()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorDir(id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: id, PID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{{Kind: "manual"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.SignalsLog(id), []byte(strings.Join(signalsLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Join // keep import
}

func TestTail_Cursor0_ReturnsAll(t *testing.T) {
	root := t.TempDir()
	setupRunning(t, root, "loop", []string{
		`{"sensor_id":"loop","verdict":"pass","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"warn","metadata":{"kind":"individual"}}`,
	})
	var buf bytes.Buffer
	exit := runTail(root, []string{"loop", "0"}, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: got %d (incl envelope)", len(lines))
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(lines[2]), &envelope); err != nil {
		t.Fatal(err)
	}
	md := envelope["metadata"].(map[string]interface{})
	if md["kind"] != "tail_envelope" {
		t.Fatalf("envelope kind: got %v", md["kind"])
	}
	if md["next_cursor"].(float64) != 2 {
		t.Fatalf("next_cursor: got %v", md["next_cursor"])
	}
}

func TestTail_CursorMid_ReturnsSuffix(t *testing.T) {
	root := t.TempDir()
	setupRunning(t, root, "loop", []string{
		`{"sensor_id":"loop","verdict":"pass","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"warn","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"fail","metadata":{"kind":"individual"}}`,
	})
	var buf bytes.Buffer
	exit := runTail(root, []string{"loop", "2"}, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	// 1 individual + 1 envelope
	if len(lines) != 2 {
		t.Fatalf("lines: got %d", len(lines))
	}
}

func TestTail_NotRunning(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	exit := runTail(root, []string{"missing", "0"}, &buf, os.Stderr)
	if exit != 1 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	_ = json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &sig)
	if sig["metadata"].(map[string]interface{})["kind"] != "tail_not_running" {
		t.Fatalf("kind: got %v", sig["metadata"])
	}
}
