# Fix blocking-dep logs: unify layout and spawn watcher

Status: proposed
Date: 2026-05-14
Related: issue [#45](https://github.com/iurykrieger/harness-framework/issues/45), prior issues [#15](https://github.com/iurykrieger/harness-framework/issues/15) (watcher binary), [#9](https://github.com/iurykrieger/harness-framework/issues/9) (heal classification gap), `lib/orchestrator/live_deps.go`, `lib/registry/paths.go`, `lib/watcher/`, `skills/stop-sensor/`, `skills/list-sensors/`

## Why

Issue #45 reports two defects in the orchestrator path of `/run-sensor`:

1. **Layout divergence.** When a root sensor declares `requires[kind=sensor]` whose target is `execution.blocking: true`, the dep's `raw.log` and `signals.log` land at the flat path `.harness/runtime/<dep-id>/{raw,signals}.log`, while the root sensor — whether started by `/run-sensor` (via `lifecycle.go`) or `/start-sensor` (via `start.go`) — writes its files at the run-id-scoped path `.harness/runtime/<id>/<runID>/{raw,signals}.log`. The orchestrator path's `lib/orchestrator/live_deps.go::startBlockingDep` is the only spawn site still on the flat layout. The registry entry it writes is *already* run-id-scoped (`LogDir: r.RelativeRunDir(dep.ID, runID)` at line 272), so the on-disk truth contradicts the registry-recorded truth for blocking deps under orchestration.

2. **Watcher absent.** The same `startBlockingDep` deliberately omits the watcher subprocess — there is an explicit comment at line 235-238: *"No watcher process is spawned for orchestrator-managed deps — the dep runs unobserved (signals.log stays empty)."* The dep's `execution.output_parsing.patterns` (a required field for `output: stream` sensors) are dormant: every line written to `raw.log` is lost as a structured Signal. Reported in the wild from a Go/charge-api project where a Docker build failure (`go work sync` pointing to a non-existent module) was captured in `raw.log` but produced zero individual Signals because none of the six declared patterns was evaluated.

The combined impact: the dep's terminal aggregate Signal emitted by `stopBlockingDep` always shows `verdict=pass` (it has no individuals to fold in), the build failure is invisible to the orchestrator, downstream consumers (`/list-sensors`, `/tail-sensor`, `/stop-sensor`, `heal-sensor`'s classifier) cannot find the evidence they need, and the root sensor that depends on this dep is informed via cascade only that "the dep finished" — not that its build was broken. The user sees a meaningless `pass` from the dep followed by the root sensor failing for opaque reasons.

A related defect surfaces while triangulating issue #45: `skills/stop-sensor/scripts/stop.go:208` reads `r.SignalsLog(sensorID)` (flat) and `skills/list-sensors/scripts/list.go:66` emits `r.SignalsLog(e.SensorID)` (flat) — both consumers ignore the run-id-scoped layout that `/start-sensor` has been writing into since `2026-05-11-resolve-requires-and-persist-deps-logs-design.md`. So even where the watcher *is* spawned today (`/start-sensor`'s root sensor), `/stop-sensor` aggregates from an empty file and `/list-sensors` advertises an empty path. `/tail-sensor` is already correct (`tail.go:155-157` reads the run-id-scoped path with `LegacySignalsLog` fallback). This spec brings `/stop-sensor` and `/list-sensors` to the same surface so all consumers read where the writers write.

## What changes

1. **`lib/orchestrator/live_deps.go::startBlockingDep`** is refactored to the same staging/spawn/derive-runID/rename/watcher pattern proven in `skills/start-sensor/scripts/start.go:189-329`. The flat-layout `RawLog`/`SignalsLog` calls (current lines 245, 248, 252) are replaced with run-id-scoped `RawLogRun`/`SignalsLogRun` calls. The `lib/watcher.Spawn` helper is invoked unconditionally (with empty `PatternsJSON` when `output_parsing.patterns` is absent — paritetic with `start.go:270-275`), so every blocking dep is observed exactly like a `/start-sensor`-launched root sensor. The registry entry's `WatcherPID` field is populated with the spawned watcher's PID (today: hardcoded `0`).

2. **`lib/orchestrator/live_deps.go::stopBlockingDep`** is extended to terminate the registered `WatcherPID` before falling through to the aggregate Signal emission. Mirrors the `stopWatcher(entry.WatcherPID)` call already present in `skills/stop-sensor/scripts/stop.go:204`. Without this, watchers spawned by `startBlockingDep` survive every detach and accumulate as orphan processes across multiple `/run-sensor` invocations.

3. **`skills/stop-sensor/scripts/stop.go:208`** migrates `readSignals(r.SignalsLog(sensorID))` to `readSignals(r.SignalsLogRun(sensorID, entry.RunID))` with a `LegacySignalsLog(sensorID)` fallback when the run-id-scoped file does not exist. Adopts the same `os.Stat`-then-fallback idiom as `skills/tail-sensor/scripts/tail.go:155-157`. Entries without `RunID` (legacy state files) continue to work read-only.

4. **`skills/list-sensors/scripts/list.go:66`** migrates the `signals_log_path` field from `r.SignalsLog(e.SensorID)` to `r.SignalsLogRun(e.SensorID, e.RunID)` for entries with a non-empty `RunID`, falling back to `r.LegacySignalsLog(e.SensorID)` otherwise.

5. **No change to** `schemas/sensor.json`, `schemas/signal.json`, `schemas/stack.json`, `schemas/usecase.json`, the `RunningSensorEntry` Go struct, the on-disk shape of `running_sensors.json`, or the `lib/watcher` package. The `lib/registry::SensorDir/RawLog/SignalsLog/LegacyRawLog/LegacySignalsLog` helpers are preserved (the legacy ones become read-only fallbacks for pre-fix state files; `SensorDir` is still used as the run-id-scoped sensor parent directory).

6. **No change to** `skills/start-sensor/` (already uses the run-id-scoped layout and spawns the watcher), `skills/tail-sensor/` (already reads run-id-scoped with `LegacySignalsLog` fallback), or `skills/run-sensor/` (delegates to `lib/orchestrator` and to `lib/orchestrator/lifecycle.go`, which already uses run-id-scoped — see `lifecycle.go:343,373,508`).

7. **No change to** the synthetic `pass` aggregate emitted by `stopBlockingDep` (current lines 314-329). That defect is acknowledged in the issue body as "issue separada — ver issue separada" and stays out of this fix's scope. The new watcher will *populate* `signals.log` with individual Signals, but the orchestrator's aggregate-on-detach still ignores them. A follow-up issue should track folding `MaxStreamVerdict(individuals)` into the aggregate, mirroring the pattern in `skills/stop-sensor/scripts/stop.go:209-218`.

## Architecture

### Staging-rename pattern in `startBlockingDep`

The current `startBlockingDep` computes the dep's `runID` *after* the subprocess is spawned (line 257-262), because `runID` derives from the freshly minted `det.PID`. That ordering forces the staging dance: we cannot point `SpawnDetached`'s `LogFile` at `RunDir(dep.ID, runID)/raw.log` because we don't know `runID` yet.

`/start-sensor` solves this by (a) pre-creating a staging file at the flat `SensorDir(id)/raw.log` path, (b) spawning the subprocess against the staging file, (c) deriving `runID = <PID>-<short-UUID>`, (d) `mkdir RunDir(id, runID)`, (e) `os.Rename(stagingRaw, RawLogRun(id, runID))`. On POSIX filesystems, `os.Rename` is atomic and preserves the subprocess's open file descriptor — writes continue uninterrupted at the new path. The signals.log file is created empty in the run dir, and the watcher is spawned with the post-rename paths in its env vars.

`startBlockingDep` adopts the identical sequence. The differences from `start.go`:

- `startBlockingDep` already runs inside `AttachLiveDep`'s `WithFileLock` (live_deps.go:93). `start.go` enters its own flock at line 189. The orchestrator path keeps that outer lock; the staging-rename-watcher-save block executes within it. The flock hold time grows by the watcher spawn cost (~150 ms warm cache, up to ~1 s cold, per the README's "Watcher latency" note). That regression is identical in shape to `start.go`'s pre-existing pattern.
- `startBlockingDep` does not perform the placeholder-PID-rebind dance. `start.go` needs it because its holder is a detached subprocess whose PID is unknown when `AttachLiveDep` is called; `startBlockingDep`'s caller (`AttachLiveDep`, called recursively or by `RunDeps`) already passes a real holder PID via the `holder registry.HeldByEntry` parameter. No rebind step is required.
- `startBlockingDep`'s return value stays `(runID string, err error)` — the caller already threads `runID` into its `LiveDep` tracking. No change to the signature contract.

### Watcher invocation

`lib/watcher.Spawn(opts SpawnOpts) (int, error)` already exists and is exercised by `/start-sensor`. It launches `go -C <pluginRoot> run -tags=start_watcher ./skills/start-sensor/scripts` with the watcher's env vars and returns the spawned PID after a 100 ms early-death probe. The orchestrator path reuses this helper unchanged. The opts populated by `startBlockingDep`:

| Field | Source |
| --- | --- |
| `PluginRoot` | `os.Getenv("CLAUDE_PLUGIN_ROOT")` — must be non-empty |
| `ProjectRoot` | `projectRoot` parameter already in scope |
| `SensorID` | `dep.ID` |
| `RunID` | the composite `<PID>-<short-UUID>` derived earlier |
| `RawLogPath` | `r.RawLogRun(dep.ID, runID)` (post-rename target) |
| `SignalsLogPath` | `r.SignalsLogRun(dep.ID, runID)` |
| `EnvelopeJSON` | `json.Marshal(envelope)` where `envelope := libsensor.BuildEnvelope(dep.JSON); envelope.RunID = runID` |
| `PatternsJSON` | `json.Marshal(execMap["output_parsing"]["patterns"])` or `[]` if absent |
| `SubprocessPID` | `det.PID` |
| `WatcherLogPath` | `filepath.Join(runDir, "watcher.log")` |

`CLAUDE_PLUGIN_ROOT` is read via `os.Getenv` at the top of `startBlockingDep`. If empty, the function returns `fmt.Errorf("plugin root not set (set CLAUDE_PLUGIN_ROOT)")` before any subprocess work — same posture as `/start-sensor`'s line 61 check. The error propagates back through `AttachLiveDep` → `RunDeps`, where it is converted into the standard cascade-fail signal for dependents.

### Failure cleanup

Every intermediate failure inside the new `startBlockingDep` body cleans up exactly what was created up to that point. The full ladder:

| Step | On failure |
| --- | --- |
| Read `CLAUDE_PLUGIN_ROOT` | return error, no side effects |
| `os.MkdirAll(r.SensorDir(dep.ID))` | return error |
| `os.WriteFile(stagingRaw, nil, 0o644)` | return error |
| `subprocess.SpawnDetached(...)` | `os.Remove(stagingRaw)`; return error |
| `os.MkdirAll(r.RunDir(dep.ID, runID))` | `killGroup(det.PGID)` + `os.Remove(stagingRaw)`; return error |
| `os.Rename(stagingRaw, r.RawLogRun(...))` | `killGroup(det.PGID)` + `os.Remove(stagingRaw)` + `os.RemoveAll(runDir)`; return error |
| `os.WriteFile(r.SignalsLogRun(...))` | `killGroup(det.PGID)` + `os.RemoveAll(runDir)`; return error |
| `libsensor.BuildEnvelope(dep.JSON)` | `killGroup(det.PGID)` + `os.RemoveAll(runDir)`; return error |
| `watcher.Spawn(...)` | `killGroup(det.PGID)` + `os.RemoveAll(runDir)`; return error |
| `registry.Save(...)` | `killGroup(det.PGID)` + `killWatcher(watcherPID)` + `os.RemoveAll(runDir)`; return error |

`killGroup` and `killWatcher` are introduced as small local helpers (or reused from `start.go`/`stop.go` via a thin extraction into `lib/subprocess` if a follow-up wants to deduplicate — out of scope here; explicit duplication is acceptable per rule 7 of CLAUDE.md ("one script, one clear job") applied analogously to lib actions). On the `registry.Save` failure path the watcher *was* spawned, so it must be terminated; the watcher process is fully detached at that point (its `cmd.Process.Release()` ran inside `Spawn`), so the same `syscall.Kill(pid, SIGTERM)` pattern as `stopWatcher` applies.

Since `startBlockingDep` runs inside `AttachLiveDep`'s flock callback and `registry.Save` is the last step of the callback, any error from the ladder prevents the registry entry from being persisted. The on-disk registry state remains the pre-call state. The caller (`AttachLiveDep`) then propagates the error, and `RunDeps` builds a cascade signal for dependents.

### Watcher termination on detach

`DetachLiveDep` (live_deps.go:192) calls `stopBlockingDep` (line 291) when the dep's `HeldBy` becomes empty. Today, `stopBlockingDep` signals the subprocess group (`syscall.Kill(-entry.PGID, SIGTERM)` then `SIGKILL` after grace) and removes the registry entry. The watcher process is never signaled — it survives indefinitely, tailing a `raw.log` whose subprocess has terminated.

The fix extends `stopBlockingDep` with a watcher termination block executed *after* the subprocess group is signaled but *before* the registry entry is removed:

```go
if entry.WatcherPID > 0 && registry.IsPIDAlive(entry.WatcherPID) {
    _ = syscall.Kill(entry.WatcherPID, syscall.SIGTERM)
    deadline := time.Now().Add(2 * time.Second)
    for time.Now().Before(deadline) {
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

The watcher drains and exits cleanly on SIGTERM (its signal handler in `watcher.go:57-63` calls `drain` and returns). The SIGKILL fallback covers cases where draining hangs (e.g., a wedged filesystem). Aggregate Signal emission proceeds afterward unchanged.

### Consumer migration: `/stop-sensor` and `/list-sensors`

`/tail-sensor` already implements the canonical read pattern at `skills/tail-sensor/scripts/tail.go:155-157`:

```go
sigsPath := r.SignalsLogRun(sensorID, entry.RunID)
if _, err := os.Stat(sigsPath); err != nil {
    sigsPath = r.LegacySignalsLog(sensorID)
}
```

`/stop-sensor` adopts the same idiom inside the existing `if !entry.Blocking { ... } else { ... }` branch where `readSignals(r.SignalsLog(sensorID))` is called. The selection happens *before* `readSignals`, with `entry.RunID` already in scope.

`/list-sensors` adopts the same idiom inline when building the per-entry response map:

```go
signalsPath := r.SignalsLogRun(e.SensorID, e.RunID)
if e.RunID == "" {
    signalsPath = r.LegacySignalsLog(e.SensorID)
} else if _, err := os.Stat(signalsPath); err != nil {
    signalsPath = r.LegacySignalsLog(e.SensorID)
}
entry := map[string]interface{}{
    // ...
    "signals_log_path": signalsPath,
}
```

The two distinct fallback conditions (`RunID == ""` for entries written before the layout migration; `os.Stat` failure for entries whose run-id-scoped file was manually deleted) collapse into one branch in `/tail-sensor` and `/stop-sensor` because they only matter where the file is actually read; `/list-sensors` reports the path without opening it, so the `RunID == ""` case must be handled separately.

### Locking discipline

`AttachLiveDep` holds `registry.WithFileLock` for the entire flow including `startBlockingDep`. With watcher.Spawn now inside that block, the flock hold time grows to the dimensions already accepted in `/start-sensor`. Two consequences:

1. **Concurrent `/run-sensor` invocations on the same project serialize during dep startup.** Each invocation that needs to attach to (or fresh-spawn) the same blocking dep waits for the prior invocation's flock to release. Pre-fix: serialization on subprocess spawn (~10 ms). Post-fix: serialization on subprocess spawn + watcher spawn (~150 ms warm, ~1 s cold). For projects with many sensors sharing the same blocking dep, the first invocation pays the cost; subsequent invocations re-attach in <10 ms.
2. **The dep's command starts emitting `raw.log` lines before `registry.Save` completes.** The subprocess is detached and forwarded to the staging file the instant `SpawnDetached` returns; rename preserves the open fd. If `os.Rename` or any later step fails, the cleanup ladder kills the subprocess group — but any bytes already written to the now-renamed `raw.log` survive the cleanup only if the rename succeeded (otherwise we `os.Remove(stagingRaw)` explicitly). In the success path, the watcher may not be running by the time the first `raw.log` line lands; the watcher uses `fsnotify` and reads from offset 0, so it picks up backfilled content on its first write event. Same posture as `/start-sensor` today.

### Test helpers

The orchestrator tests today live in `lib/orchestrator/*_test.go` and use `lib/orchestrator/export_test.go` (which re-exports `startBlockingDep` as `ExportedStartBlockingDep`). New cross-test plumbing — specifically the override of `watcher.SpawnFn` to a fake spawner — is small enough to inline in each test that needs it. If a second test file needs the same fake, the override moves to `lib/orchestrator/orchestratortest/` per rule 11 of CLAUDE.md. The seed of `lib/orchestrator/orchestratortest/` is created only if and when a second consumer materializes.

## Test plan

Tests live in the existing `lib/orchestrator/live_deps_test.go`, `lib/orchestrator/integration_runtime_logs_test.go`, `skills/stop-sensor/scripts/stop_test.go`, and `skills/list-sensors/scripts/list_test.go`. Build tags follow the existing convention per file.

### `lib/orchestrator/live_deps_test.go`

1. **`TestStartBlockingDep_UsesRunIDLayout`** — call `ExportedStartBlockingDep` with a stub sensor whose command is `printf 'hello\n'` and whose `output_parsing.patterns` is empty. After return:
   - `r.RawLogRun(dep.ID, runID)` exists.
   - `r.SignalsLogRun(dep.ID, runID)` exists (zero-length is acceptable).
   - `r.LegacyRawLog(dep.ID)` and `r.LegacySignalsLog(dep.ID)` do NOT exist (they were the staging path and the never-created flat signals path, respectively).
   - The registry entry has `LogDir == r.RelativeRunDir(dep.ID, runID)`, `PID == det.PID`, `WatcherPID > 0`.

2. **`TestStartBlockingDep_PatternsEmitSignals`** — stub sensor with `output_parsing.patterns: [{regex: "ERROR", verdict: "fail", severity: "high", rationale: "..."}]` and a command that writes `ERROR: ouch\n` to stdout. Poll `signals.log` for up to 2 s; assert at least one Signal with `metadata.kind == "individual"`, `verdict == "fail"`, `severity == "high"`. Covers the core regression: patterns are no longer dormant.

3. **`TestStartBlockingDep_NoPatterns_StillSpawnsWatcher`** — stub sensor without `output_parsing`. After return, `WatcherPID > 0` and `signals.log` exists empty. Confirms parity with `/start-sensor`'s "always spawn watcher" posture.

4. **`TestStartBlockingDep_PluginRootMissing`** — `os.Unsetenv("CLAUDE_PLUGIN_ROOT")`. Call returns an error containing `plugin root not set`. No subprocess survives (`registry.IsPIDAlive` on any captured PID is false). No registry entry persisted. No directory under `r.SensorDir(dep.ID)`.

5. **`TestStartBlockingDep_WatcherSpawnFailure`** — override `watcher.SpawnFn` with a fake returning `(0, fmt.Errorf("forced"))`. After call:
   - Returned error contains "forced".
   - The detached subprocess is no longer alive (cleanup killed it).
   - `r.RunDir(dep.ID, runID)` does not exist.
   - Registry has no entry for `dep.ID`.

6. **`TestDetachLiveDep_KillsWatcher`** — fresh spawn through `AttachLiveDep` (with `watcher.SpawnFn` overridden to a fake spawner that launches a tiny `sleep 60` Go binary or shell loop and returns its PID). Then call `DetachLiveDep` with the holder. Poll for up to 3 s; assert `registry.IsPIDAlive(savedWatcherPID) == false`. Confirms watcher termination on detach.

7. **`TestStartBlockingDep_RegistrySaveFailure`** (optional, depends on existing test infrastructure) — inject a write-only registry directory so `registry.Save` fails. Assert subprocess killed, watcher killed, runDir removed, no registry entry. Skipped if injecting a save failure is harder than the rest of the suite.

### `lib/orchestrator/integration_runtime_logs_test.go`

8. **`TestRunWithDepsRoot_DepSignalsPopulated`** — end-to-end: a root assertion sensor declares `requires: [{kind: sensor, id: blocking-dep}]`. The dep has `execution.blocking: true`, `output: stream`, and `output_parsing.patterns: [{regex: "BOOM", verdict: "fail", severity: "high"}]`. The dep's command echoes `BOOM\n` and sleeps. Run `orchestrator.RunWithDepsRoot`. Assert: `.harness/runtime/blocking-dep/<runID>/signals.log` exists and contains at least one Signal whose evidence rationale matches the pattern's rationale. The flat path `.harness/runtime/blocking-dep/raw.log` does NOT exist.

### `skills/stop-sensor/scripts/stop_test.go`

9. **`TestStop_ReadsRunIDScopedSignalsLog`** — seed a registry entry with `RunID = "12345-abcd"` and write a `signals.log` with 3 individual Signals (one `fail`, two `warn`) at `r.SignalsLogRun(sensorID, runID)`. Invoke stop's read path. The aggregate Signal's `metadata.counts` is `{pass: 0, warn: 2, fail: 1, error: 0}` and `verdict == "fail"` (highest stream verdict folded into the aggregate).

10. **`TestStop_LegacyFallback`** — seed a registry entry with `RunID = ""` and write `signals.log` at `r.LegacySignalsLog(sensorID)` with 2 `pass` individuals. Aggregate's counts is `{pass: 2, ...}`. Confirms the read path falls back when `RunID` is absent (pre-fix entries).

### `skills/list-sensors/scripts/list_test.go`

11. **`TestList_EmitsRunIDScopedPath`** — registry entry with `RunID = "7037-5ecd3f00"` and a file at `r.SignalsLogRun(...)`. The emitted entry's `signals_log_path` equals `r.SignalsLogRun(sensorID, runID)`.

12. **`TestList_LegacyFallback_EmptyRunID`** — registry entry with `RunID = ""`. `signals_log_path` equals `r.LegacySignalsLog(sensorID)`.

13. **`TestList_LegacyFallback_FileMissing`** — registry entry with `RunID = "stale-123"` but the run-id-scoped file was manually deleted. `signals_log_path` equals `r.LegacySignalsLog(sensorID)`. Confirms the `os.Stat`-driven fallback.

### Manual verification post-merge

Reproduce the original issue's scenario: a Go project with `assert-health-check-live-returns-200-health` (root) + `run-project-charge-api` (blocking dep, `output=stream` with 6 patterns). Run `/run-sensor assert-health-check-live-returns-200-health` against a broken state (e.g., temporarily rename a Go module's path so `go work sync` fails).

Expected post-fix:
- `.harness/runtime/run-project-charge-api/<runID>/raw.log` contains the full `go work sync` error.
- `.harness/runtime/run-project-charge-api/<runID>/signals.log` contains at least one individual Signal whose `evidence.excerpt` is the error line that matched one of the 6 patterns.
- `.harness/runtime/run-project-charge-api/raw.log` (flat) does NOT exist.
- `/tail-sensor run-project-charge-api 0` emits the individual Signals + the closing envelope Signal.
- `/list-sensors` reports `signals_log_path` pointing at the run-id-scoped file (during the brief window the dep is held).

## Out of scope

- Folding `MaxStreamVerdict(individuals)` into `stopBlockingDep`'s aggregate verdict. The current synthetic `pass` will become *demonstrably wrong* after this fix populates `signals.log` with `fail` individuals, but the dep's terminal verdict will still misrepresent reality until a follow-up issue lands. Tracked as the "issue separada" referenced in the body of #45.
- Extracting a shared `orchestrator.SpawnBlockingWithWatcher` helper used by both `start.go` and `live_deps.go`. The two call sites diverge on placeholder-PID-rebind and registry-write timing; deduplicating now would introduce flag parameters that obscure intent. Revisit if a third caller emerges.
- Extending `heal-sensor`'s classifier to consume the newly populated individual Signals. The classifier already reads from `signals.log`; populating it is the prerequisite this fix addresses. Behavior changes inside `lib/heal/` are tracked separately under issue #9.
- Backfilling existing flat-layout `raw.log`/`signals.log` files into run-id-scoped paths. The legacy paths remain readable via `LegacyRawLog`/`LegacySignalsLog`; users with stale state files see a one-time degraded view until the next `/run-sensor` of the affected dep cycles its registry entry.
