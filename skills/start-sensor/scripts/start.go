//go:build start_sensor

// start spawns a blocking sensor's command in a detached session,
// records it in the registry, and emits a Signal verdict=pass,
// metadata.kind=started. Runs the full lifecycle: pre-flight (deps via
// orchestrator.RunDeps) → prepare[] of root → spawn detached + watcher
// + registry write → rebind dep holder pids → started.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	libsensor "github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

func main() {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start: cwd:", err)
		os.Exit(2)
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	if len(reports) > 0 {
		_ = json.NewEncoder(os.Stdout).Encode(registry.RegistryMigratedSignal(res, reports, "start-sensor"))
	}
	exit, sig := runStart(res, os.Args[1:])
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}

// runStart performs the full /start-sensor lifecycle for sensor id given
// in args[0]. Returns (exitCode, finalSignal). The signal is encoded by
// the caller; tests inspect it directly.
//
// res carries the discovered registry root plus the diagnostic fields
// (registry_path, registry_source, registry_exists) injected into every
// signal's metadata for cross-skill uniformity with /list-sensors and
// /stop-sensor.
func runStart(res registry.Result, args []string) (int, map[string]interface{}) {
	projectRoot := res.ProjectRoot
	diagnose := registry.DiagnoseMetadata(res)
	v, vCode := schema.LoadValidator("", os.Stderr)
	if vCode != 0 {
		return vCode, finalSignal("unknown", nil, "failed", "bootstrap_failed", nil, "schema validator init failed", diagnose)
	}

	if len(args) < 1 {
		return 2, finalSignal("unknown", nil, "failed", "bootstrap_failed", nil, "missing sensor id argument", diagnose)
	}
	id := args[0]

	path, err := libsensor.Resolve(id, projectRoot)
	if err != nil {
		return 2, validateSignal(v, finalSignal(id, nil, "failed", "resolve_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("resolve: %v", err), diagnose), id)
	}

	sensorJSON, err := loadSensorJSON(path)
	if err != nil {
		return 2, validateSignal(v, finalSignal(id, nil, "failed", "resolve_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			err.Error(), diagnose), id)
	}

	if err := v.Validate(schema.TargetSensor, sensorJSON); err != nil {
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "failed", "schema_invalid",
			map[string]interface{}{"error_excerpt": fmt.Sprintf("%v", err)},
			fmt.Sprintf("schema: %v", err), diagnose), id)
	}

	execMap, _ := sensorJSON["execution"].(map[string]interface{})
	blocking, _ := execMap["blocking"].(bool)
	if !blocking {
		return 2, validateSignal(v, finalSignal(id, sensorJSON, "failed", "not_blocking", nil,
			"sensor is not blocking; use /run-sensor instead", diagnose), id)
	}

	// Pre-flight: resolve DAG, run deps, detect cascade.
	placeholderPID := os.Getpid()
	pre := orchestrator.RunDeps(
		context.Background(), id, projectRoot, "" /*schemasDir*/, id /*holderID*/, placeholderPID,
		v, os.Stdout, os.Stderr,
	)
	detachAll := func() {
		for i := len(pre.LiveStack) - 1; i >= 0; i-- {
			orchestrator.DetachLiveDep(pre.LiveStack[i], projectRoot, id, v, os.Stdout, os.Stderr)
		}
	}

	if pre.ExitCode != 0 {
		detachAll()
		return pre.ExitCode, validateSignal(v, finalSignal(id, sensorJSON, "failed", "preflight_failed", nil,
			"pre-flight failed; see earlier signals or stderr", diagnose), id)
	}
	if pre.CascadeSig != nil {
		md, _ := pre.CascadeSig["metadata"].(map[string]interface{})
		aux := map[string]interface{}{
			"failed_dep_id":       md["failed_dep_id"],
			"failed_dep_run_id":   md["failed_dep_run_id"],
			"failed_dep_verdict":  md["failed_dep_verdict"],
			"failed_dep_severity": md["failed_dep_severity"],
		}
		failedID, _ := md["failed_dep_id"].(string)
		failedVerdict, _ := md["failed_dep_verdict"].(string)
		detachAll()
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "failed", "dep_cascade", aux,
			fmt.Sprintf("dependency %q produced verdict=%s; root not started", failedID, failedVerdict), diagnose), id)
	}

	target := pre.Order[len(pre.Order)-1]

	// Run target's prepare[] fail-fast.
	prepResults, prepFailed := orchestrator.RunPreparePhase(context.Background(), target, readTimeoutMS(target.JSON))
	if prepFailed {
		detachAll()
		aux := map[string]interface{}{
			"lifecycle": map[string]interface{}{"prepare": prepResults},
		}
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "failed", "prepare_failed", aux,
			"target prepare[] failed", diagnose), id)
	}

	// Singleton + spawn detached + watcher + registry write.
	command, _ := execMap["command"].(string)
	r := registry.NewRoot(projectRoot)
	logDir := r.SensorDir(id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		detachAll()
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "failed", "registry_write_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("mkdir log dir: %v", err), diagnose), id)
	}
	if err := os.WriteFile(r.RawLog(id), nil, 0o644); err != nil {
		detachAll()
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "failed", "registry_write_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("create raw.log: %v", err), diagnose), id)
	}
	if err := os.WriteFile(r.SignalsLog(id), nil, 0o644); err != nil {
		detachAll()
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "failed", "registry_write_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("create signals.log: %v", err), diagnose), id)
	}

	watcherPath, err := watcherBinaryPath()
	if err != nil {
		detachAll()
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "failed", "watcher_spawn_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("watcher binary: %v", err), diagnose), id)
	}

	type spawnResult struct {
		det         subprocess.DetachResult
		watcherProc *os.Process
		watcherPID  int
		envelope    libsensor.Envelope
	}
	var spawned spawnResult
	var alreadyRunning bool
	var alreadyRunningPID int

	lockErr := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return fmt.Errorf("load registry: %w", err)
		}
		if existing := rs.FindEntry(id); existing != nil && registry.IsPIDAlive(existing.PID) {
			alreadyRunning = true
			alreadyRunningPID = existing.PID
			return nil
		}
		det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
			Command: command,
			LogFile: r.RawLog(id),
		})
		if err != nil {
			return fmt.Errorf("spawn: %w", err)
		}
		envelope := libsensor.Envelope{
			SensorID:   id,
			Version:    stringField(sensorJSON, "version"),
			RunID:      uuid.NewString(),
			StartedAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			SensorType: stringField(sensorJSON, "type"),
		}
		patterns := []interface{}{}
		if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
			if raw, ok := op["patterns"].([]interface{}); ok {
				patterns = raw
			}
		}
		patternsJSON, _ := json.Marshal(patterns)
		envelopeJSON, _ := json.Marshal(envelope)

		watcherLogPath := filepath.Join(r.SensorDir(id), "watcher.log")
		watcherLogFile, err := os.OpenFile(watcherLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open watcher.log: %w", err)
		}
		watcherProc, err := os.StartProcess(watcherPath, []string{watcherPath}, &os.ProcAttr{
			Env: []string{
				fmt.Sprintf("HARNESS_WATCHER_RAW=%s", r.RawLog(id)),
				fmt.Sprintf("HARNESS_WATCHER_SIGNALS=%s", r.SignalsLog(id)),
				fmt.Sprintf("HARNESS_WATCHER_PATTERNS=%s", string(patternsJSON)),
				fmt.Sprintf("HARNESS_WATCHER_ENVELOPE=%s", string(envelopeJSON)),
				fmt.Sprintf("HARNESS_WATCHER_SUBPROCESS_PID=%d", det.PID),
				fmt.Sprintf("HARNESS_WATCHER_REGISTRY_ROOT=%s", projectRoot),
				fmt.Sprintf("HARNESS_WATCHER_SENSOR_ID=%s", id),
			},
			Files: []*os.File{nil, nil, watcherLogFile},
			Sys:   &watcherSysProcAttr,
		})
		if err != nil {
			// Kill the just-spawned root subprocess so we don't orphan it.
			if det.PGID > 0 {
				_ = killGroup(det.PGID)
			}
			_ = watcherLogFile.Close()
			return fmt.Errorf("start watcher: %w", err)
		}
		// Capture the watcher pid BEFORE Release() — on Unix, Release
		// resets Process.Pid to -1, which would violate the registry
		// PID non-negativity invariant when Save validates the entry.
		watcherPID := watcherProc.Pid
		_ = watcherProc.Release()
		_ = watcherLogFile.Close() // parent's handle; child keeps its own fd open.

		rs.RemoveEntry(id)
		rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
			SensorID:   id,
			PID:        det.PID,
			PGID:       det.PGID,
			WatcherPID: watcherPID,
			StartedAt:  envelope.StartedAt,
			Command:    command,
			LogDir:     filepath.Join(".runtime", "sensors", id),
			HeldBy: []registry.HeldByEntry{
				{Kind: "manual", AttachedAt: envelope.StartedAt},
			},
		})
		if err := registry.Save(r, rs); err != nil {
			return err
		}

		spawned = spawnResult{det: det, watcherProc: watcherProc, watcherPID: watcherPID, envelope: envelope}
		return nil
	})

	if lockErr != nil {
		cause := "registry_write_failed"
		switch {
		case strings.HasPrefix(lockErr.Error(), "start watcher:"):
			cause = "watcher_spawn_failed"
		case strings.HasPrefix(lockErr.Error(), "spawn:"):
			cause = "spawn_failed"
		}
		detachAll()
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "failed", cause,
			map[string]interface{}{"error_excerpt": lockErr.Error()},
			fmt.Sprintf("write registry: %v", lockErr), diagnose), id)
	}

	if alreadyRunning {
		detachAll()
		return 1, validateSignal(v, finalSignal(id, sensorJSON, "rejected", "",
			map[string]interface{}{"existing_pid": alreadyRunningPID},
			fmt.Sprintf("sensor %q already running with pid %d", id, alreadyRunningPID), diagnose), id)
	}

	// Rebind: dep holders go from placeholderPID to spawned.det.PID.
	var rebindWarnings []interface{}
	for _, depID := range pre.LiveStack {
		if err := orchestrator.RebindDepHolderPID(depID, projectRoot, id, placeholderPID, spawned.det.PID); err != nil {
			rebindWarnings = append(rebindWarnings, map[string]interface{}{
				"dep_id": depID,
				"error":  err.Error(),
			})
		}
	}

	aux := map[string]interface{}{
		"pid":         spawned.det.PID,
		"watcher_pid": spawned.watcherPID,
		"log_dir":     filepath.Join(".runtime", "sensors", id),
		"next_cursor": 0,
	}
	if len(prepResults) > 0 {
		aux["lifecycle"] = map[string]interface{}{"prepare": prepResults}
	}
	if len(pre.LiveStack) > 0 {
		ds := []interface{}{}
		for _, d := range pre.LiveStack {
			ds = append(ds, d)
		}
		aux["dep_chain"] = ds
	}
	if len(rebindWarnings) > 0 {
		aux["rebind_warnings"] = rebindWarnings
	}

	sig := finalSignal(id, sensorJSON, "started", "", aux,
		fmt.Sprintf("sensor %q started, pid=%d, watcher_pid=%d", id, spawned.det.PID, spawned.watcherPID),
		diagnose)
	sig["run_id"] = spawned.envelope.RunID
	sig["started_at"] = spawned.envelope.StartedAt
	return 0, validateSignal(v, sig, id)
}

func loadSensorJSON(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sensor: %w", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse sensor: %w", err)
	}
	return m, nil
}

func stringField(m map[string]interface{}, k string) string {
	v, _ := m[k].(string)
	return v
}

func readTimeoutMS(s map[string]interface{}) int {
	cost, _ := s["cost"].(map[string]interface{})
	if cost == nil {
		return 0
	}
	lat, _ := cost["latency"].(map[string]interface{})
	if lat == nil {
		return 0
	}
	if v, ok := lat["timeout_ms"].(float64); ok {
		return int(v)
	}
	return 0
}

// finalSignal builds the terminal signal of /start-sensor. cause is
// required for kind="failed" and ignored for "started"/"rejected".
// aux is merged into metadata, carrying kind-specific fields per the
// design spec's table.
//
// diagnose, when non-nil, is merged into metadata last so every signal
// emitted by /start-sensor carries the standard registry-discovery
// diagnostic fields (registry_path, registry_source, registry_exists)
// matching the convention used by /list-sensors and /stop-sensor.
func finalSignal(
	id string,
	sensorJSON map[string]interface{},
	kind string,
	cause string,
	aux map[string]interface{},
	rationale string,
	diagnose map[string]interface{},
) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	verdict := "error"
	severity := "high"
	if kind == "started" {
		verdict = "pass"
		severity = "info"
	}
	version := "0.0.0"
	if sensorJSON != nil {
		version = stringField(sensorJSON, "version")
		if version == "" {
			version = "0.0.0"
		}
	}
	md := map[string]interface{}{"kind": kind}
	if kind == "failed" && cause != "" {
		md["cause"] = cause
	}
	for k, val := range aux {
		md[k] = val
	}
	for k, val := range diagnose {
		md[k] = val
	}
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     version,
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     verdict,
		"severity":    severity,
		"confidence":  1.0,
		"evidence": []interface{}{
			map[string]interface{}{"rationale": rationale},
		},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}

// validateSignal checks sig against signal.json. If validation fails it
// logs the error to stderr and returns a minimal emergency signal so
// the bug surfaces without recursion. On success it returns sig
// unchanged.
func validateSignal(v *schema.Validator, sig map[string]interface{}, id string) map[string]interface{} {
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(os.Stderr, "start: BUG: emitted signal failed signal.json validation: %v\n", err)
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
