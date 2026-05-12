//go:build list_sensors

// list reads the registry (resolved via cli.Bootstrap), annotates each
// entry with PID liveness, and emits one Signal verdict=pass /
// metadata.kind=list. When the registry file does not exist, emits
// verdict=warn pointing at HARNESS_REGISTRY_ROOT.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	b := cli.Bootstrap("list-sensors", os.Stdout, os.Stderr)
	if b.ExitCode != 0 {
		os.Exit(b.ExitCode)
	}
	os.Exit(runList(b, os.Stdout, os.Stderr))
}

func runList(b cli.BootstrapResult, stdout, stderr io.Writer) int {
	r := b.Res.Root
	rs := b.Res.State

	if !b.Res.Exists {
		sig := signal.NewBuilder("list-sensors", "0.0.0").
			WithVerdict("warn", "info").
			WithKind("list").
			WithRationale(fmt.Sprintf(
				"registry not found at %s. /start-sensor was likely run from a different cwd, or this project has no live blocking sensors. "+
					"If you expect sensors to be live, set HARNESS_REGISTRY_ROOT to the project root used at start time, or rerun /list-sensors from within that project.",
				r.RegistryFile(),
			)).
			WithMetadata(map[string]interface{}{"entries": []interface{}{}}).
			WithDiagnose(b.Diagnose).
			Build()
		_ = json.NewEncoder(stdout).Encode(signal.ValidateOrEmergency(b.Validator, sig, "list-sensors", stderr))
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
			"held_by":          registry.SummarizeHolders(e.HeldBy, registry.SummarizeOpts{}),
			"signals_log_path": r.SignalsLog(e.SensorID),
			"state":            state,
		}
		if e.Blocking {
			entry["watcher_pid"] = e.WatcherPID
			entry["watcher_alive"] = registry.IsPIDAlive(e.WatcherPID)
		}
		entries = append(entries, entry)
	}
	sig := signal.NewBuilder("list-sensors", "0.0.0").
		WithVerdict("pass", "info").
		WithKind("list").
		WithRationale(fmt.Sprintf("%d running sensor(s)", len(entries))).
		WithMetadata(map[string]interface{}{"entries": entries}).
		WithDiagnose(b.Diagnose).
		Build()
	_ = json.NewEncoder(stdout).Encode(signal.ValidateOrEmergency(b.Validator, sig, "list-sensors", stderr))
	return 0
}
