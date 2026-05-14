// Package watcher launches a watcher subprocess that tails a sensor's
// raw stdout log file, applies the sensor's output_parsing patterns, and
// writes parsed Signals to signals.log.
package watcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// SpawnOpts captures everything needed to launch a watcher subprocess.
type SpawnOpts struct {
	PluginRoot     string
	ProjectRoot    string
	SensorID       string
	RunID          string
	RawLogPath     string
	SignalsLogPath string
	EnvelopeJSON   []byte
	PatternsJSON   []byte
	SubprocessPID  int
	WatcherLogPath string
}

// SpawnFn is the spawner used by Spawn. Tests override this to avoid
// invoking the real `go run` subprocess.
var SpawnFn = realSpawn

// RealSpawn is the production spawner exported for tests that have
// (via a package-level TestMain) replaced SpawnFn with a fake and need
// to opt back into the real implementation for one specific test. The
// usual pattern is:
//
//	prev := watcher.SpawnFn
//	watcher.SpawnFn = watcher.RealSpawn
//	t.Cleanup(func() { watcher.SpawnFn = prev })
//
// Production code MUST go through Spawn, not RealSpawn — RealSpawn
// bypasses the SpawnFn indirection that tests rely on.
func RealSpawn(opts SpawnOpts) (int, error) {
	return realSpawn(opts)
}

// earlyDeathTimeout is how long realSpawn polls to confirm the watcher
// process is still alive before returning its PID. Tests may override
// this to speed up or widen the early-death probe window.
var earlyDeathTimeout = 100 * time.Millisecond

// Spawn launches the watcher subprocess via SpawnFn.
func Spawn(opts SpawnOpts) (int, error) {
	return SpawnFn(opts)
}

// realSpawn invokes `go -C <pluginRoot> run -tags=start_watcher
// ./skills/start-sensor/scripts` with the watcher env vars, returning
// the spawned process's PID after a 100ms early-death probe to catch
// compile errors.
func realSpawn(opts SpawnOpts) (int, error) {
	if opts.PluginRoot == "" {
		return 0, errors.New("plugin root not set (set CLAUDE_PLUGIN_ROOT)")
	}

	logPath := opts.WatcherLogPath
	if logPath == "" {
		logPath = filepath.Join(filepath.Dir(opts.SignalsLogPath), "watcher.log")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open watcher.log: %w", err)
	}

	cmd := exec.Command("go", "-C", opts.PluginRoot, "run",
		"-tags=start_watcher", "./skills/start-sensor/scripts")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GOCACHE=" + os.Getenv("GOCACHE"),
		"GOPATH=" + os.Getenv("GOPATH"),
		"GOWORK=off",
		"HARNESS_WATCHER_RAW=" + opts.RawLogPath,
		"HARNESS_WATCHER_SIGNALS=" + opts.SignalsLogPath,
		"HARNESS_WATCHER_PATTERNS=" + string(opts.PatternsJSON),
		"HARNESS_WATCHER_ENVELOPE=" + string(opts.EnvelopeJSON),
		fmt.Sprintf("HARNESS_WATCHER_SUBPROCESS_PID=%d", opts.SubprocessPID),
		"HARNESS_WATCHER_REGISTRY_ROOT=" + opts.ProjectRoot,
		"HARNESS_WATCHER_SENSOR_ID=" + opts.SensorID,
		"HARNESS_WATCHER_RUN_ID=" + opts.RunID,
	}
	cmd.Stdout = nil
	cmd.Stderr = logFile
	cmd.SysProcAttr = &sysProcAttr

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start watcher: %w", err)
	}

	if alive, exitCode := earlyDeathProbe(cmd.Process.Pid, earlyDeathTimeout); !alive {
		stderrTail, _ := os.ReadFile(logPath)
		_ = logFile.Close()
		return 0, fmt.Errorf("watcher exited early (code %d): %s", exitCode, string(stderrTail))
	}

	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return pid, nil
}

// earlyDeathProbe polls Wait4 with WNOHANG for up to dur to detect an
// unexpected early exit. Returns (alive=true, 0) when the process is
// still running after the window. Returns (alive=false, exitCode) when
// the process exits within the window; exitCode is -1 if the Wait4 call
// itself fails. No goroutines are spawned — polling is direct kernel
// state inspection and consistent across Linux and macOS.
func earlyDeathProbe(pid int, dur time.Duration) (bool, int) {
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if err != nil {
			return false, -1
		}
		if wpid == pid {
			return false, ws.ExitStatus()
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true, 0
}
