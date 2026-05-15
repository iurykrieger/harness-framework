// Package schematest exposes test helpers that resolve the in-repo
// schemas/ directory. Production code MUST NOT import it.
package schematest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// RepoSchemasDir returns the absolute path to <repo>/schemas/, resolved
// from this file's own location via runtime.Caller. Independent of cwd.
func RepoSchemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// .../lib/schema/schematest/repodir.go -> 3 levels up to repo root.
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	dir := filepath.Join(repoRoot, "schemas")
	if _, err := os.Stat(filepath.Join(dir, "sensor.yaml")); err != nil {
		t.Fatalf("schemas dir not where expected (%s): %v", dir, err)
	}
	return dir
}
