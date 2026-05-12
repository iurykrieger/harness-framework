package schematest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
)

func TestRepoSchemasDir_ReturnsValidPath(t *testing.T) {
	dir := schematest.RepoSchemasDir(t)
	for _, name := range []string{"sensor.json", "signal.json", "stack.json"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s in returned dir: %v", name, err)
		}
	}
}
