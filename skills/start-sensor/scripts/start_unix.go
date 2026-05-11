//go:build start_sensor && (darwin || linux)

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

var watcherSysProcAttr = syscall.SysProcAttr{Setsid: true}

func watcherBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "watcher"), nil
}

// killGroup sends SIGKILL to the entire process group identified by pgid.
// Used to undo a just-spawned root subprocess when the watcher spawn
// fails inside the flock callback.
func killGroup(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// killPID sends SIGKILL to a single process.
func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
