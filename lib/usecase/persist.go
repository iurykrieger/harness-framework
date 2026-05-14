package usecase

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

// ValidateAndPersist validates draftJSON against schemas/usecase.json,
// cross-checks the journey_id reference against stk, verifies every
// evidence file exists under projectRoot, then writes a canonicalised
// copy (2-space indent) to <outDir>/<id>.json. Returns the absolute path.
//
// Idempotent: re-persisting the same body produces a byte-identical file.
// Does NOT mutate draftJSON.
//
// Errors:
//   - JSON parse failure → wrapped *json.SyntaxError-flavored error.
//   - Schema validation failure → error from the underlying validator
//     (callers may render via schema.PrintValidationOrPlain).
//   - Cross-check failure → *stack.CrossCheckError. Nothing written.
//   - I/O failure (mkdir, write, rename) → wrapped os error; nothing
//     partial left on disk.
func ValidateAndPersist(
	draftJSON []byte,
	outDir string,
	projectRoot string,
	stk *stack.Stack,
	schemasDir string,
) (string, error) {
	var doc map[string]interface{}
	if err := json.Unmarshal(draftJSON, &doc); err != nil {
		return "", fmt.Errorf("parse usecase JSON: %w", err)
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
	if err := v.Validate(schema.TargetUseCase, doc); err != nil {
		return "", err
	}

	// Decode the typed view for cross-checks. The map carries the
	// canonical bytes for write; the struct is just for validation.
	var uc UseCase
	body, _ := json.Marshal(doc)
	if err := json.Unmarshal(body, &uc); err != nil {
		return "", fmt.Errorf("decode after schema validation: %w", err)
	}
	if err := CheckJourneyReference(&uc, stk); err != nil {
		return "", err
	}
	if err := CheckEvidenceFiles(&uc, projectRoot); err != nil {
		return "", err
	}

	id, ok := doc["id"].(string)
	if !ok || id == "" {
		return "", fmt.Errorf("usecase.id missing after validation")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	target := filepath.Join(outDir, id+".json")
	if err := writeCanonical(target, doc); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}

func writeCanonical(path string, doc map[string]interface{}) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
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
