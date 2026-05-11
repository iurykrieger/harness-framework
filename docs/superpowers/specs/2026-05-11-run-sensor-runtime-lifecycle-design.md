# /run-sensor: runtime persistence + lifecycle parity with /start-sensor

**Date:** 2026-05-11
**Author:** brainstorming session (iury.krieger@stone.com.br)
**Status:** Draft — pending implementation plan

## Problem

`/run-sensor` (non-blocking sensors) and `/start-sensor` (blocking sensors) share the same `lib/orchestrator.RunOne` for the `prepare → command → teardown` lifecycle, but diverge on persistence and registry visibility:

| Concern | `/run-sensor` (today) | `/start-sensor` (today) |
| --- | --- | --- |
| Per-sensor dir under `.runtime/` | absent | `.runtime/sensors/<id>/` |
| `raw.log` (subprocess output verbatim) | not written | written by detached process |
| `signals.log` (parsed Signals JSONL) | not written | written by watcher |
| Entry in `running_sensors.json` | none | one per blocking sensor |
| Visible to `/list-sensors` | no | yes |
| Reachable by `/tail-sensor` / `/stop-sensor` | no | yes |

Consequences:

- Post-mortem inspection of a `/run-sensor` invocation is impossible — once the agent's tool result scrolls past, the raw output and individual Signals are gone.
- A second session can't observe a running `/run-sensor` (no entry, nothing to tail or stop).
- The two runners maintain two mental models of "the runtime", even though they observe sensors symmetrically.

## Goal

Make `/run-sensor` write the same on-disk artifacts and participate in the same registry-mediated lifecycle as `/start-sensor`, with one shared on-disk layout and one set of skills (`/list-sensors`, `/tail-sensor`, `/stop-sensor`) operating uniformly across both runner types.

## Non-goals

- Auto-cleanup or retention policies for `<run-id>/` directories. Operator-managed (manual `rm -rf`). May get its own skill later.
- Refactoring `RunOne` into a separate `LifecycleManager` package. Tracked as a future cleanup; this spec keeps the orchestrator structure intact.
- Hook fingerprint refinements in `hooks/error-issue-autofiler.go`. Independent follow-up.

## Decisions

1. **Layout (both runners):** `.runtime/sensors/<sensor-id>/<run-id>/{raw.log, signals.log}`.
2. **`run-id` shape:** composite `<pid>-<short-uuid8>` (e.g. `47193-a3f9c102`). PID is the observed subprocess's PID (not the watcher's). Short UUID disambiguates PID reuse across runs separated in time.
3. **Registry discriminator:** `blocking: bool` on `RunningSensorEntry` — same field name as `sensor.execution.blocking`. No parallel `kind` vocabulary.
4. **Retention:** keep every `<run-id>/` directory indefinitely.
5. **Visibility:** non-blocking runs register an entry on start, remove on exit; `/list-sensors`, `/tail-sensor`, `/stop-sensor` operate on both `blocking:true` and `blocking:false` entries.
6. **Singleton:** `blocking:true` keeps singleton-by-`sensor.id`. `blocking:false` allows concurrent runs of the same sensor (disambiguated by `run_id`).
7. **Persistence pipeline:** non-blocking runs tee in-process (no watcher); blocking runs keep the existing detached-subprocess + watcher design.

## Architecture

### On-disk layout

```
.runtime/sensors/
├── running_sensors.json     # registry (atomic, file-locked)
├── running_sensors.lock
└── <sensor-id>/
    └── <run-id>/            # <pid>-<short-uuid8>
        ├── raw.log          # subprocess stdout+stderr, verbatim
        └── signals.log      # JSONL: individuals (if stream mode) + aggregate (last line)
```

For `/start-sensor` the existing `watcher.log` (watcher's own stderr) also lives under `<run-id>/`.

### Schema changes

`schemas/signal.json`: update the `run_id` description to acknowledge the composite form. No type change.

`schemas/sensor.json`: no change.

`RunningSensorEntry` (in `lib/registry/state.go`; not in a JSON schema today):

```go
type RunningSensorEntry struct {
    SensorID   string        `json:"sensor_id"`
    RunID      string        `json:"run_id"`             // NEW
    Blocking   bool          `json:"blocking"`            // NEW (mirrors sensor.execution.blocking)
    PID        int           `json:"pid"`
    PGID       int           `json:"pgid"`
    WatcherPID int           `json:"watcher_pid,omitempty"` // present only when blocking:true
    StartedAt  string        `json:"started_at"`
    Command    string        `json:"command"`
    LogDir     string        `json:"log_dir"`             // now .runtime/sensors/<id>/<run-id>
    HeldBy     []HeldByEntry `json:"held_by"`
}
```

### Registry API

`lib/registry/state.go`:

| Helper | Semantics |
| --- | --- |
| `FindBlockingEntry(id) *RunningSensorEntry` | replaces `FindEntry(id)`; only `blocking:true`. |
| `FindEntries(id) []*RunningSensorEntry` | returns all entries for an id (any `blocking`). |
| `FindEntryByRunID(runID) *RunningSensorEntry` | direct lookup by globally-unique `run_id`. |
| `RemoveEntryByRunID(runID)` | replaces `RemoveEntry(id)`. |

`lib/registry/sanitize.go`: extend to migrate legacy entries lacking `run_id`/`blocking`. Legacy is assumed `blocking:true` (start-sensor was the only producer); generates `run_id = <pid>-legacy` for path resolution, logs an `entry_migrated` warn-shaped report on load, persists on next `Save`.

### Paths

`lib/registry/paths.go`:

```go
func (r Root) RunDir(id, runID string) string
func (r Root) RawLog(id, runID string) string
func (r Root) SignalsLog(id, runID string) string
```

`SensorDir(id)` is preserved for ops that walk all runs of a sensor (e.g. future cleanup).

## Runtime flow

### `/run-sensor <id>` (foreground, `blocking:false`)

1. Resolve sensor; reject if `execution.blocking:true` (existing behavior).
2. Orchestrate deps via `RunDeps` → topo order. Each `RunOne(dep)` recurses through this same pipeline.
3. Run target's `prepare[]` fail-fast.
4. Enter `StreamSubprocess` with a new `RunDir` field (empty = legacy behavior, no persistence).
5. Right after `cmd.Start()`:
   - Compute `run_id = <pid>-<short-uuid8>` from `cmd.Process.Pid`.
   - `mkdir -p` the `<run-id>/` directory; open `raw.log` and `signals.log` for append.
   - Under `WithFileLock`, insert the registry entry. Validate insert: refuse if `run_id` is already present.
   - Install a `defer` that removes the entry on any exit path (success, panic, signal).
6. As the scanner drains stdout/stderr:
   - Every read line is written verbatim to `raw.log`.
   - Pattern-matched lines yield individual Signals → written to both stdout (existing) and `signals.log` (new).
7. On subprocess exit, build the aggregate; write to `signals.log` (last line) and stdout (last line).
8. Run `teardown[]` (existing).
9. `defer` removes the registry entry.

The runner installs a SIGINT/SIGTERM handler that forwards the signal to the subprocess group, waits for command exit, writes the aggregate with `metadata.terminated_externally:true`, then unwinds the defers (cleanup runs).

### `/start-sensor <id>` (detached, `blocking:true`)

1. Validate + DAG + deps (existing). Each dep `RunOne` writes its own `<run-id>/`.
2. `SpawnDetached` returns the subprocess PID.
3. Compute `run_id = <pid>-<short-uuid8>` (replaces the standalone UUID currently used in `Envelope`).
4. `mkdir -p <run-id>/`; create `raw.log` (subprocess redirects stdout/stderr here) and `signals.log` (empty file; the watcher will append).
5. Spawn the watcher with env vars pointing at the new paths.
6. Write the registry entry with `blocking:true`, the composite `run_id`, `log_dir=<run-id>`.
7. Emit `metadata.kind=started`.

### Cleanup ownership

| Runner | Removes registry entry |
| --- | --- |
| `/run-sensor` | the runner itself, via `defer` on exit |
| `/start-sensor` | `/stop-sensor` (existing) |

Orphan detection (PID dead, entry present) lives in `/list-sensors` and is operator-cleared via `/stop-sensor --reap-dead-holders`. Applies to both `blocking` values.

## Skill changes

### `/list-sensors`

- Iterate all entries (no longer first-by-id).
- Per-entry `metadata`: add `blocking` and `run_id`.
- Liveness:
  - `pid_alive` for every entry (existing).
  - `watcher_alive` only when `blocking:true`.
- A sensor may appear multiple times when it has parallel `blocking:false` runs.

### `/tail-sensor`

- New invocation forms:
  - `<sensor.id>` (compact). Resolves to the unique active entry. Errors as `metadata.kind=ambiguous_run` when multiple are active; as `metadata.kind=no_active_run` when only historical dirs exist.
  - `<sensor.id>/<run-id>` (path-like, explicit). Always resolves to the named run.
- Argument `<cursor>` unchanged.
- Reads `RunDir(id, runID)/signals.log` directly; no behavior change beyond the path.

### `/stop-sensor`

- Same path-like invocation form as tail.
- For `blocking:false` entries: SIGTERM to `pid`, wait `execution.graceful_timeout_ms` (default 5000), SIGKILL if necessary, then remove the entry. The foreground runner sees the subprocess exit, runs teardown, writes the aggregate (`terminated_externally:true`), and unwinds.
- For `blocking:true` entries: existing behavior.
- `held_by` semantics are blocking-only — `blocking:false` entries have an empty `held_by` (the entry is "owned" by the runner that wrote it). `--reap-dead-holders` is a no-op for those entries.

## Stdout contract preservation

For `/run-sensor`, no observable change on stdout: the same JSONL lines, in the same order, with the aggregate as the last line. The tee writes a copy to `signals.log` but never reorders or buffers stdout differently.

The error envelope contract (a synthetic Signal printed on early failure) is unchanged.

## Invariants

1. Every registry entry has its `log_dir` on disk with `raw.log` and `signals.log` created (possibly empty).
2. `run_id` is globally unique across active entries.
3. `blocking:false` entry implies the foreground runner is alive (PID-checked by `/list-sensors`); there is no watcher to inspect.
4. `/run-sensor`'s stdout last line is the aggregate Signal.
5. `raw.log` and `signals.log` are append-only (no `O_TRUNC` on any code path).

## Edge cases

| Case | Behavior |
| --- | --- |
| `mkdir`/`open` fails before spawn | runner emits error envelope `metadata.kind=runtime_setup_failed`; no subprocess; no registry change. |
| Spawn OK, registry insert fails | kill subprocess group, remove run dir, emit error envelope `metadata.kind=registry_write_failed`. |
| Runner receives SIGINT/SIGTERM | forward to subprocess group, drain, write aggregate, run defers, exit. |
| Runner killed via SIGKILL | subprocess orphans (reaped by init); entry orphans (PID dead). `/list-sensors` marks orphan; operator clears via `/stop-sensor --reap-dead-holders <id>/<run-id>`. |
| External `/stop-sensor` during prepare or teardown | runner is interrupted in the current phase; aggregate carries the lifecycle entries it managed to complete. |
| Two concurrent `/run-sensor X` | both get distinct `run_id`s and `<run-id>/` dirs; no file contention. |
| Invalid Signal post-tee | logged to runner stderr (existing); not written to `signals.log` or stdout. |
| PID reuse colliding with an active `run_id` | UUID8 suffix disambiguates; insert refuses if exact `run_id` already exists. |

## Definition of Done (binary)

1. `/run-sensor X` (`blocking:false`) creates `.runtime/sensors/X/<pid>-<uuid8>/{raw.log,signals.log}` and removes the entry from `running_sensors.json` on exit.
2. The last line of `/run-sensor`'s stdout is the aggregate Signal — byte-identical to a baseline pre-change run.
3. `signals.log` contains exactly the same Signals (individuals + aggregate) emitted on stdout, in the same order.
4. `/list-sensors` invoked from another session during `/run-sensor X` returns an entry with `blocking:false` and a composite `run_id`.
5. `/start-sensor Y` writes its artifacts to `.runtime/sensors/Y/<pid>-<uuid8>/` (layout parity).
6. Two concurrent `/run-sensor X` invocations coexist with distinct entries and dirs.
7. `/stop-sensor X/<run-id>` on a live non-blocking run terminates the subprocess and removes the entry; the aggregate in `signals.log` carries `metadata.terminated_externally:true`.
8. `go vet -tags=run_computational` and `go vet -tags=run_inferential` pass; `go test ./...` passes with both tags.

## Testing strategy

### Unit (table-driven, standard `testing`)

- `lib/registry/paths_test.go` — `RunDir`, `RawLog(id, runID)`, `SignalsLog(id, runID)` correctness; rejects empty `runID`.
- `lib/registry/state_test.go` — `FindEntries`, `FindEntryByRunID`, `FindBlockingEntry`, `RemoveEntryByRunID` across 0/1/N entry and mixed-`blocking` configurations.
- `lib/registry/sanitize_test.go` — legacy entry migration: defaults preserved, placeholder `run_id` synthesized, warning emitted, re-save persists.
- `lib/subprocess/stream_test.go` — new `RunDir` field: empty preserves legacy semantics; populated tees correctly; aggregate-as-last-line invariant.
- `lib/orchestrator/lifecycle_test.go` — `RunOne` creates/removes a `blocking:false` entry; aggregate present in both stdout and `signals.log`; `defer` runs on prepare-fail and on panic.
- `lib/orchestrator/run_test.go` — deps generate their own `<run-id>/`; cascade-skipped targets create no dir.

### Integration (`skills/.../scripts/*_test.go`)

- `run-computational_test.go`: stream-mode tee correctness, single-mode aggregate-only, SIGTERM mid-command (`terminated_externally`), DAG of 2 deps producing 3 distinct dirs.
- `run-inferential_test.go`: same as computational + `HARNESS_PROMPT` exposure + `HARNESS_AGGREGATE_CONFIDENCE` downgrade reflected in stdout and `signals.log`.
- `start_test.go`: `<run-id>/` path propagated to watcher; entry has `blocking:true` + composite `run_id`.
- `list-sensors`: multiple entries per `sensor_id` displayed; orphan detection for `blocking:false`.
- `tail-sensor`: implicit resolution (1 active) vs explicit `id/run-id`; `ambiguous_run` error for multiple actives.
- `stop-sensor`: stop by `run_id`; stop by `sensor.id` prefers `blocking:true`; `--reap-dead-holders` on `blocking:false` orphan.

### Shared fixtures

`lib/testfixtures/`: new `WithRunDir(t, sensorID) (root, runID, cleanup)` that materializes a temp `Root` plus a populated `<run-id>/`. Reused across subprocess, orchestrator, and skill tests.

## Out of scope (follow-ups)

- Retention policy / pruner skill for old `<run-id>/` dirs.
- `LifecycleManager` extraction merging `RunOne` and the `/start-sensor` orchestrator into one entry point.
- `hooks/error-issue-autofiler.go` fingerprint refresh (include `blocking` and `run_id`).
