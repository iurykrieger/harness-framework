package registry

import (
	"errors"

	"golang.org/x/sys/unix"
)

// IsPIDAlive returns true when pid > 0 and the process can be signalled
// (signal 0 is the standard POSIX existence probe). Permission errors
// (EPERM) also indicate the PID exists — we just don't own it.
func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, unix.EPERM)
}
