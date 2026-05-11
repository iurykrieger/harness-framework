//go:build stop_sensor

// stop sends SIGTERM to a blocking sensor's process group, waits up to
// graceful_timeout_ms, escalates to SIGKILL if needed, runs teardown,
// computes the aggregate Signal from signals.log, and removes the
// registry entry.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	libsensor "github.com/iurykrieger/harness-framework/lib/sensor"
	libsignal "github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	var reap bool
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.BoolVar(&reap, "reap-dead-holders", false, "drop kind=sensor holders whose PID is dead before deciding whether to stop")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stop: cwd:", err)
		os.Exit(2)
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	if len(reports) > 0 {
		_ = json.NewEncoder(os.Stdout).Encode(registry.RegistryMigratedSignal(res, reports, "stop-sensor"))
	}
	exit, sig := runStop(res, fs.Args(), reap)
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}

func runStop(res registry.Result, args []string, reap bool) (int, map[string]interface{}) {
	if len(args) < 1 {
		return 2, simpleSignal(res, "stop", "warn", "low", "not_running", "missing sensor id")
	}

	// Parse first arg as sensorID[/runID].
	arg := args[0]
	var sensorID, runID string
	if i := strings.IndexByte(arg, '/'); i >= 0 {
		sensorID, runID = arg[:i], arg[i+1:]
	} else {
		sensorID = arg
	}

	v, code := schema.LoadValidator("", os.Stderr)
	if code != 0 {
		return code, simpleSignal(res, sensorID, "error", "high", "failed", "schema validator init failed")
	}

	if !res.Exists {
		return 1, validateSignal(v, simpleSignal(res, sensorID, "error", "high", "stop_no_registry",
			fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", res.Root.RegistryFile())), sensorID)
	}

	r := res.Root
	projectRoot := res.ProjectRoot

	var entry *registry.RunningSensorEntry
	var reaped []registry.HeldByEntry

	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		// Resolve target entry.
		if runID != "" {
			// Explicit run_id: look up directly.
			entry = rs.FindEntryByRunID(runID)
			if entry == nil {
				return nil // signal "not_running" below
			}
			if entry.SensorID != sensorID {
				// Mismatch — surface a distinct error after release of the lock.
				entry = nil
				return mismatchErr{wantSensor: sensorID, runID: runID}
			}
		} else {
			entries := rs.FindEntries(sensorID)
			switch len(entries) {
			case 0:
				return nil // signal "not_running" below
			case 1:
				entry = entries[0]
			default:
				// Prefer the unique blocking:true entry if present.
				blocking := make([]*registry.RunningSensorEntry, 0, 1)
				for _, e := range entries {
					if e.Blocking {
						blocking = append(blocking, e)
					}
				}
				if len(blocking) == 1 {
					entry = blocking[0]
				} else {
					// Truly ambiguous: surface ambiguous_run.
					return ambiguousErr{entries: entries}
				}
			}
		}
		// Only blocking entries carry "manual" holds; the blocking:false
		// path's entry is owned by the runner and has empty HeldBy. The
		// RemoveHolder call is a no-op in that case.
		registry.RemoveHolder(entry, registry.HeldByEntry{Kind: "manual"})
		if reap && entry.Blocking {
			reaped = registry.ReapDead(entry)
		}
		// Persist removal of "manual" + reaped early so a crash here
		// doesn't leave a stale "manual" hold.
		return registry.Save(r, rs)
	}); err != nil {
		switch e := err.(type) {
		case mismatchErr:
			return 1, validateSignal(v, simpleSignal(res, sensorID, "error", "high", "run_id_sensor_mismatch",
				fmt.Sprintf("run_id %q does not belong to sensor %q", e.runID, e.wantSensor)), sensorID)
		case ambiguousErr:
			return 1, ambiguousSignal(v, res, sensorID, e.entries)
		default:
			return 1, validateSignal(v, simpleSignal(res, sensorID, "error", "high", "failed", fmt.Sprintf("registry: %v", err)), sensorID)
		}
	}

	if entry == nil {
		if runID != "" {
			return 0, validateSignal(v, simpleSignal(res, sensorID, "warn", "low", "not_running", fmt.Sprintf("no live entry for sensor %q run_id %q", sensorID, runID)), sensorID)
		}
		return 0, validateSignal(v, simpleSignal(res, sensorID, "warn", "low", "not_running", fmt.Sprintf("no live entry for %q", sensorID)), sensorID)
	}

	// blocking:true is the only branch that honors "held_by". For
	// blocking:false the entry is owned by the runner subprocess and
	// nothing else can hold it.
	if entry.Blocking && registry.IsHeld(entry) {
		sig := simpleSignal(res, sensorID, "warn", "low", "held", fmt.Sprintf("sensor %q still held by %d holders", sensorID, len(entry.HeldBy)))
		md := sig["metadata"].(map[string]interface{})
		md["holders"] = holderSummaries(entry.HeldBy)
		md["dead_holders"] = deadHolderSummaries(entry.HeldBy)
		if len(reaped) > 0 {
			md["reaped_holders"] = holderSummaries(reaped)
		}
		return 0, validateSignal(v, sig, sensorID)
	}

	if !entry.Blocking {
		return stopNonBlocking(res, v, r, projectRoot, entry, sensorID)
	}

	// We are clear to stop. Send SIGTERM to the process group.
	gracefulMS := readGracefulMS(entry, projectRoot)
	killedForcefully := terminateWithGrace(entry.PGID, gracefulMS)
	watcherKillForced, watcherKillLatencyMS := stopWatcher(entry.WatcherPID)

	sensorJSON := loadSensorJSONForStop(projectRoot, sensorID)
	teardownResults := runTeardown(sensorJSON)

	individuals := readSignals(r.SignalsLog(sensorID))
	exitVerd, exitSev := exitMappingFromSensor(sensorJSON, entry)
	streamVerd, streamSev := libsignal.MaxStreamVerdict(individuals)

	subprocessSelfExited := entry.SubprocessExit != nil
	agg := libsignal.Aggregate(libsignal.AggregateInput{
		ExitVerdict:    exitVerd,
		ExitSeverity:   exitSev,
		StreamVerdict:  streamVerd,
		StreamSeverity: streamSev,
		Blocking:       !subprocessSelfExited,
	})

	sig := buildAggregate(res, sensorID, sensorJSON, entry, individuals, agg, killedForcefully, reaped, teardownResults)
	if md, ok := sig["metadata"].(map[string]interface{}); ok {
		md["watcher_kill_forced"] = watcherKillForced
		md["watcher_kill_latency_ms"] = watcherKillLatencyMS
	}

	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntryByRunID(entry.RunID)
		return registry.Save(r, rs)
	}); err != nil {
		return 1, validateSignal(v, simpleSignal(res, sensorID, "error", "high", "failed", fmt.Sprintf("registry: %v", err)), sensorID)
	}
	return 0, validateSignal(v, sig, sensorID)
}

// stopNonBlocking handles entries with Blocking=false. The runner
// subprocess is alive and owns its own teardown/aggregate emission
// (driven by the SIGINT/SIGTERM handler installed in /run-sensor); the
// only job here is to terminate the runner's process group cleanly and
// remove the registry entry.
func stopNonBlocking(res registry.Result, v *schema.Validator, r registry.Root, projectRoot string, entry *registry.RunningSensorEntry, sensorID string) (int, map[string]interface{}) {
	gracefulMS := readGracefulMS(entry, projectRoot)
	killedForcefully := terminateWithGrace(entry.PGID, gracefulMS)

	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntryByRunID(entry.RunID)
		return registry.Save(r, rs)
	}); err != nil {
		return 1, validateSignal(v, simpleSignal(res, sensorID, "error", "high", "failed", fmt.Sprintf("registry: %v", err)), sensorID)
	}

	sig := simpleSignal(res, sensorID, "pass", "info", "stopped", fmt.Sprintf("non-blocking sensor %q (run %q) terminated", sensorID, entry.RunID))
	md := sig["metadata"].(map[string]interface{})
	md["blocking"] = false
	md["run_id"] = entry.RunID
	md["killed_forcefully"] = killedForcefully
	return 0, validateSignal(v, sig, sensorID)
}

// mismatchErr signals that a run_id was found but belongs to a
// different sensor than the one requested. Surfaced as
// metadata.kind=run_id_sensor_mismatch.
type mismatchErr struct {
	wantSensor string
	runID      string
}

func (mismatchErr) Error() string { return "run_id sensor mismatch" }

// ambiguousErr signals that a bare sensor_id matches multiple active
// non-blocking runs. Surfaced as metadata.kind=ambiguous_run with one
// evidence record per active run_id.
type ambiguousErr struct {
	entries []*registry.RunningSensorEntry
}

func (ambiguousErr) Error() string { return "ambiguous run" }

func ambiguousSignal(v *schema.Validator, res registry.Result, sensorID string, entries []*registry.RunningSensorEntry) map[string]interface{} {
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
	return validateSignal(v, sig, sensorID)
}

func terminateWithGrace(pgid int, gracefulMS int) bool {
	if pgid <= 0 {
		return false // nothing to terminate; caller has stale registry data
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(time.Duration(gracefulMS) * time.Millisecond)
	for time.Now().Before(deadline) {
		if !registry.IsPIDAlive(pgid) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	if registry.IsPIDAlive(pgid) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return true
	}
	return false
}

// stopWatcher sends SIGTERM to the watcher pid, polls for up to one
// second, and escalates to SIGKILL if needed. Returns
// (killedForcefully, latencyMS):
//   - killedForcefully = true when the SIGTERM wait timed out and we
//     fell through to SIGKILL.
//   - latencyMS is wall-clock elapsed from the first signal to either
//     observed death or the SIGKILL send-time.
//
// Pid <= 0 is a no-op (returns false, 0).
func stopWatcher(pid int) (killedForcefully bool, latencyMS int) {
	if pid <= 0 {
		return false, 0
	}
	start := time.Now()
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := start.Add(time.Second)
	for time.Now().Before(deadline) {
		if !registry.IsPIDAlive(pid) {
			return false, int(time.Since(start) / time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if registry.IsPIDAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return true, int(time.Since(start) / time.Millisecond)
	}
	return false, int(time.Since(start) / time.Millisecond)
}

func readGracefulMS(entry *registry.RunningSensorEntry, projectRoot string) int {
	sj := loadSensorJSONForStop(projectRoot, entry.SensorID)
	exec, _ := sj["execution"].(map[string]interface{})
	if exec == nil {
		return 5000
	}
	if v, ok := exec["graceful_timeout_ms"].(float64); ok {
		return int(v)
	}
	return 5000
}

func loadSensorJSONForStop(projectRoot, id string) map[string]interface{} {
	path, err := libsensor.ResolveByID(id, projectRoot)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	_ = json.Unmarshal(data, &m)
	return m
}

func readSignals(path string) []map[string]interface{} {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var m map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

func exitMappingFromSensor(sensorJSON map[string]interface{}, entry *registry.RunningSensorEntry) (string, string) {
	if sensorJSON == nil {
		return "pass", "info"
	}
	exitCode := -1
	if entry.SubprocessExit != nil {
		exitCode = entry.SubprocessExit.Code
	}
	exec, _ := sensorJSON["execution"].(map[string]interface{})
	if exec == nil {
		return "pass", "info"
	}
	ecMap, _ := exec["exit_code_map"].([]interface{})
	if len(ecMap) == 0 {
		return "pass", "info"
	}
	v, s := libsignal.MapExitCode(exitCode, ecMap)
	if v == "" {
		return "pass", "info"
	}
	return v, s
}

func runTeardown(sensorJSON map[string]interface{}) []map[string]interface{} {
	// Teardown for blocking sensors is documented as a follow-up in
	// the design spec; manual /stop-sensor invocation skips it.
	_ = sensorJSON
	return nil
}

func buildAggregate(res registry.Result, id string, sensorJSON map[string]interface{}, entry *registry.RunningSensorEntry, individuals []map[string]interface{}, agg libsignal.AggregateResult, killedForcefully bool, reaped []registry.HeldByEntry, teardown []map[string]interface{}) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := registry.DiagnoseMetadata(res)
	md["kind"] = "aggregate"
	md["output_mode"] = "stream"
	md["command"] = entry.Command
	md["counts"] = libsignal.CountVerdicts(individuals)
	if entry.SubprocessExit != nil {
		md["subprocess_self_exited"] = true
		md["subprocess_exit_code"] = entry.SubprocessExit.Code
		if entry.SubprocessExit.Code == -1 {
			md["exit_code_unknown"] = true
		}
	}
	if killedForcefully {
		md["killed_forcefully"] = true
	}
	if len(reaped) > 0 {
		md["reaped_holders"] = holderSummaries(reaped)
	}
	if len(teardown) > 0 {
		md["lifecycle"] = map[string]interface{}{"teardown": teardown}
	}
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     stringField(sensorJSON, "version"),
		"run_id":      uuid.NewString(),
		"started_at":  entry.StartedAt,
		"finished_at": now,
		"verdict":     agg.Verdict,
		"severity":    agg.Severity,
		"confidence":  1.0,
		"evidence":    libsignal.SelectTopEvidence(individuals, 5),
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}

// deadHolderSummaries returns the subset of holders with kind=sensor and
// pid no longer alive. Empty slice (not nil) when none. Allows callers
// to distinguish "no dead holders" from "no holders at all".
//
// Return type matches holderSummaries so consumers can type-assert
// metadata.dead_holders and metadata.holders the same way.
func deadHolderSummaries(holders []registry.HeldByEntry) []interface{} {
	out := []interface{}{}
	for _, h := range holders {
		if h.Kind != "sensor" {
			continue
		}
		if registry.IsPIDAlive(h.PID) {
			continue
		}
		out = append(out, map[string]interface{}{
			"kind":        h.Kind,
			"id":          h.ID,
			"pid":         h.PID,
			"attached_at": h.AttachedAt,
		})
	}
	return out
}

func holderSummaries(hs []registry.HeldByEntry) []interface{} {
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

func stringField(m map[string]interface{}, k string) string {
	if m == nil {
		return ""
	}
	v, _ := m[k].(string)
	return v
}

func validateSignal(v *schema.Validator, sig map[string]interface{}, id string) map[string]interface{} {
	if v == nil {
		return sig
	}
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(os.Stderr, "stop: BUG: emitted signal failed signal.json validation: %v\n", err)
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

func simpleSignal(res registry.Result, id, verdict, severity, kind, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := registry.DiagnoseMetadata(res)
	md["kind"] = kind
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     verdict,
		"severity":    severity,
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}
