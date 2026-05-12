# `go run` Invocation Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the requirement for pre-built `harness-watcher`/`harness-*` binaries by adopting a unified invocation contract `HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> ./skills/<name>/scripts <args>` across every entrypoint (SKILL.md, internal `exec.Command` chains, and the watcher spawn). Closes GitHub issue #15.

**Architecture:** Three coordinated moves: (1) production `start-sensor` is rewired from inline `os.StartProcess(<sibling watcher binary>)` to `lib/watcher.Spawn` (today dead code); (2) `lib/watcher.Spawn` is rewritten to invoke `go run` with `-C "${CLAUDE_PLUGIN_ROOT}"` so the watcher compiles on demand inside the plugin's own module graph; (3) all SKILL.md files and `heal-sensor/retry-original.go` adopt the same `go -C ... run` shape, so neither user-project `go.mod`/`go.work` pollution nor `os.Executable()`-based sibling lookup is possible. The runner's subprocess cwd is preserved at project-root via a new `Dir` field on the subprocess library's config structs.

**Tech Stack:** Go 1.25, `github.com/santhosh-tekuri/jsonschema/v5`, standard `os/exec`, `syscall`. No new external deps. Tests use a `withFakeGo` helper that prefixes `PATH` with a temp dir containing a shell-script masquerading as `go`.

---

## File Structure

### Files modified

| File | Why |
|---|---|
| `lib/watcher/spawn.go` | Add `spawnFn` injection; add `HARNESS_WATCHER_RUN_ID` env; add `PluginRoot`/`earlyDeathProbe`; swap `os.StartProcess` for `exec.Command("go", "-C", ...)`; delete `BinaryPath()`. |
| `lib/watcher/spawn_unix.go` | Unchanged (still exports `sysProcAttr` with `Setsid: true`). |
| `lib/watcher/spawn_test.go` | Rewritten — `withFakeGo` helper + new cases for args/env/Setsid/Release/early-death/plugin-root-missing/go-missing. |
| `skills/start-sensor/scripts/start.go` | Inline `watcherPath` resolution + `os.StartProcess(...)` block (lines 153–287) replaced with `watcher.Spawn(opts)`. Reads `os.Getenv("CLAUDE_PLUGIN_ROOT")`; emits `plugin_root_missing` Signal if empty. |
| `skills/start-sensor/scripts/start_unix.go` | `watcherBinaryPath()` deleted; `killGroup`/`killPID` retained. |
| `skills/start-sensor/scripts/start_test.go` | `TestMain` swaps stub-watcher install for `watcher.SpawnFn` override. New cases: `plugin_root_missing`, `watcher_spawn_failed` via injected error. |
| `lib/orchestrator/main_test.go` | Same — stub install replaced with `watcher.SpawnFn` override in `TestMain`. |
| `lib/subprocess/detach.go` | Add `Dir string` to `DetachConfig`; pass to `exec.Command` as `cmd.Dir`. |
| `lib/subprocess/step.go` | Add `Dir string` to `StepConfig`; pass to `cmd.Dir`. |
| `lib/subprocess/stream.go` | Add `Dir string` to `StreamConfig`; pass to `cmd.Dir`. |
| `lib/orchestrator/live_deps.go` | Pass `projectRoot` to `subprocess.SpawnDetached(DetachConfig{… Dir: projectRoot})`. |
| `lib/orchestrator/lifecycle.go` | Pass `projectRoot` to subprocess steps for prepare/teardown and the command itself. |
| `skills/run-sensor/scripts/run-computational.go` | `main()` uses `lib/registry.Lookup` (or stays as-is if already correct via projectRoot threading). Threads `projectRoot` to subprocess via the new `Dir` field. |
| `skills/run-sensor/scripts/run-inferential.go` | Same. |
| `skills/heal-sensor/scripts/retry-original.go` | `exec.Command` uses `-C pluginRoot`; `repoRoot()` deleted; env passes through. |
| `skills/heal-sensor/scripts/diagnose.go` | Read `CLAUDE_PLUGIN_ROOT` if it touches paths (verify; likely no-op). |
| `skills/heal-sensor/scripts/apply-safe.go` | Same verification. |
| `skills/heal-sensor/scripts/apply-sensors.go` | Same verification. |
| `skills/detect-sensors/scripts/write-sensor.go` | No code change needed (uses `--schemas-dir` flag, `--out` flag — already explicit). Verify. |
| `hooks/error-issue-autofiler.go` | Extend the four regexes in `buildFrameworkCommandPatterns` to allow optional `(?:-C\s+\S+\s+)?` between `go\s+` and the verb. |
| `hooks/error-issue-autofiler_test.go` | New cases for `go -C <path> run ...` matching; legacy `harness-watcher` still matches. |
| `skills/run-sensor/SKILL.md` | Replace `go run -tags=<tag> ./skills/run-sensor/scripts <id>` with the new contract on lines 68 + 78. Add a one-paragraph "Invocation contract" link/explanation. |
| `skills/start-sensor/SKILL.md` | Line 21. Add `plugin_root_missing` to `metadata.cause` list. |
| `skills/stop-sensor/SKILL.md` | Line 19. |
| `skills/tail-sensor/SKILL.md` | Line 22. |
| `skills/list-sensors/SKILL.md` | Line 19. |
| `skills/detect-sensors/SKILL.md` | Lines 245, 282, 290. |
| `skills/heal-sensor/SKILL.md` | Lines 25, 73, 92, 102. |
| `CLAUDE.md` | "Build, validate, test" section rewritten with new contract; latency note added. |
| `README.md` | (Currently empty) Populated with quick-start. |
| `CHANGELOG.md` | New entry for v1.1.0. |
| `.claude-plugin/plugin.json` | Version bump to `1.1.0`. |
| `skills/run-sensor/scripts/run-computational_test.go` | The `exec.Command("go", "build", "-tags=run_computational", ...)` block at line 163 stays (it tests an integration scenario that compiles directly); add a parallel test with the new `-C` invocation contract. |
| `skills/run-sensor/scripts/run-inferential_test.go` | Same (line 347). |
| `test/registry-discovery-e2e/registry_discovery_e2e_test.go` | New cases: `TestPluginVsProjectGoMod`, `TestGoWorkPollution`, `TestSensorCwd`. |

### Files deleted (within an edited file, not whole-file deletes)

- `lib/watcher/spawn.go::BinaryPath` (function)
- `skills/start-sensor/scripts/start_unix.go::watcherBinaryPath` (function)
- `skills/heal-sensor/scripts/retry-original.go::repoRoot` (function)

### No files renamed or fully deleted.

---

## Phase 1 — `lib/watcher` parity and resurrection (commit 1)

**Goal of phase:** `lib/watcher.Spawn` becomes the single locus of watcher-spawn logic. Production semantics are unchanged: still sibling-binary lookup. The package gains `spawnFn` injection so tests can stop installing a `/usr/bin/true` stub. The missing `HARNESS_WATCHER_RUN_ID` env var is added to bring `lib/watcher.Spawn` to parity with the inline code it will replace.

### Task 1.1: Add `HARNESS_WATCHER_RUN_ID` to `lib/watcher.Spawn` env block

**Files:**
- Modify: `lib/watcher/spawn.go:45-57`
- Test: `lib/watcher/spawn_test.go`

- [ ] **Step 1: Write the failing test**

Append to `lib/watcher/spawn_test.go`:

```go
func TestSpawn_PropagatesRunID(t *testing.T) {
	// We can't run real Spawn here because there's no watcher binary.
	// Validate via a code-shape check: SpawnOpts has RunID, and the env
	// block in realSpawn includes HARNESS_WATCHER_RUN_ID. Done by reading
	// the source — but a better integration check is to install a fake
	// `watcher` next to the test binary that echoes its env to a file,
	// then assert the file contains HARNESS_WATCHER_RUN_ID=opts.RunID.

	tmp := t.TempDir()
	exe, _ := os.Executable()
	watcher := filepath.Join(filepath.Dir(exe), "watcher")
	// Bash script that writes env to $HARNESS_WATCHER_RUN_ID-out
	stub := []byte("#!/bin/sh\nenv | grep HARNESS_WATCHER_RUN_ID > " + tmp + "/env.out\n")
	if err := os.WriteFile(watcher, stub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(watcher) })

	rawLog := filepath.Join(tmp, "raw.log")
	sigLog := filepath.Join(tmp, "signals.log")
	if err := os.WriteFile(rawLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Spawn(SpawnOpts{
		ProjectRoot:    tmp,
		SensorID:       "x",
		RunID:          "run-abc-123",
		RawLogPath:     rawLog,
		SignalsLogPath: sigLog,
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Give the stub a moment to flush
	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(filepath.Join(tmp, "env.out"))
		if len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(string(got), "HARNESS_WATCHER_RUN_ID=run-abc-123") {
		t.Errorf("env.out = %q, want it to contain HARNESS_WATCHER_RUN_ID=run-abc-123", got)
	}
}
```

Also add to the imports at the top of `spawn_test.go`: `"strings"`, `"time"`.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./lib/watcher/ -run TestSpawn_PropagatesRunID -v
```

Expected: FAIL (the env block does not currently include `HARNESS_WATCHER_RUN_ID`).

- [ ] **Step 3: Add the missing env line**

In `lib/watcher/spawn.go`, locate the env-vars block (currently lines 46–54). After the `HARNESS_WATCHER_SENSOR_ID` line, add `HARNESS_WATCHER_RUN_ID`:

```go
proc, err := os.StartProcess(bin, []string{bin}, &os.ProcAttr{
    Env: []string{
        fmt.Sprintf("HARNESS_WATCHER_RAW=%s", opts.RawLogPath),
        fmt.Sprintf("HARNESS_WATCHER_SIGNALS=%s", opts.SignalsLogPath),
        fmt.Sprintf("HARNESS_WATCHER_PATTERNS=%s", string(opts.PatternsJSON)),
        fmt.Sprintf("HARNESS_WATCHER_ENVELOPE=%s", string(opts.EnvelopeJSON)),
        fmt.Sprintf("HARNESS_WATCHER_SUBPROCESS_PID=%d", opts.SubprocessPID),
        fmt.Sprintf("HARNESS_WATCHER_REGISTRY_ROOT=%s", opts.ProjectRoot),
        fmt.Sprintf("HARNESS_WATCHER_SENSOR_ID=%s", opts.SensorID),
        fmt.Sprintf("HARNESS_WATCHER_RUN_ID=%s", opts.RunID),
    },
    Files: []*os.File{nil, nil, logFile},
    Sys:   &sysProcAttr,
})
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./lib/watcher/ -run TestSpawn_PropagatesRunID -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/watcher/spawn.go lib/watcher/spawn_test.go
git commit -m "$(cat <<'EOF'
fix(watcher): propagate HARNESS_WATCHER_RUN_ID to spawned watcher

lib/watcher.Spawn was missing the run_id env line that the inline spawn
in start.go emits. With Phase 1 about to wire start.go to lib/watcher,
parity is restored first so production behavior is unchanged across the
move.
EOF
)"
```

---

### Task 1.2: Introduce `spawnFn` injection point in `lib/watcher`

**Files:**
- Modify: `lib/watcher/spawn.go`
- Test: `lib/watcher/spawn_test.go`

- [ ] **Step 1: Write the failing test**

Append to `lib/watcher/spawn_test.go`:

```go
func TestSpawn_DelegatesToSpawnFn(t *testing.T) {
	called := false
	prev := SpawnFn
	SpawnFn = func(opts SpawnOpts) (int, error) {
		called = true
		if opts.SensorID != "marker" {
			t.Errorf("opts.SensorID = %q, want marker", opts.SensorID)
		}
		return 99999, nil
	}
	t.Cleanup(func() { SpawnFn = prev })

	pid, err := Spawn(SpawnOpts{SensorID: "marker", ProjectRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if !called {
		t.Error("SpawnFn was not invoked")
	}
	if pid != 99999 {
		t.Errorf("pid = %d, want 99999", pid)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./lib/watcher/ -run TestSpawn_DelegatesToSpawnFn -v
```

Expected: FAIL — `SpawnFn` does not exist as a package-level variable.

- [ ] **Step 3: Refactor `Spawn` to call `SpawnFn`**

In `lib/watcher/spawn.go`:

1. Rename the existing `Spawn` function to `realSpawn` (private). Keep its current body intact.
2. Add a package-level `SpawnFn` exported var initialized to `realSpawn`.
3. Add a new public `Spawn` thin wrapper that calls `SpawnFn`.

The relevant region of `spawn.go` becomes:

```go
// SpawnFn is the spawner used by Spawn. Tests override this to avoid
// invoking the real watcher binary. Default: realSpawn (sibling-binary lookup).
var SpawnFn = realSpawn

// Spawn launches the watcher subprocess via SpawnFn. Returns the PID of
// the spawned process (or 0 on error).
func Spawn(opts SpawnOpts) (int, error) {
	return SpawnFn(opts)
}

func realSpawn(opts SpawnOpts) (int, error) {
	bin, err := BinaryPath()
	if err != nil {
		return 0, fmt.Errorf("watcher binary path: %w", err)
	}
	// ... existing body of the old Spawn function, unchanged ...
}
```

- [ ] **Step 4: Run all `lib/watcher` tests**

```bash
go test ./lib/watcher/ -v
```

Expected: all PASS, including the new `TestSpawn_DelegatesToSpawnFn` and the existing `TestSpawn_ErrorWhenBinaryAbsent` and `TestSpawn_PropagatesRunID`.

- [ ] **Step 5: Commit**

```bash
git add lib/watcher/spawn.go lib/watcher/spawn_test.go
git commit -m "$(cat <<'EOF'
refactor(watcher): expose SpawnFn for test injection

Spawn now delegates to a package-level SpawnFn var (default: realSpawn,
which retains the original sibling-binary lookup). Tests can override
SpawnFn to avoid installing a stub watcher binary next to the test exe.
No production behavior change.
EOF
)"
```

---

### Task 1.3: Refactor `start.go` to call `watcher.Spawn`

**Files:**
- Modify: `skills/start-sensor/scripts/start.go:142-287`
- Modify: `skills/start-sensor/scripts/start_unix.go` (delete `watcherBinaryPath`)
- Test: `skills/start-sensor/scripts/start_test.go` (existing tests cover; we extend test scaffolding)

- [ ] **Step 1: Write the failing test**

Add to `skills/start-sensor/scripts/start_test.go` (in the existing file, anywhere after the helpers but before unrelated tests):

```go
func TestStart_DelegatesToWatcherSpawn(t *testing.T) {
	root := t.TempDir()
	writeFixtureSensor(t, root, "blocking-sensor", map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking fixture",
		"description": "blocking",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"output":      "single",
		"cost": map[string]interface{}{
			"compute": "small",
		},
		"execution": map[string]interface{}{
			"command":           "sleep 5",
			"blocking":          true,
			"graceful_timeout_ms": 1000,
			"exit_code_map": []interface{}{
				map[string]interface{}{"code": 0, "verdict": "pass", "severity": "info"},
			},
		},
	})

	var spawnedOpts watcher.SpawnOpts
	prevSpawnFn := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		spawnedOpts = opts
		return 12345, nil
	}
	t.Cleanup(func() {
		watcher.SpawnFn = prevSpawnFn
		// Subprocess started via `sleep 5` — kill it.
		// (cleaned up by t.TempDir + process exit anyway)
	})

	exit, sig := runStart(testResult(root), []string{"blocking-sensor"})
	if exit != 0 {
		t.Fatalf("runStart exit = %d, signal = %#v", exit, sig)
	}
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict = %v, want pass", sig["verdict"])
	}
	if spawnedOpts.SensorID != "blocking-sensor" {
		t.Errorf("spawnedOpts.SensorID = %q, want blocking-sensor", spawnedOpts.SensorID)
	}
	if spawnedOpts.RunID == "" {
		t.Error("spawnedOpts.RunID is empty")
	}
	if spawnedOpts.ProjectRoot != root {
		t.Errorf("spawnedOpts.ProjectRoot = %q, want %q", spawnedOpts.ProjectRoot, root)
	}
}
```

Add the import: `watcher "github.com/iurykrieger/harness-framework/lib/watcher"` (alias for clarity).

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -tags=start_sensor ./skills/start-sensor/scripts/ -run TestStart_DelegatesToWatcherSpawn -v
```

Expected: FAIL — `start.go` still spawns the watcher inline via `os.StartProcess`; `watcher.SpawnFn` is never consulted.

- [ ] **Step 3: Replace inline spawn with `watcher.Spawn`**

In `skills/start-sensor/scripts/start.go`:

1. Add import (replace the existing `subprocess` import block):

```go
import (
    // ... existing imports ...
    "github.com/iurykrieger/harness-framework/lib/subprocess"
    "github.com/iurykrieger/harness-framework/lib/watcher"
)
```

2. Replace lines 153–287 (the `watcherPath` resolution and the entire `os.StartProcess` block, plus the surrounding stages 1–4 logic that builds env/files/runDir — the registry entry write stays). After change, the lifecycle inside the flock callback becomes:

```go
// Stage 1: pre-create the staging raw.log at the flat SensorDir path.
stagingRaw := r.RawLog(id)
if err := os.WriteFile(stagingRaw, nil, 0o644); err != nil {
    return fmt.Errorf("create staging raw.log: %w", err)
}

// Stage 2: spawn the subprocess detached.
det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
    Command: command,
    LogFile: stagingRaw,
    Dir:     projectRoot,  // NEW — preserves sensor cwd
})
if err != nil {
    _ = os.Remove(stagingRaw)
    return fmt.Errorf("spawn: %w", err)
}

// Stage 3: derive composite run_id from PID + short UUID.
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

// Stage 4: rename staging raw.log into <run-id>/raw.log.
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

envelope := libsensor.Envelope{
    SensorID:   id,
    Version:    stringField(sensorJSON, "version"),
    RunID:      runID,
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

// Stage 5: spawn the watcher via lib/watcher.
watcherPID, err := watcher.Spawn(watcher.SpawnOpts{
    ProjectRoot:    projectRoot,
    SensorID:       id,
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
        _ = killGroup(det.PGID)
    }
    _ = os.RemoveAll(runDir)
    return fmt.Errorf("start watcher: %w", err)
}

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
    det:        det,
    watcherPID: watcherPID,
    envelope:   envelope,
    runID:      runID,
    runDir:     runDir,
}
return nil
```

3. Update the `spawnResult` struct definition (currently around line 161): remove the `watcherProc *os.Process` field (no longer needed — `watcher.Spawn` already releases). Keep the rest.

4. Anywhere else in `start.go` that references `watcherProc` (e.g., signal envelope construction), remove. Watcher PID is enough.

- [ ] **Step 4: Delete `watcherBinaryPath` from `start_unix.go`**

Edit `skills/start-sensor/scripts/start_unix.go`. Replace the entire file body (keeping the build tag and package declaration) with:

```go
//go:build start_sensor && (darwin || linux)

package main

import "syscall"

// killGroup sends SIGKILL to the entire process group identified by pgid.
// Used to undo a just-spawned root subprocess when the watcher spawn
// fails inside the flock callback.
func killGroup(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// killPID sends SIGKILL to a single process.
func killPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
```

- [ ] **Step 5: Run the tests**

```bash
go test -tags=start_sensor ./skills/start-sensor/scripts/ -v
```

Expected: all PASS, including the new `TestStart_DelegatesToWatcherSpawn`. The existing `TestMain` still installs a stub binary, which is fine because `realSpawn` still uses `BinaryPath()` — production behavior unchanged.

- [ ] **Step 6: Commit**

```bash
git add skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_unix.go skills/start-sensor/scripts/start_test.go
git commit -m "$(cat <<'EOF'
refactor(start-sensor): delegate watcher spawn to lib/watcher

start.go's inline os.StartProcess(<sibling watcher binary>) block is
replaced with a single watcher.Spawn(opts) call. watcherBinaryPath()
local helper deleted (lib/watcher.BinaryPath() is the source of truth
for now; Phase 2 deletes both). subprocess.DetachConfig gains an
explicit Dir so the sensor command keeps running at project root once
the runner is invoked via `go -C <plugin root>`.

lib/watcher is no longer dead code; production now flows through it.
EOF
)"
```

---

### Task 1.4: Add `Dir` field to `lib/subprocess` config structs

**Note:** Task 1.3 already references `subprocess.DetachConfig{… Dir: projectRoot}`. We need to add the field to the struct before that compiles. Reorder: do Task 1.4 *before* Task 1.3 in actual execution. The plan keeps them logically grouped, but the subagent must execute 1.4 first if 1.3's edits would not compile.

**Files:**
- Modify: `lib/subprocess/detach.go`
- Modify: `lib/subprocess/step.go`
- Modify: `lib/subprocess/stream.go`
- Test: `lib/subprocess/detach_test.go`, `lib/subprocess/step_test.go`, `lib/subprocess/stream_test.go`

- [ ] **Step 1: Write failing tests for each subprocess kind**

Append to `lib/subprocess/detach_test.go`:

```go
func TestSpawnDetached_RespectsDir(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "out.log")
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := SpawnDetached(DetachConfig{
		Command: "pwd > " + filepath.Join(tmp, "pwd.out"),
		LogFile: logFile,
		Dir:     tmp,
	})
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-res.PGID, syscall.SIGKILL)
	})

	// Wait for the pwd command to flush.
	deadline := time.Now().Add(1 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(filepath.Join(tmp, "pwd.out"))
		if len(got) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	want, _ := filepath.EvalSymlinks(tmp)
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if gotResolved != want {
		t.Errorf("subprocess cwd = %q, want %q", gotResolved, want)
	}
}
```

Add imports as needed: `"strings"`, `"syscall"`, `"time"`.

Append symmetric tests `TestRunStep_RespectsDir` to `step_test.go` and `TestStreamSubprocess_RespectsDir` to `stream_test.go` (same shape, using each function's API).

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./lib/subprocess/ -run RespectsDir -v
```

Expected: FAIL — `DetachConfig`, `StepConfig`, and `StreamConfig` do not have a `Dir` field.

- [ ] **Step 3: Add `Dir` to each config struct and apply it**

In `lib/subprocess/detach.go`, locate `type DetachConfig struct { ... }` and add `Dir string`:

```go
type DetachConfig struct {
    Command string
    LogFile string
    Dir     string // working directory for the subprocess (empty = inherit)
}
```

Then in the function that builds the `exec.Command` (currently around line 43), after `cmd := exec.Command("sh", "-c", cfg.Command)`, add:

```go
if cfg.Dir != "" {
    cmd.Dir = cfg.Dir
}
```

Symmetric edits in `lib/subprocess/step.go` (`StepConfig`, around line 44) and `lib/subprocess/stream.go` (`StreamConfig`, around line 128).

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./lib/subprocess/ -v
```

Expected: all PASS, including the three new `RespectsDir` tests and all existing tests.

- [ ] **Step 5: Commit**

```bash
git add lib/subprocess/detach.go lib/subprocess/step.go lib/subprocess/stream.go lib/subprocess/detach_test.go lib/subprocess/step_test.go lib/subprocess/stream_test.go
git commit -m "$(cat <<'EOF'
feat(subprocess): add Dir field to Detach/Step/Stream configs

The new go run -C "${CLAUDE_PLUGIN_ROOT}" invocation contract chdirs the
runner to the plugin root. Sensor commands need to keep running at the
user's project root, so the runner threads projectRoot through to the
subprocess library as an explicit Dir. Empty value preserves inherit-cwd
behavior — no regression in callers that don't yet set it.
EOF
)"
```

---

### Task 1.5: Migrate test scaffolding to `SpawnFn` override

**Files:**
- Modify: `lib/orchestrator/main_test.go`
- Modify: `skills/start-sensor/scripts/start_test.go::TestMain`

- [ ] **Step 1: Rewrite `lib/orchestrator/main_test.go`**

Replace the entire file content (lines 1–30) with:

```go
package orchestrator_test

import (
	"os"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/watcher"
)

func TestMain(m *testing.M) {
	// Override the watcher spawner with a noop returning a fake PID.
	// Orchestrator tests never exercise watcher behavior — they only
	// require that Spawn succeeds and returns a positive PID for the
	// registry entry.
	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		return 99999, nil
	}
	code := m.Run()
	watcher.SpawnFn = prev
	os.Exit(code)
}
```

- [ ] **Step 2: Rewrite `skills/start-sensor/scripts/start_test.go::TestMain`**

Replace lines 20–40 of `start_test.go` with:

```go
func TestMain(m *testing.M) {
	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		return 99999, nil
	}
	code := m.Run()
	watcher.SpawnFn = prev
	os.Exit(code)
}
```

Make sure the `watcher` alias import is at the top of the file (added in Task 1.3 Step 3). The `path/filepath` import may become unused for `TestMain`; let `goimports` clean up if so, or remove manually if `filepath` is otherwise unused.

- [ ] **Step 3: Run both test suites**

```bash
go test ./lib/orchestrator/ -v
go test -tags=start_sensor ./skills/start-sensor/scripts/ -v
```

Expected: both PASS.

- [ ] **Step 4: Commit**

```bash
git add lib/orchestrator/main_test.go skills/start-sensor/scripts/start_test.go
git commit -m "$(cat <<'EOF'
test: migrate watcher stub scaffolding to SpawnFn override

TestMain in lib/orchestrator and skills/start-sensor/scripts no longer
copies /usr/bin/true to an exe-relative `watcher` path. They override
watcher.SpawnFn directly. Removes a layer of filesystem fragility from
the test boot sequence.
EOF
)"
```

---

## Phase 2 — Rewrite `lib/watcher.realSpawn` to use `go run` (commit 2)

**Goal of phase:** `realSpawn` stops looking for a sibling binary and spawns the watcher via `exec.Command("go", "-C", pluginRoot, "run", "-tags=start_watcher", "./skills/start-sensor/scripts")`. New `SpawnOpts.PluginRoot` field; `earlyDeathProbe` to catch compile errors. `BinaryPath()` deleted.

### Task 2.1: Add `withFakeGo` helper

**Files:**
- Create: `lib/watcher/fakego_test.go`

- [ ] **Step 1: Write the helper**

Create `lib/watcher/fakego_test.go`:

```go
package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// withFakeGo installs a temp directory at the front of $PATH containing
// a bash script masquerading as `go`. The script writes its argv (one
// per line, separated by ASCII unit-separator) to <argsFile>, writes
// the env block to <envFile>, sleeps for sleepMS milliseconds, then
// exits with exitCode. If stderr is non-empty, it is written verbatim
// to stderr before exit.
//
// Returns the temp dir so the test can read argsFile/envFile. The
// PATH change is reverted via t.Cleanup.
func withFakeGo(t *testing.T, sleepMS int, exitCode int, stderr string) (tmpDir, argsFile, envFile string) {
	t.Helper()
	tmpDir = t.TempDir()
	argsFile = filepath.Join(tmpDir, "args.txt")
	envFile = filepath.Join(tmpDir, "env.txt")

	script := fmt.Sprintf(`#!/bin/sh
# Record args, one per line, separated by ASCII unit-separator.
printf '%%s\x1f' "$@" > %q
# Record env.
env > %q
%s
sleep %f
exit %d
`,
		argsFile, envFile,
		shellStderr(stderr),
		float64(sleepMS)/1000.0,
		exitCode)

	goBin := filepath.Join(tmpDir, "go")
	if err := os.WriteFile(goBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+origPath)
	return tmpDir, argsFile, envFile
}

func shellStderr(msg string) string {
	if msg == "" {
		return ""
	}
	// Quote single quotes by replacing ' with '\''.
	q := ""
	for _, r := range msg {
		if r == '\'' {
			q += `'\''`
		} else {
			q += string(r)
		}
	}
	return fmt.Sprintf("printf '%%s\\n' '%s' >&2", q)
}
```

- [ ] **Step 2: Smoke test the helper itself**

Add a smoke test in the same file:

```go
func TestWithFakeGo_RecordsArgs(t *testing.T) {
	_, argsFile, envFile := withFakeGo(t, 0, 0, "")

	cmd := exec.Command("go", "-C", "/tmp", "version")
	cmd.Env = append(os.Environ(), "MARKER=value")
	if err := cmd.Run(); err != nil {
		t.Fatalf("fake go: %v", err)
	}

	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "-C") || !strings.Contains(string(args), "/tmp") || !strings.Contains(string(args), "version") {
		t.Errorf("args = %q, want to contain -C, /tmp, version", args)
	}
	env, _ := os.ReadFile(envFile)
	if !strings.Contains(string(env), "MARKER=value") {
		t.Errorf("env file missing MARKER=value: %s", env)
	}
}
```

Add imports: `"os/exec"`, `"strings"`.

- [ ] **Step 3: Run the smoke test**

```bash
go test ./lib/watcher/ -run TestWithFakeGo_RecordsArgs -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add lib/watcher/fakego_test.go
git commit -m "$(cat <<'EOF'
test(watcher): add withFakeGo helper for spawn invocation tests

A shell script at the front of $PATH records argv/env/exit-behavior of
the `go` invocation so the watcher spawn rewrite (next commit) can be
verified without a real Go toolchain in the test loop.
EOF
)"
```

---

### Task 2.2: Add `PluginRoot` field and `plugin_root_missing` guard

**Files:**
- Modify: `lib/watcher/spawn.go`
- Test: `lib/watcher/spawn_test.go`

- [ ] **Step 1: Write the failing test**

Append to `lib/watcher/spawn_test.go`:

```go
func TestSpawn_RejectsEmptyPluginRoot(t *testing.T) {
	// Force production code path (no SpawnFn override).
	pid, err := Spawn(SpawnOpts{
		ProjectRoot:    t.TempDir(),
		SensorID:       "x",
		RunID:          "r1",
		// PluginRoot intentionally empty
		RawLogPath:     "/dev/null",
		SignalsLogPath: "/dev/null",
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
	})
	if err == nil {
		t.Fatalf("expected error for empty PluginRoot, got pid=%d", pid)
	}
	if !strings.Contains(err.Error(), "plugin root") {
		t.Errorf("err = %v, want one mentioning 'plugin root'", err)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./lib/watcher/ -run TestSpawn_RejectsEmptyPluginRoot -v
```

Expected: FAIL — `SpawnOpts` has no `PluginRoot` field; the guard does not exist.

- [ ] **Step 3: Add the field and guard**

Edit `lib/watcher/spawn.go`. In `SpawnOpts`, add a new field at the top:

```go
type SpawnOpts struct {
    PluginRoot     string  // absolute path to the plugin checkout (CLAUDE_PLUGIN_ROOT)
    ProjectRoot    string
    SensorID       string
    RunID          string
    RawLogPath     string
    SignalsLogPath string
    EnvelopeJSON   []byte
    PatternsJSON   []byte
    SubprocessPID  int
    WatcherLogPath string
}
```

In `Spawn` (the public wrapper that delegates to `SpawnFn`), add the guard *before* delegation, so it always runs regardless of whether `SpawnFn` is overridden:

```go
func Spawn(opts SpawnOpts) (int, error) {
    if opts.PluginRoot == "" {
        return 0, errors.New("plugin root not set (set CLAUDE_PLUGIN_ROOT)")
    }
    return SpawnFn(opts)
}
```

Add `"errors"` to imports if not already present.

- [ ] **Step 4: Run the test**

```bash
go test ./lib/watcher/ -run TestSpawn_RejectsEmptyPluginRoot -v
```

Expected: PASS.

- [ ] **Step 5: Other tests still pass once they set PluginRoot**

Other tests in `spawn_test.go` now need to populate `PluginRoot`. Find every `SpawnOpts{ ... }` literal in the file and add `PluginRoot: t.TempDir(),` (any non-empty path; the realSpawn path will be reached but those tests don't depend on the value at this point — they use the legacy binary lookup or the stub watcher). Update:
  - `TestSpawn_ErrorWhenBinaryAbsent` — already passes opts; just add `PluginRoot`.
  - `TestSpawn_PropagatesRunID` — same.
  - `TestSpawn_DelegatesToSpawnFn` — same.

Same for the orchestrator and start-sensor tests that call `Spawn` directly. (They don't — they go through `start.go` which sets it from `CLAUDE_PLUGIN_ROOT`. The test infrastructure overrides `SpawnFn`, so `realSpawn` isn't reached. But the guard *does* fire; the test setup needs `CLAUDE_PLUGIN_ROOT` set OR the start.go logic that reads it.) See Task 2.4 for plumbing in start.go.

```bash
go test ./lib/watcher/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add lib/watcher/spawn.go lib/watcher/spawn_test.go
git commit -m "$(cat <<'EOF'
feat(watcher): add PluginRoot field to SpawnOpts

Required by realSpawn's upcoming go-run-based implementation, which
needs to pass -C <plugin root> to `go`. Empty value fails fast with a
clear error before any process work.
EOF
)"
```

---

### Task 2.3: Rewrite `realSpawn` to use `go run`

**Files:**
- Modify: `lib/watcher/spawn.go`
- Modify: `lib/watcher/spawn_test.go` (extend with new test cases)

- [ ] **Step 1: Write failing tests for arg shape**

Append to `lib/watcher/spawn_test.go`:

```go
func TestRealSpawn_ArgShape(t *testing.T) {
	_, argsFile, _ := withFakeGo(t, 100, 0, "")

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(tmp, "watcher.log")

	pid, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake/plugin/root",
		ProjectRoot:    tmp,
		SensorID:       "s",
		RunID:          "r",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: logPath,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if pid <= 0 {
		t.Errorf("pid = %d, want > 0", pid)
	}

	// Wait for the fake go to finish writing args.
	deadline := time.Now().Add(2 * time.Second)
	var args []byte
	for time.Now().Before(deadline) {
		args, _ = os.ReadFile(argsFile)
		if len(args) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	got := strings.Split(strings.TrimRight(string(args), "\x1f"), "\x1f")
	want := []string{"-C", "/fake/plugin/root", "run", "-tags=start_watcher", "./skills/start-sensor/scripts"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %#v, want %#v", got, want)
	}
}
```

Add import: `"reflect"`.

- [ ] **Step 2: Write failing tests for env propagation**

```go
func TestRealSpawn_EnvPropagation(t *testing.T) {
	_, _, envFile := withFakeGo(t, 100, 0, "")

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644)

	_, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake/plugin/root",
		ProjectRoot:    tmp,
		SensorID:       "sensor-x",
		RunID:          "run-abc",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{"k":"v"}`),
		PatternsJSON:   []byte(`[{"p":1}]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: filepath.Join(tmp, "watcher.log"),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var env []byte
	for time.Now().Before(deadline) {
		env, _ = os.ReadFile(envFile)
		if len(env) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	required := []string{
		"HARNESS_WATCHER_RAW=" + filepath.Join(tmp, "raw.log"),
		"HARNESS_WATCHER_SIGNALS=" + filepath.Join(tmp, "sigs.log"),
		`HARNESS_WATCHER_PATTERNS=[{"p":1}]`,
		`HARNESS_WATCHER_ENVELOPE={"k":"v"}`,
		"HARNESS_WATCHER_SUBPROCESS_PID=" + fmt.Sprintf("%d", os.Getpid()),
		"HARNESS_WATCHER_REGISTRY_ROOT=" + tmp,
		"HARNESS_WATCHER_SENSOR_ID=sensor-x",
		"HARNESS_WATCHER_RUN_ID=run-abc",
		"GOWORK=off",
	}
	for _, want := range required {
		if !strings.Contains(string(env), want) {
			t.Errorf("env missing %q", want)
		}
	}
}
```

- [ ] **Step 3: Write failing test for early death detection**

```go
func TestRealSpawn_EarlyDeath(t *testing.T) {
	_, _, _ = withFakeGo(t, 10, 1, "mock compile error: bad syntax")

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644)

	pid, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake",
		ProjectRoot:    tmp,
		SensorID:       "s",
		RunID:          "r",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: filepath.Join(tmp, "watcher.log"),
	})
	if err == nil {
		t.Fatalf("expected error for early-death, got pid=%d", pid)
	}
	if !strings.Contains(err.Error(), "mock compile error") && !strings.Contains(err.Error(), "exited early") {
		t.Errorf("err = %v, want one mentioning 'exited early' or 'mock compile error'", err)
	}
}
```

- [ ] **Step 4: Write failing test for `go` not on PATH**

```go
func TestRealSpawn_GoMissing(t *testing.T) {
	// Empty PATH — exec.LookPath("go") will fail.
	t.Setenv("PATH", "")

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644)

	pid, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake",
		ProjectRoot:    tmp,
		SensorID:       "s",
		RunID:          "r",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: filepath.Join(tmp, "watcher.log"),
	})
	if err == nil {
		t.Fatalf("expected error when `go` is missing, got pid=%d", pid)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
}
```

- [ ] **Step 5: Run failing tests**

```bash
go test ./lib/watcher/ -v
```

Expected: the four new tests FAIL (`realSpawn` still uses `os.StartProcess(<sibling binary>)`); `TestSpawn_ErrorWhenBinaryAbsent` and `TestBinaryPath_NeighbourOfExecutable` still pass.

- [ ] **Step 6: Rewrite `realSpawn`**

Replace `realSpawn` in `lib/watcher/spawn.go` with the new implementation. The full file content:

```go
// Package watcher launches a watcher subprocess that tails a sensor's
// raw stdout log file, applies the sensor's output_parsing patterns, and
// writes parsed Signals to signals.log. Extracted from
// skills/start-sensor/scripts/start.go so both /start-sensor and the
// orchestrator's startBlockingDep can spawn watchers via the same code path.
package watcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// SpawnOpts captures everything needed to launch a watcher subprocess.
type SpawnOpts struct {
	PluginRoot     string
	ProjectRoot    string
	SensorID       string
	RunID          string
	RawLogPath     string
	SignalsLogPath string
	EnvelopeJSON   []byte
	PatternsJSON   []byte
	SubprocessPID  int
	WatcherLogPath string
}

// SpawnFn is the spawner used by Spawn. Tests override this to avoid
// invoking the real `go run` subprocess.
var SpawnFn = realSpawn

// Spawn launches the watcher subprocess via SpawnFn. Empty PluginRoot
// returns a clear error before any process work.
func Spawn(opts SpawnOpts) (int, error) {
	if opts.PluginRoot == "" {
		return 0, errors.New("plugin root not set (set CLAUDE_PLUGIN_ROOT)")
	}
	return SpawnFn(opts)
}

// realSpawn invokes `go -C <pluginRoot> run -tags=start_watcher
// ./skills/start-sensor/scripts` with the watcher env vars, returning
// the spawned process's PID after a 100ms early-death probe to catch
// compile errors.
func realSpawn(opts SpawnOpts) (int, error) {
	logPath := opts.WatcherLogPath
	if logPath == "" {
		logPath = filepath.Join(filepath.Dir(opts.SignalsLogPath), "watcher.log")
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open watcher.log: %w", err)
	}

	cmd := exec.Command("go", "-C", opts.PluginRoot, "run",
		"-tags=start_watcher", "./skills/start-sensor/scripts")
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GOCACHE=" + os.Getenv("GOCACHE"),
		"GOPATH=" + os.Getenv("GOPATH"),
		"GOWORK=off",
		"HARNESS_WATCHER_RAW=" + opts.RawLogPath,
		"HARNESS_WATCHER_SIGNALS=" + opts.SignalsLogPath,
		"HARNESS_WATCHER_PATTERNS=" + string(opts.PatternsJSON),
		"HARNESS_WATCHER_ENVELOPE=" + string(opts.EnvelopeJSON),
		fmt.Sprintf("HARNESS_WATCHER_SUBPROCESS_PID=%d", opts.SubprocessPID),
		"HARNESS_WATCHER_REGISTRY_ROOT=" + opts.ProjectRoot,
		"HARNESS_WATCHER_SENSOR_ID=" + opts.SensorID,
		"HARNESS_WATCHER_RUN_ID=" + opts.RunID,
	}
	cmd.Stdout = nil
	cmd.Stderr = logFile
	cmd.SysProcAttr = &sysProcAttr

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("start watcher: %w", err)
	}

	if alive, exitCode := earlyDeathProbe(cmd.Process.Pid, 100*time.Millisecond); !alive {
		stderrTail, _ := os.ReadFile(logPath)
		_ = logFile.Close()
		return 0, fmt.Errorf("watcher exited early (code %d): %s", exitCode, string(stderrTail))
	}

	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	return pid, nil
}

// earlyDeathProbe polls Wait4 for up to dur. Returns (alive, exitCode).
// alive=true means the process is still running after dur. exitCode is
// only meaningful when alive=false.
func earlyDeathProbe(pid int, dur time.Duration) (bool, int) {
	deadline := time.Now().Add(dur)
	for time.Now().Before(deadline) {
		var ws syscall.WaitStatus
		wpid, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if err != nil {
			return false, -1
		}
		if wpid == pid {
			return false, ws.ExitStatus()
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true, 0
}
```

Delete the old `BinaryPath()` function. Delete the `TestBinaryPath_NeighbourOfExecutable` test (no longer applicable).

- [ ] **Step 7: Update `TestSpawn_ErrorWhenBinaryAbsent`**

The old test relies on `BinaryPath`-based failure. Replace it with a positive case verifying that `realSpawn` returns success when a fake `go` cooperates:

```go
func TestRealSpawn_SuccessPath(t *testing.T) {
	_, _, _ = withFakeGo(t, 300, 0, "") // sleep 300ms — survives 100ms probe

	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "raw.log"), nil, 0o644)

	pid, err := Spawn(SpawnOpts{
		PluginRoot:     "/fake",
		ProjectRoot:    tmp,
		SensorID:       "s",
		RunID:          "r",
		RawLogPath:     filepath.Join(tmp, "raw.log"),
		SignalsLogPath: filepath.Join(tmp, "sigs.log"),
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
		WatcherLogPath: filepath.Join(tmp, "watcher.log"),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if pid <= 0 {
		t.Errorf("pid = %d, want > 0", pid)
	}

	// Kill the fake-go-as-watcher to avoid orphans.
	_ = syscall.Kill(pid, syscall.SIGTERM)
}
```

Also delete the legacy `TestSpawn_ErrorWhenBinaryAbsent` and `TestBinaryPath_NeighbourOfExecutable`. Reference: `TestSpawn_PropagatesRunID` from Task 1.1 still uses the old stub-binary approach — replace its body to use the fake `go` instead, OR delete it now that the env block is covered by `TestRealSpawn_EnvPropagation`.

Recommendation: delete `TestSpawn_PropagatesRunID` (covered by `TestRealSpawn_EnvPropagation`).

- [ ] **Step 8: Run all watcher tests**

```bash
go test ./lib/watcher/ -v
```

Expected: all PASS.

- [ ] **Step 9: Verify call sites still build**

```bash
go build -tags=start_sensor ./skills/start-sensor/scripts
go build ./lib/orchestrator
```

Expected: no compile errors. (`BinaryPath` no longer exists; only `start.go` referenced it via `watcherBinaryPath` local helper, which was already deleted in Task 1.3.)

- [ ] **Step 10: Delete `lib/watcher/spawn_unix.go`'s now-redundant content**

Verify `spawn_unix.go` still only exports `sysProcAttr` (used by the new `realSpawn`). No edit needed; just confirm.

- [ ] **Step 11: Commit**

```bash
git add lib/watcher/spawn.go lib/watcher/spawn_test.go
git commit -m "$(cat <<'EOF'
feat(watcher): spawn via go run instead of sibling binary

realSpawn now uses exec.Command("go", "-C", pluginRoot, "run",
"-tags=start_watcher", "./skills/start-sensor/scripts") with the same
HARNESS_WATCHER_* env vars and Setsid behavior as before. Adds an
earlyDeathProbe to catch compile errors and missing-go-on-PATH cases
before the registry entry stabilizes. BinaryPath() deleted.

The watcher is no longer required to exist as a pre-built sibling
binary. This closes #15.
EOF
)"
```

---

### Task 2.4: Plumb `PluginRoot` through `start.go`

**Files:**
- Modify: `skills/start-sensor/scripts/start.go`
- Test: `skills/start-sensor/scripts/start_test.go`

- [ ] **Step 1: Write the failing test**

Add to `start_test.go`:

```go
func TestStart_RejectsEmptyPluginRoot(t *testing.T) {
	root := t.TempDir()
	writeFixtureSensor(t, root, "blocking-sensor", map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking fixture",
		"description": "blocking",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"output":      "single",
		"cost":        map[string]interface{}{"compute": "small"},
		"execution": map[string]interface{}{
			"command":             "sleep 5",
			"blocking":            true,
			"graceful_timeout_ms": 1000,
			"exit_code_map": []interface{}{
				map[string]interface{}{"code": 0, "verdict": "pass", "severity": "info"},
			},
		},
	})

	t.Setenv("CLAUDE_PLUGIN_ROOT", "")

	exit, sig := runStart(testResult(root), []string{"blocking-sensor"})
	if exit == 0 {
		t.Fatalf("expected non-zero exit when CLAUDE_PLUGIN_ROOT empty, sig = %#v", sig)
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict = %v, want error", sig["verdict"])
	}
	meta, _ := sig["metadata"].(map[string]interface{})
	if meta["cause"] != "plugin_root_missing" {
		t.Errorf("metadata.cause = %v, want plugin_root_missing", meta["cause"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -tags=start_sensor ./skills/start-sensor/scripts/ -run TestStart_RejectsEmptyPluginRoot -v
```

Expected: FAIL — `start.go` does not yet emit `plugin_root_missing`.

- [ ] **Step 3: Add the guard in `runStart`**

In `skills/start-sensor/scripts/start.go`, near the top of `runStart` (after the `id := args[0]` line, before `path, err := libsensor.ResolveByID(...)`), add:

```go
pluginRoot := os.Getenv("CLAUDE_PLUGIN_ROOT")
if pluginRoot == "" {
    return 1, validateSignal(v, finalSignal(id, nil, "failed", "plugin_root_missing",
        nil, "CLAUDE_PLUGIN_ROOT not set in environment", diagnose), id)
}
```

Then thread `pluginRoot` into the `watcher.Spawn` call inside the flock callback:

```go
watcherPID, err := watcher.Spawn(watcher.SpawnOpts{
    PluginRoot:     pluginRoot,  // NEW
    ProjectRoot:    projectRoot,
    SensorID:       id,
    // ... rest unchanged ...
})
```

- [ ] **Step 4: Add `plugin_root_missing` to documented causes (defer to Phase 5; just leave a TODO comment if needed)**

No code change here — `validateSignal` does not enumerate causes. Documentation update in Phase 5.

- [ ] **Step 5: Update `TestStart_DelegatesToWatcherSpawn` to set `CLAUDE_PLUGIN_ROOT`**

Existing test from Task 1.3 needs `t.Setenv("CLAUDE_PLUGIN_ROOT", t.TempDir())` at the top of the test body (or in `TestMain`). Add to `TestMain`:

```go
func TestMain(m *testing.M) {
	os.Setenv("CLAUDE_PLUGIN_ROOT", os.TempDir())  // any non-empty path; spawnFn is overridden anyway
	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		return 99999, nil
	}
	code := m.Run()
	watcher.SpawnFn = prev
	os.Unsetenv("CLAUDE_PLUGIN_ROOT")
	os.Exit(code)
}
```

The new `TestStart_RejectsEmptyPluginRoot` overrides with `t.Setenv("CLAUDE_PLUGIN_ROOT", "")` so it tests the empty case.

- [ ] **Step 6: Run all start-sensor tests**

```bash
go test -tags=start_sensor ./skills/start-sensor/scripts/ -v
```

Expected: all PASS.

- [ ] **Step 7: Also update `lib/orchestrator/main_test.go`**

If any orchestrator test reaches the `start.go` code path (it does indirectly via `live_deps.go` spawning watchers for orchestrator-managed deps — though that path uses `watcher.Spawn` differently). Add `os.Setenv("CLAUDE_PLUGIN_ROOT", os.TempDir())` to `lib/orchestrator/main_test.go::TestMain` for symmetry:

```go
func TestMain(m *testing.M) {
	os.Setenv("CLAUDE_PLUGIN_ROOT", os.TempDir())
	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		return 99999, nil
	}
	code := m.Run()
	watcher.SpawnFn = prev
	os.Unsetenv("CLAUDE_PLUGIN_ROOT")
	os.Exit(code)
}
```

(Note: `live_deps.go::startBlockingDep` does not call `watcher.Spawn` today — the comment on line 185 says "No watcher process is spawned for orchestrator-managed deps". So orchestrator tests don't actually exercise `PluginRoot`. The env-set is defensive.)

```bash
go test ./lib/orchestrator/ -v
```

Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_test.go lib/orchestrator/main_test.go
git commit -m "$(cat <<'EOF'
feat(start-sensor): require CLAUDE_PLUGIN_ROOT, plumb to watcher.Spawn

runStart reads CLAUDE_PLUGIN_ROOT at entry and bails with verdict=error
metadata.cause=plugin_root_missing if absent. The value is threaded
into watcher.SpawnOpts.PluginRoot so the new go-run-based spawn knows
where to chdir.
EOF
)"
```

---

## Phase 3 — Invocation contract adopted across skills (commit 3)

**Goal of phase:** Every `SKILL.md`, every internal `exec.Command("go", "run", ...)`, and every project-root discovery point uses the new contract. The three scripts that today walk up looking for `sensors/` (run-sensor, detect-sensors, heal-sensor) switch to `lib/registry.Lookup`. Runner threads `projectRoot` into subprocess `Dir`.

### Task 3.1: Migrate `run-sensor/scripts/run-computational.go` to `lib/registry.Lookup`

**Files:**
- Modify: `skills/run-sensor/scripts/run-computational.go`
- Test: `skills/run-sensor/scripts/run-computational_test.go`

- [ ] **Step 1: Write the failing test**

Add to `run-computational_test.go`:

```go
func TestRunComputational_UsesRegistryLookupForProjectRoot(t *testing.T) {
	// Project: a temp dir with sensors/ but agent cwd is somewhere else.
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a minimal sensor.
	body := `{
		"id": "x", "version": "1.0.0", "name": "X", "description": "X",
		"determinism": "high", "kind": "observation", "type": "computational",
		"regulation": "behaviour", "output": "single",
		"cost": {"compute": "small"},
		"execution": {
			"command": "true",
			"exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]
		}
	}`
	_ = os.WriteFile(filepath.Join(proj, "sensors", "x.json"), []byte(body), 0o644)

	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	// Run from an unrelated cwd.
	unrelated := t.TempDir()
	var stdout, stderr bytes.Buffer
	exit := run([]string{"x"}, unrelated, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"verdict":"pass"`) {
		t.Errorf("stdout did not contain pass verdict: %s", stdout.String())
	}
}
```

Add imports as needed.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -tags=run_computational ./skills/run-sensor/scripts/ -run TestRunComputational_UsesRegistryLookupForProjectRoot -v
```

Expected: FAIL — today, `run-computational.go::main` passes `os.Getwd()` directly as `projectRoot`; `HARNESS_REGISTRY_ROOT` is not consulted by this script.

- [ ] **Step 3: Update `run-computational.go::main` to use `lib/registry.Lookup`**

Replace `main()` in `run-computational.go`:

```go
func main() {
	cwd, _ := os.Getwd()
	res, err := registry.Lookup(cwd)
	projectRoot := cwd
	if err == nil {
		projectRoot = res.ProjectRoot
	}
	os.Exit(run(os.Args[1:], projectRoot, os.Stdout, os.Stderr))
}
```

Add import: `"github.com/iurykrieger/harness-framework/lib/registry"`.

The `run()` function signature is unchanged.

- [ ] **Step 4: Symmetric edit in `run-inferential.go`**

Same pattern: `cwd, _ := os.Getwd()` → look up via `registry.Lookup(cwd)` → pass `projectRoot` to `run()`.

- [ ] **Step 5: Run tests**

```bash
go test -tags=run_computational ./skills/run-sensor/scripts/ -v
go test -tags=run_inferential ./skills/run-sensor/scripts/ -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add skills/run-sensor/scripts/run-computational.go skills/run-sensor/scripts/run-inferential.go skills/run-sensor/scripts/run-computational_test.go
git commit -m "$(cat <<'EOF'
feat(run-sensor): resolve project root via lib/registry.Lookup

main() now consults HARNESS_REGISTRY_ROOT (via registry.Lookup) before
falling back to cwd. With the new go-run-C invocation contract, cwd
inside the runner is the plugin root, not the user's project — so the
env var is the authoritative source.
EOF
)"
```

---

### Task 3.2: Thread `projectRoot` into subprocess `Dir` inside the orchestrator

**Files:**
- Modify: `lib/orchestrator/lifecycle.go`
- Modify: `lib/orchestrator/live_deps.go`
- Test: `lib/orchestrator/lifecycle_test.go` (or add new file)

- [ ] **Step 1: Write the failing test**

Add to a new file `lib/orchestrator/cwd_test.go`:

```go
package orchestrator_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
)

func TestRunWithDepsRoot_SubprocessCwdIsProjectRoot(t *testing.T) {
	proj := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj, "sensors"), 0o755)

	// Sensor that writes its cwd to a file under projectRoot.
	body := `{
		"id": "cwd-probe", "version": "1.0.0", "name": "Probe",
		"description": "probe cwd", "determinism": "high",
		"kind": "observation", "type": "computational",
		"regulation": "behaviour", "output": "single",
		"cost": {"compute": "small"},
		"execution": {
			"command": "pwd > $HARNESS_REGISTRY_ROOT/probe.out",
			"exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]
		}
	}`
	_ = os.WriteFile(filepath.Join(proj, "sensors", "cwd-probe.json"), []byte(body), 0o644)

	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	// Run from an unrelated cwd to prove the runner's cwd doesn't leak.
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "cwd-probe", proj, "", &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}

	got, _ := os.ReadFile(filepath.Join(proj, "probe.out"))
	want, _ := filepath.EvalSymlinks(proj)
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if gotResolved != want {
		t.Errorf("subprocess cwd = %q, want %q", gotResolved, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./lib/orchestrator/ -run TestRunWithDepsRoot_SubprocessCwdIsProjectRoot -v
```

Expected: FAIL — `orchestrator` does not yet pass `Dir` to `subprocess`.

- [ ] **Step 3: Pass `projectRoot` to subprocess configs**

In `lib/orchestrator/lifecycle.go`, find every construction of `subprocess.StreamConfig`, `subprocess.StepConfig`, or `subprocess.DetachConfig`. Each call site has `projectRoot` in scope (`RunOneWithRoot` takes it as a parameter). Add `Dir: projectRoot` to every struct literal.

In `lib/orchestrator/live_deps.go::startBlockingDep` (around line 202), change:

```go
det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
    Command: command,
    LogFile: r.RawLog(dep.ID),
    Dir:     r.ProjectRoot(),  // or whatever yields the project root
})
```

The `r` is `registry.Root`. Add a `ProjectRoot()` method if absent, OR thread `projectRoot` as a parameter to `startBlockingDep` (preferred — explicit). Pick threading; modify the function signature:

```go
func startBlockingDep(rs *registry.RunningSensors, r registry.Root, projectRoot string, dep Sensor, holder registry.HeldByEntry) (string, error) {
    // ...
    det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
        Command: command,
        LogFile: r.RawLog(dep.ID),
        Dir:     projectRoot,
    })
    // ...
}
```

Update the caller (`AttachLiveDep` and any siblings that call `startBlockingDep`) to pass `projectRoot`. Trace the chain up.

- [ ] **Step 4: Run tests**

```bash
go test ./lib/orchestrator/ -v
go test ./lib/subprocess/ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/orchestrator/lifecycle.go lib/orchestrator/live_deps.go lib/orchestrator/cwd_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): pass projectRoot as subprocess Dir

The runner is about to be invoked via `go -C "${CLAUDE_PLUGIN_ROOT}"`,
which chdirs into the plugin checkout. Sensor commands must still run
from the user's project root, so every subprocess.{Detach,Step,Stream}Config
gets an explicit Dir. preserves cwd parity end-to-end.
EOF
)"
```

---

### Task 3.3: Update `heal-sensor/retry-original.go` invocation + delete `repoRoot`

**Files:**
- Modify: `skills/heal-sensor/scripts/retry-original.go`
- Test: `skills/heal-sensor/scripts/retry-original_test.go`

- [ ] **Step 1: Write the failing test**

Add to `retry-original_test.go`:

```go
func TestRetryOriginal_UsesPluginRootAndContract(t *testing.T) {
	// Mock the `go` binary so the test can inspect args.
	tmpDir := t.TempDir()
	argsFile := filepath.Join(tmpDir, "args.txt")
	envFile := filepath.Join(tmpDir, "env.txt")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\x1f' "$@" > %q
env > %q
exit 0
`, argsFile, envFile)
	goBin := filepath.Join(tmpDir, "go")
	_ = os.WriteFile(goBin, []byte(script), 0o755)
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/fake/plugin/root")

	// Minimal valid sensor JSON.
	sensorPath := filepath.Join(t.TempDir(), "s.json")
	_ = os.WriteFile(sensorPath, []byte(`{"type":"computational"}`), 0o644)

	var stdout, stderr bytes.Buffer
	exit := run([]string{"--sensor=" + sensorPath}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}

	args, _ := os.ReadFile(argsFile)
	got := strings.Split(strings.TrimRight(string(args), "\x1f"), "\x1f")
	want := []string{"-C", "/fake/plugin/root", "run", "-tags=run_computational", "./skills/run-sensor/scripts", sensorPath}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %#v, want %#v", got, want)
	}

	env, _ := os.ReadFile(envFile)
	if !strings.Contains(string(env), "GOWORK=off") {
		t.Errorf("env missing GOWORK=off: %s", env)
	}
}
```

Add imports: `"bytes"`, `"fmt"`, `"path/filepath"`, `"reflect"`, `"strings"`.

- [ ] **Step 2: Run test to verify it fails**

```bash
go test -tags=heal_retry_original ./skills/heal-sensor/scripts/ -run TestRetryOriginal_UsesPluginRootAndContract -v
```

Expected: FAIL — `retry-original.go` does not yet use `-C "${CLAUDE_PLUGIN_ROOT}"`.

- [ ] **Step 3: Rewrite `run()` in `retry-original.go`**

Replace the body of `run()` from after the `tag := "run_computational"` block to before the `return 0`:

```go
	pluginRoot := os.Getenv("CLAUDE_PLUGIN_ROOT")
	if pluginRoot == "" {
		fmt.Fprintln(stderr, "error: CLAUDE_PLUGIN_ROOT not set")
		return 2
	}

	cmd := exec.Command("go", "-C", pluginRoot, "run", "-tags="+tag, "./skills/run-sensor/scripts", sensorPath)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "exec:", err)
		return 2
	}
	return 0
```

Also delete the entire `repoRoot()` function (lines 74–99 of the original file).

The final `retry-original.go` becomes ~70 LOC.

- [ ] **Step 4: Run all heal-sensor tests**

```bash
go test -tags=heal_retry_original ./skills/heal-sensor/scripts/ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/heal-sensor/scripts/retry-original.go skills/heal-sensor/scripts/retry-original_test.go
git commit -m "$(cat <<'EOF'
feat(heal-sensor): adopt go -C ... run invocation in retry-original

The internal exec.Command for the runner's retry path now uses
-C "${CLAUDE_PLUGIN_ROOT}" + GOWORK=off, same contract as SKILL.md.
repoRoot() walk-up helper deleted — the -C flag makes cmd.Dir
unnecessary, and the env var is the source of truth for the plugin
checkout location.
EOF
)"
```

---

### Task 3.4: Update all seven `SKILL.md` files

**Files:**
- Modify: `skills/run-sensor/SKILL.md`
- Modify: `skills/start-sensor/SKILL.md`
- Modify: `skills/stop-sensor/SKILL.md`
- Modify: `skills/tail-sensor/SKILL.md`
- Modify: `skills/list-sensors/SKILL.md`
- Modify: `skills/detect-sensors/SKILL.md`
- Modify: `skills/heal-sensor/SKILL.md`

- [ ] **Step 1: Update `skills/run-sensor/SKILL.md`**

Replace line 68's command block:

Old:
```bash
go run -tags=run_computational ./skills/run-sensor/scripts <SENSOR_ID>
```

New:
```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts <SENSOR_ID>
```

Apply the same transformation to line 78's `run_inferential` block.

After section "### 2a. `computational`" but before section "### 2b. `inferential`", insert a new subsection:

```markdown
### About the invocation contract

Every script the framework ships runs through the same three knobs:

- `-C "${CLAUDE_PLUGIN_ROOT}"` chdirs `go` itself to the plugin's checkout so the user's `go.mod` or `go.work` cannot interfere with module resolution.
- `HARNESS_REGISTRY_ROOT="$(pwd)"` captures the agent's cwd as the project root before `-C` moves `go`. The runner uses this to resolve `sensors/<id>.json` and to set the subprocess's working directory.
- `GOWORK=off` neutralizes any `go.work` in the user's tree.

`${CLAUDE_PLUGIN_ROOT}` is exposed by Claude Code to plugin-originated commands; if it is empty the runner emits `verdict=error metadata.cause=plugin_root_missing`.
```

- [ ] **Step 2: Update `skills/start-sensor/SKILL.md`**

Replace line 21:

Old:
```bash
go run -tags=start_sensor ./skills/start-sensor/scripts <sensor.id>
```

New:
```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=start_sensor \
  ./skills/start-sensor/scripts <sensor.id>
```

In the "Output contract" section, append to the `failed` cause list:

```
- `plugin_root_missing` — `CLAUDE_PLUGIN_ROOT` was empty when `/start-sensor` ran.
```

In "Notes & limits", append:

```
- The watcher subprocess is compiled on demand via `go run`. First `/start-sensor` after a fresh checkout incurs ~300ms–1s for the compile; subsequent calls hit Go's build cache and cost ~50–200ms.
```

- [ ] **Step 3: Update `skills/stop-sensor/SKILL.md`** (line 19)

Old:
```bash
go run -tags=stop_sensor ./skills/stop-sensor/scripts <sensor.id> [--reap-dead-holders]
```

New:
```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=stop_sensor \
  ./skills/stop-sensor/scripts <sensor.id> [--reap-dead-holders]
```

- [ ] **Step 4: Update `skills/tail-sensor/SKILL.md`** (line 22)

Old:
```bash
go run -tags=tail_sensor ./skills/tail-sensor/scripts <sensor.id> <cursor>
```

New:
```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=tail_sensor \
  ./skills/tail-sensor/scripts <sensor.id> <cursor>
```

- [ ] **Step 5: Update `skills/list-sensors/SKILL.md`** (line 19)

Old:
```bash
go run -tags=list_sensors ./skills/list-sensors/scripts
```

New:
```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=list_sensors \
  ./skills/list-sensors/scripts
```

- [ ] **Step 6: Update `skills/detect-sensors/SKILL.md`** (lines 245, 282, 290)

Each affected `go run` line transforms the same way: prefix env, add `-C "${CLAUDE_PLUGIN_ROOT}"`.

- [ ] **Step 7: Update `skills/heal-sensor/SKILL.md`** (lines 25, 73, 92, 102)

Same transformation. For line 102 specifically (`go run ./skills/heal-sensor/scripts/retry-original.go`), become:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=heal_retry_original \
  ./skills/heal-sensor/scripts --sensor=<sensor-path>
```

(Note: line 102 today omits the build tag — verify when editing; the actual file may use `./skills/heal-sensor/scripts/retry-original.go` as a single-file invocation. If so, use the form above which keeps the tag explicit.)

- [ ] **Step 8: Verify by grep**

```bash
grep -rn "go run -tags" skills/
```

Expected: zero matches — every line has been migrated to the new contract.

```bash
grep -rn "go run -C" skills/
```

Expected: one or more matches per skill.

- [ ] **Step 9: Commit**

```bash
git add skills/run-sensor/SKILL.md skills/start-sensor/SKILL.md skills/stop-sensor/SKILL.md skills/tail-sensor/SKILL.md skills/list-sensors/SKILL.md skills/detect-sensors/SKILL.md skills/heal-sensor/SKILL.md
git commit -m "$(cat <<'EOF'
feat(skills): adopt go run -C invocation contract in all SKILL.md

Every entrypoint the agent invokes now uses the unified contract:

  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
    go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> \
    ./skills/<name>/scripts <args>

Insulates the framework from the user's go.mod/go.work and removes the
requirement that pre-built binaries live anywhere in the user's tree.
run-sensor/SKILL.md is the canonical explanation; others link to it.
start-sensor/SKILL.md additionally documents plugin_root_missing and
the watcher compile-on-demand latency.
EOF
)"
```

---

### Task 3.5: Update internal direct-build tests for the new contract

**Files:**
- Modify: `skills/run-sensor/scripts/run-computational_test.go` (line 163, `exec.Command("go", "build", ...)`)
- Modify: `skills/run-sensor/scripts/run-inferential_test.go` (line 347)

- [ ] **Step 1: Inspect the existing tests**

Read both tests. They currently build the runner binary directly (`go build`) and then `exec.Command(bin, ...)` it. These tests do NOT depend on the invocation contract; they test the runner's own behavior with arbitrary working directories.

Decision: keep them as-is for now. They cover an orthogonal axis (the runner's behavior when called as a built binary, not via `go run`). The contract change does not break them.

- [ ] **Step 2: Run them to verify**

```bash
go test -tags=run_computational ./skills/run-sensor/scripts/ -v
go test -tags=run_inferential ./skills/run-sensor/scripts/ -v
```

Expected: all PASS unchanged.

- [ ] **Step 3: No commit needed (no changes).**

If any test failed, the failure is unrelated to the contract change; debug and fix as a separate task.

---

## Phase 4 — Autofiler regex extension (commit 4)

**Goal of phase:** `hooks/error-issue-autofiler.go::buildFrameworkCommandPatterns` recognizes the new `go -C <path> run …` shape so crashes in agent-invoked framework scripts are still fingerprinted and filed.

### Task 4.1: Extend the four regexes

**Files:**
- Modify: `hooks/error-issue-autofiler.go:63-75`
- Test: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 1: Write the failing test**

Add to `hooks/error-issue-autofiler_test.go`:

```go
func TestCommandTouchesFramework_GoRunWithC(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{
			name: "go -C run -tags scripts",
			cmd:  `go -C /Users/x/.claude/plugins/harness-framework run -tags=start_sensor ./skills/start-sensor/scripts foo`,
			want: true,
		},
		{
			name: "go -C run hooks",
			cmd:  `go -C ${CLAUDE_PLUGIN_ROOT} run -tags=error_autofiler ./hooks`,
			want: true,
		},
		{
			name: "go -C test",
			cmd:  `go -C /plugin/root test -tags=start_sensor ./skills/start-sensor/...`,
			want: true,
		},
		{
			name: "legacy go run still matches",
			cmd:  `go run -tags=run_computational ./skills/run-sensor/scripts foo`,
			want: true,
		},
		{
			name: "legacy harness-watcher still matches",
			cmd:  `harness-watcher`,
			want: true,
		},
		{
			name: "unrelated go command",
			cmd:  `go build ./...`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := commandTouchesFramework(tc.cmd)
			if got != tc.want {
				t.Errorf("commandTouchesFramework(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify failures**

```bash
go test -tags=error_autofiler ./hooks/ -run TestCommandTouchesFramework_GoRunWithC -v
```

Expected: the four "go -C ..." cases FAIL.

- [ ] **Step 3: Insert the `-C <path>` optional group into the regexes**

In `hooks/error-issue-autofiler.go`, replace `buildFrameworkCommandPatterns`:

```go
func buildFrameworkCommandPatterns() []*regexp.Regexp {
	skills := strings.Join(sensorSkills, "|")
	// optional `-C <path>` between `go` and the verb
	chDir := `(?:-C\s+\S+\s+)?`
	return []*regexp.Regexp{
		// go [-C <path>] run direct from the scripts directory
		regexp.MustCompile(`go\s+` + chDir + `run\s+(?:-tags=\S+\s+)?\./skills/(?:` + skills + `)-sensors?/scripts\b`),
		// go [-C <path>] run from hooks
		regexp.MustCompile(`go\s+` + chDir + `run\s+(?:-tags=\S+\s+)?\./hooks\b`),
		// installed binaries on PATH
		regexp.MustCompile(`\bharness-(?:(?:` + skills + `)-sensors?|watcher)\b`),
		// go [-C <path>] test/vet/build of the framework's own packages
		regexp.MustCompile(`go\s+` + chDir + `(?:test|vet|build)\s+(?:-tags=\S+\s+)?\./(?:skills|lib|hooks)\b`),
	}
}
```

Also update `extractSkill`'s fallback regexes (around line 105) the same way:

```go
// Fallback: hooks
if regexp.MustCompile(`go\s+(?:-C\s+\S+\s+)?run\s+(?:-tags=\S+\s+)?\./hooks\b`).MatchString(cmd) {
    return "hook"
}
// Fallback: go test/vet/build
if regexp.MustCompile(`go\s+(?:-C\s+\S+\s+)?(?:test|vet|build)\b`).MatchString(cmd) {
    return "test"
}
```

The `skillExtractRe` (line 91) captures the skill name from `skills/<name>/...` — that capture works regardless of the `-C` prefix because it operates on the substring after `./skills/`. No change.

- [ ] **Step 4: Run tests**

```bash
go test -tags=error_autofiler ./hooks/ -v
```

Expected: all PASS, including the new and existing cases.

- [ ] **Step 5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git commit -m "$(cat <<'EOF'
feat(autofiler): match go -C <path> run invocations

The new invocation contract inserts an optional `-C <path>` between
`go` and `run`. All four framework-detection regexes (run-scripts,
run-hooks, installed-binaries, test/vet/build) gain an optional
(?:-C\s+\S+\s+)? group. Legacy `go run -tags=... ./skills/...` and
`harness-<skill>` still match.
EOF
)"
```

---

## Phase 5 — Docs, README, CHANGELOG, version bump (commit 5)

**Goal of phase:** `CLAUDE.md` "Build, validate, test" section reflects the new contract; `README.md` exists with a quick-start; `CHANGELOG.md` records v1.1.0; `plugin.json` version bumps.

### Task 5.1: Update `CLAUDE.md` "Build, validate, test" section

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Locate the section**

Find the heading `## Build, validate, test` (line 134 of current `CLAUDE.md`).

- [ ] **Step 2: Replace the body**

Replace the content under that heading (until the next `##`) with:

```markdown
Single Go module at the repo root: `module github.com/iurykrieger/harness-framework` (Go 1.25). Per-skill modules only if a skill needs an isolated dependency graph.

The plugin **does not ship pre-built binaries**. Every script — runners, registry skills, hooks, and the watcher — is invoked via `go run`. The canonical invocation contract is:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> \
  ./skills/<name>/scripts <args>
```

Three pieces, each load-bearing:

- `-C "${CLAUDE_PLUGIN_ROOT}"` chdirs the `go` process itself to the plugin checkout before any module resolution. The user's `go.mod`/`go.work` cannot interfere.
- `HARNESS_REGISTRY_ROOT="$(pwd)"` captures the agent's cwd (the user's project root) before `-C` moves `go`. Every registry-touching skill consults this via `lib/registry.Lookup`; the runner threads it into subprocess `Dir` so sensor commands keep running from the project root.
- `GOWORK=off` neutralizes any `go.work` in the user's tree.

`${CLAUDE_PLUGIN_ROOT}` is exposed by Claude Code to plugin-originated commands. Scripts emit `verdict=error metadata.cause=plugin_root_missing` if it is empty.

### Local verification

```bash
go test ./lib/...                                     # the shared library
go test -tags=run_computational ./skills/...          # the computational runner
go test -tags=run_inferential   ./skills/...          # the inferential runner
go test -tags=start_sensor      ./skills/...          # the start-sensor runner
go test -tags=stop_sensor       ./skills/...
go test -tags=list_sensors      ./skills/...
go test -tags=tail_sensor       ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go vet -tags=run_computational  ./...
go vet -tags=run_inferential    ./...

# Run a sensor end-to-end (from the user's project, with CLAUDE_PLUGIN_ROOT set):
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts <sensor-id>
```

### Watcher latency

`/start-sensor` spawns the watcher via `go run`, which means the watcher is compiled on every invocation. With a warm Go build cache, the compile-link step costs ~150–500ms; with a cold cache (fresh plugin install or after `go clean -cache`), it costs ~600ms–1s. The cost is paid once per `/start-sensor`; subsequent reads via `/tail-sensor` and `/list-sensors` are unaffected.

### Requirements

- Go 1.20+ (the `-C` flag arrived in 1.20; `go.mod` pins `go 1.25`).
- `CLAUDE_PLUGIN_ROOT` exposed by Claude Code.
- `go` on PATH (Claude Code's `go` toolchain is the default).
```

- [ ] **Step 3: Verify the section is internally consistent**

Scan the rest of `CLAUDE.md` for any remaining `go run -tags` references that don't show the new contract. Update them to match.

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs(claude.md): rewrite Build/validate/test for new invocation contract

The canonical command pattern, the three load-bearing flags/envs, the
watcher compile-on-demand latency, and the Go version + CLAUDE_PLUGIN_ROOT
requirements are now documented in one place. Test invocations use the
new tag-per-skill layout.
EOF
)"
```

---

### Task 5.2: Populate `README.md`

**Files:**
- Modify: `README.md` (currently empty)

- [ ] **Step 1: Write the content**

Replace `README.md` content with:

```markdown
# harness-framework

A Claude Code plugin that implements a **sensor harness** for AI coding agents. Sensors observe the system after the agent acts and emit Signals optimized for self-correction.

## Requirements

- [Claude Code](https://claude.com/claude-code) with plugin support
- Go 1.20+ on PATH (Claude Code's bundled toolchain works)

No binaries are shipped or built. Scripts run on demand via `go run`.

## Installation

Install through Claude Code's plugin manager (see Claude Code docs). The plugin lives in a directory the user does not need to touch.

## Quick start

Inside any project where you want a harness:

```bash
# Create your first sensor (auto-detects archetype):
/detect-sensors

# Run it:
/run-sensor <sensor-id>

# For blocking sensors (long-running processes):
/start-sensor <sensor-id>
/tail-sensor <sensor-id> 0
/stop-sensor <sensor-id>
```

All commands resolve sensors as `sensors/<id>.json` under the user's project root.

## Architecture

See [`CLAUDE.md`](./CLAUDE.md) for the full architecture, schema overview, and project rules. The two schemas (`schemas/sensor.json` and `schemas/signal.json`) are the source of truth for sensor definitions and signal output.

## Invocation contract

The plugin's skills invoke scripts via:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> \
  ./skills/<name>/scripts <args>
```

The `-C` flag isolates the plugin's Go module from your project's `go.mod`/`go.work`. The env vars preserve the project root for sensor discovery and subprocess cwd. See `CLAUDE.md` for the full explanation.

## License

MIT — see [`LICENSE`](./LICENSE).
```

- [ ] **Step 2: Verify `LICENSE` exists**

```bash
ls LICENSE 2>/dev/null || echo "MISSING — note in CHANGELOG"
```

If missing, that's a separate task; don't block here. The README's link will 404 until a LICENSE is added (decision out of scope for this plan; the spec says MIT in plugin.json).

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs(readme): populate with quick-start and contract overview
EOF
)"
```

---

### Task 5.3: Bump version and add CHANGELOG entry

**Files:**
- Modify: `.claude-plugin/plugin.json`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Bump version**

In `.claude-plugin/plugin.json`, change `"version": "1.0.0"` to `"version": "1.1.0"`.

- [ ] **Step 2: Add CHANGELOG entry**

Prepend to `CHANGELOG.md` (read the existing file first to match its format):

```markdown
## 1.1.0 — 2026-05-12

### Changed (breaking-ish)

- **Invocation contract overhaul.** All skills and internal `exec.Command` chains now use:
  ```
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
    go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> \
    ./skills/<name>/scripts <args>
  ```
  The change is invisible to slash-command users. Anyone who copy-pasted `SKILL.md` commands into scripts or CI must update them. Closes #15.
- **Watcher is no longer a pre-built sibling binary.** `/start-sensor` spawns the watcher via `go run`, compiling on demand. Adds ~150ms–1s latency to the first `/start-sensor` after a fresh checkout; subsequent calls hit Go's build cache.

### Removed

- `lib/watcher.BinaryPath`, `skills/start-sensor/scripts/start_unix.go::watcherBinaryPath`, and `skills/heal-sensor/scripts/retry-original.go::repoRoot` are deleted as no longer needed.

### Added

- `lib/watcher.SpawnFn` injection point for test substitution.
- `lib/subprocess.{Detach,Step,Stream}Config.Dir` field so the runner can keep sensor commands at the project root after `-C` moves the runner itself to the plugin root.
- `metadata.cause=plugin_root_missing` for `failed` Signals emitted by `/start-sensor` when `CLAUDE_PLUGIN_ROOT` is empty.
- Autofiler regex now matches `go -C <path> run …` invocations.
```

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin/plugin.json CHANGELOG.md
git commit -m "$(cat <<'EOF'
chore(release): bump to 1.1.0

Records the invocation-contract change and the watcher spawn rewrite
as the headline items for this release.
EOF
)"
```

---

## Phase 6 — E2E tests for the new contract

**Goal of phase:** Three end-to-end tests prove the new contract isolates the framework from user-project Go module pollution.

### Task 6.1: `TestPluginVsProjectGoMod`

**Files:**
- Modify: `test/registry-discovery-e2e/registry_discovery_e2e_test.go`

- [ ] **Step 1: Inspect the existing test file**

```bash
head -80 test/registry-discovery-e2e/registry_discovery_e2e_test.go
```

Understand the existing helpers (`setUpProject`, `runSkill`, etc.).

- [ ] **Step 2: Add the test**

Append:

```go
func TestPluginVsProjectGoMod(t *testing.T) {
	pluginRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Walk up until we hit the plugin root (where .claude-plugin/plugin.json lives).
	for {
		if _, err := os.Stat(filepath.Join(pluginRoot, ".claude-plugin", "plugin.json")); err == nil {
			break
		}
		parent := filepath.Dir(pluginRoot)
		if parent == pluginRoot {
			t.Fatal("could not find plugin root")
		}
		pluginRoot = parent
	}

	// Build a user project with its own go.mod listing an unrelated module.
	proj := t.TempDir()
	_ = os.WriteFile(filepath.Join(proj, "go.mod"), []byte("module example.com/userapp\n\ngo 1.25\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(proj, "sensors"), 0o755)

	cmd := exec.Command("go", "run", "-C", pluginRoot, "-tags=list_sensors", "./skills/list-sensors/scripts")
	cmd.Dir = proj
	cmd.Env = append(os.Environ(),
		"HARNESS_REGISTRY_ROOT="+proj,
		"GOWORK=off",
		"CLAUDE_PLUGIN_ROOT="+pluginRoot,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run failed: %v\nstderr: %s", err, stderr.String())
	}

	// Expect a Signal with verdict=warn (no registry file) and metadata.registry_path under proj.
	var sig map[string]interface{}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	last := lines[len(lines)-1]
	if err := json.Unmarshal([]byte(last), &sig); err != nil {
		t.Fatalf("parse signal: %v\nlast line: %s", err, last)
	}
	if sig["verdict"] != "warn" {
		t.Errorf("verdict = %v, want warn", sig["verdict"])
	}
	meta, _ := sig["metadata"].(map[string]interface{})
	regPath, _ := meta["registry_path"].(string)
	wantPrefix, _ := filepath.EvalSymlinks(proj)
	gotResolved, _ := filepath.EvalSymlinks(regPath)
	if !strings.HasPrefix(gotResolved, wantPrefix) {
		t.Errorf("registry_path = %q, want prefix %q", gotResolved, wantPrefix)
	}
}
```

Add imports as needed: `"bytes"`, `"encoding/json"`, `"os/exec"`, `"path/filepath"`, `"strings"`.

- [ ] **Step 3: Run the test**

```bash
go test ./test/registry-discovery-e2e/ -run TestPluginVsProjectGoMod -v
```

Expected: PASS. If FAIL with `go: cannot find module`, the `-C` flag is not working — investigate.

- [ ] **Step 4: Commit**

```bash
git add test/registry-discovery-e2e/registry_discovery_e2e_test.go
git commit -m "$(cat <<'EOF'
test(e2e): prove -C neutralizes user-project go.mod

A temp project with its own go.mod is the antagonist; the test runs
/list-sensors from that project's cwd with CLAUDE_PLUGIN_ROOT pointing
at the harness checkout. The Signal's verdict=warn and the
metadata.registry_path lives under the project, proving both -C and
HARNESS_REGISTRY_ROOT do their jobs.
EOF
)"
```

---

### Task 6.2: `TestGoWorkPollution`

**Files:**
- Modify: `test/registry-discovery-e2e/registry_discovery_e2e_test.go`

- [ ] **Step 1: Add the test**

Append:

```go
func TestGoWorkPollution(t *testing.T) {
	pluginRoot := findPluginRoot(t)
	proj := t.TempDir()
	_ = os.WriteFile(filepath.Join(proj, "go.mod"), []byte("module example.com/userapp\n\ngo 1.25\n"), 0o644)
	_ = os.WriteFile(filepath.Join(proj, "go.work"), []byte("go 1.25\n\nuse .\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(proj, "sensors"), 0o755)

	cmd := exec.Command("go", "run", "-C", pluginRoot, "-tags=list_sensors", "./skills/list-sensors/scripts")
	cmd.Dir = proj
	cmd.Env = append(os.Environ(),
		"HARNESS_REGISTRY_ROOT="+proj,
		"GOWORK=off",
		"CLAUDE_PLUGIN_ROOT="+pluginRoot,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run failed: %v\nstderr: %s", err, stderr.String())
	}

	// As long as we got a parseable signal with verdict=warn, the contract held.
	last := lastLine(stdout.String())
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(last), &sig); err != nil {
		t.Fatalf("parse signal: %v\nlast: %s", err, last)
	}
	if sig["verdict"] != "warn" {
		t.Errorf("verdict = %v, want warn", sig["verdict"])
	}
}

// findPluginRoot walks up from CWD until .claude-plugin/plugin.json is found.
func findPluginRoot(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".claude-plugin", "plugin.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("plugin root not found")
		}
		dir = parent
	}
}

// lastLine returns the final newline-terminated line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
```

(Refactor `TestPluginVsProjectGoMod` to use `findPluginRoot` and `lastLine` too — DRY.)

- [ ] **Step 2: Run and commit**

```bash
go test ./test/registry-discovery-e2e/ -run TestGoWorkPollution -v
git add test/registry-discovery-e2e/registry_discovery_e2e_test.go
git commit -m "test(e2e): prove GOWORK=off defeats user go.work pollution"
```

---

### Task 6.3: `TestSensorCwd` — runner's subprocess runs at project root

**Files:**
- Modify: `test/registry-discovery-e2e/registry_discovery_e2e_test.go`

- [ ] **Step 1: Add the test**

Append:

```go
func TestSensorCwd(t *testing.T) {
	pluginRoot := findPluginRoot(t)
	proj := t.TempDir()
	_ = os.MkdirAll(filepath.Join(proj, "sensors"), 0o755)
	// Sentinel file in the project root only.
	_ = os.WriteFile(filepath.Join(proj, "SENTINEL"), []byte("project-root-confirmed\n"), 0o644)

	// Sensor whose command echoes the sentinel content.
	body := `{
		"id": "cwd-probe", "version": "1.0.0", "name": "Probe",
		"description": "probe cwd", "determinism": "high",
		"kind": "observation", "type": "computational",
		"regulation": "behaviour", "output": "single",
		"cost": {"compute": "small"},
		"execution": {
			"command": "cat SENTINEL",
			"exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]
		}
	}`
	_ = os.WriteFile(filepath.Join(proj, "sensors", "cwd-probe.json"), []byte(body), 0o644)

	cmd := exec.Command("go", "run", "-C", pluginRoot, "-tags=run_computational", "./skills/run-sensor/scripts", "cwd-probe")
	cmd.Dir = proj
	cmd.Env = append(os.Environ(),
		"HARNESS_REGISTRY_ROOT="+proj,
		"GOWORK=off",
		"CLAUDE_PLUGIN_ROOT="+pluginRoot,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run failed: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}

	last := lastLine(stdout.String())
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(last), &sig); err != nil {
		t.Fatalf("parse signal: %v\nlast: %s", err, last)
	}
	if sig["verdict"] != "pass" {
		t.Errorf("verdict = %v, want pass\nfull signal: %s", sig["verdict"], last)
	}
}
```

- [ ] **Step 2: Run and commit**

```bash
go test ./test/registry-discovery-e2e/ -run TestSensorCwd -v
```

Expected: PASS — `cat SENTINEL` succeeds because the runner sets `subprocess.Dir = projectRoot`.

```bash
git add test/registry-discovery-e2e/registry_discovery_e2e_test.go
git commit -m "test(e2e): sensor command runs at project root, not plugin root"
```

---

## Final verification

After all phases are committed, run the complete suite to confirm zero regressions:

- [ ] **Run all unit and integration tests**

```bash
go test ./lib/... ./hooks/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go test -tags=start_sensor ./skills/...
go test -tags=stop_sensor ./skills/...
go test -tags=list_sensors ./skills/...
go test -tags=tail_sensor ./skills/...
go test -tags=heal_diagnose ./skills/heal-sensor/...
go test -tags=heal_apply_safe ./skills/heal-sensor/...
go test -tags=heal_apply_sensors ./skills/heal-sensor/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go test -tags=error_autofiler ./hooks/...
go test ./test/...
```

Expected: every test passes.

- [ ] **Run vet on every tag**

```bash
go vet ./lib/... ./hooks/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
go vet -tags=start_sensor ./...
go vet -tags=start_watcher ./...
go vet -tags=stop_sensor ./...
go vet -tags=list_sensors ./...
go vet -tags=tail_sensor ./...
```

Expected: zero output (no findings).

- [ ] **Confirm zero references to deleted symbols**

```bash
grep -rn "watcherBinaryPath\|BinaryPath()\|repoRoot()" --include="*.go" .
```

Expected: zero matches.

- [ ] **Confirm zero pre-`-C` invocation strings remain in SKILL.md**

```bash
grep -rn "^go run -tags\|^\s*go run -tags" skills/
```

Expected: zero matches.

- [ ] **Smoke test in a fresh project (manual)**

In a temp directory with a real `go.mod` (e.g. a sample Node app — though Node doesn't have `go.mod`, just pick any non-Go project for a real test, like a fresh `mkdir foo && cd foo && touch package.json`):

1. Export `CLAUDE_PLUGIN_ROOT=<path to harness checkout>`.
2. Create `sensors/smoke.json` with a trivial computational sensor (`true` command).
3. Invoke `HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational ./skills/run-sensor/scripts smoke`.
4. Confirm a Signal with `verdict=pass` is printed.

- [ ] **Close GitHub issue #15**

```bash
gh issue close 15 --comment "Resolved by 519fd90 (and the rest of the binary-compiling branch). The watcher is no longer a sibling binary; \`go run\` from the plugin root via \`-C\` handles everything."
```

(Replace `519fd90` with the actual hash of the commit that deleted `BinaryPath()`.)

---

## Self-review checklist results

**Spec coverage:** ✓
- Section "Why" / Issue #15: addressed by Phase 2 (lib/watcher rewrite).
- Section "What changes" items 1–8: addressed by Phases 1–5 in order.
- Section "Architecture → process tree": validated implicitly via Phase 6 E2E.
- Section "Edge cases" rows: each has a corresponding test in Phases 1/2/3/4.
- Section "Testing strategy": Phases 1/2 unit; Phase 6 E2E; integration_realgo opt-in suite **deferred** (optional; can be added in a follow-up if smoke runs surface issues).
- Section "Rollout" 5 commits: this plan adds Phase 6 (E2E) to the spec's 5 commits, giving 6 logical phases / ~14 atomic commits.

**Placeholder scan:** ✓ — every code step has actual code; every test has actual assertions; no TBD/TODO.

**Type consistency:** ✓
- `watcher.SpawnFn` (Task 1.2) is used unchanged in Tasks 1.5, 2.2.
- `watcher.SpawnOpts` (defined Task 1.2 / extended Task 2.2 with `PluginRoot`) is used unchanged in subsequent tasks.
- `subprocess.{Detach,Step,Stream}Config.Dir` (Task 1.4) is used unchanged in Tasks 3.2, 1.3 (yes — 1.3 references it; the plan flagged the ordering note).
- `lib/registry.Lookup` signature `(startDir) (Result, error)` is used consistently in Tasks 3.1.

---

## Ordering note

Task 1.4 (add `Dir` to subprocess configs) must execute **before** Task 1.3 (which references `subprocess.DetachConfig{… Dir: projectRoot}`). The plan is laid out in logical sequence for the reader; the implementer should reorder execution to `1.1 → 1.2 → 1.4 → 1.3 → 1.5 → 2.x → 3.x → 4.x → 5.x → 6.x`.

---

**Plan complete.**
