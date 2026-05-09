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

// Save writes running_sensors.json atomically (temp + rename). The
// caller is expected to be holding the registry flock.
func Save(r Root, rs RunningSensors) error {
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
