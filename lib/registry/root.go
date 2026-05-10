package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// Source labels how Discover resolved the project root.
type Source string

const (
	// SourceEnv means HARNESS_REGISTRY_ROOT was honored.
	SourceEnv Source = "env"
	// SourceWalkUp means the sensors/ marker was found by walking up
	// from startDir.
	SourceWalkUp Source = "walk_up"
)

// envVarName is the env var Discover honors first.
const envVarName = "HARNESS_REGISTRY_ROOT"

// markerDir is the directory name Discover walks up looking for.
const markerDir = "sensors"

// DiscoveryError is returned when neither HARNESS_REGISTRY_ROOT nor a
// sensors/ marker resolved a project root. Callers can use errors.As to
// distinguish discovery errors from parse or I/O failures.
type DiscoveryError struct {
	StartDir string
	EnvValue string // raw env var contents, may be empty
	Reason   string // brief reason ("env not absolute", "marker not found", ...)
}

func (e *DiscoveryError) Error() string {
	return fmt.Sprintf(
		"registry root discovery failed: %s. HARNESS_REGISTRY_ROOT=%q, started walk-up from %q. "+
			"Either run from a directory inside a project that contains %q/, or set HARNESS_REGISTRY_ROOT to the project's absolute path.",
		e.Reason, e.EnvValue, e.StartDir, markerDir,
	)
}

// Discover resolves the project root using HARNESS_REGISTRY_ROOT first,
// then walking up from startDir looking for a sensors/ directory.
//
// HARNESS_REGISTRY_ROOT takes precedence because it is the operator's
// explicit override — useful when invoking skills from outside the project
// tree (CI, shell scripts). When unset, the walk-up mirrors the schema
// discovery pattern in lib/schema, but looks for sensors/ (the user-project
// tree) rather than schemas/ (the plugin tree).
//
// EvalSymlinks is applied to the env-var path so that a symlink pointing
// to a directory is treated as a valid directory root; the resolved path
// is returned so all downstream callers see a stable canonical path.
//
// Errors:
//   - HARNESS_REGISTRY_ROOT is set but not absolute, not an existing
//     directory, or otherwise unreachable.
//   - Walk-up reached the filesystem root with no sensors/ found.
//
// startDir is the caller's anchor (typically os.Getwd()). It is only
// consulted when HARNESS_REGISTRY_ROOT is unset/empty.
func Discover(startDir string) (string, Source, error) {
	if env := os.Getenv(envVarName); env != "" {
		root, err := validateEnvRoot(env)
		if err != nil {
			return "", "", &DiscoveryError{
				StartDir: startDir,
				EnvValue: env,
				Reason:   err.Error(),
			}
		}
		return root, SourceEnv, nil
	}
	root, err := walkUpForMarker(startDir)
	if err != nil {
		return "", "", &DiscoveryError{
			StartDir: startDir,
			EnvValue: "",
			Reason:   err.Error(),
		}
	}
	return root, SourceWalkUp, nil
}

// validateEnvRoot enforces the env-var contract: absolute, EvalSymlinks
// resolves, and the resolved path is an existing directory.
func validateEnvRoot(env string) (string, error) {
	if !filepath.IsAbs(env) {
		return "", fmt.Errorf("HARNESS_REGISTRY_ROOT must be an absolute path, got %q", env)
	}
	resolved, err := filepath.EvalSymlinks(env)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("HARNESS_REGISTRY_ROOT path does not exist: %q", env)
		}
		return "", fmt.Errorf("HARNESS_REGISTRY_ROOT eval symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("HARNESS_REGISTRY_ROOT stat: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("HARNESS_REGISTRY_ROOT is not a directory: %q", env)
	}
	return resolved, nil
}

// walkUpForMarker walks parent-by-parent from startDir looking for a
// directory whose sensors/ child is itself a directory (symlinks to dirs
// accepted via os.Stat; emptiness allowed). Returns the absolute path of
// the matched ancestor, or an error when the filesystem root is reached.
func walkUpForMarker(startDir string) (string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("abs(%q): %w", startDir, err)
	}
	for {
		candidate := filepath.Join(abs, markerDir)
		info, err := os.Stat(candidate) // os.Stat follows symlinks
		if err == nil && info.IsDir() {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("%s/ marker not found walking up from %q", markerDir, startDir)
		}
		abs = parent
	}
}

// Result aggregates discovery and state load in one return value so
// each skill needs a single call.
type Result struct {
	Root        Root           // ready-to-use, anchored at ProjectRoot
	ProjectRoot string         // absolute path
	Source      Source         // SourceEnv or SourceWalkUp
	Exists      bool           // running_sensors.json present on disk
	State       RunningSensors // {Version: 1, Entries: nil} if !Exists
}

// Lookup resolves the project root, builds a Root, and loads the
// registry state. "Registry file does not exist" is NOT an error — it
// is reported as Result.Exists == false with an empty State.
//
// Errors mirror Discover (returns *DiscoveryError) plus parse failures
// from a malformed running_sensors.json on disk.
func Lookup(startDir string) (Result, error) {
	root, source, err := Discover(startDir)
	if err != nil {
		return Result{}, err
	}
	r := NewRoot(root)
	state, exists, err := LoadOrEmpty(r)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Root:        r,
		ProjectRoot: root,
		Source:      source,
		Exists:      exists,
		State:       state,
	}, nil
}

// DiagnoseMetadata returns the standard registry-discovery diagnostic
// fields for embedding in any Signal's metadata. Skills should use this
// instead of inlining the three-field literal so adding or renaming a
// diagnose field is a one-place change.
//
// The map contains exactly:
//   - registry_path:   absolute path to running_sensors.json
//   - registry_source: "env" | "walk_up"
//   - registry_exists: bool indicating whether the file is on disk
//
// Callers MUST merge these into their signal's metadata map (or copy
// the fields), not replace metadata wholesale — other fields (kind,
// entries, counts, etc.) are owned by the caller.
func DiagnoseMetadata(res Result) map[string]interface{} {
	return map[string]interface{}{
		"registry_path":   res.Root.RegistryFile(),
		"registry_source": string(res.Source),
		"registry_exists": res.Exists,
	}
}

// DiscoveryErrorSignal builds an error Signal describing a failed
// registry-root discovery. The returned map satisfies signal.json
// (verdict=error, severity=high, all required envelope fields). The
// helper is exported for the four registry-touching skills so each
// emits the same shape.
//
// sensorID is the id field carried on the signal — pass the skill's
// fixed name (e.g., "list-sensors") for skills that don't take a
// sensor argument, or the user-supplied sensor id for /start-sensor.
//
// metadata.registry_path is intentionally OMITTED: discovery failed,
// so no path was resolved.
func DiscoveryErrorSignal(err error, sensorID string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	rationale := "registry root discovery failed: <nil error>"
	if err != nil {
		rationale = err.Error()
	}
	return map[string]interface{}{
		"sensor_id":   sensorID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "registry_discovery_failed"},
	}
}
