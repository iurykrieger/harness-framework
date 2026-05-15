// Package usecasetest exposes test helpers that load canonical UseCase
// fixtures from lib/usecase/testdata/. Production code MUST NOT import it.
package usecasetest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/usecase"
	"sigs.k8s.io/yaml"
)

// LoadCanonical returns the canonical UseCase fixture.
func LoadCanonical(t *testing.T) *usecase.UseCase {
	t.Helper()
	body := CanonicalBody(t)
	var uc usecase.UseCase
	if err := json.Unmarshal(body, &uc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &uc
}

// CanonicalBody returns the canonical fixture as JSON bytes, converting
// from the on-disk YAML representation so callers can hand it to
// json.Unmarshal or to ValidateAndPersist.
func CanonicalBody(t *testing.T) []byte {
	t.Helper()
	_, this, _, _ := runtime.Caller(0)
	p := filepath.Clean(filepath.Join(filepath.Dir(this), "..", "testdata", "canonical-usecase.yaml"))
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	jb, err := yaml.YAMLToJSON(body)
	if err != nil {
		t.Fatalf("yaml→json: %v", err)
	}
	return jb
}
