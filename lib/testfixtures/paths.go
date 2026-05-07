package testfixtures

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
