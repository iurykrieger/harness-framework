package usecase

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// LoadUseCaseFile reads, parses, and schema-validates a usecase JSON file
// at path. Returns the decoded map, the resolved absolute path, and an
// exit code: 0 success, 1 schema validation failure, 2 I/O or parse failure.
func LoadUseCaseFile(path, schemasDir string, stderr io.Writer) (map[string]interface{}, string, int) {
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
	var u map[string]interface{}
	if err := json.Unmarshal(body, &u); err != nil {
		fmt.Fprintln(stderr, "error: parse:", err)
		return nil, "", 2
	}
	if err := v.Validate(schema.TargetUseCase, u); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return nil, "", 1
	}
	return u, abs, 0
}
