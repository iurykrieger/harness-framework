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
)

// LoadCanonical returns the canonical UseCase fixture.
func LoadCanonical(t *testing.T) *usecase.UseCase {
	t.Helper()
	_, this, _, _ := runtime.Caller(0)
	p := filepath.Clean(filepath.Join(filepath.Dir(this), "..", "testdata", "canonical-usecase.json"))
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	var uc usecase.UseCase
	if err := json.Unmarshal(body, &uc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &uc
}

// CanonicalBody returns the raw JSON bytes of the canonical fixture.
func CanonicalBody(t *testing.T) []byte {
	t.Helper()
	_, this, _, _ := runtime.Caller(0)
	p := filepath.Clean(filepath.Join(filepath.Dir(this), "..", "testdata", "canonical-usecase.json"))
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return body
}
