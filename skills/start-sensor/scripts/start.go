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

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	libsensor "github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

func main() {
	b := cli.Bootstrap("start-sensor", os.Stdout, os.Stderr)
	if b.ExitCode != 0 {
		os.Exit(b.ExitCode)
	}
	exit, sig := runStart(b, os.Args[1:])
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}

// runStart performs the full /start-sensor lifecycle for sensor id given
// in args[0]. Returns (exitCode, finalSignal). The signal is encoded by
// the caller; tests inspect it directly.
//
// b carries the discovered registry root plus the diagnostic fields
// (registry_path, registry_source, registry_exists) injected into every
// signal's metadata for cross-skill uniformity with /list-sensors and
// /stop-sensor.
func runStart(b cli.BootstrapResult, args []string) (int, map[string]interface{}) {
	projectRoot := b.Res.ProjectRoot
	v := b.Validator

	if len(args) < 1 {
		return 2, startSignal("unknown", nil, "failed", "bootstrap_failed", "missing sensor id argument", nil, b.Diagnose)
	}
	id := args[0]

	path, err := libsensor.Resolve(id, projectRoot)
	if err != nil {
		return 2, signal.ValidateOrEmergency(v, startSignal(id, nil, "failed", "resolve_failed",
			fmt.Sprintf("resolve: %v", err),
			map[string]interface{}{"error_excerpt": err.Error()},
			b.Diagnose), id, os.Stderr)
	}

	sensorJSON, err := loadSensorJSON(path)
	if err != nil {
		return 2, signal.ValidateOrEmergency(v, startSignal(id, nil, "failed", "resolve_failed",
			err.Error(),
			map[string]interface{}{"error_excerpt": err.Error()},
			b.Diagnose), id, os.Stderr)
	}

	if err := v.Validate(schema.TargetSensor, sensorJSON); err != nil {
		return 1, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "schema_invalid",
			fmt.Sprintf("schema: %v", err),
			map[string]interface{}{"error_excerpt": fmt.Sprintf("%v", err)},
			b.Diagnose), id, os.Stderr)
	}

	execMap, _ := sensorJSON["execution"].(map[string]interface{})
	blocking, _ := execMap["blocking"].(bool)
	if !blocking {
		return 2, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "not_blocking",
			"sensor is not blocking; use /run-sensor instead",
			nil, b.Diagnose), id, os.Stderr)
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
		return pre.ExitCode, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "preflight_failed",
			"pre-flight failed; see earlier signals or stderr",
			nil, b.Diagnose), id, os.Stderr)
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
		return 1, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "dep_cascade",
			fmt.Sprintf("dependency %q produced verdict=%s; root not started", failedID, failedVerdict),
			aux, b.Diagnose), id, os.Stderr)
	}

	target := pre.Order[len(pre.Order)-1]

	// Run target's prepare[] fail-fast.
	prepResults, prepFailed := orchestrator.RunPreparePhase(context.Background(), target, readTimeoutMS(target.JSON))
	if prepFailed {
		detachAll()
		aux := map[string]interface{}{
			"lifecycle": map[string]interface{}{"prepare": prepResults},
		}
		return 1, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "prepare_failed",
			"target prepare[] failed",
			aux, b.Diagnose), id, os.Stderr)
	}

	// Singleton + spawn detached + watcher + registry write.
	command, _ := execMap["command"].(string)
	r := registry.NewRoot(projectRoot)
	logDir := r.SensorDir(id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		detachAll()
		return 1, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "registry_write_failed",
			fmt.Sprintf("mkdir log dir: %v", err),
			map[string]interface{}{"error_excerpt": err.Error()},
			b.Diagnose), id, os.Stderr)
	}

	watcherPath, err := watcherBinaryPath()
	if err != nil {
		detachAll()
		return 1, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", "watcher_spawn_failed",
			fmt.Sprintf("watcher binary: %v", err),
			map[string]interface{}{"error_excerpt": err.Error()},
			b.Diagnose), id, os.Stderr)
	}

	type spawnResult struct {
		det         subprocess.DetachResult
		watcherProc *os.Process
		watcherPID  int
		envelope    libsensor.Envelope
		runID       string
		runDir      string
	}
	var spawned spawnResult
	var alreadyRunning bool
	var alreadyRunningPID int

	lockErr := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return fmt.Errorf("load registry: %w", err)
		}
		if existing := rs.FindBlockingEntry(id); existing != nil && registry.IsPIDAlive(existing.PID) {
			alreadyRunning = true
			alreadyRunningPID = existing.PID
			return nil
		}

		// Stage 1: pre-create the staging raw.log at the flat SensorDir
		// path. SpawnDetached opens this for stdout+stderr; we rename it
		// into <run-id>/raw.log once the PID is known. os.Rename on the
		// same filesystem preserves the subprocess's open fd, so writes
		// continue uninterrupted at the new path.
		stagingRaw := r.RawLog(id)
		if err := os.WriteFile(stagingRaw, nil, 0o644); err != nil {
			return fmt.Errorf("create staging raw.log: %w", err)
		}

		// Stage 2: spawn the subprocess detached.
		det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
			Command: command,
			LogFile: stagingRaw,
		})
		if err != nil {
			_ = os.Remove(stagingRaw)
			return fmt.Errorf("spawn: %w", err)
		}

		// Stage 3: derive composite run_id from the freshly-spawned PID
		// and a short UUID. This becomes the per-run directory name and
		// the run_id carried on every Signal the watcher emits.
		shortUUID := uuid.NewString()
		if len(shortUUID) >= 8 {
			shortUUID = shortUUID[:8]
		}
		runID := fmt.Sprintf("%d-%s", det.PID, shortUUID)
		runDir := r.RunDir(id, runID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			if det.PGID > 0 {
				_ = killGroup(det.PGID)
			}
			_ = os.Remove(stagingRaw)
			return fmt.Errorf("mkdir run dir: %w", err)
		}

		// Stage 4: rename the staging raw.log into <run-id>/raw.log.
		// Atomic on POSIX; subprocess's open fd survives the rename.
		rawPath := r.RawLogRun(id, runID)
		if err := os.Rename(stagingRaw, rawPath); err != nil {
			if det.PGID > 0 {
				_ = killGroup(det.PGID)
			}
			_ = os.Remove(stagingRaw)
			_ = os.RemoveAll(runDir)
			return fmt.Errorf("rename raw.log into run dir: %w", err)
		}
		sigsPath := r.SignalsLogRun(id, runID)
		if err := os.WriteFile(sigsPath, nil, 0o644); err != nil {
			if det.PGID > 0 {
				_ = killGroup(det.PGID)
			}
			_ = os.RemoveAll(runDir)
			return fmt.Errorf("create signals.log: %w", err)
		}

		envelope, eerr := libsensor.BuildEnvelope(sensorJSON)
		if eerr != nil {
			if det.PGID > 0 {
				_ = killGroup(det.PGID)
			}
			_ = os.RemoveAll(runDir)
			return fmt.Errorf("envelope: %w", eerr)
		}
		// Override the auto-generated run_id with the composite PID-UUID
		// that names this run's directory; the watcher reads this from
		// HARNESS_WATCHER_ENVELOPE and stamps it on every emitted Signal.
		envelope.RunID = runID
		patterns := []interface{}{}
		if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
			if raw, ok := op["patterns"].([]interface{}); ok {
				patterns = raw
			}
		}
		patternsJSON, _ := json.Marshal(patterns)
		envelopeJSON, _ := json.Marshal(envelope)

		watcherLogPath := filepath.Join(runDir, "watcher.log")
		watcherLogFile, err := os.OpenFile(watcherLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			if det.PGID > 0 {
				_ = killGroup(det.PGID)
			}
			_ = os.RemoveAll(runDir)
			return fmt.Errorf("open watcher.log: %w", err)
		}
		watcherProc, err := os.StartProcess(watcherPath, []string{watcherPath}, &os.ProcAttr{
			Env: []string{
				fmt.Sprintf("HARNESS_WATCHER_RAW=%s", rawPath),
				fmt.Sprintf("HARNESS_WATCHER_SIGNALS=%s", sigsPath),
				fmt.Sprintf("HARNESS_WATCHER_PATTERNS=%s", string(patternsJSON)),
				fmt.Sprintf("HARNESS_WATCHER_ENVELOPE=%s", string(envelopeJSON)),
				fmt.Sprintf("HARNESS_WATCHER_SUBPROCESS_PID=%d", det.PID),
				fmt.Sprintf("HARNESS_WATCHER_REGISTRY_ROOT=%s", projectRoot),
				fmt.Sprintf("HARNESS_WATCHER_SENSOR_ID=%s", id),
				fmt.Sprintf("HARNESS_WATCHER_RUN_ID=%s", runID),
			},
			Files: []*os.File{nil, nil, watcherLogFile},
			Sys:   &watcherSysProcAttr,
		})
		if err != nil {
			if det.PGID > 0 {
				_ = killGroup(det.PGID)
			}
			_ = watcherLogFile.Close()
			_ = os.RemoveAll(runDir)
			return fmt.Errorf("start watcher: %w", err)
		}
		watcherPID := watcherProc.Pid

		if stale := rs.FindBlockingEntry(id); stale != nil {
			rs.RemoveEntryByRunID(stale.RunID)
		}
		rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
			SensorID:   id,
			RunID:      runID,
			Blocking:   true,
			PID:        det.PID,
			PGID:       det.PGID,
			WatcherPID: watcherPID,
			StartedAt:  envelope.StartedAt,
			Command:    command,
			LogDir:     filepath.Join(".runtime", "sensors", id, runID),
			HeldBy: []registry.HeldByEntry{
				{Kind: "manual", AttachedAt: envelope.StartedAt},
			},
		})
		if err := registry.Save(r, rs); err != nil {
			return err
		}

		spawned = spawnResult{
			det:         det,
			watcherProc: watcherProc,
			watcherPID:  watcherPID,
			envelope:    envelope,
			runID:       runID,
			runDir:      runDir,
		}
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
		return 1, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "failed", cause,
			fmt.Sprintf("write registry: %v", lockErr),
			map[string]interface{}{"error_excerpt": lockErr.Error()},
			b.Diagnose), id, os.Stderr)
	}

	if alreadyRunning {
		detachAll()
		return 1, signal.ValidateOrEmergency(v, startSignal(id, sensorJSON, "rejected", "",
			fmt.Sprintf("sensor %q already running with pid %d", id, alreadyRunningPID),
			map[string]interface{}{"existing_pid": alreadyRunningPID},
			b.Diagnose), id, os.Stderr)
	}

	// Rebind: dep holders go from placeholderPID to spawned.det.PID.
	var rebindWarnings []interface{}
	for _, live := range pre.LiveStack {
		if err := orchestrator.RebindDepHolderPID(live.ID, projectRoot, id, placeholderPID, spawned.det.PID); err != nil {
			rebindWarnings = append(rebindWarnings, map[string]interface{}{
				"dep_id": live.ID,
				"error":  err.Error(),
			})
		}
	}

	aux := map[string]interface{}{
		"pid":         spawned.det.PID,
		"watcher_pid": spawned.watcherPID,
		"log_dir":     filepath.Join(".runtime", "sensors", id, spawned.runID),
		"next_cursor": 0,
	}
	if len(prepResults) > 0 {
		aux["lifecycle"] = map[string]interface{}{"prepare": prepResults}
	}
	if len(pre.LiveStack) > 0 {
		ds := []interface{}{}
		for _, d := range pre.LiveStack {
			ds = append(ds, d.ID)
		}
		aux["dep_chain"] = ds
	}
	if len(rebindWarnings) > 0 {
		aux["rebind_warnings"] = rebindWarnings
	}

	sig := startSignal(id, sensorJSON, "started", "",
		fmt.Sprintf("sensor %q started, pid=%d, watcher_pid=%d", id, spawned.det.PID, spawned.watcherPID),
		aux, b.Diagnose)
	sig["run_id"] = spawned.envelope.RunID
	sig["started_at"] = spawned.envelope.StartedAt
	return 0, signal.ValidateOrEmergency(v, sig, id, os.Stderr)
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

// startSignal builds the terminal signal of /start-sensor. The verdict is
// derived from kind ("started" → pass/info; anything else → error/high).
// cause is included in metadata only when kind=="failed".
func startSignal(id string, sensorJSON map[string]interface{}, kind, cause, rationale string, aux, diagnose map[string]interface{}) map[string]interface{} {
	verdict, severity := "error", "high"
	if kind == "started" {
		verdict, severity = "pass", "info"
	}
	version := "0.0.0"
	if sensorJSON != nil {
		if v, _ := sensorJSON["version"].(string); v != "" {
			version = v
		}
	}
	md := map[string]interface{}{}
	if kind == "failed" && cause != "" {
		md["cause"] = cause
	}
	for k, v := range aux {
		md[k] = v
	}
	return signal.NewBuilder(id, version).
		WithVerdict(verdict, severity).
		WithKind(kind).
		WithRationale(rationale).
		WithMetadata(md).
		WithDiagnose(diagnose).
		Build()
}
