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
