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
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start: cwd:", err)
		os.Exit(2)
	}
	exit, sig := runStart(root, os.Args[1:])
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}

// runStart performs the full /start-sensor lifecycle for sensor id given
// in args[0]. Returns (exitCode, finalSignal). The signal is encoded by
// the caller; tests inspect it directly.
func runStart(projectRoot string, args []string) (int, map[string]interface{}) {
	if len(args) < 1 {
		return 2, errorSignal("start", "missing sensor id argument")
	}
	id := args[0]

	path, err := libsensor.ResolveByID(id, projectRoot)
	if err != nil {
		return 2, errorSignal(id, fmt.Sprintf("resolve: %v", err))
	}

	sensorJSON, err := loadSensorJSON(path)
	if err != nil {
		return 2, errorSignal(id, err.Error())
	}

	// 1. Schema-validate the sensor definition before any further checks.
	v, code := schema.LoadValidator("", os.Stderr)
	if code != 0 {
		return code, errorSignal(id, "schema validator init failed")
	}
	if err := v.Validate(schema.TargetSensor, sensorJSON); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("schema: %v", err))
	}

	// 2. Reject non-blocking sensors with exit 2 (usage error).
	execMap, _ := sensorJSON["execution"].(map[string]interface{})
	blocking, _ := execMap["blocking"].(bool)
	if !blocking {
		return 2, errorSignal(id, "sensor is not blocking; use /run-sensor instead")
	}

	// 3. Check whether the sensor is already running.
	r := registry.NewRoot(projectRoot)
	rs, err := registry.Load(r)
	if err != nil {
		return 1, errorSignal(id, fmt.Sprintf("load registry: %v", err))
	}
	if existing := rs.FindEntry(id); existing != nil && registry.IsPIDAlive(existing.PID) {
		sig := buildStartedSkeleton(id, sensorJSON)
		sig["verdict"] = "error"
		sig["severity"] = "high"
		sig["evidence"] = []interface{}{map[string]interface{}{
			"rationale": fmt.Sprintf("sensor %q already running with pid %d", id, existing.PID),
		}}
		sig["metadata"].(map[string]interface{})["kind"] = "start_rejected"
		return 1, validateSignal(v, sig, id)
	}

	command, _ := execMap["command"].(string)
	logDir := r.SensorDir(id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("mkdir log dir: %v", err))
	}
	if err := os.WriteFile(r.RawLog(id), nil, 0o644); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("create raw.log: %v", err))
	}
	if err := os.WriteFile(r.SignalsLog(id), nil, 0o644); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("create signals.log: %v", err))
	}

	det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: command,
		LogFile: r.RawLog(id),
	})
	if err != nil {
		return 1, errorSignal(id, fmt.Sprintf("spawn: %v", err))
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

	watcherPath, err := watcherBinaryPath()
	if err != nil {
		return 1, errorSignal(id, fmt.Sprintf("watcher binary: %v", err))
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
		Files: []*os.File{nil, nil, nil},
		Sys:   &watcherSysProcAttr,
	})
	if err != nil {
		return 1, errorSignal(id, fmt.Sprintf("start watcher: %v", err))
	}
	_ = watcherProc.Release()

	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntry(id) // safety: we already verified it's not running
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
		return registry.Save(r, rs)
	}); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("write registry: %v", err))
	}

	sig := buildStartedSkeleton(id, sensorJSON)
	sig["verdict"] = "pass"
	sig["severity"] = "info"
	sig["evidence"] = []interface{}{map[string]interface{}{
		"rationale": fmt.Sprintf("sensor %q started, pid=%d, watcher_pid=%d", id, det.PID, watcherProc.Pid),
	}}
	sig["run_id"] = envelope.RunID
	sig["started_at"] = envelope.StartedAt
	md := sig["metadata"].(map[string]interface{})
	md["kind"] = "started"
	md["pid"] = det.PID
	md["watcher_pid"] = watcherProc.Pid
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
// signals. It does NOT set verdict, severity, or evidence — callers must set
// those explicitly so the intent is clear at each call site.
func buildStartedSkeleton(id string, sensorJSON map[string]interface{}) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     stringField(sensorJSON, "version"),
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"confidence":  1.0,
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "started"},
	}
}

func errorSignal(id, rationale string) map[string]interface{} {
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
		"metadata":    map[string]interface{}{"kind": "start_failed"},
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
