package registry

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

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

// SanitizeReport records one mutation performed by SanitizeAll.
type SanitizeReport struct {
	SensorID string `json:"sensor_id"`
	Field    string `json:"field"`   // "watcher_pid" | "held_by[i].pid" | "pid" | "pgid"
	OldValue int    `json:"old_value"`
	Dropped  bool   `json:"dropped"` // entry or holder discarded entirely
}

// SanitizeAll rewrites legacy invalid PID fields in rs to safe values.
// Mutation is in-memory; caller persists via Save (which will succeed
// because ValidateEntry passes on the sanitized state).
//
// Rules, applied per entry:
//   - WatcherPID < 0       → rewrite to 0, report (Dropped: false).
//   - HeldByEntry.PID < 0 with Kind == "manual" → rewrite to 0, report (Dropped: false).
//   - HeldByEntry.PID < 1 with Kind == "sensor" → drop the holder, report (Dropped: true).
//   - PID < 1 or PGID < 1  → drop the entire entry, report (Dropped: true).
//
// Returns an empty slice when nothing changed.
func SanitizeAll(rs *RunningSensors) []SanitizeReport {
	if rs == nil {
		return nil
	}
	reports := make([]SanitizeReport, 0)
	keep := rs.Entries[:0]
	for _, e := range rs.Entries {
		if e.PID < 1 {
			reports = append(reports, SanitizeReport{SensorID: e.SensorID, Field: "pid", OldValue: e.PID, Dropped: true})
			continue
		}
		if e.PGID < 1 {
			reports = append(reports, SanitizeReport{SensorID: e.SensorID, Field: "pgid", OldValue: e.PGID, Dropped: true})
			continue
		}
		if e.WatcherPID < 0 {
			reports = append(reports, SanitizeReport{SensorID: e.SensorID, Field: "watcher_pid", OldValue: e.WatcherPID, Dropped: false})
			e.WatcherPID = 0
		}
		newHolders := e.HeldBy[:0]
		for i, h := range e.HeldBy {
			switch {
			case h.Kind == "sensor" && h.PID < 1:
				reports = append(reports, SanitizeReport{
					SensorID: e.SensorID,
					Field:    fmt.Sprintf("held_by[%d].pid", i),
					OldValue: h.PID,
					Dropped:  true,
				})
				continue
			case h.PID < 0:
				reports = append(reports, SanitizeReport{
					SensorID: e.SensorID,
					Field:    fmt.Sprintf("held_by[%d].pid", i),
					OldValue: h.PID,
					Dropped:  false,
				})
				h.PID = 0
			}
			newHolders = append(newHolders, h)
		}
		e.HeldBy = newHolders

		// Legacy migration: pre-spec entries lack RunID/Blocking. Synthesize a
		// <pid>-legacy run_id and assume blocking=true (start-sensor was the
		// only producer before this spec). LogDir is preserved as-is; the
		// *Run path helpers won't apply — read-only consumers fall back to
		// LegacyRawLog / LegacySignalsLog when RunID has the "-legacy" suffix.
		if e.RunID == "" {
			legacyRunID := fmt.Sprintf("%d-legacy", e.PID)
			reports = append(reports, SanitizeReport{
				SensorID: e.SensorID, Field: "run_id", OldValue: 0, Dropped: false,
			})
			e.RunID = legacyRunID
			e.Blocking = true
		}

		keep = append(keep, e)
	}
	rs.Entries = keep
	return reports
}

// RegistryMigratedSignal builds the precedence warn Signal emitted by
// the four registry-touching skills when SanitizeAll returns non-empty
// reports. The Signal carries DiagnoseMetadata fields plus the
// migration report list under metadata.reports, and is structured to
// pass signal.json validation.
//
// sensorID is the skill name ("list-sensors", "tail-sensor",
// "stop-sensor", "start-sensor") — same convention as DiscoveryErrorSignal.
func RegistryMigratedSignal(res Result, reports []SanitizeReport, sensorID string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	rewritten := 0
	dropped := 0
	for _, r := range reports {
		if r.Dropped {
			dropped++
		} else {
			rewritten++
		}
	}
	rationale := fmt.Sprintf("rewrote %d invalid PID field(s) and dropped %d entry/holder(s) in running_sensors.json", rewritten, dropped)
	md := DiagnoseMetadata(res)
	md["kind"] = "registry_migrated"
	md["reports"] = reports
	return map[string]interface{}{
		"sensor_id":   sensorID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "warn",
		"severity":    "low",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}
