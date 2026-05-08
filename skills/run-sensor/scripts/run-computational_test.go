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

func writeSensor(t *testing.T, dir, id string, mut func(map[string]interface{})) string {
	t.Helper()
	s := testfixtures.ValidSensorComputational()
	s["id"] = id
	if mut != nil {
		mut(s)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_NoDeps(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	dir := t.TempDir()
	path := writeSensor(t, dir, "noop", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, path}, &out, &errBuf)
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
	dir := t.TempDir()
	writeSensor(t, dir, "dep", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})
	mainPath := writeSensor(t, dir, "main", func(s map[string]interface{}) {
		s["depends_on"] = []interface{}{"dep"}
		s["execution"].(map[string]interface{})["command"] = "true"
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, mainPath}, &out, &errBuf)
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
	var out, errBuf bytes.Buffer
	if code := run([]string{}, &out, &errBuf); code != 2 {
		t.Fatalf("expected 2 (no args), got %d", code)
	}
	if code := run([]string{"a", "b"}, &out, &errBuf); code != 2 {
		t.Fatalf("expected 2 (extra args), got %d", code)
	}
}

func TestRun_DraftFileMissing(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", testfixtures.RepoSchemasDir(t), "/nonexistent/x.json"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("expected 2 when sensor missing, got %d", code)
	}
}
