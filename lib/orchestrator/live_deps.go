package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	libsignal "github.com/iurykrieger/harness-framework/lib/signal"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

// Tunables for the health-gate + stop-aggregate path. Values match the
// rationale in docs/superpowers/specs/2026-05-14-blocking-dep-fail-fast-design.md.
// Declared as vars (not consts) so tests can shorten them.
var (
	healthGateTimeout      = 5 * time.Second
	healthGatePollInterval = 100 * time.Millisecond
	watcherDrainTimeout    = 1 * time.Second
	stopGracefulMS         = 5000
)

const rawLogTailLines = 40

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
// requested sensor is identified by id (resolved to <root>/.harness/sensors/<id>.yaml),
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
	var freshSpawn *spawnedDep
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
		sp, startErr := startBlockingDep(&rs, r, dep, holder, projectRoot)
		if startErr != nil {
			return startErr
		}
		runID = sp.RunID
		freshSpawn = &sp
		return nil
	}); err != nil {
		return AttachResult{}, err
	}

	if gateSig != nil {
		gateSig = validateOrFallback(v, gateSig, dep.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(gateSig)
		return AttachResult{GateSignal: gateSig}, nil
	}

	// Re-attach to live dep: no health gate. Existing watcher continues;
	// soundness was established when the first holder ran its gate.
	if !startedFresh {
		sig := buildSimpleSignal(dep.ID, "pass", "info", "dep_attached", fmt.Sprintf("blocking dep %q held by %q", dep.ID, holderID))
		sig = validateOrFallback(v, sig, dep.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(sig)
		return AttachResult{Live: LiveDep{ID: dep.ID, RunID: runID}}, nil
	}

	// Fresh-spawn branch: run health gate via watcher.WaitForReady.
	res := watcher.WaitForReady(watcher.HealthGateOpts{
		SignalsLogPath: freshSpawn.SignalsLogPath,
		SubprocessPID:  freshSpawn.PID,
		Timeout:        healthGateTimeout,
		PollInterval:   healthGatePollInterval,
	})

	switch res.Outcome {
	case watcher.OutcomeReady:
		sig := buildSimpleSignal(dep.ID, "pass", "info", "dep_started", fmt.Sprintf("blocking dep %q held by %q", dep.ID, holderID))
		sig = validateOrFallback(v, sig, dep.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(sig)
		return AttachResult{Live: LiveDep{ID: dep.ID, RunID: runID}}, nil

	case watcher.OutcomeTimedOut:
		sig := buildSimpleSignal(dep.ID, "pass", "info", "dep_started", fmt.Sprintf("blocking dep %q held by %q (health gate timed out, proceeding)", dep.ID, holderID))
		md, _ := sig["metadata"].(map[string]interface{})
		md["health_gate"] = "timed_out_proceeding"
		sig = validateOrFallback(v, sig, dep.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(sig)
		return AttachResult{Live: LiveDep{ID: dep.ID, RunID: runID}}, nil

	case watcher.OutcomeFailed, watcher.OutcomeDiedSilently:
		tearDownFailedSpawn(r, freshSpawn)
		var failSig map[string]interface{}
		if res.Outcome == watcher.OutcomeFailed {
			failSig = buildDepStartFailedSignal(dep.ID, res.Signal, "")
		} else {
			tail := readRawLogTail(freshSpawn.RawLogPath, rawLogTailLines)
			failSig = buildDepStartFailedSignal(dep.ID, nil, tail)
		}
		failSig = validateOrFallback(v, failSig, dep.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(failSig)
		return AttachResult{GateSignal: failSig}, nil
	}

	// Should not be reached — defensive fallback.
	return AttachResult{Live: LiveDep{ID: dep.ID, RunID: runID}}, nil
}

// buildDepStartFailedSignal constructs the failure signal emitted by
// AttachLiveDep when the health gate detects a boot-time failure. When
// observed != nil the signal records the watcher's individual signal that
// triggered the failure; when rawTail != "" the signal records the tail of
// raw.log (used for died_silently when no signal was ever emitted).
func buildDepStartFailedSignal(depID string, observed map[string]interface{}, rawTail string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	ev := map[string]interface{}{}
	if observed != nil {
		v, _ := observed["verdict"].(string)
		s, _ := observed["severity"].(string)
		ev["rationale"] = fmt.Sprintf("blocking dep %q emitted verdict=%s/%s during health gate; aborting attach", depID, v, s)
	} else {
		ev["rationale"] = fmt.Sprintf("blocking dep %q subprocess died before emitting any signal; aborting attach", depID)
	}
	if rawTail != "" {
		ev["excerpt"] = rawTail
	}
	md := map[string]interface{}{"kind": "dep_start_failed"}
	if observed != nil {
		md["observed_signal"] = observed
		md["cause"] = "watcher_reported_failure"
	} else {
		md["cause"] = "subprocess_died_silently"
	}
	return map[string]interface{}{
		"sensor_id":   depID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "fail",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{ev},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}

// tearDownFailedSpawn cleans up a blocking dep whose health gate just
// reported failure. Sends SIGTERM to the subprocess group (no-op when
// already dead), signals the watcher to drain, and removes the registry
// entry. No aggregate Signal is emitted — AttachLiveDep emits
// dep_start_failed instead, which is the canonical evidence for cascade.
func tearDownFailedSpawn(r registry.Root, sp *spawnedDep) {
	if sp == nil {
		return
	}
	if sp.PGID > 0 && watcher.IsSubprocessAlive(sp.PID) {
		_ = syscall.Kill(-sp.PGID, syscall.SIGTERM)
		graceful := sp.GracefulMS
		if graceful <= 0 {
			graceful = stopGracefulMS
		}
		deadline := time.Now().Add(time.Duration(graceful) * time.Millisecond)
		for time.Now().Before(deadline) {
			if !watcher.IsSubprocessAlive(sp.PID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if watcher.IsSubprocessAlive(sp.PID) {
			_ = syscall.Kill(-sp.PGID, syscall.SIGKILL)
		}
	}
	if sp.WatcherPID > 0 {
		_ = syscall.Kill(sp.WatcherPID, syscall.SIGTERM)
		drainWatcher(sp.WatcherPID, watcherDrainTimeout)
	}
	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntryByRunID(sp.RunID)
		return registry.Save(r, rs)
	})
}

// drainWatcher polls IsPIDAlive(watcherPID) until it dies or the timeout
// elapses. Bounded so a misbehaving watcher cannot block detach.
func drainWatcher(watcherPID int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !registry.IsPIDAlive(watcherPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readRawLogTail returns the last n lines of path as a single newline-joined
// string. Missing or unreadable files return "" so the caller can degrade
// gracefully.
func readRawLogTail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	ring := make([]string, 0, n)
	for sc.Scan() {
		if len(ring) >= n {
			ring = ring[1:]
		}
		ring = append(ring, sc.Text())
	}
	out := ""
	for i, line := range ring {
		if i > 0 {
			out += "\n"
		}
		out += line
	}
	return out
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

// spawnedDep collects everything AttachLiveDep needs to run the health gate
// and (on failure) clean up after a fresh-spawn blocking dep.
type spawnedDep struct {
	RunID          string
	PID            int
	PGID           int
	WatcherPID     int
	RawLogPath     string
	SignalsLogPath string
	GracefulMS     int
}

// startBlockingDep is called from AttachLiveDep under flock. It spawns the
// dep's command detached, renames the staging raw.log into a per-run
// directory, spawns the watcher binary alongside it (which tails raw.log and
// appends parsed Signals to signals.log), and writes a registry entry with
// the given holder and the spawned watcher's PID. Returns a spawnedDep with
// the freshly-minted run_id and the paths the health gate needs to read.
//
// projectRoot is set as the working directory for the detached subprocess so
// the blocking dep's command runs from the user's project directory, not
// from the runner's own cwd.
//
// CLAUDE_PLUGIN_ROOT must be set in the environment so lib/watcher.Spawn can
// locate the watcher source tree (the watcher is launched via `go run
// -tags=start_watcher`). Missing CLAUDE_PLUGIN_ROOT aborts the spawn before
// any side effects.
//
// AttachLiveDep then runs watcher.WaitForReady against signals.log to
// determine whether the dep is healthy enough to proceed (see issue #46).
func startBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry, projectRoot string) (spawnedDep, error) {
	pluginRoot := os.Getenv("CLAUDE_PLUGIN_ROOT")
	if pluginRoot == "" {
		return spawnedDep{}, fmt.Errorf("plugin root not set (set CLAUDE_PLUGIN_ROOT)")
	}

	execMap, _ := dep.JSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)

	if err := os.MkdirAll(r.SensorDir(dep.ID), 0o755); err != nil {
		return spawnedDep{}, fmt.Errorf("mkdir sensor dir: %w", err)
	}

	// Stage 1: pre-create the staging raw.log at the flat SensorDir path.
	// SpawnDetached opens this for stdout+stderr; we rename it into
	// <run-id>/raw.log once the PID is known. os.Rename on the same
	// filesystem preserves the subprocess's open fd, so writes continue
	// uninterrupted at the new path.
	stagingRaw := r.RawLog(dep.ID)
	if err := os.WriteFile(stagingRaw, nil, 0o644); err != nil {
		return spawnedDep{}, fmt.Errorf("create staging raw.log: %w", err)
	}

	// Stage 2: spawn the subprocess detached.
	det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: command,
		LogFile: stagingRaw,
		Dir:     projectRoot,
	})
	if err != nil {
		_ = os.Remove(stagingRaw)
		return spawnedDep{}, fmt.Errorf("spawn: %w", err)
	}

	// Stage 3: derive composite run_id from the freshly-spawned PID and a
	// short UUID. This becomes the per-run directory name and the run_id
	// carried on every Signal the watcher emits.
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
		return spawnedDep{}, fmt.Errorf("mkdir run dir: %w", err)
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
		return spawnedDep{}, fmt.Errorf("rename raw.log into run dir: %w", err)
	}

	sigsPath := r.SignalsLogRun(dep.ID, runID)
	if err := os.WriteFile(sigsPath, nil, 0o644); err != nil {
		if det.PGID > 0 {
			_ = syscall.Kill(-det.PGID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(runDir)
		return spawnedDep{}, fmt.Errorf("create signals.log: %w", err)
	}

	envelope, eerr := sensor.BuildEnvelope(dep.JSON)
	if eerr != nil {
		if det.PGID > 0 {
			_ = syscall.Kill(-det.PGID, syscall.SIGKILL)
		}
		_ = os.RemoveAll(runDir)
		return spawnedDep{}, fmt.Errorf("build envelope: %w", eerr)
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
		return spawnedDep{}, fmt.Errorf("start watcher: %w", err)
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
		return spawnedDep{}, err
	}
	return spawnedDep{
		RunID:          runID,
		PID:            det.PID,
		PGID:           det.PGID,
		WatcherPID:     watcherPID,
		RawLogPath:     rawPath,
		SignalsLogPath: sigsPath,
		GracefulMS:     readGracefulTimeoutMS(dep.JSON),
	}, nil
}

// readGracefulTimeoutMS extracts execution.graceful_timeout_ms from a sensor
// JSON. Default 5000ms when missing or malformed (matches the historical
// hard-coded value in stopBlockingDep).
func readGracefulTimeoutMS(s map[string]interface{}) int {
	execMap, _ := s["execution"].(map[string]interface{})
	if execMap == nil {
		return 5000
	}
	if v, ok := execMap["graceful_timeout_ms"].(float64); ok {
		return int(v)
	}
	return 5000
}

// stringFieldFromJSON extracts a string field from a sensor's parsed JSON
// without panicking on type mismatch. Local helper to keep the orchestrator
// independent of start.go's stringField (same purpose, different package).
func stringFieldFromJSON(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// stopBlockingDep terminates the dep's process group and removes its
// registry entry. Emits an aggregate Signal on stdout whose verdict reflects
// the worst-of of (a) what the watcher's individual signals reported during
// the dep's lifetime and (b) whether the subprocess was already dead when
// detach was called.
//
// Concretely:
//
//   - liveBeforeStop is captured before any SIGTERM is sent. When false, the
//     dep crashed on its own; the aggregate is forced to verdict=fail with
//     metadata.subprocess_state="died_before_stop", and a tail of raw.log is
//     attached as evidence.
//   - When liveBeforeStop is true, the dep is stopped via SIGTERM/SIGKILL of
//     its process group; the watcher is signalled to drain (SIGTERM on
//     WatcherPID) and given a bounded window to finish writing signals.log;
//     the aggregate verdict is then libsignal.MaxStreamVerdict over the
//     parsed individuals.
//   - signals.log being unreadable degrades to a pass aggregate with a note
//     in evidence — never blocks detach.
func stopBlockingDep(r registry.Root, entry *registry.RunningSensorEntry, v *schema.Validator, stdout, stderr io.Writer) {
	liveBeforeStop := watcher.IsSubprocessAlive(entry.PID)
	gracefulMS := stopGracefulMS
	if liveBeforeStop && entry.PGID > 0 {
		_ = syscall.Kill(-entry.PGID, syscall.SIGTERM)
		deadline := time.Now().Add(time.Duration(gracefulMS) * time.Millisecond)
		for time.Now().Before(deadline) {
			if !watcher.IsSubprocessAlive(entry.PID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if watcher.IsSubprocessAlive(entry.PID) {
			_ = syscall.Kill(-entry.PGID, syscall.SIGKILL)
		}
	}
	// Drain the watcher so signals.log captures any trailing individuals
	// emitted by the subprocess immediately before death, then force-kill if
	// it overstays the drain window. The drain wait is bounded by
	// watcherDrainTimeout so a misbehaving watcher cannot block detach;
	// SIGKILL is the upper bound for any watcher that ignored SIGTERM.
	if entry.WatcherPID > 0 && registry.IsPIDAlive(entry.WatcherPID) {
		_ = syscall.Kill(entry.WatcherPID, syscall.SIGTERM)
		drainWatcher(entry.WatcherPID, watcherDrainTimeout)
		if registry.IsPIDAlive(entry.WatcherPID) {
			_ = syscall.Kill(entry.WatcherPID, syscall.SIGKILL)
		}
	}

	// Load signals.log (run-specific path; fall back to the legacy flat path
	// for entries written before the per-run layout was introduced).
	individuals, readErr := readIndividuals(r, entry)

	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntryByRunID(entry.RunID)
		return registry.Save(r, rs)
	})

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	verdict, severity := libsignal.MaxStreamVerdict(individuals)
	counts := libsignal.CountVerdicts(individuals)

	evidence := []interface{}{}
	subprocessState := "stopped_on_detach"
	if !liveBeforeStop {
		subprocessState = "died_before_stop"
		// died_before_stop trumps a positive stream verdict — a service
		// that crashed on its own is a failure regardless of what it
		// emitted earlier.
		if libsignal.VerdictRank[verdict] < libsignal.VerdictRank["fail"] {
			verdict, severity = "fail", "high"
		}
		evidence = append(evidence, map[string]interface{}{
			"rationale": fmt.Sprintf("blocking dep %q subprocess died before stop was requested", entry.SensorID),
		})
		if tail := readRawLogTail(r.RawLogRun(entry.SensorID, entry.RunID), rawLogTailLines); tail != "" {
			evidence = append(evidence, map[string]interface{}{
				"rationale": "tail of raw.log immediately before subprocess exit",
				"excerpt":   tail,
			})
		}
	} else {
		evidence = append(evidence, map[string]interface{}{
			"rationale": fmt.Sprintf("blocking dep %q stopped on detach", entry.SensorID),
		})
	}
	if readErr != nil {
		evidence = append(evidence, map[string]interface{}{
			"rationale": fmt.Sprintf("signals.log read failed: %v", readErr),
		})
	}
	if topEv := libsignal.SelectTopEvidence(individuals, 3); len(topEv) > 0 {
		evidence = append(evidence, topEv...)
	}

	agg := map[string]interface{}{
		"sensor_id":   entry.SensorID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  entry.StartedAt,
		"finished_at": now,
		"verdict":     verdict,
		"severity":    severity,
		"confidence":  1.0,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":             "aggregate",
			"command":          entry.Command,
			"output_mode":      "stream",
			"counts":           counts,
			"subprocess_state": subprocessState,
		},
	}
	agg = validateOrFallback(v, agg, entry.SensorID, stderr)
	_ = json.NewEncoder(stdout).Encode(agg)
}

// readIndividuals parses signals.log for entry's run, returning the slice of
// individual signals (envelope/aggregate/cascade kinds are filtered out) and
// the first read error encountered. Legacy run_ids fall back to the flat
// per-sensor signals.log path.
func readIndividuals(r registry.Root, entry *registry.RunningSensorEntry) ([]map[string]interface{}, error) {
	path := r.SignalsLogRun(entry.SensorID, entry.RunID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Try the legacy flat path as a fallback (pre-per-run layout).
			legacy := r.LegacySignalsLog(entry.SensorID)
			f, err = os.Open(legacy)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, nil
				}
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	defer f.Close()

	var out []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if !looksIndividual(m) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// looksIndividual mirrors lib/watcher.isIndividualSignal but is local to
// orchestrator to avoid exporting that helper.
func looksIndividual(m map[string]interface{}) bool {
	verdict, _ := m["verdict"].(string)
	if verdict == "" {
		return false
	}
	md, _ := m["metadata"].(map[string]interface{})
	if md == nil {
		return true
	}
	kind, _ := md["kind"].(string)
	switch kind {
	case "envelope", "aggregate", "cascade", "started", "dep_started",
		"dep_attached", "dep_detached", "dep_start_failed", "failed":
		return false
	}
	return true
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
// always conform to schemas/signal.yaml.
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
