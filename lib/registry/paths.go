// Package registry owns the .runtime/sensors/ directory layout, atomic
// state-file writes, file locks, PID liveness checks, and held_by
// refcount management for blocking-sensor runs.
package registry

import "path/filepath"

// Root is the absolute path to a project's .runtime/sensors/ directory.
// All registry helpers are methods on Root so tests can pivot to a temp
// directory by constructing a Root around it.
type Root struct {
	projectRoot string
}

// NewRoot returns a Root anchored at <projectRoot>/.runtime/sensors/.
func NewRoot(projectRoot string) Root {
	return Root{projectRoot: projectRoot}
}

// SensorsDir is the directory holding running_sensors.json and per-sensor
// subdirectories.
func (r Root) SensorsDir() string {
	return filepath.Join(r.projectRoot, ".runtime", "sensors")
}

// RegistryFile is the absolute path to running_sensors.json.
func (r Root) RegistryFile() string {
	return filepath.Join(r.SensorsDir(), "running_sensors.json")
}

// LockFile is the sibling lock used by WithFileLock.
func (r Root) LockFile() string {
	return filepath.Join(r.SensorsDir(), "running_sensors.lock")
}

// SensorDir returns the per-sensor directory under .runtime/sensors/<id>/.
func (r Root) SensorDir(id string) string {
	return filepath.Join(r.SensorsDir(), id)
}

// RawLog is the per-sensor raw subprocess output file.
func (r Root) RawLog(id string) string {
	return filepath.Join(r.SensorDir(id), "raw.log")
}

// SignalsLog is the per-sensor JSONL signals file written by the watcher.
func (r Root) SignalsLog(id string) string {
	return filepath.Join(r.SensorDir(id), "signals.log")
}

// ProjectRoot returns the absolute path of the project root that anchors
// this Root. Exposed so callers in lib/orchestrator can derive sibling
// paths (like .runtime/sensors/<id>/<run_id>/) without re-computing the
// walk from cwd.
func (r Root) ProjectRoot() string {
	return r.projectRoot
}

// RunDir returns the per-run directory under .runtime/sensors/<id>/<runID>/.
func (r Root) RunDir(id, runID string) string {
	return filepath.Join(r.SensorDir(id), runID)
}

// RawLogRun is the raw subprocess output file for one run.
func (r Root) RawLogRun(id, runID string) string {
	return filepath.Join(r.RunDir(id, runID), "raw.log")
}

// SignalsLogRun is the parsed JSONL signals file for one run.
func (r Root) SignalsLogRun(id, runID string) string {
	return filepath.Join(r.RunDir(id, runID), "signals.log")
}

// LegacyRawLog is the flat (pre-runID) raw.log path. Read-only fallback
// for entries migrated from before run-id-aware layouts existed.
func (r Root) LegacyRawLog(id string) string {
	return filepath.Join(r.SensorDir(id), "raw.log")
}

// LegacySignalsLog is the flat (pre-runID) signals.log path. Read-only
// fallback; mirrors LegacyRawLog.
func (r Root) LegacySignalsLog(id string) string {
	return filepath.Join(r.SensorDir(id), "signals.log")
}
