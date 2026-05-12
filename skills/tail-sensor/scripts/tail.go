//go:build tail_sensor

// tail returns Signals from a blocking sensor's signals.log starting
// from a 1-based line cursor, plus a final tail-envelope Signal that
// carries metadata.next_cursor for the agent to use on the next call.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/iurykrieger/harness-framework/lib/cli"
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
					WithRationale("expected <sensor.id> <cursor>").
					WithDiagnose(b.Diagnose).
					Build(),
				"tail", stderr))
		return 2
	}
	id := args[0]
	cursor, err := strconv.Atoi(args[1])
	if err != nil || cursor < 0 {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(id, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_invalid_cursor").
					WithRationale(fmt.Sprintf("cursor must be a non-negative integer, got %q", args[1])).
					WithDiagnose(b.Diagnose).
					Build(),
				id, stderr))
		return 1
	}

	if !b.Res.Exists {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(id, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_no_registry").
					WithRationale(fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", b.Res.Root.RegistryFile())).
					WithDiagnose(b.Diagnose).
					Build(),
				id, stderr))
		return 1
	}

	r := b.Res.Root
	if b.Res.State.FindEntry(id) == nil {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(id, "0.0.0").
					WithVerdict("error", "high").
					WithKind("not_running").
					WithRationale(fmt.Sprintf("no live entry for %q", id)).
					WithDiagnose(b.Diagnose).
					Build(),
				id, stderr))
		return 1
	}

	f, err := os.Open(r.SignalsLog(id))
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(id, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_failed").
					WithRationale(fmt.Sprintf("open signals.log: %v", err)).
					WithDiagnose(b.Diagnose).
					Build(),
				id, stderr))
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

	envelope := signal.NewBuilder(id, "0.0.0").
		WithVerdict("pass", "info").
		WithKind("envelope").
		WithEvidence([]interface{}{map[string]interface{}{"rationale": "tail envelope"}}).
		WithMetadata(map[string]interface{}{
			"next_cursor": current,
			"sensor_id":   id, // legacy field, do not remove
		}).
		WithDiagnose(b.Diagnose).
		Build()
	_ = json.NewEncoder(stdout).Encode(signal.ValidateOrEmergency(b.Validator, envelope, id, stderr))
	return 0
}
