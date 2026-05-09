//go:build run_computational

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

// writeSensor writes a sensor fixture to <root>/sensors/<id>.json and returns
// the sensor id. Tests pass root as projectRoot so ResolveByID can find it.
func writeSensor(t *testing.T, root, id string, mut func(map[string]interface{})) string {
	t.Helper()
	s := testfixtures.ValidSensorComputational()
	s["id"] = id
	if mut != nil {
		mut(s)
	}
	sensorsDir := filepath.Join(root, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(sensorsDir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRun_NoDeps(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeSensor(t, root, "noop", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, id}, root, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 aggregate Signal, got %d:\n%s", len(lines), out.String())
	}
}

func TestRun_WithDep(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensor(t, root, "dep", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})
	mainID := writeSensor(t, root, "main", func(s map[string]interface{}) {
		s["depends_on"] = []interface{}{"dep"}
		s["execution"].(map[string]interface{})["command"] = "true"
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, mainID}, root, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 Signals (dep + main), got %d", len(lines))
	}
	var lastSig map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &lastSig); err != nil {
		t.Fatal(err)
	}
	if lastSig["sensor_id"] != "main" {
		t.Errorf("last sensor_id = %v, want main", lastSig["sensor_id"])
	}
}

func TestRun_UsageError(t *testing.T) {
	root := t.TempDir()
	var out, errBuf bytes.Buffer
	if code := run([]string{}, root, &out, &errBuf); code != 2 {
		t.Fatalf("expected 2 (no args), got %d", code)
	}
	if code := run([]string{"a", "b"}, root, &out, &errBuf); code != 2 {
		t.Fatalf("expected 2 (extra args), got %d", code)
	}
}

func TestRun_SensorNotFound(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "sensors"), 0o755)
	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", testfixtures.RepoSchemasDir(t), "nonexistent"}, root, &out, &errBuf)
	if code != 2 {
		t.Fatalf("expected 2 when sensor missing, got %d", code)
	}
}

func TestRun_BlockingSensorRejected(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	id := writeSensor(t, root, "block-me", func(s map[string]interface{}) {
		exec := s["execution"].(map[string]interface{})
		exec["command"] = "sleep 999"
		exec["blocking"] = true
		// Blocking sensors require output=stream and output_parsing.patterns.
		s["output"] = "stream"
		exec["output_parsing"] = map[string]interface{}{
			"patterns": []interface{}{
				map[string]interface{}{"regex": "^READY", "verdict": "pass", "severity": "info"},
			},
		}
		// Blocking sensors forbid cost.latency.timeout_ms per schema.
		latency := s["cost"].(map[string]interface{})["latency"].(map[string]interface{})
		delete(latency, "timeout_ms")
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, id}, root, &out, &errBuf)
	if code != 2 {
		t.Fatalf("expected exit 2 for blocking sensor, got %d stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(errBuf.String(), "blocking") {
		t.Fatalf("stderr should mention 'blocking': %s", errBuf.String())
	}
}
