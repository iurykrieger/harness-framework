//go:build write_fixture

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withProjectRoot sets HARNESS_REGISTRY_ROOT to a fresh temp dir
// containing a .harness/ marker, restores the previous value on cleanup,
// and returns the absolute project root.
func withProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, hadPrev := os.LookupEnv("HARNESS_REGISTRY_ROOT")
	os.Setenv("HARNESS_REGISTRY_ROOT", root)
	t.Cleanup(func() {
		if hadPrev {
			os.Setenv("HARNESS_REGISTRY_ROOT", prev)
		} else {
			os.Unsetenv("HARNESS_REGISTRY_ROOT")
		}
	})
	return root
}

func TestRun_HappyPath_Stdin(t *testing.T) {
	root := withProjectRoot(t)
	rel := "assert-x/pass.txt"

	var stdout, stderr bytes.Buffer
	stdin := bytes.NewBufferString("200\n")
	code := runWithStdin([]string{rel}, stdin, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(root, ".harness/fixtures", rel))
	if err != nil {
		t.Fatalf("fixture not written: %v", err)
	}
	if string(body) != "200\n" {
		t.Fatalf("payload=%q want %q", body, "200\n")
	}
	if !strings.Contains(stdout.String(), `"verdict":"pass"`) {
		t.Fatalf("missing pass signal: %q", stdout.String())
	}
}

func TestRun_PathEscape_Rejected(t *testing.T) {
	withProjectRoot(t)
	for _, bad := range []string{
		"../escape.txt",
		"../outside.txt",
		"sub/../../escape.txt",
	} {
		var stdout, stderr bytes.Buffer
		stdin := bytes.NewBufferString("payload")
		code := runWithStdin([]string{bad}, stdin, &stdout, &stderr)
		if code == 0 {
			t.Fatalf("expected non-zero exit for %q, got 0", bad)
		}
		if !strings.Contains(stdout.String(), "fixture_path_escape") {
			t.Fatalf("missing fixture_path_escape for %q: %q", bad, stdout.String())
		}
	}
}

func TestRun_LegacyPrefix_Rejected(t *testing.T) {
	withProjectRoot(t)
	for _, bad := range []string{
		".harness/sensors/fixtures/assert-x/pass.txt",
		".harness/fixtures/assert-x/pass.txt",
	} {
		var stdout, stderr bytes.Buffer
		stdin := bytes.NewBufferString("payload")
		code := runWithStdin([]string{bad}, stdin, &stdout, &stderr)
		if code == 0 {
			t.Fatalf("expected non-zero exit for legacy prefix %q, got 0", bad)
		}
		if !strings.Contains(stdout.String(), "legacy_fixture_prefix") {
			t.Fatalf("missing legacy_fixture_prefix for %q: %q", bad, stdout.String())
		}
	}
}

func TestRun_FromFile(t *testing.T) {
	root := withProjectRoot(t)
	src := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(src, []byte("404\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := "assert-y/fail.txt"

	var stdout, stderr bytes.Buffer
	code := runWithStdin([]string{"--from-file", src, rel}, bytes.NewBuffer(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(root, ".harness/fixtures", rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "404\n" {
		t.Fatalf("body=%q", body)
	}
}

func TestRun_CreatesNestedParents(t *testing.T) {
	root := withProjectRoot(t)
	rel := "deeply/nested/case/pass.txt"

	var stdout, stderr bytes.Buffer
	code := runWithStdin([]string{rel}, bytes.NewBufferString("ok"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".harness/fixtures", rel)); err != nil {
		t.Fatalf("nested fixture not written: %v", err)
	}
}

func TestRun_IdempotentRewrite(t *testing.T) {
	root := withProjectRoot(t)
	rel := "assert-z/pass.txt"

	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		code := runWithStdin([]string{rel}, bytes.NewBufferString("same"), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("iter %d: exit=%d stderr=%s", i, code, stderr.String())
		}
	}
	body, err := os.ReadFile(filepath.Join(root, ".harness/fixtures", rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "same" {
		t.Fatalf("body=%q want %q", body, "same")
	}
}
