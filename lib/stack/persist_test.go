package stack

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

// readFixtureAsJSON reads a YAML fixture from testdata/ and returns its
// canonical JSON byte representation, so callers can pass it to
// ValidateAndPersist (which expects JSON bytes).
func readFixtureAsJSON(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	jb, err := yaml.YAMLToJSON(body)
	if err != nil {
		t.Fatalf("yaml→json %s: %v", name, err)
	}
	return jb
}

func TestValidateAndPersist_Golden(t *testing.T) {
	body := readFixtureAsJSON(t, "golden-stack.yaml")
	root := t.TempDir()
	target, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	want := filepath.Join(root, ".harness", "stack.yaml")
	if target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Round-trip: re-decode the persisted YAML and compare against the
	// canonical JSON-decoded view of the input.
	out, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	outJSON, err := yaml.YAMLToJSON(out)
	if err != nil {
		t.Fatalf("yaml→json persisted: %v", err)
	}
	var a, b map[string]interface{}
	_ = json.Unmarshal(body, &a)
	_ = json.Unmarshal(outJSON, &b)
	if ja, _ := json.Marshal(a); !bytes.Equal(ja, mustMarshal(b)) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestValidateAndPersist_Idempotent(t *testing.T) {
	body := readFixtureAsJSON(t, "golden-stack.yaml")
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
	body := readFixtureAsJSON(t, "invalid-missing-required.yaml")
	root := t.TempDir()
	_, err := ValidateAndPersist(body, root, "")
	if err == nil {
		t.Fatal("expected schema error, got nil")
	}
	if _, statErr := os.Stat(filepath.Join(root, ".harness", "stack.yaml")); statErr == nil {
		t.Fatal("expected no file on disk after validation failure")
	}
}

func TestValidateAndPersist_Permissions(t *testing.T) {
	body := readFixtureAsJSON(t, "golden-stack.yaml")
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
