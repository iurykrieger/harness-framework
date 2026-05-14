package watcher

import (
	"syscall"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// IsSubprocessAlive is the exported variant of isSubprocessAlive, available
// to other packages (notably lib/orchestrator's stopBlockingDep) that need
// to distinguish zombies from genuinely running subprocesses.
func IsSubprocessAlive(pid int) bool {
	return isSubprocessAlive(pid)
}

// isSubprocessAlive returns true when pid is a running (non-zombie) process.
// Crucially, this distinguishes zombies (kernel still has the entry until
// reaped) from genuinely running processes — kill(pid, 0) cannot, and
// returns true for both.
//
// When pid IS our child (i.e., we spawned it via SpawnDetached and the kernel
// still has us as parent), Wait4 with WNOHANG reaps the zombie as a side
// effect when the subprocess has exited. That is by design: once the health
// gate has decided "this dep is dead", the zombie should not linger.
//
// When pid is NOT our child (re-attach path: another process spawned the
// dep), Wait4 returns ECHILD; we then fall back to kill(pid, 0) which may
// still see a zombie owned by a different parent. Re-attach does not run
// the health gate today, so this fallback is conservative — better to
// (rarely) misclassify a zombie as alive than to spuriously misclassify a
// genuinely running process as dead.
func isSubprocessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if !registry.IsPIDAlive(pid) {
		return false
	}
	var ws syscall.WaitStatus
	wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
	if err != nil {
		// ECHILD: not our child (re-attach case). Fall back to kill(0)'s
		// answer, which is already known to be "exists" — best effort.
		return true
	}
	if wpid == pid {
		// Subprocess has exited; Wait4 just reaped the zombie.
		return false
	}
	// wpid == 0: WNOHANG returned because the subprocess is still running.
	return true
}
