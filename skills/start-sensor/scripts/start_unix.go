//go:build start_sensor && (darwin || linux)

package main

import "syscall"

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
