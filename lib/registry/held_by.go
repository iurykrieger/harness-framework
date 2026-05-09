package registry

import "os"

// AddHolder appends h to entry.HeldBy. Caller is responsible for not
// adding duplicates of the same (Kind, ID, PID) tuple.
func AddHolder(entry *RunningSensorEntry, h HeldByEntry) {
	entry.HeldBy = append(entry.HeldBy, h)
}

// RemoveHolder drops the FIRST entry in HeldBy matching match. For
// kind=manual only Kind needs to match. For kind=sensor, both ID and
// PID must match (so concurrent runs of the same dependent — different
// orchestrator processes — don't release each other's hold).
//
// Returns true when an entry was removed.
func RemoveHolder(entry *RunningSensorEntry, match HeldByEntry) bool {
	for i, h := range entry.HeldBy {
		if !holdersMatch(h, match) {
			continue
		}
		entry.HeldBy = append(entry.HeldBy[:i], entry.HeldBy[i+1:]...)
		return true
	}
	return false
}

// IsHeld returns true when HeldBy is non-empty.
func IsHeld(entry *RunningSensorEntry) bool {
	return len(entry.HeldBy) > 0
}

// ReapDead removes every kind=sensor holder whose PID is no longer alive
// and returns the removed entries (so the caller can surface them as
// evidence). Manual holders are never reaped — they do not have a PID.
func ReapDead(entry *RunningSensorEntry) []HeldByEntry {
	var reaped []HeldByEntry
	keep := entry.HeldBy[:0]
	for _, h := range entry.HeldBy {
		if h.Kind == "sensor" && !IsPIDAlive(h.PID) {
			reaped = append(reaped, h)
			continue
		}
		keep = append(keep, h)
	}
	entry.HeldBy = keep
	return reaped
}

// SelfPID is exported for tests that need the running process's PID.
func SelfPID() int { return os.Getpid() }

func holdersMatch(a, b HeldByEntry) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == "sensor" {
		return a.ID == b.ID && a.PID == b.PID
	}
	return true
}
