package subprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// DetachConfig is the input to SpawnDetached.
type DetachConfig struct {
	Command string            // raw shell, executed via sh -c
	Env     map[string]string // additional env vars
	LogFile string            // stdout+stderr redirected here (append, mode 0644)
	Dir     string            // working directory for the subprocess (empty = inherit)
}

// DetachResult holds the spawned subprocess identity. The caller is
// responsible for kill(-PGID, SIG…) when shutting down.
type DetachResult struct {
	PID  int
	PGID int
}

// SpawnDetached spawns sh -c <Command> in a new session and process group
// (Setsid:true), redirects stdout+stderr to LogFile (open in append
// mode), and returns once the child has been started. The caller does
// NOT receive a Cmd handle — the process outlives the caller's lifetime
// by design (this is for blocking sensors).
func SpawnDetached(cfg DetachConfig) (DetachResult, error) {
	if cfg.Command == "" {
		return DetachResult{}, errors.New("detach: empty command")
	}
	if cfg.LogFile == "" {
		return DetachResult{}, errors.New("detach: empty log file")
	}
	logF, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return DetachResult{}, fmt.Errorf("open log: %w", err)
	}
	defer logF.Close() // child inherits the open fd; we close our own.

	cmd := exec.Command("sh", "-c", cfg.Command)
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	if len(cfg.Env) > 0 {
		envList := append([]string{}, os.Environ()...)
		for k, v := range cfg.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = envList
	}

	if err := cmd.Start(); err != nil {
		return DetachResult{}, fmt.Errorf("start: %w", err)
	}
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// Setsid implies pgid == pid; fall back if Getpgid races on macOS.
		pgid = pid
	}
	// Release: do not Wait() here. Caller (or the watcher's reaper) waits.
	if err := cmd.Process.Release(); err != nil {
		return DetachResult{}, fmt.Errorf("release: %w", err)
	}
	return DetachResult{PID: pid, PGID: pgid}, nil
}
