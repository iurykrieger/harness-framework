package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// ValidateAndPersist validates stackJSON against schemas/stack.json and,
// on success, writes a canonicalised copy (2-space indent) to
// <projectRoot>/.harness/stack.json. Returns the absolute path on success.
//
// The function is idempotent: writing the same payload twice produces a
// byte-identical file. It does NOT mutate stackJSON.
func ValidateAndPersist(stackJSON []byte, projectRoot string, schemasDir string) (string, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(stackJSON, &m); err != nil {
		return "", fmt.Errorf("parse stack JSON: %w", err)
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
	if err := v.Validate(schema.TargetStack, m); err != nil {
		return "", err
	}

	outDir := filepath.Join(projectRoot, ".harness")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	target := filepath.Join(outDir, "stack.json")
	if err := writeCanonical(target, m); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return target, nil
}

func writeCanonical(path string, v map[string]interface{}) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
