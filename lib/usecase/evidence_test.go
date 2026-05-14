package usecase

import (
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
