package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

// LiveDep identifies a single live blocking-dep entry that
// AttachLiveDep attached to. The pair (ID, RunID) is the unique key
// that DetachLiveDep uses to address exactly the registry entry we
// hold — never a non-blocking entry that happens to share the same
// sensor id.
type LiveDep struct {
	ID    string
	RunID string
}

// AttachResult is the structured return of AttachLiveDep. Exactly one of
// Live or GateSignal is populated on err==nil:
//
//	Live.ID != ""       → attach succeeded (fresh spawn or re-attach).
//	                      Caller pushes Live onto its LiveStack for later
//	                      detach.
//	GateSignal != nil   → spawn-fresh path detected an unmet precondition.
//	                      No subprocess was spawned and no registry entry
//	                      was created. Caller emits the signal and records
//	                      it for downstream cascade machinery.
type AttachResult struct {
	Live       LiveDep
	GateSignal map[string]interface{}
}

// RunWithDepsRoot is the id-resolving variant of RunWithDeps. The
// requested sensor is identified by id (resolved to <root>/.harness/sensors/<id>.json),
// schemasDir is resolved by the schema package's discovery if empty.
// All blocking deps along the chain are started/attached before the
// requested sensor runs and stopped/detached after.
func RunWithDepsRoot(ctx context.Context, id, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
	path, err := sensor.Resolve(id, projectRoot)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return 2
	}
	root := registry.NewRoot(projectRoot)
	return runWithDepsImpl(ctx, path, schemasDir, &root, projectRoot, stdout, stderr)
}

// AttachLiveDep starts (or attaches to) a blocking dep. Emits a
// `dep_attached` or `dep_started` Signal on stdout. Returns a LiveDep
// carrying the dep's id and run_id so the caller can stack it for
// detach by exact run_id (never by id, which could match a sibling
// non-blocking entry of the same sensor).
//
// holderPID is recorded in held_by as the holder's pid. Callers that are
// the holder use os.Getpid(); callers that will hand the holder over to
// a different process (notably /start-sensor, which spawns a detached
// subprocess that becomes the holder) pass a placeholder pid and later
// rebind via RebindDepHolderPID.
//
// Reap-on-attach: when the dep is alive and we are adding a new
// (kind=sensor, id=holderID) holder, any pre-existing
// (kind=sensor, id=holderID, pid=DEAD) entries are dropped first. This
// prevents accumulation of dead holders across re-runs of the same
// holder identity (e.g., /start-sensor target re-runs after start.go
// crashes between AttachLiveDep and RebindDepHolderPID).
func AttachLiveDep(
	ctx context.Context,
	dep Sensor,
	projectRoot, holderID string,
	holderPID int,
	v *schema.Validator,
	stdout, stderr io.Writer,
) (AttachResult, error) {
	r := registry.NewRoot(projectRoot)
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	holder := registry.HeldByEntry{Kind: "sensor", ID: holderID, PID: holderPID, AttachedAt: now}

	startedFresh := false
	var runID string
	var gateSig map[string]interface{}
	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		existing := rs.FindBlockingEntry(dep.ID)
		if existing != nil && registry.IsPIDAlive(existing.PID) {
			reapDeadSameIDHolders(existing, holderID)
			if !hasLiveSameIDHolder(existing, holderID) {
				registry.AddHolder(existing, holder)
			}
			runID = existing.RunID
			return registry.Save(r, rs)
		}
		// Spawn-fresh branch — gate the dep's requires[] BEFORE startBlockingDep.
		// Re-attach (above) explicitly does NOT gate: the dep is already alive
		// with whatever env/PATH it spawned with; gating with the current
		// holder's environment would falsely abort legitimate attaches when
		// the holder's PATH/env differs from the dep's spawn-time environment.
		env, eerr := sensor.BuildEnvelope(dep.JSON)
		if eerr != nil {
			return fmt.Errorf("build envelope for gate: %w", eerr)
		}
		output, _ := dep.JSON["output"].(string)
		if sig, failed := PreflightGate(dep, env, output); failed {
			gateSig = sig
			return nil
		}
		startedFresh = true
		newID, startErr := startBlockingDep(&rs, r, dep, holder, projectRoot)
		if startErr != nil {
			return startErr
		}
		runID = newID
		return nil
	}); err != nil {
		return AttachResult{}, err
	}

	if gateSig != nil {
		gateSig = validateOrFallback(v, gateSig, dep.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(gateSig)
		return AttachResult{GateSignal: gateSig}, nil
	}

	kind := "dep_attached"
	if startedFresh {
		kind = "dep_started"
	}
	sig := buildSimpleSignal(dep.ID, "pass", "info", kind, fmt.Sprintf("blocking dep %q held by %q", dep.ID, holderID))
	sig = validateOrFallback(v, sig, dep.ID, stderr)
	_ = json.NewEncoder(stdout).Encode(sig)
	return AttachResult{Live: LiveDep{ID: dep.ID, RunID: runID}}, nil
}

// reapDeadSameIDHolders drops every (kind="sensor", id=holderID, pid=DEAD)
// entry from entry.HeldBy in place. Live holders, manual holders, and
// holders with different ids are preserved.
//
// Similar to registry.ReapDead but scoped to holders that match a
// specific holderID. Used by AttachLiveDep to clean stale entries left
// over from a previous run of the same holder (e.g., a /start-sensor
// that crashed after AttachLiveDep but before RebindDepHolderPID).
func reapDeadSameIDHolders(entry *registry.RunningSensorEntry, holderID string) {
	keep := entry.HeldBy[:0]
	for _, h := range entry.HeldBy {
		if h.Kind == "sensor" && h.ID == holderID && !registry.IsPIDAlive(h.PID) {
			continue
		}
		keep = append(keep, h)
	}
	entry.HeldBy = keep
}

// hasLiveSameIDHolder returns true when entry.HeldBy contains at least one
// (kind="sensor", id=holderID, pid=ALIVE) entry.
//
// Used by AttachLiveDep to make re-attach idempotent: if a live holder
// with the same id already exists, skip adding a duplicate. Combined
// with reapDeadSameIDHolders, this keeps held_by free of duplicates
// per logical (id, lifetime) pair without requiring the caller to
// pre-check.
func hasLiveSameIDHolder(entry *registry.RunningSensorEntry, holderID string) bool {
	for _, h := range entry.HeldBy {
		if h.Kind == "sensor" && h.ID == holderID && registry.IsPIDAlive(h.PID) {
			return true
		}
	}
	return false
}

// DetachLiveDep removes the holder from dep's HeldBy. If HeldBy becomes
// empty, the dep is stopped (SIGTERM/SIGKILL, registry cleanup) and an
// aggregate Signal is emitted on stdout. Otherwise emits dep_detached.
//
// The dep is addressed by (ID, RunID): RunID disambiguates the blocking
// entry we attached to from any sibling non-blocking entries of the
// same sensor id, so detach never accidentally tears down work the
// orchestrator does not own.
func DetachLiveDep(dep LiveDep, projectRoot, holderID string, v *schema.Validator, stdout, stderr io.Writer) {
	r := registry.NewRoot(projectRoot)
	var entry *registry.RunningSensorEntry
	stopNow := false
	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		entry = rs.FindEntryByRunID(dep.RunID)
		if entry == nil {
			return nil
		}
		// Copy entry before removing so we can reference it after.
		entryCopy := *entry
		entry = &entryCopy
		registry.RemoveHolder(rs.FindEntryByRunID(dep.RunID), registry.HeldByEntry{Kind: "sensor", ID: holderID, PID: os.Getpid()})
		if !registry.IsHeld(rs.FindEntryByRunID(dep.RunID)) {
			stopNow = true
		}
		return registry.Save(r, rs)
	})
	if entry == nil {
		return
	}
	if !stopNow {
		sig := buildSimpleSignal(dep.ID, "pass", "info", "dep_detached", fmt.Sprintf("blocking dep %q remains held", dep.ID))
		sig = validateOrFallback(v, sig, dep.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(sig)
		return
	}
	stopBlockingDep(r, entry, v, stdout, stderr)
}

// startBlockingDep is called from AttachLiveDep under flock. It spawns
// the dep's command detached, renames the staging raw.log into a
// per-run directory, spawns a watcher that tails the raw.log and emits
// parsed Signals to signals.log, and writes a registry entry with the
// given holder and the spawned watcher's PID. Returns the freshly-minted
// run_id so the caller can thread it into LiveDep.
//
// projectRoot is set as the working directory for the detached subprocess
// so the blocking dep's command runs from the user's project directory,
// not from the runner's own cwd.
//
// CLAUDE_PLUGIN_ROOT must be set in the environment so lib/watcher.Spawn
// can locate the watcher source tree (the watcher is launched via
// `go run -tags=start_watcher`). Missing CLAUDE_PLUGIN_ROOT aborts the
// spawn before any side effects.
func startBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry, projectRoot string) (string, error) {
	pluginRoot := os.Getenv("CLAUDE_PLUGIN_ROOT")
	if pluginRoot == "" {
		return "", fmt.Errorf("plugin root not set (set CLAUDE_PLUGIN_ROOT)")
	}

	execMap, _ := dep.JSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)

	if err := os.MkdirAll(r.SensorDir(dep.ID), 0o755); err != nil {
		return "", fmt.Errorf("mkdir sensor dir: %w", err)
	}

	// Stage 1: pre-create the staging raw.log at the flat SensorDir path.
	// SpawnDetached opens this for stdout+stderr; we rename it into
	// <run-id>/raw.log once the PID is known. os.Rename on the same
	// filesystem preserves the subprocess's open fd, so writes continue
	// uninterrupted at the new path.
	stagingRaw := r.RawLog(dep.ID)
	if err := os.WriteFile(stagingRaw, nil, 0o644); err != nil {
		return "", fmt.Errorf("create staging raw.log: %w", err)
	}

	// Stage 2: spawn the subprocess detached.
	det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: command,
		LogFile: stagingRaw,
		Dir:     projectRoot,
	})
	if err != nil {
		_ = os.Remove(stagingRaw)
		return "", fmt.Errorf("spawn: %w", err)
	}

	// Stage 3: derive composite run_id from the freshly-spawned PID
	// and a short UUID. This becomes the per-run directory name and
	// the run_id carried on every Signal the watcher emits.
	shortUUID := uuid.NewString()
	if len(shortUUID) >= 8 {
		shortUUID = shortUUID[:8]
	}
	runID := fmt.Sprintf("%d-%s", det.PID, shortUUID)
	runDir := r.RunDir(dep.ID, runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		if det.PGID > 0 {
			_ = syscall.Kill(-det.PGID, syscall.SIGKILL)
		}
		_ = os.Remove(stagingRaw)
		return "", fmt.Errorf("mkdir run dir: %w", err)
	}

	// Stage 4: rename the staging raw.log into <run-id>/raw.log.
	// Atomic on POSIX; subprocess's open fd survives the rename.
	rawPath := r.RawLogRun(dep.ID, runID)
	if err := os.Rename(stagingRaw, rawPath); err != nil {
		if det.PGID > 0 {
			_ = syscall.Kill(-det.PGID, syscall.SIGKILL)
		}
		_ = os.Remove(stagingRaw)
		_ = os.RemoveAll(runDir)
		return "", fmt.Errorf("rename raw.log into run dir: %w", err)
	}

	sigsPath := r.SignalsLogRun(dep.ID, runID)
	if err := os.WriteFile(sigsPath, nil, 0o644); err != nil {
		if det.PGID > 0 {
			_ = syscall.Kill(-det.PGID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(runDir)
		return "", fmt.Errorf("create signals.log: %w", err)
	}

	envelope, eerr := sensor.BuildEnvelope(dep.JSON)
	if eerr != nil {
		if det.PGID > 0 {
			_ = syscall.Kill(-det.PGID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(runDir)
		return "", fmt.Errorf("build envelope: %w", eerr)
	}
	envelope.RunID = runID

	patterns := []interface{}{}
	if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
		if raw, ok := op["patterns"].([]interface{}); ok {
			patterns = raw
		}
	}
	patternsJSON, _ := json.Marshal(patterns)
	envelopeJSON, _ := json.Marshal(envelope)

	// Stage 5: spawn the watcher via lib/watcher.
	watcherPID, err := watcher.Spawn(watcher.SpawnOpts{
		PluginRoot:     pluginRoot,
		ProjectRoot:    projectRoot,
		SensorID:       dep.ID,
		RunID:          runID,
		RawLogPath:     rawPath,
		SignalsLogPath: sigsPath,
		EnvelopeJSON:   envelopeJSON,
		PatternsJSON:   patternsJSON,
		SubprocessPID:  det.PID,
		WatcherLogPath: filepath.Join(runDir, "watcher.log"),
	})
	if err != nil {
		if det.PGID > 0 {
			_ = syscall.Kill(-det.PGID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(runDir)
		return "", fmt.Errorf("start watcher: %w", err)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
		SensorID:   dep.ID,
		RunID:      runID,
		Blocking:   true,
		PID:        det.PID,
		PGID:       det.PGID,
		WatcherPID: watcherPID,
		StartedAt:  now,
		Command:    command,
		LogDir:     r.RelativeRunDir(dep.ID, runID),
		HeldBy:     []registry.HeldByEntry{holder},
	})
	if err := registry.Save(r, *rs); err != nil {
		if det.PGID > 0 {
			_ = syscall.Kill(-det.PGID, syscall.SIGKILL)
		}
		if watcherPID > 0 {
			_ = syscall.Kill(watcherPID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(runDir)
		// Remove the entry we appended so callers see the registry as it
		// was before the failed Save.
		rs.Entries = rs.Entries[:len(rs.Entries)-1]
		return "", err
	}
	return runID, nil
}

// stringFieldFromJSON extracts a string field from a sensor's parsed JSON
// without panicking on type mismatch. Local helper to keep the orchestrator
// independent of start.go's stringField (same purpose, different package).
func stringFieldFromJSON(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// stopBlockingDep terminates the dep's process group and removes its
// registry entry. Emits an aggregate Signal on stdout.
func stopBlockingDep(r registry.Root, entry *registry.RunningSensorEntry, v *schema.Validator, stdout, stderr io.Writer) {
	gracefulMS := 5000
	if entry.PGID > 0 {
		_ = syscall.Kill(-entry.PGID, syscall.SIGTERM)
		deadline := time.Now().Add(time.Duration(gracefulMS) * time.Millisecond)
		for time.Now().Before(deadline) {
			if !registry.IsPIDAlive(entry.PID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if registry.IsPIDAlive(entry.PID) {
			_ = syscall.Kill(-entry.PGID, syscall.SIGKILL)
		}
	}
	// Kill the watcher subprocess if one was registered. Mirrors the
	// stopWatcher helper in skills/stop-sensor/scripts/stop.go.
	if entry.WatcherPID > 0 && registry.IsPIDAlive(entry.WatcherPID) {
		_ = syscall.Kill(entry.WatcherPID, syscall.SIGTERM)
		watcherDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(watcherDeadline) {
			if !registry.IsPIDAlive(entry.WatcherPID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if registry.IsPIDAlive(entry.WatcherPID) {
			_ = syscall.Kill(entry.WatcherPID, syscall.SIGKILL)
		}
	}
	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntryByRunID(entry.RunID)
		return registry.Save(r, rs)
	})
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	agg := map[string]interface{}{
		"sensor_id":   entry.SensorID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  entry.StartedAt,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": fmt.Sprintf("blocking dep %q stopped on detach", entry.SensorID)}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "aggregate", "command": entry.Command, "output_mode": "stream", "counts": map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 0}},
	}
	agg = validateOrFallback(v, agg, entry.SensorID, stderr)
	_ = json.NewEncoder(stdout).Encode(agg)
}

// RebindDepHolderPID atomically updates the pid of a holder in dep.HeldBy.
// Match by (kind="sensor", id=holderID, pid=oldPID); if found, swap to
// newPID. Idempotent: no matching holder (or no dep entry at all) →
// silent no-op (returns nil).
//
// Used by /start-sensor after spawning the root subprocess to swap the
// placeholder pid (os.Getpid() of start.go) for the actual root subproc
// pid, so /list-sensors and /stop-sensor see a holder pid that mirrors
// the root sensor's lifetime.
func RebindDepHolderPID(depID, projectRoot, holderID string, oldPID, newPID int) error {
	r := registry.NewRoot(projectRoot)
	return registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		entry := rs.FindBlockingEntry(depID)
		if entry == nil {
			return nil
		}
		for i := range entry.HeldBy {
			h := &entry.HeldBy[i]
			if h.Kind == "sensor" && h.ID == holderID && h.PID == oldPID {
				h.PID = newPID
				return registry.Save(r, rs)
			}
		}
		return nil
	})
}

func buildSimpleSignal(id, verdict, severity, kind, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
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
		"metadata":    map[string]interface{}{"kind": kind},
	}
}

// validateOrFallback validates a signal and falls back to a minimal valid
// emergency signal on failure. This ensures orchestrator-emitted signals
// always conform to schemas/signal.json.
func validateOrFallback(v *schema.Validator, sig map[string]interface{}, id string, stderr io.Writer) map[string]interface{} {
	if v == nil {
		return sig
	}
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(stderr, "orchestrator: emitted signal failed validation: %v\n", err)
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
			"evidence":    []interface{}{map[string]interface{}{"rationale": fmt.Sprintf("emitted signal invalid: %v", err)}},
			"cost_actual": map[string]interface{}{"latency_ms": 0},
			"metadata":    map[string]interface{}{"kind": "signal_validation_failed"},
		}
	}
	return sig
}
