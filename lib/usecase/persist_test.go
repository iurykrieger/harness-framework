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
	wantSuffix := filepath.Join("user-registration", "create-user-with-email.yaml")
	if !strings.HasSuffix(path, wantSuffix) {
		t.Errorf("path = %q, want suffix %q", path, wantSuffix)
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

	bad := &stack.Stack{Archetypes: []stack.Archetype{stack.ArchetypeHTTPAPI}}
	if _, err := usecase.ValidateAndPersist(body, outDir, projectRoot, bad, schemasDir); err == nil {
		t.Fatal("expected journey cross-check error")
	}
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Errorf("expected nothing written on validation failure, got %d entries (subdir leak)", len(entries))
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
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Errorf("expected nothing written on validation failure, got %d entries (subdir leak)", len(entries))
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

	journeyDir := filepath.Join(outDir, "user-registration")
	if err := os.MkdirAll(journeyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(journeyDir, "create-user-with-email.yaml")
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

func TestValidateAndPersist_IdempotentUnderJourneyDir(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)
	body := usecasetest.CanonicalBody(t)

	first, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	dataFirst, err := os.ReadFile(first)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}

	second, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first != second {
		t.Errorf("paths differ: first=%q second=%q", first, second)
	}
	dataSecond, err := os.ReadFile(second)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if string(dataFirst) != string(dataSecond) {
		t.Errorf("bytes differ between calls; idempotency broken")
	}
}
