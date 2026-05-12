// Package watcher launches a watcher subprocess that tails a sensor's
// raw stdout log file, applies the sensor's output_parsing patterns, and
// writes parsed Signals to signals.log. Extracted from
// skills/start-sensor/scripts/start.go so both /start-sensor and the
// orchestrator's startBlockingDep can spawn watchers via the same code path.
package watcher

import (
	"fmt"
	"os"
	"path/filepath"
)

// SpawnOpts captures everything needed to launch a watcher subprocess.
type SpawnOpts struct {
	ProjectRoot    string
	SensorID       string
	RunID          string
	RawLogPath     string
	SignalsLogPath string
	EnvelopeJSON   []byte
	PatternsJSON   []byte
	SubprocessPID  int
	// WatcherLogPath is where the watcher's own stderr is appended.
	// Defaults to <dir of SignalsLogPath>/watcher.log when empty.
	WatcherLogPath string
}

// Spawn launches the watcher binary detached. Returns the watcher's PID
// (captured before Release, so the registry's non-negativity invariant
// is preserved on Unix). On error the returned PID is 0.
func Spawn(opts SpawnOpts) (int, error) {
	bin, err := BinaryPath()
	if err != nil {
		return 0, fmt.Errorf("watcher binary path: %w", err)
	}
	logPath := opts.WatcherLogPath
	if logPath == "" {
		logPath = filepath.Join(filepath.Dir(opts.SignalsLogPath), "watcher.log")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open watcher.log: %w", err)
	}
	proc, err := os.StartProcess(bin, []string{bin}, &os.ProcAttr{
		Env: []string{
			fmt.Sprintf("HARNESS_WATCHER_RAW=%s", opts.RawLogPath),
			fmt.Sprintf("HARNESS_WATCHER_SIGNALS=%s", opts.SignalsLogPath),
			fmt.Sprintf("HARNESS_WATCHER_PATTERNS=%s", string(opts.PatternsJSON)),
			fmt.Sprintf("HARNESS_WATCHER_ENVELOPE=%s", string(opts.EnvelopeJSON)),
			fmt.Sprintf("HARNESS_WATCHER_SUBPROCESS_PID=%d", opts.SubprocessPID),
			fmt.Sprintf("HARNESS_WATCHER_REGISTRY_ROOT=%s", opts.ProjectRoot),
			fmt.Sprintf("HARNESS_WATCHER_SENSOR_ID=%s", opts.SensorID),
			fmt.Sprintf("HARNESS_WATCHER_RUN_ID=%s", opts.RunID),
		},
		Files: []*os.File{nil, nil, logFile},
		Sys:   &sysProcAttr,
	})
	if err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start watcher: %w", err)
	}
	pid := proc.Pid
	_ = proc.Release()
	_ = logFile.Close() // parent's handle; child keeps its own fd open
	return pid, nil
}

// BinaryPath returns the absolute path of the watcher binary, which is
// expected to live alongside the caller's executable in production.
func BinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "watcher"), nil
}
