package stack

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// Result is the outcome of a stack Lookup.
type Result struct {
	ProjectRoot string                 // absolute path
	Path        string                 // absolute path to .harness/stack.yaml
	Exists      bool                   // stack.yaml present on disk
	Stack       map[string]interface{} // nil when Exists=false
}

// Lookup resolves the project root (via lib/registry.Discover), then
// resolves <root>/.harness/stack.yaml. Returns Exists=false when the
// file is absent — that is NOT an error.
//
// Schema validation runs only when the file exists.
func Lookup(startDir string) (Result, error) {
	root, _, err := registry.Discover(startDir)
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(root, ".harness", "stack.yaml")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Result{ProjectRoot: root, Path: path, Exists: false}, nil
		}
		return Result{}, err
	}
	var stderr bytes.Buffer
	s, _, code := LoadStackFile(path, "", &stderr)
	if code != 0 {
		return Result{}, &LoadError{Path: path, Code: code, Stderr: stderr.String()}
	}
	return Result{ProjectRoot: root, Path: path, Exists: true, Stack: s}, nil
}

// LoadError carries the exit-code + stderr details from LoadStackFile.
type LoadError struct {
	Path   string
	Code   int
	Stderr string
}

func (e *LoadError) Error() string {
	return "stack load failed at " + e.Path + ": " + e.Stderr
}
