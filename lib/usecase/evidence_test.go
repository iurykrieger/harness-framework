package usecase

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestCheckEvidenceFiles_OK(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("//"), 0o644); err != nil {
		t.Fatal(err)
	}
	uc := &UseCase{Evidence: []stack.Evidence{{File: "a.go", Rationale: "x"}}}
	if err := CheckEvidenceFiles(uc, root); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCheckEvidenceFiles_Missing(t *testing.T) {
	root := t.TempDir()
	uc := &UseCase{Evidence: []stack.Evidence{
		{File: "a.go", Rationale: "x"},
		{File: "b.go", Rationale: "x"},
	}}
	err := CheckEvidenceFiles(uc, root)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "a.go") || !strings.Contains(err.Error(), "b.go") {
		t.Errorf("error should list both missing files; got %q", err)
	}
}

func TestCheckEvidenceFiles_Empty(t *testing.T) {
	uc := &UseCase{}
	if err := CheckEvidenceFiles(uc, "/no/such"); err != nil {
		t.Errorf("empty evidence should be OK at this layer (schema validates minItems)")
	}
}

func TestCheckEvidenceFiles_ReturnsTypedError(t *testing.T) {
	uc := &UseCase{Evidence: []stack.Evidence{{File: "a.go", Rationale: "x"}}}
	err := CheckEvidenceFiles(uc, t.TempDir())
	var cce *stack.CrossCheckError
	if !errors.As(err, &cce) {
		t.Fatalf("expected *stack.CrossCheckError, got %T", err)
	}
	if cce.Kind != "evidence_file_missing" {
		t.Errorf("kind = %q", cce.Kind)
	}
}

func TestCheckFixtureContractEvidence_StructuredTriggerWithoutContract(t *testing.T) {
	uc := &UseCase{
		ID: "create-charge",
		Trigger: Trigger{
			Fixture: map[string]any{"body": map[string]any{"amount": 2500}},
		},
		Evidence: []stack.Evidence{
			{File: "src/charge/charge.controller.ts", Rationale: "POST /charges handler"},
		},
	}
	err := CheckFixtureContractEvidence(uc)
	if err == nil {
		t.Fatal("expected error: non-primitive trigger.fixture but no kind=contract evidence")
	}
	var cce *stack.CrossCheckError
	if !errors.As(err, &cce) {
		t.Fatalf("expected *stack.CrossCheckError, got %T", err)
	}
	if cce.Kind != "contract_evidence_missing" {
		t.Errorf("kind = %q", cce.Kind)
	}
	if !strings.Contains(err.Error(), "create-charge") {
		t.Errorf("error should name the usecase id; got %q", err)
	}
}

func TestCheckFixtureContractEvidence_StructuredExpectedOutcomeWithoutContract(t *testing.T) {
	uc := &UseCase{
		ID: "create-charge",
		Trigger: Trigger{Fixture: "noop"}, // primitive
		ExpectedOutcome: ExpectedOutcome{
			Fixture: map[string]any{"id": "uuid", "pix_transaction": map[string]any{"qrcode_content": "..."}},
		},
		Evidence: []stack.Evidence{
			{File: "src/charge/charge.controller.ts", Rationale: "POST /charges handler"},
		},
	}
	if err := CheckFixtureContractEvidence(uc); err == nil {
		t.Fatal("expected error: non-primitive expected_outcome.fixture but no kind=contract evidence")
	}
}

func TestCheckFixtureContractEvidence_ContractCitationSatisfies(t *testing.T) {
	uc := &UseCase{
		ID: "create-charge",
		Trigger: Trigger{
			Fixture: map[string]any{"body": map[string]any{"amount": 2500}},
		},
		Evidence: []stack.Evidence{
			{File: "src/charge/charge.controller.ts", Rationale: "POST /charges handler"},
			{File: "src/charge/models/charge.model.ts", Rationale: "ChargeCreateRequest DTO", Kind: EvidenceKindContract},
		},
	}
	if err := CheckFixtureContractEvidence(uc); err != nil {
		t.Errorf("unexpected: %v", err)
	}
}

func TestCheckFixtureContractEvidence_PrimitiveFixturesSkip(t *testing.T) {
	cases := []struct {
		name string
		t    any
		e    any
	}{
		{"both nil", nil, nil},
		{"both string", "tick", "ok"},
		{"both number", float64(1), float64(2)},
		{"both bool", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uc := &UseCase{
				ID:              "scheduled-ping",
				Trigger:         Trigger{Fixture: tc.t},
				ExpectedOutcome: ExpectedOutcome{Fixture: tc.e},
				Evidence: []stack.Evidence{
					{File: "x.go", Rationale: "handler"},
				},
			}
			if err := CheckFixtureContractEvidence(uc); err != nil {
				t.Errorf("primitive fixtures should not require contract evidence; got %v", err)
			}
		})
	}
}

func TestCheckFixtureContractEvidence_ListFixtureIsStructured(t *testing.T) {
	uc := &UseCase{
		ID:      "cli-invoke",
		Trigger: Trigger{Fixture: []any{"--flag", "value"}},
		Evidence: []stack.Evidence{
			{File: "cmd/x/main.go", Rationale: "Cobra command"},
		},
	}
	if err := CheckFixtureContractEvidence(uc); err == nil {
		t.Fatal("expected error: a list-shaped fixture still needs a contract citation (CLI args declaration, message schema, etc.)")
	}
}

func TestCheckFixtureContractEvidence_EmptyKindIsImplementation(t *testing.T) {
	// Backwards compatibility: existing UseCases with no `kind` field at
	// all on their evidence rows must still be flagged when fixtures are
	// structured. Empty string is treated as `implementation`.
	uc := &UseCase{
		ID:      "create-charge",
		Trigger: Trigger{Fixture: map[string]any{"x": 1}},
		Evidence: []stack.Evidence{
			{File: "a.go", Rationale: "handler"}, // Kind not set
			{File: "b.go", Rationale: "service"}, // Kind not set
		},
	}
	if err := CheckFixtureContractEvidence(uc); err == nil {
		t.Fatal("expected error: empty Kind must not satisfy the contract requirement")
	}
}
