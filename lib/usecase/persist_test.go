package usecase_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
	"github.com/iurykrieger/harness-framework/lib/usecase/usecasetest"
)

// minimalStack returns a stack with the journey the canonical UseCase
// references, so cross-check passes.
func minimalStack() *stack.Stack {
	return &stack.Stack{
		Archetypes: []stack.Archetype{stack.ArchetypeHTTPAPI},
		Journeys: []stack.Journey{
			{ID: "user-registration", Archetype: stack.ArchetypeHTTPAPI},
		},
	}
}

// projectRootWithEvidence creates a temp dir, writes the file the
// canonical UseCase points to, and returns the dir.
func projectRootWithEvidence(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	target := filepath.Join(root, "src", "users", "users.controller.ts")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestValidateAndPersist_Happy(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)
	body := usecasetest.CanonicalBody(t)

	path, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.HasSuffix(path, "create-user-with-email.yaml") {
		t.Errorf("path = %q, want suffix create-user-with-email.yaml", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not written: %v", err)
	}
}

func TestValidateAndPersist_RejectsBadJourney(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)
	body := usecasetest.CanonicalBody(t)

	// Strip the matching journey so cross-check fails.
	bad := &stack.Stack{Archetypes: []stack.Archetype{stack.ArchetypeHTTPAPI}}
	if _, err := usecase.ValidateAndPersist(body, outDir, projectRoot, bad, schemasDir); err == nil {
		t.Fatal("expected journey cross-check error")
	}
	files, _ := os.ReadDir(outDir)
	if len(files) != 0 {
		t.Errorf("expected nothing written on validation failure, got %d files", len(files))
	}
}

func TestValidateAndPersist_RejectsMissingEvidence(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	body := usecasetest.CanonicalBody(t)

	// projectRoot WITHOUT the evidence file
	if _, err := usecase.ValidateAndPersist(body, outDir, t.TempDir(), minimalStack(), schemasDir); err == nil {
		t.Fatal("expected evidence cross-check error")
	}
}

func TestValidateAndPersist_RejectsSchemaViolation(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)

	var doc map[string]interface{}
	if err := json.Unmarshal(usecasetest.CanonicalBody(t), &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "journey_id")
	body, _ := json.Marshal(doc)

	if _, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir); err == nil {
		t.Fatal("expected schema validation error")
	}
}

func TestValidateAndPersist_OverwritesAtomically(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)

	target := filepath.Join(outDir, "create-user-with-email.yaml")
	if err := os.WriteFile(target, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := usecasetest.CanonicalBody(t)

	if _, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "STALE") {
		t.Errorf("expected target to be overwritten")
	}
}
