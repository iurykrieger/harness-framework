package stack

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAndPersist_Golden(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	root := t.TempDir()
	target, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	want := filepath.Join(root, ".harness", "stack.json")
	if target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Round-trip
	out, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var a, b map[string]interface{}
	_ = json.Unmarshal(body, &a)
	_ = json.Unmarshal(out, &b)
	if ja, _ := json.Marshal(a); !bytes.Equal(ja, mustMarshal(b)) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestValidateAndPersist_Idempotent(t *testing.T) {
	body, _ := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	root := t.TempDir()
	target1, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	first, _ := os.ReadFile(target1)
	target2, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	second, _ := os.ReadFile(target2)
	if !bytes.Equal(first, second) {
		t.Fatalf("idempotency: bytes differ across writes")
	}
}

func TestValidateAndPersist_SchemaFail(t *testing.T) {
	body, _ := os.ReadFile(filepath.Join("testdata", "invalid-missing-required.json"))
	root := t.TempDir()
	_, err := ValidateAndPersist(body, root, "")
	if err == nil {
		t.Fatal("expected schema error, got nil")
	}
	if _, statErr := os.Stat(filepath.Join(root, ".harness", "stack.json")); statErr == nil {
		t.Fatal("expected no file on disk after validation failure")
	}
}

func TestValidateAndPersist_Permissions(t *testing.T) {
	body, _ := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	root := t.TempDir()
	target, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	info, _ := os.Stat(target)
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("perm = %o, want 0o644", perm)
	}
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
