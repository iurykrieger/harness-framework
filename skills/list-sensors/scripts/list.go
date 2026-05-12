//go:build list_sensors

// list reads .harness/runtime/running_sensors.json (resolved via
// registry.Lookup, NOT os.Getwd()), annotates each entry with PID
// liveness, and emits one Signal verdict=pass / metadata.kind=list.
//
// When the registry file does not exist, emits verdict=warn with
// remediation pointing at HARNESS_REGISTRY_ROOT.
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
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "list: cwd:", err)
		os.Exit(2)
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, "list-sensors"))
		os.Exit(1)
	}
	os.Exit(runList(res, reports, os.Stdout, os.Stderr))
}

func runList(res registry.Result, reports []registry.SanitizeReport, stdout, stderr io.Writer) int {
	if len(reports) > 0 {
		_ = json.NewEncoder(stdout).Encode(registry.RegistryMigratedSignal(res, reports, "list-sensors"))
	}
	v, code := schema.LoadValidator("", stderr)
	if code != 0 {
		_ = json.NewEncoder(stdout).Encode(errorListSignal(res, "schema validator init failed"))
		return code
	}

	r := res.Root
	rs := res.State

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	if !res.Exists {
		sig := map[string]interface{}{
			"sensor_id":   "list-sensors",
			"version":     "0.0.0",
			"run_id":      uuid.NewString(),
			"started_at":  now,
			"finished_at": now,
			"verdict":     "warn",
			"severity":    "info",
			"confidence":  1.0,
			"evidence": []interface{}{
				map[string]interface{}{
					"rationale": fmt.Sprintf(
						"registry not found at %s. /start-sensor was likely run from a different cwd, or this project has no live blocking sensors. "+
							"If you expect sensors to be live, set HARNESS_REGISTRY_ROOT to the project root used at start time, or rerun /list-sensors from within that project.",
						r.RegistryFile(),
					),
				},
			},
			"cost_actual": map[string]interface{}{"latency_ms": 0},
			"metadata":    listMetadata(res, []interface{}{}),
		}
		_ = json.NewEncoder(stdout).Encode(validateSignal(v, sig, stderr))
		return 0
	}

	entries := make([]interface{}, 0, len(rs.Entries))
	for _, e := range rs.Entries {
		pidAlive := registry.IsPIDAlive(e.PID)
		state := "running"
		if !pidAlive {
			state = "orphan"
		}
		entry := map[string]interface{}{
			"sensor_id":        e.SensorID,
			"run_id":           e.RunID,
			"blocking":         e.Blocking,
			"pid":              e.PID,
			"pid_alive":        pidAlive,
			"started_at":       e.StartedAt,
			"command":          e.Command,
			"held_by":          heldBySummaries(e.HeldBy),
			"signals_log_path": r.SignalsLog(e.SensorID),
			"state":            state,
		}
		if e.Blocking {
			entry["watcher_pid"] = e.WatcherPID
			entry["watcher_alive"] = registry.IsPIDAlive(e.WatcherPID)
		}
		entries = append(entries, entry)
	}
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
		"metadata":    listMetadata(res, entries),
	}
	_ = json.NewEncoder(stdout).Encode(validateSignal(v, sig, stderr))
	return 0
}

// listMetadata builds the metadata map shared by both verdict branches.
// All entries except "entries" are diagnostic (where the registry was
// looked up and how).
func listMetadata(res registry.Result, entries []interface{}) map[string]interface{} {
	md := registry.DiagnoseMetadata(res)
	md["kind"] = "list"
	md["entries"] = entries
	return md
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

func errorListSignal(res registry.Result, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := registry.DiagnoseMetadata(res)
	md["kind"] = "list_failed"
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
		"metadata":    md,
	}
}
