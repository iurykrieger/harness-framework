package schema_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestFindSchemasDir(t *testing.T) {
	expected := testfixtures.RepoSchemasDir(t)
	_, thisFile, _, _ := runtime.Caller(0)
	got, err := schema.FindSchemasDir(filepath.Dir(thisFile))
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("got %s, want %s", got, expected)
	}
}

func TestFindSchemasDir_Missing(t *testing.T) {
	if _, err := schema.FindSchemasDir(t.TempDir()); err == nil {
		t.Fatal("expected error when no schemas dir exists")
	}
}
