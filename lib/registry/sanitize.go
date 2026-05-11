package registry

import "fmt"

// InvalidEntryError is returned by ValidateEntry when a RunningSensorEntry
// violates a PID non-negativity invariant. Save propagates this unwrapped
// so callers can errors.As(err, new(*InvalidEntryError)).
type InvalidEntryError struct {
	SensorID string
	Field    string // "pid" | "pgid" | "watcher_pid" | "held_by[i].pid"
	Value    int
}

func (e *InvalidEntryError) Error() string {
	return fmt.Sprintf("registry: invalid %s=%d for sensor %q", e.Field, e.Value, e.SensorID)
}

// ValidateEntry enforces the PID non-negativity invariant.
// Returns nil if valid; otherwise *InvalidEntryError naming the first
// offending field.
//
// Rules:
//   - PID must be > 0
//   - PGID must be > 0
//   - WatcherPID must be >= 0 (0 means "no watcher", as in the orchestrator path)
//   - HeldBy[i].PID must be >= 0 always; when Kind == "sensor", it must be > 0.
func ValidateEntry(e RunningSensorEntry) error {
	if e.PID < 1 {
		return &InvalidEntryError{SensorID: e.SensorID, Field: "pid", Value: e.PID}
	}
	if e.PGID < 1 {
		return &InvalidEntryError{SensorID: e.SensorID, Field: "pgid", Value: e.PGID}
	}
	if e.WatcherPID < 0 {
		return &InvalidEntryError{SensorID: e.SensorID, Field: "watcher_pid", Value: e.WatcherPID}
	}
	for i, h := range e.HeldBy {
		if h.PID < 0 {
			return &InvalidEntryError{SensorID: e.SensorID, Field: fmt.Sprintf("held_by[%d].pid", i), Value: h.PID}
		}
		if h.Kind == "sensor" && h.PID < 1 {
			return &InvalidEntryError{SensorID: e.SensorID, Field: fmt.Sprintf("held_by[%d].pid", i), Value: h.PID}
		}
	}
	return nil
}
