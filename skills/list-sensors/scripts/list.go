//go:build list_sensors

// list reads .runtime/sensors/running_sensors.json, annotates each
// entry with PID liveness, and emits one Signal verdict=pass,
// metadata.kind=list with the full table under metadata.entries.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "list: cwd:", err)
		os.Exit(2)
	}
	os.Exit(runList(root, os.Stdout, os.Stderr))
}

func runList(projectRoot string, stdout, stderr io.Writer) int {
	v, code := schema.LoadValidator("", stderr)
	if code != 0 {
		_ = json.NewEncoder(stdout).Encode(errorListSignal("schema validator init failed"))
		return code
	}

	r := registry.NewRoot(projectRoot)
	rs, err := registry.Load(r)
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(validateSignal(v, errorListSignal(fmt.Sprintf("load registry: %v", err)), stderr))
		return 1
	}
	entries := make([]interface{}, 0, len(rs.Entries))
	for _, e := range rs.Entries {
		pidAlive := registry.IsPIDAlive(e.PID)
		watcherAlive := registry.IsPIDAlive(e.WatcherPID)
		state := "running"
		if !pidAlive {
			state = "orphan"
		}
		entries = append(entries, map[string]interface{}{
			"sensor_id":        e.SensorID,
			"pid":              e.PID,
			"pid_alive":        pidAlive,
			"watcher_pid":      e.WatcherPID,
			"watcher_alive":    watcherAlive,
			"started_at":       e.StartedAt,
			"command":          e.Command,
			"held_by":          heldBySummaries(e.HeldBy),
			"signals_log_path": r.SignalsLog(e.SensorID),
			"state":            state,
		})
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	sig := map[string]interface{}{
		"sensor_id":   "list-sensors",
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": fmt.Sprintf("%d running sensor(s)", len(entries))}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":    "list",
			"entries": entries,
		},
	}
	_ = json.NewEncoder(stdout).Encode(validateSignal(v, sig, stderr))
	return 0
}

func heldBySummaries(hs []registry.HeldByEntry) []interface{} {
	out := make([]interface{}, 0, len(hs))
	for _, h := range hs {
		entry := map[string]interface{}{"kind": h.Kind, "attached_at": h.AttachedAt}
		if h.Kind == "sensor" {
			entry["id"] = h.ID
			entry["pid"] = h.PID
			entry["pid_alive"] = registry.IsPIDAlive(h.PID)
		}
		out = append(out, entry)
	}
	return out
}

// validateSignal checks sig against signal.json. If validation fails it logs
// the error to stderr and returns a minimal emergency signal so the bug
// surfaces without recursion. On success it returns sig unchanged.
func validateSignal(v *schema.Validator, sig map[string]interface{}, stderr io.Writer) map[string]interface{} {
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(stderr, "list: BUG: emitted signal failed signal.json validation: %v\n", err)
		now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		return map[string]interface{}{
			"sensor_id":   "list-sensors",
			"version":     "0.0.0",
			"run_id":      uuid.NewString(),
			"started_at":  now,
			"finished_at": now,
			"verdict":     "error",
			"severity":    "high",
			"confidence":  1.0,
			"evidence":    []interface{}{map[string]interface{}{"rationale": fmt.Sprintf("signal_validation_failed: %v", err)}},
			"cost_actual": map[string]interface{}{"latency_ms": 0},
			"metadata":    map[string]interface{}{"kind": "signal_validation_failed"},
		}
	}
	return sig
}

func errorListSignal(rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "list-sensors",
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "list_failed"},
	}
}
