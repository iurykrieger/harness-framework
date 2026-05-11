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
	res, err := registry.Lookup(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	exit := runTail(res, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(exit)
}

func runTail(res registry.Result, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(res, "tail", "tail_invalid_args", "expected <sensor.id> <cursor>"))
		return 2
	}
	id := args[0]
	cursor, err := strconv.Atoi(args[1])
	if err != nil || cursor < 0 {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(res, id, "tail_invalid_cursor", fmt.Sprintf("cursor must be a non-negative integer, got %q", args[1])))
		return 1
	}

	v, code := schema.LoadValidator("", stderr)
	if code != 0 {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(res, id, "tail_failed", "schema validator init failed"))
		return code
	}

	if !res.Exists {
		_ = json.NewEncoder(stdout).Encode(validateSignal(v,
			simpleErrSignal(res, id, "tail_no_registry",
				fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", res.Root.RegistryFile())),
			id, stderr))
		return 1
	}

	r := res.Root
	rs := res.State
	entry := rs.FindEntry(id)
	if entry == nil {
		_ = json.NewEncoder(stdout).Encode(validateSignal(v, simpleErrSignal(res, id, "not_running", fmt.Sprintf("no live entry for %q", id)), id, stderr))
		return 1
	}

	f, err := os.Open(r.SignalsLog(id))
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(validateSignal(v, simpleErrSignal(res, id, "tail_failed", fmt.Sprintf("open signals.log: %v", err)), id, stderr))
		return 1
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
	envelope := tailEnvelope(res, id, current)
	_ = json.NewEncoder(stdout).Encode(validateSignal(v, envelope, id, stderr))
	return 0
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
