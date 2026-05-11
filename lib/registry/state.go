package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// RunningSensors is the schema of running_sensors.json.
type RunningSensors struct {
	Version int                  `json:"version"`
	Entries []RunningSensorEntry `json:"entries"`
}

// RunningSensorEntry is one live blocking sensor's state.
type RunningSensorEntry struct {
	SensorID       string          `json:"sensor_id"`
	RunID          string          `json:"run_id"`
	Blocking       bool            `json:"blocking"`
	PID            int             `json:"pid"`
	PGID           int             `json:"pgid"`
	WatcherPID     int             `json:"watcher_pid"`
	StartedAt      string          `json:"started_at"`
	Command        string          `json:"command"`
	LogDir         string          `json:"log_dir"`
	HeldBy         []HeldByEntry   `json:"held_by"`
	SubprocessExit *SubprocessExit `json:"subprocess_exit,omitempty"`
}

// HeldByEntry is a discriminated record: kind=manual carries only
// AttachedAt; kind=sensor carries ID and PID of the dependent holder.
type HeldByEntry struct {
	Kind       string `json:"kind"` // "manual" or "sensor"
	ID         string `json:"id,omitempty"`
	PID        int    `json:"pid,omitempty"`
	AttachedAt string `json:"attached_at"`
}

// SubprocessExit is set by the watcher's reaper after wait(). Absent
// while the subprocess is still running.
type SubprocessExit struct {
	Code     int    `json:"code"`
	ExitedAt string `json:"exited_at"`
}

// Load reads running_sensors.json. A missing file returns an empty,
// version-1 RunningSensors (the canonical "no live sensors" state).
func Load(r Root) (RunningSensors, error) {
	data, err := os.ReadFile(r.RegistryFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RunningSensors{Version: 1}, nil
		}
		return RunningSensors{}, fmt.Errorf("read registry: %w", err)
	}
	var rs RunningSensors
	if err := json.Unmarshal(data, &rs); err != nil {
		return RunningSensors{}, fmt.Errorf("parse registry: %w", err)
	}
	if rs.Version == 0 {
		rs.Version = 1
	}
	return rs, nil
}

// Save writes running_sensors.json atomically (temp + rename). Each
// entry is validated via ValidateEntry before any bytes are written;
// the first invalid entry causes Save to return *InvalidEntryError
// without touching the file. The caller is expected to be holding the
// registry flock.
func Save(r Root, rs RunningSensors) error {
	for _, e := range rs.Entries {
		if err := ValidateEntry(e); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		return fmt.Errorf("mkdir sensors dir: %w", err)
	}
	data, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := r.RegistryFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, r.RegistryFile()); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// FindEntry returns a pointer to the entry for sensor id, or nil.
func (rs *RunningSensors) FindEntry(id string) *RunningSensorEntry {
	for i := range rs.Entries {
		if rs.Entries[i].SensorID == id {
			return &rs.Entries[i]
		}
	}
	return nil
}

// RemoveEntry deletes the entry matching id (no-op if absent).
func (rs *RunningSensors) RemoveEntry(id string) {
	out := rs.Entries[:0]
	for _, e := range rs.Entries {
		if e.SensorID != id {
			out = append(out, e)
		}
	}
	rs.Entries = out
}

// LoadOrEmpty reads running_sensors.json and reports existence
// explicitly:
//   - file present and parseable → (state, true, nil)
//   - file absent                → (RunningSensors{Version: 1}, false, nil)
//   - file present but malformed → (zero, false, parse error)
//
// Load is preserved unchanged for callers that do not care about
// existence (orchestrator, watcher).
func LoadOrEmpty(r Root) (RunningSensors, bool, error) {
	data, err := os.ReadFile(r.RegistryFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RunningSensors{Version: 1}, false, nil
		}
		return RunningSensors{}, false, fmt.Errorf("read registry: %w", err)
	}
	var rs RunningSensors
	if err := json.Unmarshal(data, &rs); err != nil {
		return RunningSensors{}, false, fmt.Errorf("parse registry: %w", err)
	}
	if rs.Version == 0 {
		rs.Version = 1
	}
	return rs, true, nil
}

// LoadSanitized loads running_sensors.json, applies SanitizeAll, and
// best-effort re-persists when any mutation occurred. Returns the
// sanitized in-memory state plus the migration reports so callers can
// surface a warn Signal.
//
// A failure to re-Save the sanitized state is silenced: the in-memory
// state is still correct, persistence retries on the next invocation.
// A Load failure (parse error, I/O error) returns (zero, nil, err)
// untouched.
func LoadSanitized(r Root) (RunningSensors, []SanitizeReport, error) {
	rs, err := Load(r)
	if err != nil {
		return rs, nil, err
	}
	reports := SanitizeAll(&rs)
	if len(reports) > 0 {
		_ = WithFileLock(r.LockFile(), func() error { return Save(r, rs) })
	}
	return rs, reports, nil
}
