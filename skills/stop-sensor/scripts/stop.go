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
	id := args[0]

	v, code := schema.LoadValidator("", os.Stderr)
	if code != 0 {
		return code, simpleSignal(res, id, "error", "high", "failed", "schema validator init failed")
	}

	if !res.Exists {
		return 1, validateSignal(v, simpleSignal(res, id, "error", "high", "stop_no_registry",
			fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", res.Root.RegistryFile())), id)
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
		entry = rs.FindEntry(id)
		if entry == nil {
			return nil
		}
		registry.RemoveHolder(entry, registry.HeldByEntry{Kind: "manual"})
		if reap {
			reaped = registry.ReapDead(entry)
		}
		// Persist removal of "manual" + reaped early so a crash here
		// doesn't leave a stale "manual" hold.
		return registry.Save(r, rs)
	}); err != nil {
		return 1, validateSignal(v, simpleSignal(res, id, "error", "high", "failed", fmt.Sprintf("registry: %v", err)), id)
	}

	if entry == nil {
		return 0, validateSignal(v, simpleSignal(res, id, "warn", "low", "not_running", fmt.Sprintf("no live entry for %q", id)), id)
	}

	if registry.IsHeld(entry) {
		sig := simpleSignal(res, id, "warn", "low", "held", fmt.Sprintf("sensor %q still held by %d holders", id, len(entry.HeldBy)))
		md := sig["metadata"].(map[string]interface{})
		md["holders"] = registry.SummarizeHolders(entry.HeldBy, registry.SummarizeOpts{})
		md["dead_holders"] = registry.SummarizeHolders(entry.HeldBy, registry.SummarizeOpts{DeadOnly: true})
		if len(reaped) > 0 {
			md["reaped_holders"] = registry.SummarizeHolders(reaped, registry.SummarizeOpts{})
		}
		return 0, validateSignal(v, sig, id)
	}

	// We are clear to stop. Send SIGTERM to the process group.
	gracefulMS := readGracefulMS(entry, projectRoot)
	killedForcefully := terminateWithGrace(entry.PGID, gracefulMS)
	watcherKillForced, watcherKillLatencyMS := stopWatcher(entry.WatcherPID)

	sensorJSON := loadSensorJSONForStop(projectRoot, id)
	teardownResults := runTeardown(sensorJSON)

	individuals := readSignals(r.SignalsLog(id))
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

	sig := buildAggregate(res, id, sensorJSON, entry, individuals, agg, killedForcefully, reaped, teardownResults)
	if md, ok := sig["metadata"].(map[string]interface{}); ok {
		md["watcher_kill_forced"] = watcherKillForced
		md["watcher_kill_latency_ms"] = watcherKillLatencyMS
	}

	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntry(id)
		return registry.Save(r, rs)
	}); err != nil {
		return 1, validateSignal(v, simpleSignal(res, id, "error", "high", "failed", fmt.Sprintf("registry: %v", err)), id)
	}
	return 0, validateSignal(v, sig, id)
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
	path, err := libsensor.Resolve(id, projectRoot)
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
		md["reaped_holders"] = registry.SummarizeHolders(reaped, registry.SummarizeOpts{})
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
