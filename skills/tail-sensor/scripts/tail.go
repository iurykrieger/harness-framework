//go:build tail_sensor

// tail returns Signals from a blocking sensor's signals.log starting
// from a 1-based line cursor, plus a final tail-envelope Signal that
// carries metadata.next_cursor for the agent to use on the next call.
//
// The first argument can be `<sensor.id>` or `<sensor.id>/<run.id>`.
// When the registry has multiple active runs of the same sensor, a bare
// id resolves to verdict=error / metadata.kind=ambiguous_run with the
// list of active run ids as evidence.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	b := cli.Bootstrap("tail-sensor", os.Stdout, os.Stderr)
	if b.ExitCode != 0 {
		os.Exit(b.ExitCode)
	}
	os.Exit(runTail(b, os.Args[1:], os.Stdout, os.Stderr))
}

func runTail(b cli.BootstrapResult, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder("tail", "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_invalid_args").
					WithRationale("expected <sensor.id>[/<run.id>] <cursor>").
					WithDiagnose(b.Diagnose).
					Build(),
				"tail", stderr))
		return 2
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
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(sensorID, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_invalid_cursor").
					WithRationale(fmt.Sprintf("cursor must be a non-negative integer, got %q", args[1])).
					WithDiagnose(b.Diagnose).
					Build(),
				sensorID, stderr))
		return 1
	}

	if !b.Res.Exists {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(sensorID, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_no_registry").
					WithRationale(fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", b.Res.Root.RegistryFile())).
					WithDiagnose(b.Diagnose).
					Build(),
				sensorID, stderr))
		return 1
	}

	r := b.Res.Root
	rs := b.Res.State

	// Resolve entry.
	var entry *registry.RunningSensorEntry
	if runID != "" {
		// Explicit run_id: look up directly.
		entry = rs.FindEntryByRunID(runID)
		if entry == nil {
			_ = json.NewEncoder(stdout).Encode(
				signal.ValidateOrEmergency(b.Validator,
					signal.NewBuilder(sensorID, "0.0.0").
						WithVerdict("error", "high").
						WithKind("not_running").
						WithRationale(fmt.Sprintf("no live entry for sensor %q run_id %q", sensorID, runID)).
						WithDiagnose(b.Diagnose).
						Build(),
					sensorID, stderr))
			return 1
		}
		if entry.SensorID != sensorID {
			_ = json.NewEncoder(stdout).Encode(
				signal.ValidateOrEmergency(b.Validator,
					signal.NewBuilder(sensorID, "0.0.0").
						WithVerdict("error", "high").
						WithKind("run_id_sensor_mismatch").
						WithRationale(fmt.Sprintf("run_id %q belongs to sensor %q, not %q", runID, entry.SensorID, sensorID)).
						WithDiagnose(b.Diagnose).
						Build(),
					sensorID, stderr))
			return 1
		}
	} else {
		// Bare sensor ID: check for ambiguity.
		entries := rs.FindEntries(sensorID)
		switch len(entries) {
		case 0:
			_ = json.NewEncoder(stdout).Encode(
				signal.ValidateOrEmergency(b.Validator,
					signal.NewBuilder(sensorID, "0.0.0").
						WithVerdict("error", "high").
						WithKind("not_running").
						WithRationale(fmt.Sprintf("no live entry for %q", sensorID)).
						WithDiagnose(b.Diagnose).
						Build(),
					sensorID, stderr))
			return 1
		case 1:
			entry = entries[0]
		default:
			// Ambiguous: multiple active runs.
			runIDs := make([]interface{}, len(entries))
			for i, e := range entries {
				runIDs[i] = map[string]interface{}{"rationale": fmt.Sprintf("active run_id: %s", e.RunID)}
			}
			_ = json.NewEncoder(stdout).Encode(
				signal.ValidateOrEmergency(b.Validator,
					signal.NewBuilder(sensorID, "0.0.0").
						WithVerdict("error", "high").
						WithKind("ambiguous_run").
						WithEvidence(runIDs).
						WithDiagnose(b.Diagnose).
						Build(),
					sensorID, stderr))
			return 1
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
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(sensorID, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_failed").
					WithRationale(fmt.Sprintf("open signals.log: %v", err)).
					WithDiagnose(b.Diagnose).
					Build(),
				sensorID, stderr))
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

	envelope := signal.NewBuilder(sensorID, "0.0.0").
		WithVerdict("pass", "info").
		WithKind("envelope").
		WithEvidence([]interface{}{map[string]interface{}{"rationale": "tail envelope"}}).
		WithMetadata(map[string]interface{}{
			"next_cursor": current,
			"sensor_id":   sensorID, // legacy field, do not remove
		}).
		WithDiagnose(b.Diagnose).
		Build()
	_ = json.NewEncoder(stdout).Encode(signal.ValidateOrEmergency(b.Validator, envelope, sensorID, stderr))
	return 0
}
