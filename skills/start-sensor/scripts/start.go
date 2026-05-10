//go:build start_sensor

// start spawns a blocking sensor's command in a detached session,
// records it in the registry, and emits a Signal verdict=pass,
// metadata.kind=started.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

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
	res, err := registry.Lookup(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
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
// Note: execution.prepare[] is not yet executed for blocking sensors; this
// is a documented follow-up. Manual /start-sensor invocation skips prepare
// today; orchestrator-driven blocking deps don't run it either.
func runStart(res registry.Result, args []string) (int, map[string]interface{}) {
	if len(args) < 1 {
		return 2, errorSignal(res, "start", "missing sensor id argument")
	}
	id := args[0]
	projectRoot := res.ProjectRoot

	path, err := libsensor.ResolveByID(id, projectRoot)
	if err != nil {
		return 2, errorSignal(res, id, fmt.Sprintf("resolve: %v", err))
	}

	sensorJSON, err := loadSensorJSON(path)
	if err != nil {
		return 2, errorSignal(res, id, err.Error())
	}

	// 1. Schema-validate the sensor definition before any further checks.
	v, code := schema.LoadValidator("", os.Stderr)
	if code != 0 {
		return code, errorSignal(res, id, "schema validator init failed")
	}
	if err := v.Validate(schema.TargetSensor, sensorJSON); err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("schema: %v", err))
	}

	// 2. Reject non-blocking sensors with exit 2 (usage error).
	execMap, _ := sensorJSON["execution"].(map[string]interface{})
	blocking, _ := execMap["blocking"].(bool)
	if !blocking {
		return 2, errorSignal(res, id, "sensor is not blocking; use /run-sensor instead")
	}

	command, _ := execMap["command"].(string)
	r := res.Root
	logDir := r.SensorDir(id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("mkdir log dir: %v", err))
	}
	if err := os.WriteFile(r.RawLog(id), nil, 0o644); err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("create raw.log: %v", err))
	}
	if err := os.WriteFile(r.SignalsLog(id), nil, 0o644); err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("create signals.log: %v", err))
	}

	watcherPath, err := watcherBinaryPath()
	if err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("watcher binary: %v", err))
	}

	// 3. Serialize the entire check-then-spawn-then-write sequence under a
	// single flock so two concurrent /start-sensor calls cannot both pass the
	// liveness check and both spawn subprocesses (which would leak the first
	// process when the second's lock block overwrites its registry entry).
	//
	// Holding the flock during fork is acceptable because:
	//   - The flock is process-bound; the child does NOT inherit it.
	//   - The fork itself is sub-millisecond.
	//   - This matches the spec's intent: serialize the entire
	//     "no-other-process-is-spawning-this-id" guarantee.
	type spawnResult struct {
		det         subprocess.DetachResult
		watcherProc *os.Process
		envelope    libsensor.Envelope
	}
	var spawned spawnResult
	var lockErr error
	var alreadyRunning bool
	var alreadyRunningPID int

	lockErr = registry.WithFileLock(r.LockFile(), func() error {
		// 3a. Load registry inside the lock.
		rs, err := registry.Load(r)
		if err != nil {
			return fmt.Errorf("load registry: %w", err)
		}

		// 3b. Liveness check inside the lock — early return if already alive.
		if existing := rs.FindEntry(id); existing != nil && registry.IsPIDAlive(existing.PID) {
			alreadyRunning = true
			alreadyRunningPID = existing.PID
			return nil
		}

		// 3c. Spawn the detached subprocess.
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

		// 3d. Spawn the watcher process.
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
			Files: []*os.File{nil, nil, nil},
			Sys:   &watcherSysProcAttr,
		})
		if err != nil {
			return fmt.Errorf("start watcher: %w", err)
		}
		_ = watcherProc.Release()

		// 3e. Write the new registry entry — only reached if both spawns succeeded.
		rs.RemoveEntry(id)
		rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
			SensorID:   id,
			PID:        det.PID,
			PGID:       det.PGID,
			WatcherPID: watcherProc.Pid,
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

		spawned = spawnResult{det: det, watcherProc: watcherProc, envelope: envelope}
		return nil
	})

	if lockErr != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("write registry: %v", lockErr))
	}

	if alreadyRunning {
		sig := buildStartedSkeleton(res, id, sensorJSON)
		sig["verdict"] = "error"
		sig["severity"] = "high"
		sig["evidence"] = []interface{}{map[string]interface{}{
			"rationale": fmt.Sprintf("sensor %q already running with pid %d", id, alreadyRunningPID),
		}}
		sig["metadata"].(map[string]interface{})["kind"] = "start_rejected"
		return 1, validateSignal(v, sig, id)
	}

	sig := buildStartedSkeleton(res, id, sensorJSON)
	sig["verdict"] = "pass"
	sig["severity"] = "info"
	sig["evidence"] = []interface{}{map[string]interface{}{
		"rationale": fmt.Sprintf("sensor %q started, pid=%d, watcher_pid=%d", id, spawned.det.PID, spawned.watcherProc.Pid),
	}}
	sig["run_id"] = spawned.envelope.RunID
	sig["started_at"] = spawned.envelope.StartedAt
	md := sig["metadata"].(map[string]interface{})
	md["kind"] = "started"
	md["pid"] = spawned.det.PID
	md["watcher_pid"] = spawned.watcherProc.Pid
	md["log_dir"] = filepath.Join(".runtime", "sensors", id)
	md["next_cursor"] = 0
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

// buildStartedSkeleton returns the envelope fields common to all started/rejected
// signals, including the registry diagnose metadata. Callers must set verdict,
// severity, and evidence explicitly so the intent is clear at each call site.
func buildStartedSkeleton(res registry.Result, id string, sensorJSON map[string]interface{}) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     stringField(sensorJSON, "version"),
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"confidence":  1.0,
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":            "started",
			"registry_path":   res.Root.RegistryFile(),
			"registry_source": string(res.Source),
			"registry_exists": res.Exists,
		},
	}
}

func errorSignal(res registry.Result, id, rationale string) map[string]interface{} {
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
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":            "start_failed",
			"registry_path":   res.Root.RegistryFile(),
			"registry_source": string(res.Source),
			"registry_exists": res.Exists,
		},
	}
}

// validateSignal checks sig against signal.json. If validation fails it logs
// the error to stderr and returns a minimal emergency signal so the bug
// surfaces without recursion. On success it returns sig unchanged.
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
