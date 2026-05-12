// Package stack owns the project-level Stack artifact: the LLM-derived
// description of a project's languages, components, and canonical stdout
// shapes. Mirrors lib/sensor's load/persist/lookup pattern.
package stack

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// LoadStackFile reads, parses, and schema-validates a stack JSON file at
// path. Returns the decoded map, the resolved absolute path, and an exit
// code: 0 success, 1 schema validation failure, 2 I/O or parse failure.
func LoadStackFile(path, schemasDir string, stderr io.Writer) (map[string]interface{}, string, int) {
	if path == "" {
		fmt.Fprintln(stderr, "error: empty path")
		return nil, "", 2
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return nil, "", 2
	}
	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return nil, "", code
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return nil, "", 2
	}
	var s map[string]interface{}
	if err := json.Unmarshal(body, &s); err != nil {
		fmt.Fprintln(stderr, "error: parse:", err)
		return nil, "", 2
	}
	if err := v.Validate(schema.TargetStack, s); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return nil, "", 1
	}
	return s, abs, 0
}
