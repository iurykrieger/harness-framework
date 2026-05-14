package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/heal"
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

// depExitFileSettleTimeout is the maximum time AwaitDepLiveness waits for
// the exit_code sidecar to appear. A healthy long-running dep never writes
// this file, so this timeout is the full overhead for such deps. Dying deps
// typically write the file within a few milliseconds of spawn — the loop
// exits early as soon as the file appears.
const depExitFileSettleTimeout = 250 * time.Millisecond

// depExitFilePollInterval is the sleep between exit_code file checks inside
// AwaitDepLiveness's settle loop.
const depExitFilePollInterval = 10 * time.Millisecond

// AwaitDepLiveness checks every LiveDep in the passed slice for premature
// failure. For each dep, it briefly polls the exit_code sidecar file (up to
// depExitFileSettleTimeout) and exits the loop as soon as the file appears.
//
// Two signals tell us the dep is no longer healthy:
//   - exit_code sidecar exists with content → wrapper finished, command exited
//     (regardless of whether the shell PID lingers as an orphan — a macOS quirk).
//   - PID is dead AND no exit_code → dep was killed abnormally before the
//     wrapper could write the file.
//
// If a dep's exit_code file has content (non-zero) when the settle window
// closes, that dep has died prematurely: its honest aggregate is constructed
// and a (deadDepID, depAggregate, runID) triple is returned. The registry
// entry is removed before returning so DetachLiveDep (via defer detachAll)
// sees no entry and emits nothing — preventing a duplicate aggregate.
//
// When no dep has died within the settle window, returns ("", nil, "").
// Deps that die later are caught by stopBlockingDep at detach time.
//
// The returned depAggregate is NOT yet validated; callers send it
// through validateOrFallback before emitting.
func AwaitDepLiveness(deps []LiveDep, projectRoot string) (deadDepID string, depAggregate map[string]interface{}, depRunID string) {
	r := registry.NewRoot(projectRoot)
	for _, d := range deps {
		rs, err := registry.Load(r)
		if err != nil {
			continue
		}
		entry := rs.FindEntryByRunID(d.RunID)
		if entry == nil {
			continue
		}

		// Poll the exit_code sidecar file for up to depExitFileSettleTimeout.
		// A healthy long-running dep never writes this file, so the poll runs
		// its full duration and then moves on. A dep that exits quickly writes
		// the file within milliseconds; we break as soon as it appears.
		// The PID check provides an additional signal: if the PID is already
		// dead with no file written, the dep was killed abnormally (no cascade).
		exitFile := filepath.Join(r.SensorDir(d.ID), "exit_code")
		fileBody, fileErr := os.ReadFile(exitFile)
		fileHasContent := fileErr == nil && len(bytes.TrimSpace(fileBody)) > 0

		if !fileHasContent {
			// File not written yet. Poll briefly to catch deps that exit fast.
			// Exit early if PID is dead and no file (killed before write —
			// treat as pass). Continue polling if PID is still alive.
			deadline := time.Now().Add(depExitFileSettleTimeout)
			for !fileHasContent && time.Now().Before(deadline) {
				b, rerr := os.ReadFile(exitFile)
				if rerr == nil && len(bytes.TrimSpace(b)) > 0 {
					fileHasContent = true
					fileBody = b
					break
				}
				// If PID is dead and file still not written, dep was killed
				// abnormally (SIGKILL before wrapper could write). No cascade.
				if !registry.IsPIDAlive(entry.PID) {
					break
				}
				time.Sleep(depExitFilePollInterval)
			}
		}

		// Dep is finished. readDepExitState computes the verdict + evidence.
		verdict, severity, exitCode, evidenceItems, healHint := readDepExitState(r, d.ID, d.RunID)
		if verdict == "pass" {
			// Finished cleanly (exit 0 or exit_code absent after kill);
			// no cascade.
			continue
		}

		now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		md := map[string]interface{}{
			"kind":        "aggregate",
			"command":     entry.Command,
			"output_mode": "stream",
			"counts":      map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 0},
		}
		if exitCode != nil {
			md["exit_code"] = *exitCode
		}
		if healHint != "" {
			md["heal_hint"] = healHint
		}
		agg := map[string]interface{}{
			"sensor_id":   d.ID,
			"version":     "0.0.0",
			"run_id":      uuid.NewString(),
			"started_at":  entry.StartedAt,
			"finished_at": now,
			"verdict":     verdict,
			"severity":    severity,
			"confidence":  1.0,
			"evidence":    evidenceItems,
			"cost_actual": map[string]interface{}{"latency_ms": 0},
			"metadata":    md,
		}

		// BEFORE returning: remove the dead dep's registry entry so
		// DetachLiveDep called later (via defer detachAll) sees no entry
		// and emits nothing. Without this step, detachAll emits a second
		// aggregate for the same dep.
		_ = registry.WithFileLock(r.LockFile(), func() error {
			rs2, lerr := registry.Load(r)
			if lerr != nil {
				return lerr
			}
			rs2.RemoveEntryByRunID(d.RunID)
			return registry.Save(r, rs2)
		})

		return d.ID, agg, d.RunID
	}
	return "", nil, ""
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

	// Wrap the user command so the subprocess's exit status is captured
	// into a sidecar file the orchestrator reads at detach time. The
	// file lives at the sensor-level runtime dir; subsequent re-spawns
	// of the same dep overwrite it idempotently (only the latest run's
	// exit code matters at detach time). Inside the wrapper, the
	// parentheses isolate set -e bleed; ec captures $? before the file
	// write so a write failure cannot corrupt the original exit status.
	// The final `exit $ec` preserves the original status.
	exitCodeFile := filepath.Join(r.SensorDir(dep.ID), "exit_code")
	wrapped := fmt.Sprintf("( %s ); ec=$?; echo $ec > %s; exit $ec",
		command, shellQuote(exitCodeFile))

	// Stage 2: spawn the subprocess detached.
	det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: wrapped,
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

	// Read graceful_timeout_ms from the sensor's execution config so
	// stopBlockingDep can use the sensor-declared grace period instead of
	// a hardcoded default. Zero means "use stopBlockingDep's default".
	gracefulMS := 0
	if gv, ok := execMap["graceful_timeout_ms"].(float64); ok && gv > 0 {
		gracefulMS = int(gv)
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
		SensorID:          dep.ID,
		RunID:             runID,
		Blocking:          true,
		PID:               det.PID,
		PGID:              det.PGID,
		WatcherPID:        watcherPID,
		StartedAt:         now,
		Command:           command,
		LogDir:            r.RelativeRunDir(dep.ID, runID),
		GracefulTimeoutMS: gracefulMS,
		HeldBy:            []registry.HeldByEntry{holder},
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

// shellQuote returns s wrapped in POSIX-safe single quotes, escaping any
// embedded single quotes via the standard close-reopen idiom (close the
// single-quoted string, emit an escaped quote, reopen). Used to splice
// file paths into shell-wrapped commands without command-injection risk.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// stringFieldFromJSON extracts a string field from a sensor's parsed JSON
// without panicking on type mismatch. Local helper to keep the orchestrator
// independent of start.go's stringField (same purpose, different package).
func stringFieldFromJSON(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// stopBlockingDep terminates the dep's process group and removes its
// registry entry. Emits an aggregate Signal on stdout. When the dep
// exited non-zero (recorded in <runtimeDir>/exit_code by the wrapper
// in startBlockingDep), the aggregate carries verdict=fail with a
// tail of raw.log in evidence and metadata.heal_hint synthesized from
// curated stderr patterns. When the file is absent (the subprocess was killed before writing
// it, or the file is absent for any other reason), the verdict=pass
// aggregate is preserved.
func stopBlockingDep(r registry.Root, entry *registry.RunningSensorEntry, v *schema.Validator, stdout, stderr io.Writer) {
	// gracefulMS is the SIGTERM grace period before SIGKILL. Prefer the
	// sensor-declared value stored in the registry entry; fall back to
	// 500ms when GracefulTimeoutMS is unset (zero value).
	gracefulMS := 500
	if entry.GracefulTimeoutMS > 0 {
		gracefulMS = entry.GracefulTimeoutMS
	}
	if entry.PGID > 0 {
		_ = syscall.Kill(-entry.PGID, syscall.SIGTERM)
		// Wait the grace period, then SIGKILL regardless. We do NOT poll
		// IsPIDAlive here: detached processes (Setsid:true, Release()'d)
		// become zombies after exiting — kill(pid,0) returns success for
		// zombies, so polling would always wait the full grace period. A
		// time-bounded wait followed by SIGKILL is simpler and correct
		// (SIGKILL on an already-dead process is a harmless no-op).
		time.Sleep(time.Duration(gracefulMS) * time.Millisecond)
		_ = syscall.Kill(-entry.PGID, syscall.SIGKILL)
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

	verdict, severity, exitCode, evidenceItems, healHint := readDepExitState(r, entry.SensorID, entry.RunID)

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := map[string]interface{}{
		"kind":        "aggregate",
		"command":     entry.Command,
		"output_mode": "stream",
		"counts":      map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 0},
	}
	if exitCode != nil {
		md["exit_code"] = *exitCode
	}
	if healHint != "" {
		md["heal_hint"] = healHint
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
		"evidence":    evidenceItems,
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
	agg = validateOrFallback(v, agg, entry.SensorID, stderr)
	_ = json.NewEncoder(stdout).Encode(agg)
}

// depRawLogTailLines is the number of trailing non-empty raw.log lines
// surfaced as evidence[] excerpts when a blocking dep exits non-zero.
const depRawLogTailLines = 20

// readDepExitState returns the aggregate-shaped pieces describing the
// dep's exit state, derived from <runtime>/<id>/exit_code and raw.log.
//
//   - verdict, severity: "pass"/"info" when exit_code is 0 or absent,
//     "fail"/"high" otherwise.
//   - exitCode: pointer to the parsed int when the file existed, nil
//     when it didn't.
//   - evidenceItems: a default rationale on success; otherwise the last
//     depRawLogTailLines non-empty lines of raw.log as excerpt entries.
//   - healHint: "<shape>:<line>" when any tail line matches a curated
//     heal pattern, empty otherwise.
func readDepExitState(r registry.Root, depID, runID string) (verdict, severity string, exitCode *int, evidenceItems []interface{}, healHint string) {
	exitFile := filepath.Join(r.SensorDir(depID), "exit_code")
	body, err := os.ReadFile(exitFile)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return "pass", "info",
			nil,
			[]interface{}{map[string]interface{}{
				"rationale": fmt.Sprintf("blocking dep %q stopped on detach", depID),
			}},
			""
	}
	code, perr := strconv.Atoi(strings.TrimSpace(string(body)))
	if perr != nil {
		// File present but unparseable — treat like missing, no exit code.
		return "pass", "info",
			nil,
			[]interface{}{map[string]interface{}{
				"rationale": fmt.Sprintf("blocking dep %q stopped on detach (exit_code unparseable: %v)", depID, perr),
			}},
			""
	}
	if code == 0 {
		return "pass", "info",
			intPtr(0),
			[]interface{}{map[string]interface{}{
				"rationale": fmt.Sprintf("blocking dep %q stopped on detach (exit_code=0)", depID),
			}},
			""
	}

	tail := tailRawLog(r.RawLogRun(depID, runID), depRawLogTailLines)
	ev := make([]interface{}, 0, len(tail))
	for _, line := range tail {
		ev = append(ev, map[string]interface{}{
			"rationale": fmt.Sprintf("blocking dep %q stderr/stdout tail", depID),
			"excerpt":   line,
		})
	}
	if len(ev) == 0 {
		ev = append(ev, map[string]interface{}{
			"rationale": fmt.Sprintf("blocking dep %q exited with code %d; raw.log empty", depID, code),
		})
	}

	// Synthesize metadata.heal_hint when any tail line matches a curated pattern.
	for _, line := range tail {
		if shape, ok := heal.MatchStderrPattern(line); ok {
			truncated := line
			if utf8.RuneCountInString(truncated) > 120 {
				runes := []rune(truncated)
				truncated = string(runes[:120])
			}
			healHint = string(shape) + ":" + truncated
			break
		}
	}

	return "fail", "high", intPtr(code), ev, healHint
}

func intPtr(i int) *int { return &i }

// tailRawLog returns the last n non-empty lines of the file at path.
// Reads the whole file (raw.log is bounded by the dep's runtime) and
// keeps things simple. Returns nil on read error.
func tailRawLog(path string, n int) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	allLines := strings.Split(string(body), "\n")
	out := make([]string, 0, n)
	// Walk backward, skipping empty lines.
	for i := len(allLines) - 1; i >= 0 && len(out) < n; i-- {
		line := strings.TrimRight(allLines[i], "\r")
		if line == "" {
			continue
		}
		out = append([]string{line}, out...)
	}
	return out
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
