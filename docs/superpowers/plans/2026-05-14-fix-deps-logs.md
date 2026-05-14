# Fix blocking-dep logs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make orchestrator-managed blocking deps write logs to the same run-id-scoped layout as `/start-sensor`, spawn a watcher so `signals.log` is populated, terminate that watcher on detach, and migrate `/stop-sensor` and `/list-sensors` to read/emit the run-id-scoped path with the `-legacy` suffix sentinel fallback.

**Architecture:** Replicate the staging-rename-watcher pattern from `skills/start-sensor/scripts/start.go:189-329` inside `lib/orchestrator/live_deps.go::startBlockingDep`. Extend `stopBlockingDep` with the SIGTERM→grace→SIGKILL watcher termination block mirrored from `skills/stop-sensor/scripts/stop.go::stopWatcher`. Migrate `/stop-sensor` and `/list-sensors` to the `strings.HasSuffix(entry.RunID, "-legacy")` discipline already used by `/tail-sensor` and underpinned by `lib/registry/sanitize.go`'s Load-time RunID synthesis.

**Tech Stack:** Go 1.25, standard library `os/exec`/`syscall`/`os`, `github.com/fsnotify/fsnotify` (already a transitive dep via `lib/watcher`), `github.com/google/uuid`, project-internal `lib/registry`, `lib/sensor`, `lib/signal`, `lib/subprocess`, `lib/watcher`, `lib/schema`.

**Spec:** `docs/superpowers/specs/2026-05-14-fix-deps-logs-design.md`

---

## File Map

**Modified (production):**
- `lib/orchestrator/live_deps.go` — refactor `startBlockingDep`, extend `stopBlockingDep`.
- `skills/stop-sensor/scripts/stop.go:208` — switch to `SignalsLogRun` with `-legacy` fallback.
- `skills/list-sensors/scripts/list.go:66` — switch `signals_log_path` emission.

**Modified (tests):**
- `lib/orchestrator/live_deps_test.go` — add six new test functions.
- `lib/orchestrator/integration_runtime_logs_test.go` — add one end-to-end test function.
- `skills/stop-sensor/scripts/stop_test.go` — add two new test functions.
- `skills/list-sensors/scripts/list_test.go` — add two new test functions.

**Untouched (verified intact):**
- `schemas/*.json` — no schema change.
- `lib/registry/paths.go`, `lib/registry/sanitize.go`, `lib/registry/state.go` — no API change.
- `lib/watcher/spawn.go`, `lib/watcher/spawn_unix.go` — reused unchanged.
- `lib/orchestrator/export_test.go` — `ExportedStartBlockingDep`'s signature stays the same.
- `skills/start-sensor/scripts/*.go` — already on run-id-scoped layout.
- `skills/tail-sensor/scripts/tail.go` — already uses the `-legacy` fallback.

---

## Task 1: Refactor `startBlockingDep` to run-id-scoped layout + watcher spawn

**Files:**
- Modify: `lib/orchestrator/live_deps.go:226-279` (the `startBlockingDep` function and its leading comment)
- Test: `lib/orchestrator/live_deps_test.go` (append new tests)

**Background context (for the executing agent):**
The current `startBlockingDep` writes to a flat layout (`.harness/runtime/<id>/raw.log`) and does not spawn a watcher, even though it stores the run-id-scoped path in `registry.RunningSensorEntry.LogDir`. The fix replicates the staging-rename pattern from `start.go:189-329`: pre-create staging raw.log at the flat path, spawn the subprocess, derive `runID` from `<PID>-<short-UUID>`, mkdir the run dir, rename staging into the run dir, create signals.log, spawn the watcher via `lib/watcher.Spawn`, then save the registry entry with the spawned `WatcherPID`. Cleanup on each intermediate failure kills the subprocess group and removes whatever was created so far. The function signature stays exactly the same, so `export_test.go::ExportedStartBlockingDep` does not change.

- [ ] **Step 1: Write the failing test `TestStartBlockingDep_UsesRunIDLayout`**

Append the following at the end of `lib/orchestrator/live_deps_test.go`. This is the central regression test — asserts that after `startBlockingDep` returns, the run-id-scoped paths exist, the flat paths do not, and the registry carries a non-zero `WatcherPID`.

```go
func TestStartBlockingDep_UsesRunIDLayout(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))

	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "holder-id", PID: 99999, AttachedAt: "2026-05-14T00:00:00Z"}

	runID, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err != nil {
		t.Fatalf("ExportedStartBlockingDep: %v", err)
	}
	t.Cleanup(func() {
		teardownEntry(t, r, runID)
	})

	if _, err := os.Stat(r.RawLogRun("blocking-tick", runID)); err != nil {
		t.Errorf("RawLogRun missing: %v", err)
	}
	if _, err := os.Stat(r.SignalsLogRun("blocking-tick", runID)); err != nil {
		t.Errorf("SignalsLogRun missing: %v", err)
	}
	if _, err := os.Stat(r.LegacyRawLog("blocking-tick")); err == nil {
		t.Error("LegacyRawLog still exists at flat path; expected staged-then-renamed away")
	}
	if _, err := os.Stat(r.LegacySignalsLog("blocking-tick")); err == nil {
		t.Error("LegacySignalsLog should never be created on the fix path")
	}

	entry := rs.FindEntry("blocking-tick")
	if entry == nil {
		t.Fatal("registry entry missing")
	}
	if entry.RunID != runID {
		t.Errorf("entry.RunID: got %q, want %q", entry.RunID, runID)
	}
	if entry.LogDir != r.RelativeRunDir("blocking-tick", runID) {
		t.Errorf("entry.LogDir: got %q, want %q", entry.LogDir, r.RelativeRunDir("blocking-tick", runID))
	}
	if entry.WatcherPID <= 0 {
		t.Errorf("entry.WatcherPID: got %d, want > 0", entry.WatcherPID)
	}
}

// pluginRootForTest returns the plugin checkout's absolute path. Tests
// must point CLAUDE_PLUGIN_ROOT at it so lib/watcher.Spawn can find the
// watcher source tree.
func pluginRootForTest(t *testing.T) string {
	t.Helper()
	// orchestrator package lives at <pluginRoot>/lib/orchestrator
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// teardownEntry tears down a spawned subprocess + watcher recorded under
// runID. Idempotent; safe to call on a partially-completed test.
func teardownEntry(t *testing.T, r registry.Root, runID string) {
	t.Helper()
	rs, err := registry.Load(r)
	if err != nil {
		return
	}
	entry := rs.FindEntryByRunID(runID)
	if entry == nil {
		return
	}
	if entry.PGID > 0 {
		_ = syscallKill(-entry.PGID, syscall.SIGKILL)
	}
	if entry.WatcherPID > 0 {
		_ = syscallKill(entry.WatcherPID, syscall.SIGKILL)
	}
}

// syscallKill wraps syscall.Kill so the test file does not need its
// own platform-conditional build tag. SIGKILL is universally available
// on darwin and linux (the only test targets per the project's go test
// invocation).
func syscallKill(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
```

Add the imports `"syscall"` (top-level) at the top of `live_deps_test.go` if not already present. (Verify by reading the import block; the file currently imports `bytes`, `context`, `encoding/json`, `fmt`, `io`, `os`, `path/filepath`, `strings`, `testing` — `syscall` is new and needed by `teardownEntry`.)

- [ ] **Step 2: Run the test, confirm it fails for the expected reasons**

```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/fix-45
go test ./lib/orchestrator/ -run TestStartBlockingDep_UsesRunIDLayout -v 2>&1 | tail -40
```

Expected: FAIL. The error should mention either `RawLogRun missing` or `entry.WatcherPID: got 0, want > 0` — the current `startBlockingDep` writes to the flat path and stores `WatcherPID: 0`.

- [ ] **Step 3: Replace `startBlockingDep` with the run-id-scoped + watcher version**

Open `lib/orchestrator/live_deps.go`. Replace the block from line 226 (the `// startBlockingDep is called from AttachLiveDep` comment) through line 279 (the closing `}` of the function) with the following. Also update the package imports at the top of the file: add `"path/filepath"` and `"github.com/iurykrieger/harness-framework/lib/watcher"` (verify the existing import block; `time` and `os` are already there).

```go
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

	envelope, eerr := libsensor.BuildEnvelope(dep.JSON)
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
```

`envelope` is `libsensor.Envelope`. The `libsensor` alias matches the existing import at the top of `live_deps.go` (verify: the current file imports `"github.com/iurykrieger/harness-framework/lib/sensor"` as `sensor`, not `libsensor`. **Rename the call to `sensor.BuildEnvelope` and use the type `sensor.Envelope`** to match the file's existing alias. Adjust the code above accordingly when applying.)

- [ ] **Step 4: Re-run the test, confirm it passes**

```bash
go test ./lib/orchestrator/ -run TestStartBlockingDep_UsesRunIDLayout -v 2>&1 | tail -20
```

Expected: PASS. If the test fails on the `WatcherPID > 0` assertion, the most likely cause is `CLAUDE_PLUGIN_ROOT` not being resolved — verify `pluginRootForTest` returns the worktree absolute path (it should end in `.claude/worktrees/fix-45`).

- [ ] **Step 5: Run the wider orchestrator suite to catch regressions**

```bash
go test ./lib/orchestrator/ -v 2>&1 | tail -60
```

Expected: All pre-existing tests still pass. `TestAttachLiveDep_PassesHolderPID`, `TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep`, `TestRunWithDepsImpl_BlockingDep_AggregateLast`, etc. all continue to PASS. If any fail, the most likely cause is a missing `CLAUDE_PLUGIN_ROOT` in those tests — add `t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))` to the affected tests, since they now drive `startBlockingDep` which needs the env var. Tests that go through `AttachLiveDep`/`RunWithDepsRoot`/`RunDeps` *all* now drive a real watcher spawn.

- [ ] **Step 6: Add `TestStartBlockingDep_NoPatterns_StillSpawnsWatcher`**

Append to `live_deps_test.go`. This confirms that a dep whose JSON omits `output_parsing.patterns` still gets a watcher (matching `/start-sensor`'s posture). Use a separate sensor writer that omits the `output_parsing` field — note that `writeBlockingDep` always includes it, so write a new variant inline.

```go
func writeBlockingDepNoPatterns(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Same as writeBlockingDep but output: "single" so output_parsing is
	// not required by the schema. blocking: true is still legal for
	// output: single.
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Quiet blocker",
"description": "blocker without patterns",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50}
},
"execution": {
  "command": "sleep 5",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartBlockingDep_NoPatterns_StillSpawnsWatcher(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	root := t.TempDir()
	writeBlockingDepNoPatterns(t, root, "quiet-blocker")
	dep := loadDepSensor(t, root, "quiet-blocker")

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "h", PID: 1, AttachedAt: "2026-05-14T00:00:00Z"}

	runID, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err != nil {
		t.Fatalf("ExportedStartBlockingDep: %v", err)
	}
	t.Cleanup(func() { teardownEntry(t, r, runID) })

	entry := rs.FindEntry("quiet-blocker")
	if entry == nil || entry.WatcherPID <= 0 {
		t.Fatalf("expected watcher_pid > 0, entry=%+v", entry)
	}
	// signals.log MUST exist and be empty (or contain only post-spawn signals).
	info, err := os.Stat(r.SignalsLogRun("quiet-blocker", runID))
	if err != nil {
		t.Fatalf("signals.log: %v", err)
	}
	_ = info
}
```

- [ ] **Step 7: Run the test, confirm it passes**

```bash
go test ./lib/orchestrator/ -run TestStartBlockingDep_NoPatterns_StillSpawnsWatcher -v 2>&1 | tail -15
```

Expected: PASS.

- [ ] **Step 8: Add `TestStartBlockingDep_PatternsEmitSignals`**

This is the real regression test — proves the watcher receives the patterns and writes individuals.

Append to `live_deps_test.go`:

```go
func writeBlockingDepWithErrorPattern(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Error emitter",
"description": "emits one ERROR line then sleeps",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50}
},
"execution": {
  "command": "printf 'ERROR: ouch\\n' && sleep 5",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^ERROR","verdict":"fail","severity":"high","rationale":"error line"}]}
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStartBlockingDep_PatternsEmitSignals(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	root := t.TempDir()
	writeBlockingDepWithErrorPattern(t, root, "errs")
	dep := loadDepSensor(t, root, "errs")

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "h", PID: 1, AttachedAt: "2026-05-14T00:00:00Z"}

	runID, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err != nil {
		t.Fatalf("ExportedStartBlockingDep: %v", err)
	}
	t.Cleanup(func() { teardownEntry(t, r, runID) })

	// Poll signals.log for up to 3 seconds.
	sigsPath := r.SignalsLogRun("errs", runID)
	deadline := time.Now().Add(3 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(sigsPath)
		if len(data) > 0 {
			lines = strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) >= 1 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(lines) == 0 {
		t.Fatalf("signals.log empty after 3s; expected ≥1 individual signal")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &sig); err != nil {
		t.Fatalf("parse signal: %v (line=%q)", err, lines[0])
	}
	if v, _ := sig["verdict"].(string); v != "fail" {
		t.Errorf("verdict: got %q, want fail", v)
	}
	if s, _ := sig["severity"].(string); s != "high" {
		t.Errorf("severity: got %q, want high", s)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if k, _ := md["kind"].(string); k != "individual" {
		t.Errorf("metadata.kind: got %q, want individual", k)
	}
}
```

Add `"time"` to the import block if not already present.

- [ ] **Step 9: Run the test, confirm it passes**

```bash
go test ./lib/orchestrator/ -run TestStartBlockingDep_PatternsEmitSignals -v -timeout 30s 2>&1 | tail -20
```

Expected: PASS within ~3s once the watcher has compiled and started. If FAIL with "signals.log empty after 3s", increase the poll deadline to 5s — the watcher's first compile after a cache wipe takes longer. If still empty, the most likely cause is a pattern compilation error inside the watcher — check `<runDir>/watcher.log` for errors.

- [ ] **Step 10: Add `TestStartBlockingDep_PluginRootMissing`**

Append to `live_deps_test.go`:

```go
func TestStartBlockingDep_PluginRootMissing(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "h", PID: 1, AttachedAt: "2026-05-14T00:00:00Z"}

	_, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err == nil {
		t.Fatal("expected error when CLAUDE_PLUGIN_ROOT empty, got nil")
	}
	if !strings.Contains(err.Error(), "plugin root not set") {
		t.Errorf("error: got %q, want substring 'plugin root not set'", err)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("registry should be untouched; got %d entries", len(rs.Entries))
	}
	if _, statErr := os.Stat(r.SensorDir("blocking-tick")); statErr == nil {
		t.Error("SensorDir created despite early error; want no side effects")
	}
}
```

- [ ] **Step 11: Run the test, confirm it passes**

```bash
go test ./lib/orchestrator/ -run TestStartBlockingDep_PluginRootMissing -v 2>&1 | tail -10
```

Expected: PASS.

- [ ] **Step 12: Add `TestStartBlockingDep_WatcherSpawnFailure`**

This test overrides `watcher.SpawnFn` with a fake returning an error and asserts the cleanup ladder. Append to `live_deps_test.go`:

```go
func TestStartBlockingDep_WatcherSpawnFailure(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		return 0, fmt.Errorf("forced")
	}
	t.Cleanup(func() { watcher.SpawnFn = prev })

	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{}
	holder := registry.HeldByEntry{Kind: "sensor", ID: "h", PID: 1, AttachedAt: "2026-05-14T00:00:00Z"}

	_, err := orchestrator.ExportedStartBlockingDep(&rs, r, dep, holder, root)
	if err == nil {
		t.Fatal("expected error from forced watcher failure, got nil")
	}
	if !strings.Contains(err.Error(), "forced") {
		t.Errorf("error: got %q, want substring 'forced'", err)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("registry should be untouched; got %d entries", len(rs.Entries))
	}
	// Run dir must have been removed.
	entries, _ := os.ReadDir(r.SensorDir("blocking-tick"))
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("found run-dir %q after watcher-failure cleanup; should be gone", e.Name())
		}
	}
}
```

Add `"github.com/iurykrieger/harness-framework/lib/watcher"` to the imports if not present.

- [ ] **Step 13: Run the test, confirm it passes**

```bash
go test ./lib/orchestrator/ -run TestStartBlockingDep_WatcherSpawnFailure -v 2>&1 | tail -10
```

Expected: PASS.

- [ ] **Step 14: Run the full orchestrator suite once more**

```bash
go test ./lib/orchestrator/ -v -timeout 60s 2>&1 | tail -40
```

Expected: All tests PASS. Watch for `TestAttachLiveDep_PassesHolderPID` and other pre-existing tests that now go through real watcher spawn — they may require `CLAUDE_PLUGIN_ROOT` to be set. If you see "plugin root not set" failures, add `t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))` at the top of those tests.

- [ ] **Step 15: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
fix(orchestrator): startBlockingDep uses run-id layout and spawns watcher

Replaces the flat-layout, watcher-less spawn in lib/orchestrator with
the same staging-rename-watcher pattern proven in /start-sensor. The
dep's signals.log is now populated with parsed individuals from the
declared output_parsing.patterns, and the registry entry carries the
real watcher PID instead of 0. Closes part of #45.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Extend `stopBlockingDep` to terminate the watcher

**Files:**
- Modify: `lib/orchestrator/live_deps.go:291-330` (the `stopBlockingDep` function)
- Test: `lib/orchestrator/live_deps_test.go` (append)

**Background context (for the executing agent):**
After Task 1, `startBlockingDep` spawns a watcher process for every blocking dep. The existing `stopBlockingDep` does not signal that watcher when the dep is torn down, leaving an orphan tailing a now-dead `raw.log`. The fix is a small SIGTERM→grace→SIGKILL block mirrored from `skills/stop-sensor/scripts/stop.go::stopWatcher`, added between the subprocess kill (lines 293-305) and the registry removal (lines 306-313).

- [ ] **Step 1: Write the failing test `TestDetachLiveDep_KillsWatcher`**

Append to `lib/orchestrator/live_deps_test.go`:

```go
func TestDetachLiveDep_KillsWatcher(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	v := loadValidator(t)
	var stdout, stderr bytes.Buffer

	const holderPID = 12345
	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "h", holderPID,
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}

	r := registry.NewRoot(root)
	rsBefore, _ := registry.Load(r)
	entry := rsBefore.FindEntry("blocking-tick")
	if entry == nil || entry.WatcherPID <= 0 {
		t.Fatalf("expected live watcher in registry, entry=%+v", entry)
	}
	savedWatcherPID := entry.WatcherPID

	// Detach should tear down the dep (only holder) and kill the watcher.
	orchestrator.DetachLiveDep(result.Live, root, "h", v, &stdout, &stderr)

	// Poll up to 3 seconds for the watcher to exit.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !registry.IsPIDAlive(savedWatcherPID) {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("watcher pid %d still alive 3s after DetachLiveDep", savedWatcherPID)
	// Best-effort cleanup so the orphan does not survive the test process.
	_ = syscall.Kill(savedWatcherPID, syscall.SIGKILL)
}
```

- [ ] **Step 2: Run the test, confirm it fails**

```bash
go test ./lib/orchestrator/ -run TestDetachLiveDep_KillsWatcher -v -timeout 30s 2>&1 | tail -15
```

Expected: FAIL with "watcher pid N still alive 3s after DetachLiveDep". The current `stopBlockingDep` doesn't signal the watcher.

- [ ] **Step 3: Extend `stopBlockingDep` with the watcher-kill block**

In `lib/orchestrator/live_deps.go`, locate `stopBlockingDep` (around current line 291). Between the existing subprocess-kill block (which ends with the `if registry.IsPIDAlive(entry.PID) { _ = syscall.Kill(-entry.PGID, syscall.SIGKILL) }` at around line 304) and the `_ = registry.WithFileLock(...)` block (around line 306), insert:

```go
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
```

- [ ] **Step 4: Run the test, confirm it passes**

```bash
go test ./lib/orchestrator/ -run TestDetachLiveDep_KillsWatcher -v -timeout 30s 2>&1 | tail -10
```

Expected: PASS (the test returns from inside the `for` loop when `IsPIDAlive` flips to false).

- [ ] **Step 5: Run the wider suite, confirm no regressions**

```bash
go test ./lib/orchestrator/ -v -timeout 60s 2>&1 | tail -40
```

Expected: All PASS, including `TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep` (which exercises the full attach→run→detach cycle).

- [ ] **Step 6: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
fix(orchestrator): stopBlockingDep terminates the dep's watcher

Adds a SIGTERM → 2s grace → SIGKILL block to stopBlockingDep so the
watcher spawned by startBlockingDep does not survive detach. Mirrors
the stopWatcher pattern already used by /stop-sensor. Closes the
orphan-watcher follow-up from #45.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Migrate `/stop-sensor` to read run-id-scoped `signals.log`

**Files:**
- Modify: `skills/stop-sensor/scripts/stop.go:208`
- Test: `skills/stop-sensor/scripts/stop_test.go` (append)

**Background context (for the executing agent):**
`/stop-sensor` currently reads `r.SignalsLog(sensorID)` (flat path), which has been empty for `/start-sensor`-launched sensors since the run-id-scoped layout migration. Replace with `r.SignalsLogRun(sensorID, entry.RunID)` and fall back to `r.LegacySignalsLog(sensorID)` when `strings.HasSuffix(entry.RunID, "-legacy")` — the suffix is synthesized by `lib/registry/sanitize.go` for pre-spec entries that arrived via `LookupSanitized`.

- [ ] **Step 1: Write the failing test `TestStop_ReadsRunIDScopedSignalsLog`**

Read the existing `stop_test.go` to understand the helper functions (`writeRegistryEntry`, `runStopAndDecode`, etc.) and the build-tag setup (`//go:build stop_sensor`). Append the following test after the last existing one (around line 330). The test seeds a registry entry with a non-`-legacy` `RunID`, writes signals at the run-id-scoped path, runs the stop flow, and asserts the aggregate counts reflect the run-id-scoped read.

```go
func TestStop_ReadsRunIDScopedSignalsLog(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)

	// Spawn a real, long-running subprocess so /stop-sensor has something
	// to SIGTERM. Recording stdin keeps go test from leaving zombies.
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	runID := fmt.Sprintf("%d-deadbeef", cmd.Process.Pid)
	if err := os.MkdirAll(r.RunDir("svc-x", runID), 0o755); err != nil {
		t.Fatal(err)
	}
	// 3 individuals: 1 fail (severity high), 2 warn (severity low).
	body := strings.Join([]string{
		`{"sensor_id":"svc-x","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"fail","severity":"high","confidence":1.0,"evidence":[{"rationale":"e1"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
		`{"sensor_id":"svc-x","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"warn","severity":"low","confidence":1.0,"evidence":[{"rationale":"e2"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
		`{"sensor_id":"svc-x","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"warn","severity":"low","confidence":1.0,"evidence":[{"rationale":"e3"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(r.SignalsLogRun("svc-x", runID), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rs := registry.RunningSensors{
		Entries: []registry.RunningSensorEntry{{
			SensorID: "svc-x", RunID: runID, Blocking: true,
			PID:        cmd.Process.Pid,
			PGID:       cmd.Process.Pid,
			WatcherPID: 0,
			StartedAt:  "2026-05-14T00:00:00Z",
			Command:    "sleep 60",
			LogDir:     r.RelativeRunDir("svc-x", runID),
			HeldBy:     []registry.HeldByEntry{},
		}},
	}
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	sig := runStopAndDecode(t, root, "svc-x")
	md, _ := sig["metadata"].(map[string]interface{})
	counts, _ := md["counts"].(map[string]interface{})
	if counts["fail"] != float64(1) {
		t.Errorf("counts.fail: got %v, want 1", counts["fail"])
	}
	if counts["warn"] != float64(2) {
		t.Errorf("counts.warn: got %v, want 2", counts["warn"])
	}
	if v, _ := sig["verdict"].(string); v != "fail" {
		t.Errorf("verdict: got %q, want fail", v)
	}
}

// runStopAndDecode invokes the same code path that the CLI runs, returns
// the last JSONL signal emitted on stdout. The harness is intentionally
// thin: it bootstraps cli, calls runStop with reap=false, and parses the
// last line of captured stdout as the aggregate Signal.
func runStopAndDecode(t *testing.T, projectRoot, sensorID string) map[string]interface{} {
	t.Helper()
	t.Setenv("HARNESS_REGISTRY_ROOT", projectRoot)
	var stdout, stderr bytes.Buffer
	b := cli.Bootstrap("stop-sensor", &stdout, &stderr)
	if b.ExitCode != 0 {
		t.Fatalf("cli.Bootstrap exit=%d stderr=%s", b.ExitCode, stderr.String())
	}
	_, _ = runStop(b, []string{sensorID}, false)
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 {
		t.Fatalf("no signal emitted; stderr=%s", stderr.String())
	}
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sig); err != nil {
		t.Fatalf("parse last signal: %v (line=%q)", err, lines[len(lines)-1])
	}
	return sig
}
```

Add imports as needed at the top of `stop_test.go`: `"os/exec"`, `"fmt"`, `"strings"`, `"github.com/iurykrieger/harness-framework/lib/cli"`. Verify against the current import block.

- [ ] **Step 2: Run the test, confirm it fails**

```bash
go test -tags=stop_sensor ./skills/stop-sensor/scripts/ -run TestStop_ReadsRunIDScopedSignalsLog -v 2>&1 | tail -15
```

Expected: FAIL with `counts.fail: got 0, want 1` (or similar). The current code reads the flat `r.SignalsLog(...)` path, which is empty in this test setup.

- [ ] **Step 3: Update `stop.go:208` to read the run-id-scoped path**

Open `skills/stop-sensor/scripts/stop.go`. Locate line 208:

```go
individuals := readSignals(r.SignalsLog(sensorID))
```

Replace with:

```go
sigsPath := r.SignalsLogRun(sensorID, entry.RunID)
if strings.HasSuffix(entry.RunID, "-legacy") {
	sigsPath = r.LegacySignalsLog(sensorID)
}
individuals := readSignals(sigsPath)
```

Verify the file imports `"strings"` — it does not by default in this file. Add `"strings"` to the import block.

- [ ] **Step 4: Run the test, confirm it passes**

```bash
go test -tags=stop_sensor ./skills/stop-sensor/scripts/ -run TestStop_ReadsRunIDScopedSignalsLog -v 2>&1 | tail -10
```

Expected: PASS.

- [ ] **Step 5: Add `TestStop_LegacyFallback`**

Append to `stop_test.go`:

```go
func TestStop_LegacyFallback(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)

	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	runID := fmt.Sprintf("%d-legacy", cmd.Process.Pid)
	if err := os.MkdirAll(r.SensorDir("svc-legacy"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"sensor_id":"svc-legacy","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"pass","severity":"info","confidence":1.0,"evidence":[{"rationale":"e1"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
		`{"sensor_id":"svc-legacy","version":"1.0.0","run_id":"` + runID + `","started_at":"2026-05-14T00:00:00Z","finished_at":"2026-05-14T00:00:01Z","verdict":"pass","severity":"info","confidence":1.0,"evidence":[{"rationale":"e2"}],"cost_actual":{"latency_ms":0},"metadata":{"kind":"individual"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(r.LegacySignalsLog("svc-legacy"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	rs := registry.RunningSensors{
		Entries: []registry.RunningSensorEntry{{
			SensorID: "svc-legacy", RunID: runID, Blocking: true,
			PID: cmd.Process.Pid, PGID: cmd.Process.Pid, WatcherPID: 0,
			StartedAt: "2026-05-14T00:00:00Z",
			Command:   "sleep 60",
			LogDir:    "", // pre-spec entries had no LogDir
			HeldBy:    []registry.HeldByEntry{},
		}},
	}
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	sig := runStopAndDecode(t, root, "svc-legacy")
	md, _ := sig["metadata"].(map[string]interface{})
	counts, _ := md["counts"].(map[string]interface{})
	if counts["pass"] != float64(2) {
		t.Errorf("counts.pass: got %v, want 2 (legacy fallback should have read 2 pass signals)", counts["pass"])
	}
}
```

- [ ] **Step 6: Run the test, confirm it passes**

```bash
go test -tags=stop_sensor ./skills/stop-sensor/scripts/ -run TestStop_LegacyFallback -v 2>&1 | tail -10
```

Expected: PASS.

- [ ] **Step 7: Run the full stop-sensor suite**

```bash
go test -tags=stop_sensor ./skills/stop-sensor/scripts/ -v 2>&1 | tail -30
```

Expected: All PASS. The pre-existing tests `TestStop_BlockingFalse_TerminatesRunnerSubprocess`, `TestStop_BlockingPreferred_WhenMixedActives`, etc., should not regress; they don't exercise the `readSignals` path with non-empty content, so they're unaffected.

- [ ] **Step 8: Commit**

```bash
git add skills/stop-sensor/scripts/stop.go skills/stop-sensor/scripts/stop_test.go
git commit -m "$(cat <<'EOF'
fix(stop-sensor): read signals.log from run-id-scoped path

/stop-sensor was reading the flat .harness/runtime/<id>/signals.log,
which has been empty for /start-sensor-launched sensors since the
run-id-scoped layout migration. Now uses SignalsLogRun with the
"-legacy" suffix fallback established by /tail-sensor and
lib/registry/sanitize.go. Closes part of #45.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Migrate `/list-sensors` to emit run-id-scoped `signals_log_path`

**Files:**
- Modify: `skills/list-sensors/scripts/list.go:66`
- Test: `skills/list-sensors/scripts/list_test.go` (append)

**Background context (for the executing agent):**
`/list-sensors` builds a per-entry response map with `signals_log_path: r.SignalsLog(e.SensorID)` (flat). Migrate to `SignalsLogRun(e.SensorID, e.RunID)` with `-legacy` fallback. Same idiom as Task 3, but inline because `/list-sensors` emits the path without reading the file.

- [ ] **Step 1: Write the failing test `TestList_EmitsRunIDScopedPath`**

Read the existing `list_test.go` to understand helpers (`writeRegistryEntry`, `runListAndDecode`, etc.). Append:

```go
func TestList_EmitsRunIDScopedPath(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Entries: []registry.RunningSensorEntry{{
			SensorID:   "svc-x",
			RunID:      "7037-5ecd3f00",
			Blocking:   true,
			PID:        os.Getpid(), // current pid is always alive in test
			PGID:       os.Getpid(),
			WatcherPID: os.Getpid(),
			StartedAt:  "2026-05-14T00:00:00Z",
			Command:    "true",
			LogDir:     r.RelativeRunDir("svc-x", "7037-5ecd3f00"),
			HeldBy:     []registry.HeldByEntry{{Kind: "manual", AttachedAt: "2026-05-14T00:00:00Z"}},
		}},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	sig := runListAndDecode(t, root)
	md, _ := sig["metadata"].(map[string]interface{})
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	first, _ := entries[0].(map[string]interface{})
	got, _ := first["signals_log_path"].(string)
	want := r.SignalsLogRun("svc-x", "7037-5ecd3f00")
	if got != want {
		t.Errorf("signals_log_path: got %q, want %q", got, want)
	}
}

func runListAndDecode(t *testing.T, projectRoot string) map[string]interface{} {
	t.Helper()
	t.Setenv("HARNESS_REGISTRY_ROOT", projectRoot)
	var stdout, stderr bytes.Buffer
	b := cli.Bootstrap("list-sensors", &stdout, &stderr)
	if b.ExitCode != 0 {
		t.Fatalf("cli.Bootstrap exit=%d stderr=%s", b.ExitCode, stderr.String())
	}
	_ = runList(b, &stdout, &stderr)
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sig); err != nil {
		t.Fatalf("parse last signal: %v", err)
	}
	return sig
}
```

Add imports: `"bytes"`, `"encoding/json"`, `"strings"`, `"github.com/iurykrieger/harness-framework/lib/cli"`.

- [ ] **Step 2: Run the test, confirm it fails**

```bash
go test -tags=list_sensors ./skills/list-sensors/scripts/ -run TestList_EmitsRunIDScopedPath -v 2>&1 | tail -15
```

Expected: FAIL with `signals_log_path: got "<root>/.harness/runtime/svc-x/signals.log", want "<root>/.harness/runtime/svc-x/7037-5ecd3f00/signals.log"`.

- [ ] **Step 3: Update `list.go:66`**

Open `skills/list-sensors/scripts/list.go`. Find the per-entry map (around line 57-66). Replace this block:

```go
		entry := map[string]interface{}{
			"sensor_id":        e.SensorID,
			"run_id":           e.RunID,
			"blocking":         e.Blocking,
			"pid":              e.PID,
			"pid_alive":        pidAlive,
			"started_at":       e.StartedAt,
			"command":          e.Command,
			"held_by":          registry.SummarizeHolders(e.HeldBy, registry.SummarizeOpts{}),
			"signals_log_path": r.SignalsLog(e.SensorID),
			"state":            state,
		}
```

With:

```go
		signalsPath := r.SignalsLogRun(e.SensorID, e.RunID)
		if strings.HasSuffix(e.RunID, "-legacy") {
			signalsPath = r.LegacySignalsLog(e.SensorID)
		}
		entry := map[string]interface{}{
			"sensor_id":        e.SensorID,
			"run_id":           e.RunID,
			"blocking":         e.Blocking,
			"pid":              e.PID,
			"pid_alive":        pidAlive,
			"started_at":       e.StartedAt,
			"command":          e.Command,
			"held_by":          registry.SummarizeHolders(e.HeldBy, registry.SummarizeOpts{}),
			"signals_log_path": signalsPath,
			"state":            state,
		}
```

Add `"strings"` to the import block.

- [ ] **Step 4: Run the test, confirm it passes**

```bash
go test -tags=list_sensors ./skills/list-sensors/scripts/ -run TestList_EmitsRunIDScopedPath -v 2>&1 | tail -10
```

Expected: PASS.

- [ ] **Step 5: Add `TestList_LegacyFallback`**

Append to `list_test.go`:

```go
func TestList_LegacyFallback(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Entries: []registry.RunningSensorEntry{{
			SensorID:   "svc-legacy",
			RunID:      "12345-legacy",
			Blocking:   true,
			PID:        os.Getpid(),
			PGID:       os.Getpid(),
			WatcherPID: 0,
			StartedAt:  "2026-05-14T00:00:00Z",
			Command:    "true",
			LogDir:     "",
			HeldBy:     []registry.HeldByEntry{{Kind: "manual", AttachedAt: "2026-05-14T00:00:00Z"}},
		}},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	sig := runListAndDecode(t, root)
	md, _ := sig["metadata"].(map[string]interface{})
	entries, _ := md["entries"].([]interface{})
	first, _ := entries[0].(map[string]interface{})
	got, _ := first["signals_log_path"].(string)
	want := r.LegacySignalsLog("svc-legacy")
	if got != want {
		t.Errorf("signals_log_path: got %q, want %q (legacy fallback expected)", got, want)
	}
}
```

- [ ] **Step 6: Run the test, confirm it passes**

```bash
go test -tags=list_sensors ./skills/list-sensors/scripts/ -run TestList_LegacyFallback -v 2>&1 | tail -10
```

Expected: PASS.

- [ ] **Step 7: Run the full list-sensors suite**

```bash
go test -tags=list_sensors ./skills/list-sensors/scripts/ -v 2>&1 | tail -30
```

Expected: All PASS, including `TestList_FileAbsent_Warn`, `TestList_FilePresentEmpty_Pass`, `TestList_AnnotatesOrphan`, `TestList_MultipleEntriesPerSensor`, `TestList_RegistryMigratedSignal_ViaBootstrap`.

- [ ] **Step 8: Commit**

```bash
git add skills/list-sensors/scripts/list.go skills/list-sensors/scripts/list_test.go
git commit -m "$(cat <<'EOF'
fix(list-sensors): emit run-id-scoped signals_log_path

/list-sensors was reporting the flat .harness/runtime/<id>/signals.log
which has been empty for /start-sensor-launched sensors. Now reports
SignalsLogRun with the "-legacy" suffix fallback established by
/tail-sensor and lib/registry/sanitize.go. Closes part of #45.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: End-to-end integration test through `RunWithDepsRoot`

**Files:**
- Test: `lib/orchestrator/integration_runtime_logs_test.go` (append)

**Background context (for the executing agent):**
Tasks 1–4 cover the individual changes. This task adds one end-to-end test that drives `orchestrator.RunWithDepsRoot` against a root sensor whose blocking dep declares `output_parsing.patterns` and emits a matching line. Confirms the whole story: the watcher is spawned for the dep, the patterns produce individuals, those individuals land in the run-id-scoped `signals.log`, and the flat path is never created. This is the test that would have caught the original bug reported in #45.

- [ ] **Step 1: Read the existing integration test file to understand conventions**

Open `lib/orchestrator/integration_runtime_logs_test.go` and note: package name, imports, build tag (none — it's a regular `_test.go`), how it instantiates a project + registry root + writes a sensor JSON, and how it asserts file presence.

- [ ] **Step 2: Add `TestRunWithDepsRoot_DepSignalsPopulated`**

Append at the end of the file:

```go
func TestRunWithDepsRoot_DepSignalsPopulated(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRootForTest(t))
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()

	// Blocking dep with one matching pattern. Command echoes BOOM then
	// sleeps, so the dep stays alive long enough to be observed; the
	// orchestrator tears it down at the end of the root sensor's run.
	depBody := []byte(`{
"id": "blocking-boom",
"version": "1.0.0",
"name": "BOOM emitter",
"description": "emits one BOOM line then sleeps",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":32},"latency":{"p50_ms":10,"p95_ms":50}},
"execution": {
  "command": "printf 'BOOM\\n' && sleep 5",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^BOOM","verdict":"fail","severity":"high","rationale":"boom line"}]}
}
}`)
	consumerBody := []byte(`{
"id": "uses-boom",
"version": "1.0.0",
"name": "Uses boom",
"description": "consumer",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"blocking-boom"}],
"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":32},"latency":{"p50_ms":10,"p95_ms":50,"timeout_ms":2000}},
"execution": {
  "command": "echo OK",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)

	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "blocking-boom.json"), depBody, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uses-boom.json"), consumerBody, 0o644); err != nil {
		t.Fatal(err)
	}

	r := registry.NewRoot(root)

	// Capture the dep's run dir before the orchestrator tears it down.
	// We do this by scanning .harness/runtime/blocking-boom/ for the
	// minted run-id directory during the run.
	exit := orchestrator.RunWithDepsRoot(context.Background(), "uses-boom", root, schemasDir, io.Discard, io.Discard)
	if exit != 0 {
		t.Fatalf("RunWithDepsRoot exit=%d", exit)
	}

	// After teardown, the per-run directory still exists with raw.log and
	// signals.log preserved (registry entry is gone, files are not removed).
	depBase := r.SensorDir("blocking-boom")
	subs, err := os.ReadDir(depBase)
	if err != nil {
		t.Fatalf("read dep dir: %v", err)
	}
	var runDir string
	for _, e := range subs {
		if e.IsDir() {
			runDir = filepath.Join(depBase, e.Name())
			break
		}
	}
	if runDir == "" {
		t.Fatalf("no run-id-scoped run dir found under %s; flat layout suspected", depBase)
	}

	sigsData, err := os.ReadFile(filepath.Join(runDir, "signals.log"))
	if err != nil {
		t.Fatalf("read signals.log: %v", err)
	}
	if len(sigsData) == 0 {
		t.Fatalf("signals.log empty; pattern did not fire")
	}
	lines := strings.Split(strings.TrimSpace(string(sigsData)), "\n")
	matched := false
	for _, line := range lines {
		var sig map[string]interface{}
		if err := json.Unmarshal([]byte(line), &sig); err != nil {
			continue
		}
		if v, _ := sig["verdict"].(string); v == "fail" {
			ev, _ := sig["evidence"].([]interface{})
			if len(ev) > 0 {
				first, _ := ev[0].(map[string]interface{})
				if r, _ := first["rationale"].(string); r == "boom line" {
					matched = true
					break
				}
			}
		}
	}
	if !matched {
		t.Errorf("expected a fail individual with rationale=\"boom line\"; got: %s", string(sigsData))
	}

	// Flat path must not exist.
	if _, err := os.Stat(r.LegacyRawLog("blocking-boom")); err == nil {
		t.Error("flat raw.log exists at .harness/runtime/blocking-boom/raw.log; expected only run-id-scoped")
	}
}

// pluginRootForTest is duplicated locally here because integration_runtime_logs_test.go
// lives in the same package as live_deps_test.go (orchestrator_test). If the
// helper is already present in live_deps_test.go after Task 1, just call it;
// remove this local copy to avoid the duplicate-symbol error.
//
// Verify before adding: if `go test ./lib/orchestrator/` complains about
// duplicate pluginRootForTest, remove this definition.
func pluginRootForTestIntegration(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}
```

Read the existing imports in `integration_runtime_logs_test.go` and add as needed: `"context"`, `"encoding/json"`, `"io"`, `"os"`, `"path/filepath"`, `"strings"`, `"github.com/iurykrieger/harness-framework/lib/orchestrator"`, `"github.com/iurykrieger/harness-framework/lib/registry"`, `"github.com/iurykrieger/harness-framework/lib/schema/schematest"`.

After confirming `pluginRootForTest` exists from Task 1 (it should — Task 1 added it to `live_deps_test.go`, which is in the same `orchestrator_test` package), replace the call in this new test with just `pluginRootForTest(t)` and remove the local `pluginRootForTestIntegration` definition.

- [ ] **Step 3: Run the integration test**

```bash
go test ./lib/orchestrator/ -run TestRunWithDepsRoot_DepSignalsPopulated -v -timeout 60s 2>&1 | tail -25
```

Expected: PASS. If it fails on "signals.log empty; pattern did not fire", check whether the watcher's `go run` compile took longer than the orchestrator's run-to-teardown window — the consumer `uses-boom`'s command is `echo OK`, which finishes in <50ms. The dep is then torn down before the watcher has compiled. **If this race surfaces**, change `consumerBody`'s command from `"echo OK"` to `"sleep 1 && echo OK"` to give the watcher time to compile and observe.

- [ ] **Step 4: Run the full repo test suite as a regression sweep**

```bash
go test ./... -timeout 120s 2>&1 | tail -40
```

Some packages require build tags; the above will only run untagged tests. To exercise the tagged scripts:

```bash
go test -tags=run_computational ./skills/run-sensor/... -timeout 60s 2>&1 | tail -15
go test -tags=run_inferential ./skills/run-sensor/... -timeout 60s 2>&1 | tail -15
go test -tags=start_sensor ./skills/start-sensor/... -timeout 60s 2>&1 | tail -15
go test -tags=stop_sensor ./skills/stop-sensor/... -timeout 60s 2>&1 | tail -15
go test -tags=list_sensors ./skills/list-sensors/... -timeout 60s 2>&1 | tail -15
go test -tags=tail_sensor ./skills/tail-sensor/... -timeout 60s 2>&1 | tail -15
```

Expected: every command reports PASS for all tests. Investigate any FAIL before committing.

- [ ] **Step 5: Run `go vet` for the impacted packages**

```bash
go vet ./lib/orchestrator/
go vet -tags=stop_sensor ./skills/stop-sensor/...
go vet -tags=list_sensors ./skills/list-sensors/...
```

Expected: no output (no warnings).

- [ ] **Step 6: Commit**

```bash
git add lib/orchestrator/integration_runtime_logs_test.go
git commit -m "$(cat <<'EOF'
test(orchestrator): e2e coverage for dep signals.log via RunWithDepsRoot

End-to-end regression test for issue #45 that would have caught the
original bug. Drives RunWithDepsRoot against a root sensor whose
blocking dep declares an output_parsing pattern; asserts the dep's
run-id-scoped signals.log contains the parsed individual and the flat
path is never created.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Final verification

- [ ] **Verify the branch is ready to push**

```bash
git log --oneline main..HEAD
```

Expected output (five new commits, one per task):

```
<sha> test(orchestrator): e2e coverage for dep signals.log via RunWithDepsRoot
<sha> fix(list-sensors): emit run-id-scoped signals_log_path
<sha> fix(stop-sensor): read signals.log from run-id-scoped path
<sha> fix(orchestrator): stopBlockingDep terminates the dep's watcher
<sha> fix(orchestrator): startBlockingDep uses run-id layout and spawns watcher
<sha> docs: align fallback discipline with lib/registry/sanitize convention
<sha> docs: spec for issue #45 — unify dep log layout and spawn watcher
```

- [ ] **Manual reproduction (optional, only if a real project is available)**

In a project that has both `.harness/sensors/` and a blocking dep with `output_parsing.patterns`, run:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts <root-sensor-id>
```

Confirm during execution:
- `.harness/runtime/<dep-id>/<runID>/raw.log` is populated with the dep's stdout/stderr.
- `.harness/runtime/<dep-id>/<runID>/signals.log` is populated with individual Signals.
- `.harness/runtime/<dep-id>/raw.log` (flat) does NOT exist.
- During the dep's life, `/list-sensors` emits `signals_log_path` pointing at the run-id-scoped file.

- [ ] **Open the PR**

```bash
git push -u origin fix/issue-45-deps-logs
gh pr create --title "fix: blocking deps log to run-id layout with watcher (#45)" --body "$(cat <<'EOF'
## Summary

- `lib/orchestrator/live_deps.go::startBlockingDep` now writes to the run-id-scoped layout and spawns a watcher per dep (parity with `/start-sensor`).
- `stopBlockingDep` terminates the dep's watcher on detach.
- `/stop-sensor` and `/list-sensors` now read/emit the run-id-scoped `signals.log` with the `-legacy` suffix fallback already used by `/tail-sensor`.

Spec: `docs/superpowers/specs/2026-05-14-fix-deps-logs-design.md`.

Closes #45.

## Test plan

- [ ] `go test ./lib/orchestrator/ -timeout 60s` passes (includes 6 new tests covering layout, patterns, plugin-root-missing, watcher-spawn-failure, detach-kills-watcher, and the E2E).
- [ ] `go test -tags=stop_sensor ./skills/stop-sensor/... -timeout 60s` passes (includes 2 new tests for run-id-scoped + legacy fallback).
- [ ] `go test -tags=list_sensors ./skills/list-sensors/... -timeout 60s` passes (includes 2 new tests).
- [ ] `go vet` clean for affected packages.
- [ ] Manual reproduction in a real project confirms `signals.log` is populated for blocking deps and `/tail-sensor <dep-id>` emits the parsed individuals.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review notes

**Spec coverage check:**
- (1) `startBlockingDep` refactor → Task 1.
- (2) `stopBlockingDep` watcher termination → Task 2.
- (3) `/stop-sensor` migration → Task 3.
- (4) `/list-sensors` migration → Task 4.
- (5) Schemas untouched → no task needed.
- (6) `/start-sensor`, `/tail-sensor`, `/run-sensor` untouched → no task needed.
- (7) Synthetic `pass` aggregate explicitly out of scope → no task; called out at end of spec and PR body.
- Test plan items 1-10 → Tasks 1–4 cover items 1, 2, 3, 4, 5 (in Task 1), 6 (Task 2), 9, 10 (Task 3), 11, 12 (Task 4). Item 7 (registry-save failure) marked optional in the spec; not in the plan (skipped per spec's optional clause).
- Test plan item 8 (E2E) → Task 5.

**Type and signature consistency:**
- `startBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry, projectRoot string) (string, error)` — unchanged.
- `stopBlockingDep(r registry.Root, entry *registry.RunningSensorEntry, v *schema.Validator, stdout, stderr io.Writer)` — unchanged.
- `watcher.SpawnOpts` fields and `watcher.Spawn` signature — unchanged (existing `lib/watcher` API).
- `registry.RunningSensorEntry.WatcherPID` — already an `int` field on the struct; populated with the spawned PID instead of `0`.

**Placeholder scan:** None. Every step contains either exact code, exact commands, or both.
