//go:build write_sensor

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func writeDraft(t *testing.T, sensor map[string]interface{}) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "draft.json")
	b, err := json.Marshal(sensor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_ValidComputationalSensor(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	outDir := t.TempDir()
	draft := writeDraft(t, sensortest.LoadComputational(t).AsMap())

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	expected := filepath.Join(outDir, "smoke-comp.json")
	if !strings.Contains(stdout.String(), expected) {
		t.Fatalf("stdout=%q want path containing %q", stdout.String(), expected)
	}
	data, err := os.ReadFile(expected)
	if err != nil {
		t.Fatalf("written file missing: %v", err)
	}
	var s map[string]interface{}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("written JSON invalid: %v", err)
	}
	if s["id"] != "smoke-comp" {
		t.Fatalf("id=%v", s["id"])
	}
}

func TestRun_ValidInferentialSensor(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	outDir := t.TempDir()
	draft := writeDraft(t, sensortest.LoadInferential(t).AsMap())

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "smoke-inf.json")); err != nil {
		t.Fatalf("inferential sensor not written: %v", err)
	}
}

func TestRun_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", t.TempDir(), "--schemas-dir", testfixtures.RepoSchemasDir(t), path}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2 (parse error), got %d", code)
	}
}

func TestRun_SchemaViolation(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	bad := sensortest.LoadComputational(t).AsMap()
	delete(bad, "regulation") // required field
	draft := writeDraft(t, bad)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", t.TempDir(), "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected 1 (schema fail), got %d (stderr=%s)", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "regulation") {
		t.Fatalf("expected stderr to mention missing field; got %q", stderr.String())
	}
}

func TestRun_TypeMismatchedExecution(t *testing.T) {
	// Inferential sensor missing the required execution.model field — the
	// allOf discriminator must reject it.
	schemasDir := testfixtures.RepoSchemasDir(t)
	bad := sensortest.LoadInferential(t).AsMap()
	exec := bad["execution"].(map[string]interface{})
	delete(exec, "model")
	draft := writeDraft(t, bad)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", t.TempDir(), "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected 1 (schema fail), got %d", code)
	}
}

func TestRun_MissingOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"some.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2 when --out missing, got %d", code)
	}
}

func TestRun_NoPositional(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--out", t.TempDir()}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2 when draft path missing, got %d", code)
	}
}

func TestRun_ExtraPositional(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--out", t.TempDir(), "a.json", "b.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2 with extra positional, got %d", code)
	}
}

func TestRun_DraftFileMissing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", t.TempDir(), "--schemas-dir", testfixtures.RepoSchemasDir(t), "/nonexistent/x.json"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2 when draft missing, got %d", code)
	}
}

func TestRun_OutDirCreatedIfMissing(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	parent := t.TempDir()
	out := filepath.Join(parent, "nested", ".harness", "sensors")
	draft := writeDraft(t, sensortest.LoadComputational(t).AsMap())

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--out", out, "--schemas-dir", schemasDir, draft}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(out, "smoke-comp.json")); err != nil {
		t.Fatalf("expected nested out dir to be created: %v", err)
	}
}

func TestRun_OverwritesExistingFile(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	outDir := t.TempDir()
	// Pre-existing file at the target path with stale content.
	target := filepath.Join(outDir, "smoke-comp.json")
	if err := os.WriteFile(target, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	draft := writeDraft(t, sensortest.LoadComputational(t).AsMap())

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("STALE")) {
		t.Fatal("expected target to be overwritten with canonical sensor JSON")
	}
}
