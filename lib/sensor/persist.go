// lib/sensor/persist.go
package sensor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"sigs.k8s.io/yaml"
)

// PersistOpts is the single options struct accepted by ValidateAndPersist.
// The zero value (other than OutDir/SchemasDir) yields the permissive
// behavior used by /detect-sensors and /heal-sensor: silent overwrite, no
// usecase pre-check. /create-sensor sets RejectIfExists and
// RequireUseCaseFilesOnDisk to get strict authoring semantics.
type PersistOpts struct {
	// OutDir is the directory where <id>.yaml will be written. Required.
	OutDir string

	// SchemasDir is the schemas/ directory. When empty, the schema library
	// walks up from cwd to locate it.
	SchemasDir string

	// RejectIfExists causes ValidateAndPersist to return a
	// *SensorAlreadyExistsError instead of overwriting when
	// <OutDir>/<id>.yaml already exists on disk.
	RejectIfExists bool

	// RequireUseCaseFilesOnDisk causes ValidateAndPersist to verify
	// every entry in use_cases[] resolves to a real
	// .harness/usecases/**/<id>.yaml under ProjectRoot, returning a
	// *MissingUseCaseError if any are absent. Requires ProjectRoot
	// to be set.
	RequireUseCaseFilesOnDisk bool

	// ProjectRoot is the absolute path against which usecase ids
	// resolve. Only consulted when RequireUseCaseFilesOnDisk is true.
	ProjectRoot string
}

// SensorAlreadyExistsError is returned when RejectIfExists is set and the
// target <id>.yaml already exists.
type SensorAlreadyExistsError struct {
	Path string
}

func (e *SensorAlreadyExistsError) Error() string {
	return fmt.Sprintf("sensor file already exists at %s", e.Path)
}

// MissingUseCaseError is returned when RequireUseCaseFilesOnDisk is set
// and a referenced usecase id does not match any
// <ProjectRoot>/.harness/usecases/**/<id>.yaml file.
type MissingUseCaseError struct {
	ID         string // the use_cases[] entry that did not resolve
	SearchRoot string // the project root the lookup was anchored at; the actual walk happens under <SearchRoot>/.harness/usecases
}

func (e *MissingUseCaseError) Error() string {
	return fmt.Sprintf("usecase %q not found under %s/.harness/usecases", e.ID, e.SearchRoot)
}

// ValidateAndPersist is the single entrypoint for writing a sensor file
// to <OutDir>/<id>.yaml after schema validation. Behavior is governed by
// opts; see PersistOpts. Used by every skill that authors sensors
// (/detect-sensors, /create-sensor, /heal-sensor).
//
// On success, returns the absolute path of the written file.
//
// Errors:
//   - JSON parse failure → wrapped error.
//   - RejectIfExists + collision → *SensorAlreadyExistsError.
//   - RequireUseCaseFilesOnDisk + missing → *MissingUseCaseError.
//   - Schema validation failure → error from the underlying validator
//     (callers may render via schema.PrintValidationOrPlain).
//   - I/O failure (mkdir, write, rename) → wrapped os error; nothing
//     partial left on disk.
//
// The function is idempotent when RejectIfExists is false: writing the
// same sensor twice produces a byte-identical file.
func ValidateAndPersist(sensorJSON []byte, opts PersistOpts) (string, error) {
	if opts.OutDir == "" {
		return "", errors.New("PersistOpts.OutDir is required")
	}
	if opts.RequireUseCaseFilesOnDisk && opts.ProjectRoot == "" {
		return "", errors.New("PersistOpts.ProjectRoot is required when RequireUseCaseFilesOnDisk is set")
	}

	var sensorMap map[string]interface{}
	if err := json.Unmarshal(sensorJSON, &sensorMap); err != nil {
		return "", fmt.Errorf("parse sensor JSON: %w", err)
	}

	id, _ := sensorMap["id"].(string)

	// Pre-checks (id-collision and missing usecase files) run before
	// schema validation so the caller gets a structural rejection without
	// paying the cost of a full schema parse when the answer is going to
	// be no.
	if opts.RejectIfExists && id != "" {
		target := filepath.Join(opts.OutDir, id+".yaml")
		if _, err := os.Stat(target); err == nil {
			abs, _ := filepath.Abs(target)
			return "", &SensorAlreadyExistsError{Path: abs}
		}
	}
	if opts.RequireUseCaseFilesOnDisk {
		if err := checkUseCasesOnDisk(sensorMap, opts.ProjectRoot); err != nil {
			return "", err
		}
	}

	dir := opts.SchemasDir
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

	if id == "" {
		return "", errors.New("sensor.id missing or empty after validation")
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	target := filepath.Join(opts.OutDir, id+".yaml")
	if err := writeCanonical(target, sensorMap); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}

func checkUseCasesOnDisk(sensorMap map[string]interface{}, projectRoot string) error {
	rawIDs, ok := sensorMap["use_cases"].([]interface{})
	if !ok {
		return nil // schema will catch this later
	}
	usecasesRoot := filepath.Join(projectRoot, ".harness", "usecases")
	for _, raw := range rawIDs {
		id, ok := raw.(string)
		if !ok || id == "" {
			continue
		}
		found, err := usecaseFileExists(usecasesRoot, id)
		if err != nil {
			return err
		}
		if !found {
			return &MissingUseCaseError{ID: id, SearchRoot: projectRoot}
		}
	}
	return nil
}

// usecaseFileExists walks usecasesRoot looking for a <id>.yaml file.
// Match is by basename equality (filepath.Walk is bounded to the
// .harness/usecases subtree).
func usecaseFileExists(usecasesRoot, id string) (bool, error) {
	target := id + ".yaml"
	found := false
	err := filepath.Walk(usecasesRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return filepath.SkipDir
			}
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if info.Name() == target {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return found, nil
}

func writeCanonical(path string, sensor map[string]interface{}) error {
	jsonBytes, err := json.Marshal(sensor)
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	yamlBytes, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		return fmt.Errorf("convert to YAML: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(yamlBytes); err != nil {
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
