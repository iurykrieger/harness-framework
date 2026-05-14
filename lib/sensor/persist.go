// lib/sensor/persist.go
package sensor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// ValidateAndPersist validates sensorJSON against schemas/sensor.json
// (loaded from schemasDir; if empty, the schema lib walks up from cwd)
// and, on success, writes a canonicalised copy (2-space indent) to
// <outDir>/<id>.json. Returns the absolute path on success.
//
// The function is idempotent: writing the same sensor twice produces a
// byte-identical file. It does NOT mutate sensorJSON.
//
// Errors:
//   - JSON parse failure → wrapped *json.SyntaxError-flavored error.
//   - Schema validation failure → error from the underlying validator
//     (callers may render via schema.PrintValidationOrPlain).
//   - I/O failure (mkdir, write, rename) → wrapped os error; nothing
//     partial left on disk.
func ValidateAndPersist(sensorJSON []byte, outDir string, schemasDir string) (string, error) {
	var sensorMap map[string]interface{}
	if err := json.Unmarshal(sensorJSON, &sensorMap); err != nil {
		return "", fmt.Errorf("parse sensor JSON: %w", err)
	}

	dir := schemasDir
	if dir == "" {
		cwd, _ := os.Getwd()
		found, ferr := schema.FindSchemasDir(cwd)
		if ferr != nil {
			return "", fmt.Errorf("locate schemas: %w", ferr)
		}
		dir = found
	}
	v, err := schema.NewValidator(dir)
	if err != nil {
		return "", fmt.Errorf("load schemas: %w", err)
	}
	if err := v.Validate(schema.TargetSensor, sensorMap); err != nil {
		return "", err
	}

	id, ok := sensorMap["id"].(string)
	if !ok || id == "" {
		return "", fmt.Errorf("sensor.id missing or empty after validation")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	target := filepath.Join(outDir, id+".json")
	if err := writeCanonical(target, sensorMap); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}

func writeCanonical(path string, sensor map[string]interface{}) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sensor); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
