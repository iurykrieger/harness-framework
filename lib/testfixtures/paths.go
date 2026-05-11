package testfixtures

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// WithRunDir materializes a temp registry Root, a populated <run-id>/
// directory with empty raw.log and signals.log files. Returns the Root,
// the synthesized run_id (<pid>-<short>), and a cleanup function. Tests
// in lib/subprocess, lib/orchestrator, and skill scripts use this to
// avoid duplicating mkdir/touch boilerplate.
//
// runIDSeed lets the caller pin a deterministic value; pass "" to get
// an os.Getpid()-based composite.
func WithRunDir(t testing.TB, sensorID, runIDSeed string) (root registry.Root, runID, runDir string) {
	t.Helper()
	proj := t.TempDir()
	root = registry.NewRoot(proj)
	if runIDSeed == "" {
		runIDSeed = fmt.Sprintf("%d-test0001", os.Getpid())
	}
	runID = runIDSeed
	runDir = root.RunDir(sensorID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("mkdir runDir: %v", err)
	}
	for _, fname := range []string{"raw.log", "signals.log"} {
		f, err := os.Create(filepath.Join(runDir, fname))
		if err != nil {
			t.Fatalf("create %s: %v", fname, err)
		}
		_ = f.Close()
	}
	return root, runID, runDir
}

// RepoSchemasDir returns the absolute path to schemas/ in the repo root,
// resolved from this file's own location (independent of cwd).
func RepoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../lib/testfixtures/paths.go → 2 levels up to repo root.
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	dir := filepath.Join(repoRoot, "schemas")
	if _, err := os.Stat(filepath.Join(dir, "sensor.json")); err != nil {
		t.Fatalf("schemas dir not where expected (%s): %v", dir, err)
	}
	return dir
}
