# Registry PID invariant and /stop-sensor robustness design

Status: proposed
Date: 2026-05-11
Related: issue [#11](https://github.com/iurykrieger/harness-framework/issues/11), prior issue [#6](https://github.com/iurykrieger/harness-framework/issues/6), `lib/registry/`, `skills/start-sensor/`, `skills/list-sensors/`, `skills/stop-sensor/`, `skills/tail-sensor/`

## Why

Issue #11 reports a real-world `running_sensors.json` from the `payment-card-api` project containing `"watcher_pid": -1`:

```json
{
  "sensor_id": "run-api-local",
  "pid": 90006,
  "watcher_pid": -1,
  "started_at": "2026-05-09T13:51:38Z",
  "command": "docker compose up --no-deps payment-card-api",
  "log_dir": ".runtime/sensors/run-api-local",
  "held_by": [{"kind": "manual", "attached_at": "..."}]
}
```

Audit of every PID writer in the current tree:

- `skills/start-sensor/scripts/start.go:227` writes `watcherProc.Pid` — always positive after a successful `os.StartProcess` (a failed `os.StartProcess` aborts before any `Save`).
- `lib/orchestrator/live_deps.go:183` writes `0` (the orchestrator path runs deps unwatched; no watcher process exists).
- All other call sites are tests, writing positive values.

No code path in the current tree produces `WatcherPID < 0`. The most plausible origin of the observed `-1` is a pre-#6 build of the plugin that has since been refactored away; the entry survived because `running_sensors.json` is persisted in the user's project and the cwd-fragmentation bug fixed in #6 left stale state behind in some project trees.

The fact that the current code is incidentally safe does not protect against regression. The `RunningSensorEntry` struct accepts any integer; nothing rejects a negative `WatcherPID`, `PID`, `PGID`, or `HeldByEntry.PID` at write time, and nothing remediates a corrupt entry at read time. A future PR can re-introduce the bug and ship; the user discovers it only when something else breaks downstream.

Two consequences make the lack of invariant load-bearing:

1. **`syscall.Kill(-1, sig)` is POSIX broadcast.** A negative `PGID` reaching `terminateWithGrace` would compute `-pgid = 1` and signal init, but a negative `WatcherPID` reaching a hypothetical future `stopWatcher` variant that drops the `pid > 0` guard would broadcast SIGTERM/SIGKILL to every process the user can signal. The current guards are scattered defenses; a centralized invariant is cheaper to maintain.
2. **`/list-sensors`, `/stop-sensor`, `/tail-sensor` silently consume bad state.** `IsPIDAlive(-1)` returns `false`, so a `-1` `watcher_pid` produces a `watcher_alive: false` line that looks like a dead watcher to the user. The user has no signal that their state was wrong, just that something they don't understand is happening.

Issue #11 also surfaces a related symptom from the same registry surface: on macOS, the watcher process sometimes survives `SIGTERM` from `/stop-sensor`, and the e2e tests added in #6 had to add a defensive `SIGKILL` in cleanup (`test/registry-discovery-e2e/registry_discovery_e2e_test.go:182, :290, :389`). The defensive kill masks the underlying race instead of diagnosing it. While the root cause of the `-1` artifact and the SIGTERM survival are likely independent, they share an owner (the registry surface) and benefit from being addressed together: the same PR adds the PID invariant *and* hardens `/stop-sensor` with measurable, observable termination semantics.

The fix is to make PID-field non-negativity an **enforced, package-internal invariant** of `lib/registry` — `Save` rejects negative values with a typed error, `Load`'s sanitizing variant self-heals legacy entries, and the four registry-touching skills surface migrations as a `warn` Signal so the user knows when their state was rewritten. The watcher termination path in `/stop-sensor` is upgraded from fire-and-forget to measured-and-reported, with the defensive `SIGKILL` removed from the tests so any future regression in watcher signal handling fails CI loudly instead of being silently papered over.

## What changes

1. **New `lib/registry/sanitize.go`** with:
   - `ValidateEntry(e RunningSensorEntry) error` — gate of `Save`. Returns `*InvalidEntryError`.
   - `SanitizeAll(rs *RunningSensors) []SanitizeReport` — mutation invoked by the new `LoadSanitized`.
   - Types `InvalidEntryError` and `SanitizeReport`.
2. **New `lib/registry/state.go::LoadSanitized(r Root) (RunningSensors, []SanitizeReport, error)`** — additive companion to the existing `Load`. Combines `Load` + `SanitizeAll` + best-effort re-`Save` under flock.
3. **New `lib/registry/root.go::LookupSanitized(startDir string) (Result, []SanitizeReport, error)`** — additive companion to the existing `Lookup`. Internally calls `Discover` + `LoadSanitized`, returning the same `Result` plus the migration reports. This is the skill-facing entry point; the existing `Lookup` stays untouched for non-skill callers (none today, but keeps the option open).
4. **`lib/registry/state.go::Save`** validates every entry via `ValidateEntry` before marshalling.
5. **`skills/{list,stop,tail,start}-sensor/scripts/*.go`** migrate from `registry.Lookup(startDir)` to `registry.LookupSanitized(startDir)` and emit a precedence `warn` Signal `metadata.kind=registry_migrated` as the first JSONL line on stdout when `reports` is non-empty. The deeper `registry.Load(r)` calls under flock in `start.go:168`, `stop.go:74`, `stop.go:132` remain unchanged — by the time those run, `LookupSanitized` has already persisted the sanitized state to disk, so `Load` reads a clean view.
6. **`skills/stop-sensor/scripts/stop.go::stopWatcher`** signature widened to return `(killedForcefully bool, latencyMS int)`. The aggregate Signal carries those under `metadata.watcher_kill_forced` and `metadata.watcher_kill_latency_ms`.
7. **`skills/start-sensor/scripts/start.go`** redirects the watcher subprocess's stderr to a new `<r.SensorDir(id)>/watcher.log` file instead of discarding it.
8. **`skills/start-sensor/scripts/watcher.go`** logs `"watcher: <sig> received, draining"` to stderr (now captured into `watcher.log`) inside the signal-handling goroutine.
9. **`test/registry-discovery-e2e/registry_discovery_e2e_test.go`** removes the `killWatcherIfAlive` helper (currently at lines 170–185) and its two callers in `t.Cleanup` blocks (lines 294 and 393). Adds a new test asserting the legacy `-1` migration through `/list-sensors`.
10. **No change to** `schemas/sensor.json`, `schemas/signal.json`, `RunningSensors.Version`, or the `running_sensors.json` on-disk shape (the saneamento writes the same fields, just with non-negative values).
11. **No change to** the orchestrator path (`lib/orchestrator/live_deps.go`) or the watcher reaper (`skills/start-sensor/scripts/watcher.go::runReaper`) — they continue to use `Load` directly because they execute on the runtime fast path and migration belongs in user-facing skills.

## Architecture

### Invariants and decisions

The invariant enforced by this PR:

| Field | Allowed values | Where enforced |
| --- | --- | --- |
| `RunningSensorEntry.PID` | `> 0` | `ValidateEntry`; rejected by `Save`. Entry with `PID < 1` is **dropped** by `SanitizeAll`. |
| `RunningSensorEntry.PGID` | `> 0` | Same as `PID`. Entry dropped on `< 1`. |
| `RunningSensorEntry.WatcherPID` | `>= 0` (zero means "no watcher", as in the orchestrator path) | `ValidateEntry`; rejected by `Save`. Sanitized to `0` on legacy negative. |
| `HeldByEntry.PID` (when `Kind == "manual"`) | Any (typically `0`; PID is meaningless for manual holders) | `ValidateEntry` always accepts. Sanitize leaves untouched. |
| `HeldByEntry.PID` (when `Kind == "sensor"`) | `> 0` (a sensor holder must reference a live dependent process) | `ValidateEntry` rejects on `< 1`; `SanitizeAll` **drops the holder** (not the entry) on legacy negative. |

`SubprocessExit.Code` is **explicitly excluded** from the invariant. The watcher's reaper deliberately writes `-1` when it cannot recover the exact code (`watcher.go:223`), and `/stop-sensor` interprets that sentinel (`stop.go:265`). Touching it would change runner semantics.

### `lib/registry/sanitize.go`

```go
package registry

// InvalidEntryError is returned by ValidateEntry when a registry entry
// violates a PID non-negativity invariant. Save propagates this unwrapped
// so callers can errors.As it.
type InvalidEntryError struct {
    SensorID string
    Field    string // "pid" | "pgid" | "watcher_pid" | "held_by[i].pid"
    Value    int
}

func (e *InvalidEntryError) Error() string {
    return fmt.Sprintf("registry: invalid %s=%d for sensor %q", e.Field, e.Value, e.SensorID)
}

// SanitizeReport records one mutation performed by SanitizeAll. The
// receiver of these reports surfaces them as a warn Signal so the user
// learns their state was rewritten.
type SanitizeReport struct {
    SensorID string `json:"sensor_id"`
    Field    string `json:"field"`
    OldValue int    `json:"old_value"`
    Dropped  bool   `json:"dropped"` // entry or holder discarded entirely
}

// ValidateEntry enforces the PID non-negativity invariant. Returns nil if
// the entry is valid; otherwise *InvalidEntryError naming the first
// offending field. PID and PGID must be > 0. WatcherPID must be >= 0.
// HeldByEntry.PID must be >= 0 always; when Kind == "sensor", it must be
// > 0.
func ValidateEntry(e RunningSensorEntry) error { ... }

// SanitizeAll rewrites legacy invalid PID fields in rs to safe values.
// Mutation is in-memory; the caller is responsible for persisting via
// Save (which will then succeed because ValidateEntry passes).
//
// Rules, applied per entry:
//   - WatcherPID < 0       → rewrite to 0, report (Dropped: false).
//   - HeldByEntry.PID < 0 with Kind == "manual" → rewrite to 0, report.
//   - HeldByEntry.PID < 1 with Kind == "sensor" → drop the holder from
//     HeldBy[], report (Dropped: true).
//   - PID < 1 or PGID < 1  → drop the entire entry, report (Dropped: true).
//
// Returns nil reports when nothing changed.
func SanitizeAll(rs *RunningSensors) []SanitizeReport { ... }
```

### `lib/registry/state.go` changes

**`Save` validates before persisting:**

```go
func Save(r Root, rs RunningSensors) error {
    for _, e := range rs.Entries {
        if err := ValidateEntry(e); err != nil {
            return err
        }
    }
    if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil { ... }
    // ... existing marshal + atomic write
}
```

The validation error is returned unwrapped so callers can `errors.As(err, new(*registry.InvalidEntryError))`. `start.go`'s existing `lockErr` branch already maps unknown `Save` failures to `cause: registry_write_failed`; an `*InvalidEntryError` follows the same path (cause stays `registry_write_failed`, message includes the offending field via the error string).

**`LoadSanitized` is additive (no change to `Load` signature):**

```go
// LoadSanitized loads running_sensors.json, applies SanitizeAll, and
// best-effort re-persists when mutations occurred. Returns the
// sanitized state and the list of reports so callers can surface a
// migration Signal. Re-Save failure is silenced (in-memory state is
// still correct; persistence retries on next invocation).
func LoadSanitized(r Root) (RunningSensors, []SanitizeReport, error) {
    rs, err := Load(r)
    if err != nil {
        return rs, nil, err
    }
    reports := SanitizeAll(&rs)
    if len(reports) > 0 {
        _ = WithFileLock(r.LockFile(), func() error { return Save(r, rs) })
    }
    return rs, reports, nil
}
```

`Load` and `LoadOrEmpty` are unchanged. The orchestrator (`lib/orchestrator/live_deps.go`) and the watcher reaper (`skills/start-sensor/scripts/watcher.go`) keep using `Load`; both run on the runtime fast path and migration noise is inappropriate there. Migration is observed *only* through user-invoked skills.

### `lib/registry/root.go` — `LookupSanitized`

All four registry-touching skills today funnel root discovery through `Lookup(startDir)`, which internally calls `Discover` + `LoadOrEmpty` and returns a `Result{Root, ProjectRoot, Source, Exists, State}`. The migration entry point is an additive companion:

```go
// LookupSanitized is the skill-facing entry point that combines root
// discovery with PID-invariant sanitation. It is otherwise identical to
// Lookup: same Result, same error semantics. The extra []SanitizeReport
// return value is non-empty when LoadSanitized rewrote or dropped one or
// more entries from running_sensors.json; callers surface this as a warn
// Signal.
func LookupSanitized(startDir string) (Result, []SanitizeReport, error) {
    root, source, err := Discover(startDir)
    if err != nil {
        return Result{}, nil, err
    }
    rs, reports, err := LoadSanitized(root)
    if err != nil {
        return Result{Root: root, ProjectRoot: root.ProjectRoot(), Source: source}, nil, err
    }
    return Result{
        Root:        root,
        ProjectRoot: root.ProjectRoot(),
        Source:      source,
        Exists:      // computed same way as Lookup; LoadSanitized does not surface Exists,
                     // so this is derived from os.Stat(root.RegistryFile()) inside LookupSanitized.
        State:       rs,
    }, reports, nil
}
```

The exact derivation of `Exists` inside `LookupSanitized` mirrors `LoadOrEmpty`'s `errors.Is(err, os.ErrNotExist)` check — implementation detail for the plan, not architectural.

Skills replace exactly one call:

| Skill | Before | After |
| --- | --- | --- |
| `list-sensors/scripts/list.go:30` | `res, err := registry.Lookup(startDir)` | `res, reports, err := registry.LookupSanitized(startDir)` |
| `tail-sensor/scripts/tail.go:32` | same | same |
| `stop-sensor/scripts/stop.go:39` | same | same |
| `start-sensor/scripts/start.go:28-34` | `root, err := os.Getwd()` then `runStart(root, …)` | `startDir, err := os.Getwd()` then `res, reports, err := registry.LookupSanitized(startDir)` then `runStart(res.ProjectRoot, …)` |

`start-sensor` is migrated to `LookupSanitized` for uniformity with the other three skills (it currently calls `os.Getwd()` directly instead of going through `Lookup`; this PR closes that small inconsistency at no extra cost). The other three skills already call `Lookup`; the rename is one line each.

The deeper `registry.Load(r)` invocations under flock (`start.go:168`, `stop.go:74`, `stop.go:132`) are **not** migrated — they continue to use `Load`. By the time they execute, `LookupSanitized` has already persisted sanitized state to disk, so `Load` sees clean entries.

### Skill integration: the `registry_migrated` warn Signal

When `len(reports) > 0`, each of the four registry-touching skills emits this Signal as the **first JSONL line** on stdout, *before* its normal signal:

```json
{
  "sensor_id": "list-sensors",
  "version": "0.0.0",
  "run_id": "<uuid>",
  "started_at": "<iso>",
  "finished_at": "<iso>",
  "verdict": "warn",
  "severity": "low",
  "confidence": 1.0,
  "evidence": [
    {"rationale": "rewrote 1 invalid PID field(s) and dropped 0 entry/holder(s) in running_sensors.json"}
  ],
  "cost_actual": {"latency_ms": 0},
  "metadata": {
    "kind": "registry_migrated",
    "reports": [
      {"sensor_id": "run-api-local", "field": "watcher_pid", "old_value": -1, "dropped": false}
    ],
    "registry_path": "<absolute path>",
    "registry_source": "env|walk_up",
    "registry_exists": true
  }
}
```

The `sensor_id` of this Signal is the **skill name** (`list-sensors`, `stop-sensor`, `tail-sensor`, `start-sensor`) — same convention as the skill's primary diagnostic Signal. The skill's main signal follows on the second JSONL line, behavior unchanged. Cascade chains and stop aggregates continue to be the *last* JSONL line per the existing contract.

`metadata.kind=registry_migrated` is new but does not require a schema bump — `signal.json` allows additionalProperties under `metadata`.

### `/stop-sensor` watcher termination — measure and report

Current `stopWatcher` is fire-and-forget with internal escalation:

```go
// stop.go:163 (before)
func stopWatcher(pid int) {
    if pid <= 0 { return }
    _ = syscall.Kill(pid, syscall.SIGTERM)
    deadline := time.Now().Add(time.Second)
    for time.Now().Before(deadline) {
        if !registry.IsPIDAlive(pid) { return }
        time.Sleep(20 * time.Millisecond)
    }
    if registry.IsPIDAlive(pid) {
        _ = syscall.Kill(pid, syscall.SIGKILL)
    }
}
```

New signature:

```go
// stop.go (after)
func stopWatcher(pid int) (killedForcefully bool, latencyMS int) {
    start := time.Now()
    if pid <= 0 {
        return false, 0
    }
    _ = syscall.Kill(pid, syscall.SIGTERM)
    deadline := start.Add(time.Second)
    for time.Now().Before(deadline) {
        if !registry.IsPIDAlive(pid) {
            return false, int(time.Since(start) / time.Millisecond)
        }
        time.Sleep(20 * time.Millisecond)
    }
    if registry.IsPIDAlive(pid) {
        _ = syscall.Kill(pid, syscall.SIGKILL)
        return true, int(time.Since(start) / time.Millisecond)
    }
    return false, int(time.Since(start) / time.Millisecond)
}
```

`runStop` captures the two return values and folds them into the aggregate Signal's `metadata`:

```json
"metadata": {
  "kind": "stop",
  ...
  "watcher_kill_forced": false,
  "watcher_kill_latency_ms": 47
}
```

These are diagnostic — they do not change the aggregate verdict. CI / users can grep `watcher_kill_forced=true` to detect regressions.

### Watcher signal-handling visibility

The watcher's signal goroutine currently swallows the signal kind:

```go
// watcher.go:55 (before)
go func() {
    ch := make(chan os.Signal, 1)
    signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
    <-ch
    close(stop)
}()
```

After:

```go
go func() {
    ch := make(chan os.Signal, 1)
    signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
    s := <-ch
    fmt.Fprintf(os.Stderr, "watcher: %s received, draining\n", s)
    close(stop)
}()
```

For this stderr to survive, `/start-sensor` redirects the watcher's stderr to a new log file:

```go
// start.go (after)
watcherLog, err := os.OpenFile(
    filepath.Join(r.SensorDir(id), "watcher.log"),
    os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644,
)
if err != nil { ... handle ... }
defer watcherLog.Close()
// ...
watcherProc, err := os.StartProcess(watcherPath, []string{watcherPath}, &os.ProcAttr{
    Env:   [...],
    Files: []*os.File{nil, nil, watcherLog}, // was: {nil, nil, nil}
    Sys:   &watcherSysProcAttr,
})
```

The file is `O_APPEND` so concurrent watcher restarts (in test scenarios) do not stomp each other. It is **not** read by any code in this PR — it is purely a diagnostic surface, written by the watcher, inspected by a human after a regression.

### Idempotency and concurrency

`LoadSanitized` is idempotent: on a healthy `running_sensors.json`, `SanitizeAll` returns an empty slice and no re-`Save` is attempted. The function never lifts file mtime unnecessarily.

Concurrent `/list-sensors` invocations on a registry with a `-1` entry race only on the re-`Save`. Both calls `Load` (return state with `-1`), then `SanitizeAll` (each mutates its own in-memory copy), then attempt `WithFileLock`. The first acquires the flock and persists; the second acquires next, but its `Save` is the same content (Sanitize is deterministic), so the disk state is consistent regardless of ordering. Both surface the `registry_migrated` warn — neither user-visible run loses information.

A more pedantic ordering would re-`Load` inside the flock and re-`Sanitize` to avoid the redundant write. It is not implemented because the redundant write is a single atomic rename of a tiny JSON file and the cost is negligible compared to skill startup.

### Backwards compatibility

| Disk state | Behavior |
| --- | --- |
| Healthy (all PIDs ≥ allowed) | No change. No reports, no warn, no re-Save. |
| `watcher_pid: -1` (the issue) | First invocation of any of the four registry skills rewrites to `0` + emits warn. Subsequent invocations: healthy path. |
| `pid: -1` or `pgid: -1` | Entry dropped (the subprocess is unkillable through this entry anyway). Warn names the dropped sensor. User must manually clean up the orphan process. |
| `held_by` entry with `kind=sensor, pid=-1` | Holder dropped from the entry. If `HeldBy` becomes empty, entry is detached but kept. |
| `RunningSensors.Version` bumped above 1 | Save accepts; Load passes through. Future versioning is out of scope. |
| `running_sensors.json` corrupt (parse error) | Unchanged — `Load` returns parse error, `LoadSanitized` returns `(zero, nil, err)`, skill emits its existing error Signal. |

No migration step is required. The first run of any registry skill after deploy performs the migration transparently.

### Non-goals

- **Schema-as-source-of-truth for `running_sensors.json`.** This file is runtime state, not a contract between components. Introducing `schemas/running_sensors.json` would add ceremony without benefit; the invariant is exclusively internal to `lib/registry`.
- **Validating fields other than PIDs.** Strings (`SensorID`, `Command`, `LogDir`, `StartedAt`) and `SubprocessExit.Code` are not in scope. The runtime tolerates non-PID weirdness; the OS-level danger is specifically negative PIDs.
- **Migrating callers off `Load` onto `LoadSanitized`.** Orchestrator and watcher reaper deliberately stay on `Load` (no migration noise on the runtime fast path). Their users hit the four skills regularly; migration converges quickly.
- **Reproducing the original `-1` in a test.** The current code does not produce it (every writer is audited above); a reproduction test would assert the impossible. Regression coverage is instead via tests that inject `-1` directly into `running_sensors.json` and assert sanitization.
- **Diagnosing the macOS SIGTERM survival's root cause.** The PR adds visibility (`watcher.log`, `watcher_kill_forced`, `watcher_kill_latency_ms`) and removes the defensive `SIGKILL` from tests, so any recurrence fails CI with diagnostic surface intact. Root-cause investigation, if it is needed, becomes a follow-up issue with concrete data.

## Testing

### `lib/registry/sanitize_test.go` (new)

Standard `testing` package, table-driven.

- **`TestValidateEntry_AcceptsValid`** — matrix:
  - `PID=1, PGID=1, WatcherPID=0, HeldBy=[]`
  - `PID=42, PGID=42, WatcherPID=43, HeldBy=[{Kind:"manual"}]`
  - `PID=10, PGID=10, WatcherPID=11, HeldBy=[{Kind:"sensor", ID:"dep", PID:99}]`
- **`TestValidateEntry_RejectsNegative`** — table: each invalid case asserts `*InvalidEntryError` with the expected `Field` and `Value`:
  - `{PID: 0}` → `field=pid, value=0`
  - `{PID: -1}` → `field=pid, value=-1`
  - `{PGID: 0}` → `field=pgid, value=0`
  - `{WatcherPID: -1}` → `field=watcher_pid, value=-1`
  - `HeldBy=[{Kind:"sensor", PID:0}]` → `field=held_by[0].pid, value=0`
  - `HeldBy=[{Kind:"manual", PID:-1}]` → `field=held_by[0].pid, value=-1`
- **`TestSanitizeAll_RewritesWatcherPID`** — entry with `WatcherPID=-1`. Asserts: WatcherPID is now `0`; one report with `Field=watcher_pid, OldValue=-1, Dropped=false`.
- **`TestSanitizeAll_DropsHolderWithBadSensorPID`** — entry with `HeldBy=[{Kind:"sensor", ID:"x", PID:-1}, {Kind:"manual"}]`. Asserts: HeldBy now has only the manual holder; one report `Dropped=true`.
- **`TestSanitizeAll_DropsEntryWithBadPID`** — two entries, one with `PID=-1`. Asserts: only the healthy one remains; one report `Dropped=true` naming the dropped sensor.
- **`TestSanitizeAll_Idempotent`** — run Sanitize twice on a state with `WatcherPID=-1`. First call mutates and reports; second call is a no-op (empty reports, state unchanged).
- **`TestSanitizeAll_NoOpOnHealthy`** — healthy state in, empty reports out, state byte-identical.

### `lib/registry/state_test.go` (additions)

- **`TestSave_RejectsInvalidEntry`** — call `Save` with an entry that has `WatcherPID=-1`. Asserts: returned `error` matches `*InvalidEntryError` via `errors.As`; `running_sensors.json` was **not** created/overwritten.
- **`TestLoadSanitized_MigratesLegacy`** — write `running_sensors.json` manually with `{watcher_pid: -1}`; call `LoadSanitized`; assert: returned state has `WatcherPID=0`; reports is non-empty; the file on disk has been rewritten.
- **`TestLoadSanitized_NoOpOnHealthy`** — healthy file; `LoadSanitized` returns empty reports; file mtime unchanged.
- **`TestLoadSanitized_PreservesOnReSaveFailure`** — simulate a write failure (read-only dir or sentinel); assert: in-memory state still sanitized; reports returned; on-disk state unchanged.

### `skills/stop-sensor/scripts/stop_test.go` (additions)

- **`TestStopWatcher_NormalSIGTERM`** — spawn a real watcher-like subprocess (a Go test helper binary that installs `signal.Notify(SIGTERM)` and exits on receipt). Call `stopWatcher(pid)`. Assert: `killedForcefully=false`; `latencyMS < 200`; process is dead.
- **`TestStopWatcher_RequiresSIGKILL`** — spawn a test helper that *ignores* SIGTERM (installs an empty signal handler). Call `stopWatcher(pid)`. Assert: `killedForcefully=true`; `latencyMS` between ~1000 and ~1100; process is dead.
- **`TestStopWatcher_DeadAlready`** — pass a PID that has already exited. Assert: `killedForcefully=false`; `latencyMS < 50`; no error.
- **`TestStopWatcher_NonPositivePID`** — pass `pid=0` and `pid=-1`. Assert: `killedForcefully=false, latencyMS=0` for both; no syscalls are made (cannot be observed directly, but the path returns early).

### `skills/start-sensor/scripts/watcher_test.go` (additions)

- **`TestWatcher_LogsOnSIGTERM`** — start the watcher with stderr captured to a temp file. Send SIGTERM. Wait for exit. Assert: stderr contains `watcher: terminated received, draining` (the `terminated` token is what `syscall.SIGTERM.String()` produces on Unix).

### `skills/list-sensors/scripts/list_test.go` (additions)

- **`TestList_EmitsRegistryMigratedSignal`** — write `running_sensors.json` with `watcher_pid=-1`. Run `list-sensors`. Assert stdout has two JSONL lines: first is `verdict=warn, metadata.kind=registry_migrated, reports[0].field=watcher_pid, reports[0].old_value=-1`; second is the normal `list` signal where `entries[0].watcher_pid=0`.

### `test/registry-discovery-e2e/registry_discovery_e2e_test.go` (changes)

- **Remove** the `killWatcherIfAlive` helper (lines 170–185) and both of its `t.Cleanup` callers (lines ~294 and ~393), along with the `// Capture watcher PID …` comment blocks (~281, ~378) that exist only to feed the helper. If a watcher survives `stop-sensor` after this PR, that is a test failure — the SIGKILL-defensive cleanup is exactly the thing this PR removes.
- **Add `TestSanitize_LegacyMinusOneViaListSensors`** — end-to-end: `t.TempDir()` as project root, manual `running_sensors.json` with `watcher_pid: -1`, invoke `/list-sensors` via the built binary, assert two JSONL lines, and on disk the registry now has `watcher_pid: 0`.

### Manual verification (post-CI)

1. In a fresh project: `mkdir test-project && cd test-project && mkdir sensors`. Verify nothing breaks with no registry.
2. Manually craft a `.runtime/sensors/running_sensors.json` with `watcher_pid: -1`. Run `/list-sensors`. Verify two JSONL lines on stdout. Verify the file on disk now has `watcher_pid: 0`.
3. Start a blocking sensor, run `/stop-sensor`, observe the new `watcher_kill_forced` and `watcher_kill_latency_ms` metadata. On a healthy macOS run, `watcher_kill_forced=false` and `latency_ms < 200`.
4. Inspect `.runtime/sensors/<id>/watcher.log` after a `/stop-sensor`. Should contain `watcher: terminated received, draining`.

## Open questions

None. All decisions are settled in the sections above (scope: both invariant and stop-sensor robustness; legacy `-1` handling: self-heal + warn; field coverage: `WatcherPID`, `PID`, `PGID`, `HeldBy[].PID`; reproducer policy: regression-style only; macOS strategy: harden `stopWatcher` + diagnostics, do not investigate root cause in this PR).

## References

- Issue [#11](https://github.com/iurykrieger/harness-framework/issues/11)
- Issue [#6](https://github.com/iurykrieger/harness-framework/issues/6) (registry root discovery; source of the original `-1` evidence)
- `lib/registry/state.go` (current `Load`/`Save`)
- `skills/stop-sensor/scripts/stop.go` (`stopWatcher`, `terminateWithGrace`)
- `skills/start-sensor/scripts/watcher.go` (signal handler, `runReaper`)
