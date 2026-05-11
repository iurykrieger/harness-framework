package orchestrator

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRuntimeDir_CreatesNestedPath(t *testing.T) {
	tmp := t.TempDir()
	rawLog, sigLog, err := prepareRuntimeDir(tmp, "my-sensor", "run-abc")
	if err != nil {
		t.Fatalf("prepareRuntimeDir: %v", err)
	}
	wantDir := filepath.Join(tmp, ".runtime", "sensors", "my-sensor", "run-abc")
	if filepath.Dir(rawLog) != wantDir {
		t.Errorf("raw log dir = %q, want %q", filepath.Dir(rawLog), wantDir)
	}
	if filepath.Base(rawLog) != "raw.log" {
		t.Errorf("raw log base = %q, want raw.log", filepath.Base(rawLog))
	}
	if filepath.Base(sigLog) != "signals.log" {
		t.Errorf("signals log base = %q", filepath.Base(sigLog))
	}
	if _, err := os.Stat(wantDir); err != nil {
		t.Errorf("dir not created: %v", err)
	}
}

func TestPrepareRuntimeDir_FailsOnNonexistentParent(t *testing.T) {
	_, _, err := prepareRuntimeDir("/dev/null/cannot-mkdir-under-this", "x", "r")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestEmitSignalWithPersistence_WritesBothSinks(t *testing.T) {
	tmp := t.TempDir()
	sig := map[string]interface{}{
		"sensor_id": "my-sensor",
		"run_id":    "run-xyz",
		"verdict":   "pass",
	}
	var stdout, stderr bytes.Buffer
	if err := emitSignalWithPersistence(sig, &stdout, tmp, "my-sensor", &stderr); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if stdout.Len() == 0 {
		t.Fatal("stdout empty")
	}
	sigLog := filepath.Join(tmp, ".runtime", "sensors", "my-sensor", "run-xyz", "signals.log")
	fileBytes, err := os.ReadFile(sigLog)
	if err != nil {
		t.Fatalf("read signals.log: %v", err)
	}
	if !bytes.Equal(stdout.Bytes(), fileBytes) {
		t.Errorf("stdout vs signals.log differ:\nstdout=%q\nfile=%q", stdout.String(), string(fileBytes))
	}
	var out map[string]interface{}
	if err := json.Unmarshal(bytes.TrimRight(fileBytes, "\n"), &out); err != nil {
		t.Errorf("signals.log not valid JSON: %v", err)
	}
	if out["run_id"] != "run-xyz" {
		t.Errorf("run_id round-trip mismatch")
	}
}

func TestEmitSignalWithPersistence_MissingRunID_UsesFallback(t *testing.T) {
	tmp := t.TempDir()
	sig := map[string]interface{}{
		"sensor_id": "my-sensor",
		"verdict":   "pass",
	}
	var stdout, stderr bytes.Buffer
	if err := emitSignalWithPersistence(sig, &stdout, tmp, "my-sensor", &stderr); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("missing run_id")) {
		t.Errorf("expected warning about missing run_id, stderr=%q", stderr.String())
	}
	parent := filepath.Join(tmp, ".runtime", "sensors", "my-sensor")
	entries, _ := os.ReadDir(parent)
	if len(entries) != 1 {
		t.Fatalf("expected one fallback dir, got %d entries", len(entries))
	}
	if !strings.HasPrefix(entries[0].Name(), "ts-") {
		t.Errorf("fallback dir name = %q, want ts-* prefix", entries[0].Name())
	}
}
