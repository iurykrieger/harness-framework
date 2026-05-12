//go:build run_computational || run_inferential

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveProjectRoot(t *testing.T) {
	t.Run("honors HARNESS_REGISTRY_ROOT", func(t *testing.T) {
		proj := t.TempDir()
		if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HARNESS_REGISTRY_ROOT", proj)

		got := resolveProjectRoot(t.TempDir()) // unrelated cwd
		want, _ := filepath.EvalSymlinks(proj)
		gotResolved, _ := filepath.EvalSymlinks(got)
		if gotResolved != want {
			t.Errorf("got %q, want %q", gotResolved, want)
		}
	})

	t.Run("walks up from cwd when env unset", func(t *testing.T) {
		proj := t.TempDir()
		if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
			t.Fatal(err)
		}
		subdir := filepath.Join(proj, "sub", "deep")
		if err := os.MkdirAll(subdir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HARNESS_REGISTRY_ROOT", "")

		got := resolveProjectRoot(subdir)
		want, _ := filepath.EvalSymlinks(proj)
		gotResolved, _ := filepath.EvalSymlinks(got)
		if gotResolved != want {
			t.Errorf("got %q, want %q", gotResolved, want)
		}
	})

	t.Run("falls back to cwd when discovery fails", func(t *testing.T) {
		// A temp dir with no sensors/ ancestor — walk-up will fail and
		// the function should return the cwd unchanged.
		unrelated := t.TempDir()
		t.Setenv("HARNESS_REGISTRY_ROOT", "")

		got := resolveProjectRoot(unrelated)
		if got != unrelated {
			t.Errorf("got %q, want cwd fallback %q", got, unrelated)
		}
	})
}

// repoRootForTest returns the absolute path of the repository root by
// walking up from the current working directory until a go.mod file is
// found. Tests that spawn `go run` need an absolute Dir so the module
// resolves regardless of where the test binary is executed from.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found walking up from %s", cwd)
		}
		dir = parent
	}
}
