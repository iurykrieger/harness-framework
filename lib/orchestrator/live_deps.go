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
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

// RunWithDepsRoot is the id-resolving variant of RunWithDeps. The
// requested sensor is identified by id (resolved to <root>/sensors/<id>.json),
// schemasDir is resolved by the schema package's discovery if empty.
// All blocking deps along the chain are started/attached before the
// requested sensor runs and stopped/detached after.
func RunWithDepsRoot(ctx context.Context, id, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
	path := filepath.Join(projectRoot, "sensors", id+".json")
	return RunWithDeps(ctx, path, schemasDir, stdout, stderr)
}

// AttachLiveDep starts (or attaches to) a blocking dep. Emits a
// `dep_attached` or `dep_started` Signal on stdout. Returns the dep id
// so the caller can stack it for detach.
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
func AttachLiveDep(ctx context.Context, dep Sensor, projectRoot, holderID string, holderPID int, v *schema.Validator, stdout, stderr io.Writer) (string, error) {
	r := registry.NewRoot(projectRoot)
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	holder := registry.HeldByEntry{Kind: "sensor", ID: holderID, PID: holderPID, AttachedAt: now}

	startedFresh := false
	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		existing := rs.FindEntry(dep.ID)
		if existing != nil && registry.IsPIDAlive(existing.PID) {
			reapDeadSameIDHolders(existing, holderID)
			if !hasLiveSameIDHolder(existing, holderID) {
				registry.AddHolder(existing, holder)
			}
			return registry.Save(r, rs)
		}
		// Not live: start it.
		startedFresh = true
		return startBlockingDep(&rs, r, dep, holder)
	}); err != nil {
		return "", err
	}

	kind := "dep_attached"
	if startedFresh {
		kind = "dep_started"
	}
	sig := buildSimpleSignal(dep.ID, "pass", "info", kind, fmt.Sprintf("blocking dep %q held by %q", dep.ID, holderID))
	sig = validateOrFallback(v, sig, dep.ID, stderr)
	_ = json.NewEncoder(stdout).Encode(sig)
	return dep.ID, nil
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
func DetachLiveDep(depID, projectRoot, holderID string, v *schema.Validator, stdout, stderr io.Writer) {
	r := registry.NewRoot(projectRoot)
	var entry *registry.RunningSensorEntry
	stopNow := false
	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		entry = rs.FindEntry(depID)
		if entry == nil {
			return nil
		}
		// Copy entry before removing so we can reference it after.
		entryCopy := *entry
		entry = &entryCopy
		registry.RemoveHolder(rs.FindEntry(depID), registry.HeldByEntry{Kind: "sensor", ID: holderID, PID: os.Getpid()})
		if !registry.IsHeld(rs.FindEntry(depID)) {
			stopNow = true
		}
		return registry.Save(r, rs)
	})
	if entry == nil {
		return
	}
	if !stopNow {
		sig := buildSimpleSignal(depID, "pass", "info", "dep_detached", fmt.Sprintf("blocking dep %q remains held", depID))
		sig = validateOrFallback(v, sig, depID, stderr)
		_ = json.NewEncoder(stdout).Encode(sig)
		return
	}
	stopBlockingDep(r, entry, v, stdout, stderr)
}

// startBlockingDep is called from AttachLiveDep under flock. It spawns
// the dep's command detached and writes a registry entry with the given
// holder. No watcher process is spawned for orchestrator-managed deps —
// the dep runs unobserved (signals.log stays empty); /stop-sensor of
// dep would also see no individuals, which is intentional for the
// orchestrator path (the dependent's aggregate is what matters).
func startBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry) error {
	execMap, _ := dep.JSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)
	if err := os.MkdirAll(r.SensorDir(dep.ID), 0o755); err != nil {
		return fmt.Errorf("mkdir log dir: %w", err)
	}
	if err := os.WriteFile(r.RawLog(dep.ID), nil, 0o644); err != nil {
		return fmt.Errorf("create raw.log: %w", err)
	}
	if err := os.WriteFile(r.SignalsLog(dep.ID), nil, 0o644); err != nil {
		return fmt.Errorf("create signals.log: %w", err)
	}
	det, err := subprocess.SpawnDetached(subprocess.DetachConfig{Command: command, LogFile: r.RawLog(dep.ID)})
	if err != nil {
		return fmt.Errorf("spawn: %w", err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	rs.RemoveEntry(dep.ID)
	rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
		SensorID:   dep.ID,
		PID:        det.PID,
		PGID:       det.PGID,
		WatcherPID: 0,
		StartedAt:  now,
		Command:    command,
		LogDir:     filepath.Join(".runtime", "sensors", dep.ID),
		HeldBy:     []registry.HeldByEntry{holder},
	})
	return registry.Save(r, *rs)
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
	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntry(entry.SensorID)
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
		entry := rs.FindEntry(depID)
		if entry == nil {
			return nil
		}
		matched := false
		for i := range entry.HeldBy {
			h := &entry.HeldBy[i]
			if h.Kind == "sensor" && h.ID == holderID && h.PID == oldPID {
				h.PID = newPID
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		return registry.Save(r, rs)
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
