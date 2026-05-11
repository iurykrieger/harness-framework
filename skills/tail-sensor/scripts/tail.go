//go:build tail_sensor

// tail returns Signals from a blocking sensor's signals.log starting
// from a 1-based line cursor, plus a final tail-envelope Signal that
// carries metadata.next_cursor for the agent to use on the next call.
//
// When the registry file does not exist, emits verdict=error /
// metadata.kind=tail_no_registry — sensor cannot be running.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

func main() {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tail: cwd:", err)
		os.Exit(2)
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	if len(reports) > 0 {
		_ = json.NewEncoder(os.Stdout).Encode(registry.RegistryMigratedSignal(res, reports, "tail-sensor"))
	}
	exit, sig := runTail(res, os.Args[1:])
	_ = json.NewEncoder(os.Stdout).Encode(sig)
	os.Exit(exit)
}

// runTail resolves the sensor entry, reads the signals.log from the
// given cursor, and returns an exit code plus the final Signal to emit.
// Individual log lines are written directly to os.Stdout by the caller
// (main) via the streamed output approach; the returned Signal is the
// tail envelope.
func runTail(res registry.Result, args []string) (int, map[string]interface{}) {
	// Lazy validator init — set stderr to os.Stderr for the real path;
	// errors here are rare and only affect schema validation of emitted
	// signals.
	return runTailWithWriter(res, args, os.Stdout, os.Stderr)
}

func runTailWithWriter(res registry.Result, args []string, stdout, stderr io.Writer) (int, map[string]interface{}) {
	if len(args) < 2 {
		sig := simpleErrSignal(res, "tail", "tail_invalid_args", "expected <sensor.id>[/<run.id>] <cursor>")
		return 2, sig
	}

	// Parse first arg as sensorID[/runID].
	arg := args[0]
	var sensorID, runID string
	if i := strings.IndexByte(arg, '/'); i >= 0 {
		sensorID, runID = arg[:i], arg[i+1:]
	} else {
		sensorID = arg
	}

	cursor, err := strconv.Atoi(args[1])
	if err != nil || cursor < 0 {
		sig := simpleErrSignal(res, sensorID, "tail_invalid_cursor", fmt.Sprintf("cursor must be a non-negative integer, got %q", args[1]))
		return 1, sig
	}

	v, code := schema.LoadValidator("", stderr)
	if code != 0 {
		sig := simpleErrSignal(res, sensorID, "tail_failed", "schema validator init failed")
		return code, sig
	}

	if !res.Exists {
		sig := validateSignal(v,
			simpleErrSignal(res, sensorID, "tail_no_registry",
				fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", res.Root.RegistryFile())),
			sensorID, stderr)
		return 1, sig
	}

	r := res.Root
	rs := res.State

	// Resolve entry.
	var entry *registry.RunningSensorEntry
	if runID != "" {
		// Explicit run_id: look up directly.
		entry = rs.FindEntryByRunID(runID)
		if entry == nil {
			sig := validateSignal(v, simpleErrSignal(res, sensorID, "not_running", fmt.Sprintf("no live entry for sensor %q run_id %q", sensorID, runID)), sensorID, stderr)
			return 1, sig
		}
		if entry.SensorID != sensorID {
			sig := validateSignal(v, simpleErrSignal(res, sensorID, "run_id_sensor_mismatch",
				fmt.Sprintf("run_id %q belongs to sensor %q, not %q", runID, entry.SensorID, sensorID)), sensorID, stderr)
			return 1, sig
		}
	} else {
		// Bare sensor ID: check for ambiguity.
		entries := rs.FindEntries(sensorID)
		switch len(entries) {
		case 0:
			sig := validateSignal(v, simpleErrSignal(res, sensorID, "not_running", fmt.Sprintf("no live entry for %q", sensorID)), sensorID, stderr)
			return 1, sig
		case 1:
			entry = entries[0]
			runID = entry.RunID
		default:
			// Ambiguous: multiple active runs.
			runIDs := make([]interface{}, len(entries))
			for i, e := range entries {
				runIDs[i] = map[string]interface{}{"rationale": fmt.Sprintf("active run_id: %s", e.RunID)}
			}
			now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
			md := registry.DiagnoseMetadata(res)
			md["kind"] = "ambiguous_run"
			sig := map[string]interface{}{
				"sensor_id":   sensorID,
				"version":     "0.0.0",
				"run_id":      uuid.NewString(),
				"started_at":  now,
				"finished_at": now,
				"verdict":     "error",
				"severity":    "high",
				"confidence":  1.0,
				"evidence":    runIDs,
				"cost_actual": map[string]interface{}{"latency_ms": 0},
				"metadata":    md,
			}
			return 1, validateSignal(v, sig, sensorID, stderr)
		}
	}

	// Determine the signals.log path. Legacy run_ids ending in "-legacy"
	// fall back to the flat per-sensor path.
	sigsPath := r.SignalsLogRun(sensorID, entry.RunID)
	if strings.HasSuffix(entry.RunID, "-legacy") {
		sigsPath = r.LegacySignalsLog(sensorID)
	}

	f, err := os.Open(sigsPath)
	if err != nil {
		sig := validateSignal(v, simpleErrSignal(res, sensorID, "tail_failed", fmt.Sprintf("open signals.log: %v", err)), sensorID, stderr)
		return 1, sig
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	current := 0
	for sc.Scan() {
		current++
		if current <= cursor {
			continue
		}
		fmt.Fprintln(stdout, sc.Text())
	}
	envelope := tailEnvelope(res, sensorID, current)
	return 0, validateSignal(v, envelope, sensorID, stderr)
}

func tailEnvelope(res registry.Result, id string, nextCursor int) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := registry.DiagnoseMetadata(res)
	md["kind"] = "envelope"
	md["next_cursor"] = nextCursor
	md["sensor_id"] = id // legacy field, do not remove
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "tail envelope"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}

func validateSignal(v *schema.Validator, sig map[string]interface{}, id string, stderr io.Writer) map[string]interface{} {
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(stderr, "tail: BUG: emitted signal failed signal.json validation: %v\n", err)
		now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		return map[string]interface{}{
			"sensor_id":   id,
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

func simpleErrSignal(res registry.Result, id, kind, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := registry.DiagnoseMetadata(res)
	md["kind"] = kind
	return map[string]interface{}{
		"sensor_id":   id,
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
