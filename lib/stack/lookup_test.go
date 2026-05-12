package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// Lookup tests use HARNESS_REGISTRY_ROOT to pin the project root.
// Fixtures include an empty .harness/ subdir so registry.Discover
// walk-up finds the marker.

func TestLookup_StackPresent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	if err := os.WriteFile(filepath.Join(root, ".harness", "stack.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HARNESS_REGISTRY_ROOT", root)
	res, err := Lookup(root)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !res.Exists {
		t.Fatalf("Exists=false, want true")
	}
	wantPath := filepath.Join(root, ".harness", "stack.json")
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	if res.Stack["version"] != "0.1.0" {
		t.Fatalf("stack content not decoded; got %v", res.Stack["version"])
	}
}

func TestLookup_StackAbsent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", root)
	res, err := Lookup(root)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res.Exists {
		t.Fatalf("Exists=true, want false")
	}
	wantPath := filepath.Join(root, ".harness", "stack.json")
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	if res.Stack != nil {
		t.Fatalf("Stack should be nil when Exists=false, got %v", res.Stack)
	}
}
