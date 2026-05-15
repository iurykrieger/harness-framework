//go:build write_stack

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRun_Happy(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot := repoRootForTest(t)
	payload := filepath.Join(repoRoot, "lib", "stack", "testdata", "golden-stack.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out=" + tmp,
		"--schemas-dir=" + filepath.Join(repoRoot, "schemas"),
		payload,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	got := filepath.Join(tmp, ".harness", "stack.yaml")
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("stack.yaml not on disk: %v", err)
	}
}

func TestRun_SchemaFail(t *testing.T) {
	tmp := t.TempDir()
	repoRoot := repoRootForTest(t)
	payload := filepath.Join(repoRoot, "lib", "stack", "testdata", "invalid-missing-required.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out=" + tmp,
		"--schemas-dir=" + filepath.Join(repoRoot, "schemas"),
		payload,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr = %s", code, stderr.String())
	}
}

func TestRun_ProducedByOrphan(t *testing.T) {
	tmp := t.TempDir()
	repoRoot := repoRootForTest(t)
	payload := filepath.Join(repoRoot, "lib", "stack", "testdata", "invalid-produced-by-orphan.yaml")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out=" + tmp,
		"--schemas-dir=" + filepath.Join(repoRoot, "schemas"),
		payload,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr = %s", code, stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("stack_produced_by_orphan")) {
		t.Fatalf("stderr does not mention orphan: %s", stderr.String())
	}
}

func TestRun_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	repoRoot := repoRootForTest(t)
	payload := filepath.Join(repoRoot, "lib", "stack", "testdata", "golden-stack.yaml")
	args := []string{
		"--out=" + tmp,
		"--schemas-dir=" + filepath.Join(repoRoot, "schemas"),
		payload,
	}
	var sb1, sb2 bytes.Buffer
	if code := run(args, &sb1, &sb1); code != 0 {
		t.Fatalf("first: code=%d, %s", code, sb1.String())
	}
	first, _ := os.ReadFile(filepath.Join(tmp, ".harness", "stack.yaml"))
	if code := run(args, &sb2, &sb2); code != 0 {
		t.Fatalf("second: code=%d, %s", code, sb2.String())
	}
	second, _ := os.ReadFile(filepath.Join(tmp, ".harness", "stack.yaml"))
	if !bytes.Equal(first, second) {
		t.Fatalf("not idempotent")
	}
	_ = json.RawMessage(first)
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
	t.Fatal("repo root not found within 8 levels")
	return ""
}
