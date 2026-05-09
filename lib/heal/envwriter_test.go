// lib/heal/envwriter_test.go
package heal_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestWriteEnvVar_AppendsLine(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte("EXISTING=1\n"), 0o600)
	// Make sure the dir is gitignored to satisfy the gitignore-coverage check.
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	res := heal.WriteEnvVar(envFile, "FOO", "bar")
	if !res.Applied {
		t.Fatalf("expected applied; got %s", res.Reason)
	}
	body, _ := os.ReadFile(envFile)
	if string(body) != "EXISTING=1\nFOO=bar\n" {
		t.Fatalf("env content = %q", body)
	}
}

func TestWriteEnvVar_Idempotent(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte("FOO=bar\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	res := heal.WriteEnvVar(envFile, "FOO", "bar")
	if !res.Applied {
		t.Fatalf("expected applied (no-op); got %s", res.Reason)
	}
	body, _ := os.ReadFile(envFile)
	if string(body) != "FOO=bar\n" {
		t.Fatalf("must not duplicate; got %q", body)
	}
}

func TestWriteEnvVar_Chmod600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on Windows")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	heal.WriteEnvVar(envFile, "FOO", "bar")
	st, _ := os.Stat(envFile)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600, got %o", st.Mode().Perm())
	}
}

func TestWriteEnvVar_RejectsWhenNotGitignored(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte(""), 0o600)
	// Intentionally no .gitignore.
	res := heal.WriteEnvVar(envFile, "FOO", "bar")
	if res.Applied {
		t.Fatal(".env not gitignored — write must be downgraded to propose_only")
	}
	if res.Reason == "" {
		t.Fatal("expected non-empty Reason explaining the downgrade")
	}
}
