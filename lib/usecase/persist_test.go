package usecase_test

import (
	"encoding/json"
	"errors"
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

// projectRootWithEvidence creates a temp dir, writes the files the
// canonical UseCase points to (handler + DTO contract), and returns the
// dir.
func projectRootWithEvidence(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range []string{
		filepath.Join("src", "users", "users.controller.ts"),
		filepath.Join("src", "users", "dto", "create-user.dto.ts"),
	} {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("//"), 0o644); err != nil {
			t.Fatal(err)
		}
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

func TestValidateAndPersist_RejectsMissingContractEvidence(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := projectRootWithEvidence(t)

	var doc map[string]interface{}
	if err := json.Unmarshal(usecasetest.CanonicalBody(t), &doc); err != nil {
		t.Fatal(err)
	}
	// Strip the kind=contract row, leaving only kind=implementation
	// citations. The fixture is still a map, so the check must reject.
	evRaw, _ := doc["evidence"].([]interface{})
	var kept []interface{}
	for _, item := range evRaw {
		m := item.(map[string]interface{})
		if m["kind"] == "contract" {
			continue
		}
		kept = append(kept, m)
	}
	doc["evidence"] = kept
	body, _ := json.Marshal(doc)

	_, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)
	if err == nil {
		t.Fatal("expected contract evidence cross-check error")
	}
	var cce *stack.CrossCheckError
	if !errors.As(err, &cce) {
		t.Fatalf("expected *stack.CrossCheckError, got %T (%v)", err, err)
	}
	if cce.Kind != "contract_evidence_missing" {
		t.Errorf("kind = %q", cce.Kind)
	}
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Errorf("expected nothing written on validation failure, got %d entries", len(entries))
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

// buildDraftWithFixtures returns a JSON UseCase body derived from the
// canonical fixture but with trigger.fixture and expected_outcome.fixture
// replaced by the supplied envelope maps. projectRoot is used to place
// fixture files for ref-form envelopes so Task 3's existence check
// (not yet wired) does not interfere when it lands.
func buildDraftWithFixtures(
	t *testing.T,
	projectRoot string,
	triggerFx map[string]any,
	outcomeFx map[string]any,
) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(usecasetest.CanonicalBody(t), &doc); err != nil {
		t.Fatalf("unmarshal canonical: %v", err)
	}

	trigger, _ := doc["trigger"].(map[string]any)
	trigger["fixture"] = triggerFx
	doc["trigger"] = trigger

	outcome, _ := doc["expected_outcome"].(map[string]any)
	outcome["fixture"] = outcomeFx
	doc["expected_outcome"] = outcome

	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal draft: %v", err)
	}
	return body
}

// createFixturePlaceholder writes an empty placeholder file at
// <projectRoot>/.harness/fixtures/<name> so Task 3's existence check
// (not yet wired in Task 1) does not fail when it lands.
func createFixturePlaceholder(t *testing.T, projectRoot, name string) {
	t.Helper()
	full := filepath.Join(projectRoot, ".harness", "fixtures", name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir for fixture placeholder %s: %v", name, err)
	}
	if err := os.WriteFile(full, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write fixture placeholder %s: %v", name, err)
	}
}

func TestValidateAndPersist_FixtureRefEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		triggerFx  map[string]any
		outcomeFx  map[string]any
		wantErrSub string // empty means: expect success
		refs       []string // fixture refs to create on disk
	}{
		{
			name:      "ref form ok",
			triggerFx: map[string]any{"ref": "framework/x/trigger.json"},
			outcomeFx: map[string]any{"ref": "framework/x/outcome.json"},
			refs:      []string{"framework/x/trigger.json", "framework/x/outcome.json"},
		},
		{
			name:      "inline primitive ok",
			triggerFx: map[string]any{"inline": "tick"},
			outcomeFx: map[string]any{"inline": map[string]any{"exit_code": float64(0)}},
		},
		{
			name:       "both arms rejected",
			triggerFx:  map[string]any{"ref": "a.json", "inline": "x"},
			outcomeFx:  map[string]any{"inline": "ok"},
			wantErrSub: "oneOf",
		},
		{
			name:       "neither arm rejected",
			triggerFx:  map[string]any{},
			outcomeFx:  map[string]any{"inline": "ok"},
			wantErrSub: "oneOf",
		},
		{
			name:       "extra property rejected",
			triggerFx:  map[string]any{"ref": "a.json", "extra": 1},
			outcomeFx:  map[string]any{"inline": "ok"},
			wantErrSub: "additionalProperties",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schemasDir := schematest.RepoSchemasDir(t)
			outDir := t.TempDir()
			projectRoot := projectRootWithEvidence(t)

			// Create placeholder files for any ref-form fixtures.
			for _, ref := range tc.refs {
				createFixturePlaceholder(t, projectRoot, ref)
			}

			body := buildDraftWithFixtures(t, projectRoot, tc.triggerFx, tc.outcomeFx)
			_, err := usecase.ValidateAndPersist(body, outDir, projectRoot, minimalStack(), schemasDir)

			if tc.wantErrSub == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrSub)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}
