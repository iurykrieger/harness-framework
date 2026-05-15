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
// fixture pre-check. /create-sensor sets RejectIfExists and
// RequireFixturesOnDisk to get strict authoring semantics.
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

	// RequireFixturesOnDisk causes ValidateAndPersist to stat every
	// verification.golden_cases[].fixture path (resolved against
	// ProjectRoot) and return a *MissingFixtureError if any are absent.
	// Requires ProjectRoot to be set.
	RequireFixturesOnDisk bool

	// ProjectRoot is the absolute path against which fixture relative
	// paths resolve. Only consulted when RequireFixturesOnDisk is true.
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

// MissingFixtureError is returned when RequireFixturesOnDisk is set and a
// referenced fixture is not on disk.
type MissingFixtureError struct {
	Rel  string // path as written in golden_cases[].fixture
	Full string // resolved absolute path that was missing
}

func (e *MissingFixtureError) Error() string {
	return fmt.Sprintf("fixture %q not found at %s", e.Rel, e.Full)
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
//   - RequireFixturesOnDisk + missing → *MissingFixtureError.
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
	if opts.RequireFixturesOnDisk && opts.ProjectRoot == "" {
		return "", errors.New("PersistOpts.ProjectRoot is required when RequireFixturesOnDisk is set")
	}

	var sensorMap map[string]interface{}
	if err := json.Unmarshal(sensorJSON, &sensorMap); err != nil {
		return "", fmt.Errorf("parse sensor JSON: %w", err)
	}

	id, _ := sensorMap["id"].(string)

	// Pre-checks (id-collision and missing fixtures) run before schema
	// validation so the caller gets a structural rejection without paying
	// the cost of a full schema parse when the answer is going to be no.
	if opts.RejectIfExists && id != "" {
		target := filepath.Join(opts.OutDir, id+".yaml")
		if _, err := os.Stat(target); err == nil {
			abs, _ := filepath.Abs(target)
			return "", &SensorAlreadyExistsError{Path: abs}
		}
	}
	if opts.RequireFixturesOnDisk {
		if err := checkFixturesOnDisk(sensorMap, opts.ProjectRoot); err != nil {
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

func checkFixturesOnDisk(sensorMap map[string]interface{}, projectRoot string) error {
	ver, ok := sensorMap["verification"].(map[string]interface{})
	if !ok {
		return nil // schema will catch this later
	}
	cases, ok := ver["golden_cases"].([]interface{})
	if !ok {
		return nil
	}
	for _, raw := range cases {
		gc, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		rel, _ := gc["fixture"].(string)
		if rel == "" {
			continue
		}
		full := filepath.Join(projectRoot, rel)
		if _, err := os.Stat(full); err != nil {
			return &MissingFixtureError{Rel: rel, Full: full}
		}
	}
	return nil
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
	return os.Rename(tmpPath, path)
}
