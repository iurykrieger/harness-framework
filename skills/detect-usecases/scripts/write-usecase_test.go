//go:build write_usecase

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/usecase/usecasetest"
)

func writeDraftAt(t *testing.T, dir string, doc []byte) string {
	t.Helper()
	p := filepath.Join(dir, "draft.json")
	if err := os.WriteFile(p, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeStackJSON(t *testing.T, projectRoot string) {
	t.Helper()
	harness := filepath.Join(projectRoot, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
  "version": "0.2.0",
  "detected_at": "2026-05-14T10:00:00Z",
  "detected_by": "manual",
  "languages": [{"name":"go"}],
  "components": [{"role":"http-server","name":"net/http","evidence":[{"file":"x.go","rationale":"x"}]}],
  "log_shapes": [{"id":"x","produced_by":["net/http"],"format":"plain","sample":"x"}],
  "archetypes": ["http-api"],
  "journeys": [{
    "id": "user-registration",
    "name": "x",
    "summary": "x",
    "archetype": "http-api",
    "entry_points": [{
      "kind": "http-route",
      "method": "POST",
      "path": "/users",
      "evidence": {"file":"x.go","rationale":"x"}
    }]
  }]
}`)
	if err := os.WriteFile(filepath.Join(harness, "stack.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeEvidenceFile(t *testing.T, projectRoot string) {
	t.Helper()
	target := filepath.Join(projectRoot, "src", "users", "users.controller.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun_Happy(t *testing.T) {
	projectRoot := t.TempDir()
	writeStackJSON(t, projectRoot)
	writeEvidenceFile(t, projectRoot)
	schemasDir := schematest.RepoSchemasDir(t)
	out := filepath.Join(projectRoot, ".harness", "usecases")
	draft := writeDraftAt(t, t.TempDir(), usecasetest.CanonicalBody(t))

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out", out,
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
		draft,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	expected := filepath.Join(out, "create-user-with-email.json")
	if !strings.Contains(stdout.String(), "create-user-with-email.json") {
		t.Fatalf("stdout %q missing expected filename", stdout.String())
	}
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

func TestRun_StackMissing(t *testing.T) {
	projectRoot := t.TempDir() // NO .harness/stack.json
	schemasDir := schematest.RepoSchemasDir(t)
	out := filepath.Join(projectRoot, ".harness", "usecases")
	draft := writeDraftAt(t, t.TempDir(), usecasetest.CanonicalBody(t))

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out", out,
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
		draft,
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d want 2 (setup error)", code)
	}
	if !strings.Contains(stderr.String(), "stack") {
		t.Errorf("stderr %q should mention stack", stderr.String())
	}
}

func TestRun_SchemaViolation(t *testing.T) {
	projectRoot := t.TempDir()
	writeStackJSON(t, projectRoot)
	writeEvidenceFile(t, projectRoot)
	schemasDir := schematest.RepoSchemasDir(t)
	out := filepath.Join(projectRoot, ".harness", "usecases")

	var doc map[string]interface{}
	if err := json.Unmarshal(usecasetest.CanonicalBody(t), &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "journey_id")
	bad, _ := json.Marshal(doc)
	draft := writeDraftAt(t, t.TempDir(), bad)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out", out,
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
		draft,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1 (schema fail)", code)
	}
}

func TestRun_JourneyOrphan(t *testing.T) {
	projectRoot := t.TempDir()
	writeStackJSON(t, projectRoot) // declares journey "user-registration"
	writeEvidenceFile(t, projectRoot)
	schemasDir := schematest.RepoSchemasDir(t)
	out := filepath.Join(projectRoot, ".harness", "usecases")

	var doc map[string]interface{}
	json.Unmarshal(usecasetest.CanonicalBody(t), &doc)
	doc["journey_id"] = "ghost"
	bad, _ := json.Marshal(doc)
	draft := writeDraftAt(t, t.TempDir(), bad)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out", out,
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
		draft,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1 (cross-check fail)", code)
	}
	if !strings.Contains(stderr.String(), "ghost") {
		t.Errorf("stderr %q must name the bad journey", stderr.String())
	}
}

func TestRun_EvidenceMissing(t *testing.T) {
	projectRoot := t.TempDir()
	writeStackJSON(t, projectRoot)
	// do NOT writeEvidenceFile
	schemasDir := schematest.RepoSchemasDir(t)
	out := filepath.Join(projectRoot, ".harness", "usecases")
	draft := writeDraftAt(t, t.TempDir(), usecasetest.CanonicalBody(t))

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out", out,
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
		draft,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1 (evidence missing)", code)
	}
	if !strings.Contains(stderr.String(), "users.controller.ts") {
		t.Errorf("stderr should name the missing evidence file; got %q", stderr.String())
	}
}

func TestRun_MissingOut(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"draft.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
}

func TestRun_MissingProjectRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--out", t.TempDir(), "draft.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
}

func TestRun_NoPositional(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", t.TempDir(), "--project-root", t.TempDir()}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
}
