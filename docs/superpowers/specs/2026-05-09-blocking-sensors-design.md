# Blocking sensors and explicit exit design

Status: proposed
Date: 2026-05-09
Related: `schemas/sensor.json`, `schemas/signal.json`, `skills/run-sensor/`, `lib/orchestrator/`, `lib/signal/aggregate.go`, `lib/subprocess/stream.go`

## Why

Some of the most useful capabilities a coding agent can observe are not commands that finish — they are processes that stay alive: a dev server, a log tailer, a file watcher, a database in foreground mode. Today the schema has a half-formed accommodation for this (`execution.long_running: true`) that hard-caps the lifetime at `cost.latency.timeout_ms` and treats the deadline-driven kill as an *intended* termination. That model has two problems:

1. **The agent cannot work against the live process.** Once `/run-sensor watch-logs` is invoked, the runner blocks the entire skill turn until the timeout elapses. The agent cannot, in the meantime, issue a curl, edit code, run another sensor, observe the new logs, and react. The whole point of leaving a server up — exactly the way a human developer would in a separate terminal — is to *interact* while it runs.
2. **There is no explicit termination.** A developer ending a `npm run dev` types `Ctrl+C` when satisfied. The current model has only a clock: the runner kills the subprocess when the timeout strikes, regardless of whether the agent has gathered enough evidence or is still mid-investigation. There is no equivalent of "I'm done, please shut down."

This spec replaces the timeout-driven model with an explicit start / observe / stop protocol. Sensors that don't terminate on their own are reclassified as **blocking**: they are spawned by `/start-sensor`, observed incrementally via `/tail-sensor`, and shut down explicitly by `/stop-sensor`. The agent gains the same control a developer has at a terminal — and other sensors can declare blocking sensors as live dependencies that the orchestrator will start before and stop after.

## What changes

1. **`execution.blocking: bool` replaces `execution.long_running: bool`.** Default `false`. When `true`, the schema:
   - Forbids `cost.latency.timeout_ms` (blocking sensors have no hard cap on lifetime).
   - Allows a new optional `execution.graceful_timeout_ms` (default `5000`), which controls how long `/stop-sensor` waits between SIGTERM and SIGKILL.
   - Makes the sensor invalid for `/run-sensor` invocation. Only `/start-sensor` may dispatch it.
2. **Four new skills.** `/start-sensor`, `/stop-sensor`, `/tail-sensor`, `/list-sensors`. Each backed by a Go script under `skills/<skill>/scripts/`. All accept a `<sensor.id>` argument; the runner resolves to `sensors/<id>.json` by convention.
3. **`/run-sensor` accepts `<sensor.id>` instead of a path.** Same convention as the new skills. Path resolution moves into `lib/sensor/path.go` as a single resolver shared by all five skills.
4. **New `lib/registry/` subpackage.** Owns the `.runtime/sensors/` directory: state file format, atomic writes, lock files, refcount management via `held_by`, and orphan detection. Reused by the four new scripts and by the orchestrator.
5. **Orchestrator extended for live dependencies.** When a sensor's `depends_on` includes a sensor whose `execution.blocking: true`, the orchestrator starts that dep (or attaches to it if already running), runs the dependent, and stops the dep at teardown — all by reusing the same registry primitives the standalone skills use.
6. **`lib/signal/aggregate.go` rename.** The `LongRunning` field on `AggregateInput` is renamed `Blocking` and its semantics retained (timeout treated as expected termination, exit side becomes `pass/info`, stream verdict drives aggregate). Callers in `lib/orchestrator/lifecycle.go` follow.
7. **`.runtime/` directory.** New gitignored directory at the project root. Layout: `.runtime/sensors/<sensor.id>/{raw.log, signals.log}` per sensor, plus the shared `.runtime/sensors/running_sensors.json`. The `sensors/` segment under `.runtime/` keeps the namespace open for future runtime entities (e.g. inferential calibration runs, experiment outputs).

## Architecture

```
                                  ┌──────────────────────────────┐
                                  │ /start-sensor <id>           │
                                  │ /stop-sensor  <id>           │
                                  │ /tail-sensor  <id> <cursor>  │
                                  │ /list-sensors                │
                                  │ /run-sensor   <id>           │
                                  └──────────────┬───────────────┘
                                                 │
                                                 ▼
                       ┌──────────────────────────────────────────┐
                       │ skills/<skill>/scripts/<command>.go      │
                       │  (thin CLI: arg parse, signal emit)      │
                       └──────────────┬───────────────────────────┘
                                      │
              ┌───────────────────────┼─────────────────────────┐
              ▼                       ▼                         ▼
   ┌─────────────────────┐ ┌────────────────────┐  ┌────────────────────┐
   │ lib/sensor/path.go  │ │ lib/registry/       │  │ lib/orchestrator/  │
   │  resolves <id> →    │ │  state.json IO,     │  │  resolves chain,   │
   │  sensors/<id>.json  │ │  flock, held_by,    │  │  starts/attaches   │
   │                     │ │  PID lifeness check │  │  blocking deps,    │
   └─────────────────────┘ └────────────────────┘  │  stops at teardown │
                                      │            └─────────┬──────────┘
                                      │                      │
                                      ▼                      ▼
                       ┌─────────────────────────────────────────────┐
                       │ .runtime/sensors/                           │
                       │   running_sensors.json   (global state)     │
                       │   running_sensors.lock   (flock)            │
                       │   <sensor.id>/                              │
                       │     raw.log              (subprocess output)│
                       │     signals.log          (parsed JSONL)     │
                       └─────────────────────────────────────────────┘

                                      ▲
                                      │ tail -f, parse, append
                                      │
                       ┌──────────────┴──────────────┐
                       │ watcher (detached process)  │
                       │ spawned by /start-sensor    │
                       │ killed by /stop-sensor      │
                       └─────────────────────────────┘
```

### Lifecycle: standalone `/start-sensor watch-logs`

1. Resolve `sensors/watch-logs.json`, validate against `schemas/sensor.json`, require `execution.blocking: true`.
2. `flock(.runtime/sensors/running_sensors.lock)`, read `running_sensors.json`. If `watch-logs` already present and PID alive → emit Signal `verdict=error`, `metadata.kind=start_rejected`, `evidence` references the live entry. Release lock, exit 1.
3. Run `execution.prepare[]` fail-fast. If any step fails → emit Signal `verdict=error`, `metadata.kind=start_failed`, `metadata.lifecycle.prepare` carries per-step results. No registry entry created. Exit 1. Teardown does **not** run for a never-started subprocess (consistent with the rule "teardown is finally for the command").
4. `os.StartProcess` the command via `sh -c`, with `Setsid: true` and `Setpgid: true` (so the watcher can SIGTERM the whole group later). Stdout and stderr redirected to `.runtime/sensors/watch-logs/raw.log` (append).
5. Spawn the watcher as a detached `os.StartProcess` invocation of the same Go binary with a hidden subcommand (`--watcher-mode`). Pass via env vars: `HARNESS_WATCHER_RAW=<raw.log>`, `HARNESS_WATCHER_SIGNALS=<signals.log>`, `HARNESS_WATCHER_PATTERNS=<json>`, `HARNESS_WATCHER_ENVELOPE=<json>`. Watcher uses `fsnotify` to follow `raw.log`, applies `signal.MatchLine` to each line, appends matches to `signals.log`.
6. Append entry to `running_sensors.json` (sensor.id, pid, pgid, watcher_pid, started_at, command, `held_by: ["manual"]`). Write atomically (`.tmp` + rename). Release lock.
7. Emit Signal `verdict=pass`, `metadata.kind=started`, payload includes `pid`, `started_at`, `log_dir`, and `next_cursor: 0`. Exit 0.

### Lifecycle: `/tail-sensor watch-logs 12`

1. Lookup in `running_sensors.json`. Not found → emit Signal `verdict=error`, `metadata.kind=tail_not_running`. Exit 1.
2. Read `.runtime/sensors/watch-logs/signals.log` line by line, skipping the first 12. Each line is already a valid Signal — re-emit verbatim on stdout.
3. Emit a final tail-envelope Signal: `verdict=pass`, `metadata.kind=tail_envelope`, `metadata.next_cursor=<total_lines_at_read>`, `metadata.sensor_id="watch-logs"`. This stays a valid Signal (signal.json keeps `metadata` open).
4. Exit 0. Cursor is **never persisted** by the script; the agent passes whatever cursor value it received last.

### Lifecycle: `/stop-sensor watch-logs`

1. Lookup. Not found → Signal `verdict=warn`, `metadata.kind=stop_not_running`. Exit 0 (idempotent — stopping nothing is fine).
2. `flock`. Remove `"manual"` from `held_by` (or whichever caller token applies; here it's the explicit user invocation). If `held_by` still non-empty → Signal `verdict=warn`, `metadata.kind=stop_held`, evidence enumerates remaining holders. Release lock. Exit 0. Process keeps running.
3. `held_by` empty → SIGTERM the process group (`syscall.Kill(-pgid, SIGTERM)`).
4. Wait up to `execution.graceful_timeout_ms` (default 5000) for `wait()`. If still alive → SIGKILL the group, mark `metadata.killed_forcefully=true` (warn evidence on aggregate).
5. Signal the watcher process to drain (SIGTERM to its PID). Wait up to 1s for it to exit cleanly. If still alive → SIGKILL. If watcher had already died → mark `metadata.watcher_died=true` (warn evidence) and do an inline drain: read `raw.log` from where `signals.log` left off, apply patterns, append remaining matches to `signals.log`.
6. Run `execution.teardown[]` best-effort. Per-step results fold into `metadata.lifecycle.teardown` exactly as today.
7. Read all of `signals.log`, compute aggregate via `signal.Aggregate(AggregateInput{Blocking: true, ...})` — same path as today's blocking aggregate, but driven by registry data (no `TimedOut` flag, since timeout no longer applies).
8. Emit aggregate Signal as JSONL. Aggregate's `metadata.kind="aggregate"`, `metadata.output_mode` echoes `sensor.output`, `metadata.counts` is the histogram across all individuals.
9. Remove the entry from `running_sensors.json` atomically. Release lock. **Do not** delete `.runtime/sensors/watch-logs/` (auditable). Exit 0.

### Lifecycle: `/list-sensors`

1. `flock(running_sensors.lock)` (shared lock — multiple lists can run; only writes are exclusive).
2. Read `running_sensors.json`.
3. For each entry, `kill(pid, 0)` to check liveness. If dead → annotate `state="orphan"`. Watcher pid checked the same way; annotated separately.
4. Emit a single Signal `verdict=pass`, `metadata.kind=list`, `metadata.entries=<array of summaries>`. Each summary: `sensor_id`, `pid`, `pid_alive`, `watcher_pid`, `watcher_alive`, `started_at`, `command`, `held_by`, `signals_log_path`. Exit 0.

### Lifecycle: dep B has `depends_on: ["run-project"]` where run-project is blocking

`/run-sensor B` flow (extended `lib/orchestrator/`):

1. Resolve `depends_on` transitive closure (existing logic).
2. Topological sort (existing logic).
3. For each dep S in order:
   - **non-blocking S** → `RunOne(S)` as today.
   - **blocking S** →
     - `flock`, lookup S in `running_sensors.json`.
     - If S not present → run prepare, fork+exec, spawn watcher, write entry with `held_by: [B.id]`. Equivalent of `/start-sensor S` minus the user-facing emit.
     - If S already present and PID alive → append `B.id` to `held_by`. **Attach** semantics. Skip prepare (it already ran).
     - If S present but PID dead (orphan) → emit cascade-style Signal `verdict=error`, `metadata.kind=dep_orphan`, abort: B and all dependents skipped.
     - On any failure to start → emit `verdict=error`, `metadata.kind=dep_start_failed`. Cascade applies.
     - Release lock. Emit Signal `verdict=pass`, `metadata.kind=dep_attached` (or `dep_started`).
4. Run B normally via existing `RunOne`. B's individuals and aggregate stream as today.
5. **Always** at the end (success, fail, cascade): walk the `live_deps_stack` in reverse. For each:
   - `flock`, remove `B.id` from `held_by`.
   - If `held_by` now empty → run the same SIGTERM/wait/SIGKILL/teardown/aggregate sequence as `/stop-sensor`. Aggregate goes into the JSONL stream **before** B's aggregate.
   - If `held_by` still non-empty → emit Signal `verdict=pass`, `metadata.kind=dep_detached`. Process stays alive for the other holders.
6. B's aggregate is the last JSONL line on stdout (contract preserved).

### Edge cases

- **Watcher crashes mid-run.** Detected at `/stop-sensor` time; inline drain recovers any unparsed lines from `raw.log`. Aggregate flagged `watcher_died=true`, severity warn, verdict not downgraded.
- **Subprocess exits on its own before /stop-sensor.** Watcher will continue tailing `raw.log` (which stops growing) and stay alive. `/stop-sensor` detects PID dead, drains watcher, and computes aggregate from `signals.log`. Aggregate uses the recorded exit code (read from `wait()` ghost in registry — recorded by a tiny reaper goroutine in the watcher) and applies `exit_code_map`. If exit code unavailable → aggregate falls back to stream verdict, with `metadata.exit_code_unknown=true`.
- **Orchestrator crashes between attaching a dep and running B.** Dep is left with B in `held_by`. B's run never completes. Next `/list-sensors` shows the orphan-style state (B not running, but holding the dep). `/stop-sensor <dep>` from the user fails with `stop_held` until they manually `--release-holder` (deferred — see "out of scope").
- **Concurrent `/start-sensor` of the same id.** flock serializes; second invocation reads the new state and rejects with `already running`.
- **Concurrent `/stop-sensor` of the same id.** First wins the flock and removes the entry; second sees `not_running` and exits warn (idempotent).
- **`graceful_timeout_ms` set very high (e.g. 60s) on a stuck process.** The agent's tool-use turn includes `/stop-sensor`. If the runner blocks 60s, the agent's turn waits. Acceptable: the agent author chose the timeout. No async background stop.

## Schema changes

Concrete diffs against `schemas/sensor.json`:

```diff
 "execution": {
   "type": "object",
   "properties": {
-    "long_running": {
-      "type": "boolean",
-      "default": false,
-      "description": "When true, the command is expected to run continuously..."
-    },
+    "blocking": {
+      "type": "boolean",
+      "default": false,
+      "description": "When true, the sensor does not terminate on its own and must be invoked via /start-sensor / /stop-sensor. Forbids cost.latency.timeout_ms; allows execution.graceful_timeout_ms. Other sensors may declare a blocking sensor in depends_on; the orchestrator will start (or attach to) the blocking dep before the dependent runs and stop it at teardown when no other dependent holds it."
+    },
+    "graceful_timeout_ms": {
+      "type": "integer",
+      "minimum": 1,
+      "default": 5000,
+      "description": "Time to wait between SIGTERM and SIGKILL on /stop-sensor. Applies only when execution.blocking is true."
+    },
     ...
   }
 }
```

A new `if/then` branch in the top-level `allOf`:

```json
{
  "if":   { "properties": { "execution": { "properties": { "blocking": { "const": true } } } } },
  "then": {
    "properties": {
      "cost": { "properties": { "latency": { "not": { "required": ["timeout_ms"] } } } }
    }
  }
}
```

The existing `if/then` for `cost.latency.timeout_ms` (currently always required for computational) is narrowed to `blocking: false`.

`signal.json` is unchanged. New `metadata.kind` values (`started`, `start_rejected`, `start_failed`, `tail_envelope`, `tail_not_running`, `stop_not_running`, `stop_held`, `dep_attached`, `dep_started`, `dep_detached`, `dep_orphan`, `dep_start_failed`, `list`) are conventions; `metadata` remains free-form per signal.json.

## Library changes

### New: `lib/registry/`

Files (one action/aspect per file, regra 9):

- `state.go` — `RunningSensorEntry` struct, `LoadRunningSensors()`, `SaveRunningSensors()` (atomic temp+rename).
- `lock.go` — `WithFileLock(path string, fn func() error) error` using `flock(2)`.
- `liveness.go` — `IsPIDAlive(pid int) bool` via `kill(pid, 0)`.
- `held_by.go` — `Add(entry, holder)`, `Remove(entry, holder)`, `IsEmpty(entry)`.
- `paths.go` — resolves `.runtime/sensors/...` paths from project root.

All exported functions take a `RegistryRoot` parameter (default `<cwd>/.runtime/sensors/`) so tests can use temp dirs.

### Extended: `lib/sensor/`

- `path.go` gains `ResolveByID(id string) (string, error)` returning `sensors/<id>.json` or an error.
- `load.go` already loads from a path; called after `ResolveByID`.

### Extended: `lib/orchestrator/`

- New `live_deps.go`: `AttachLiveDep(...)`, `DetachLiveDep(...)`. Both take `*registry.RegistryRoot`.
- `lifecycle.go` extended with `RunOneWithLiveDeps(...)` that wraps `RunOne` with the attach/detach stack.
- The standalone scripts in `skills/start-sensor/scripts/` etc. import `lib/orchestrator/live_deps.go` directly so the start/attach logic has a single implementation.

### Renamed: `lib/signal/aggregate.go`

- `AggregateInput.LongRunning` → `AggregateInput.Blocking`.
- Comment block updated. Behavior unchanged (timeout treatment kept; for blocking sensors `TimedOut` is now always false because there is no timeout, but the field stays for non-blocking cases that still time out).

### Extended: `lib/subprocess/`

- New `detach.go`: `SpawnDetached(StreamConfig) (DetachResult, error)` for `Setsid: true`, `Setpgid: true`, redirected stdout/stderr to a file. Used by `/start-sensor` only; existing `StreamSubprocess` is unchanged.
- New `watcher.go`: the watcher mode entrypoint. Reads env vars, follows `raw.log` with `fsnotify`, applies `signal.MatchLine`, appends matched signals to `signals.log`. Exits cleanly on SIGTERM.

## Skills changes

### Modified: `skills/run-sensor/`

- SKILL.md updated: argument is `<sensor.id>`. New "Refusing blocking sensors" section: if `execution.blocking: true`, the runner exits 2 with a message pointing the user to `/start-sensor`.
- `run-computational.go` and `run-inferential.go` use `lib/sensor/path.go` to resolve `<id>` → path. They also use `lib/orchestrator/RunOneWithLiveDeps` instead of `RunOne` directly, so blocking deps are honored automatically.

### New: `skills/start-sensor/`

- `SKILL.md`: argument `<sensor.id>`, validates `blocking: true`, fails on `cost.latency.timeout_ms` mismatch, emits `started` Signal.
- `scripts/start.go` (`//go:build start_sensor`): the implementation as described above.
- `scripts/start_test.go`: table-driven coverage of accept/reject paths, registry interactions, watcher spawn.

### New: `skills/stop-sensor/`

- `SKILL.md`: argument `<sensor.id>`, idempotent, returns aggregate Signal.
- `scripts/stop.go` (`//go:build stop_sensor`).
- `scripts/stop_test.go`: covers held_by refcount, graceful → SIGKILL escalation, watcher_died inline drain, teardown.

### New: `skills/tail-sensor/`

- `SKILL.md`: arguments `<sensor.id> <cursor>`, returns JSONL Signals + tail-envelope.
- `scripts/tail.go` (`//go:build tail_sensor`).
- `scripts/tail_test.go`: covers cursor=0, cursor=mid, cursor>EOF, missing run.

### New: `skills/list-sensors/`

- `SKILL.md`: no arguments, returns `list` Signal with all live entries.
- `scripts/list.go` (`//go:build list_sensors`).
- `scripts/list_test.go`: covers empty registry, multiple entries, orphan annotation.

### Watcher binary

The watcher does not have its own SKILL.md (not user-facing). It lives at `skills/start-sensor/scripts/watcher.go` (`//go:build start_sensor_watcher`) and is invoked by `start.go` via `os.StartProcess` with the build-tag-built binary. Tests at `skills/start-sensor/scripts/watcher_test.go`.

## File contracts

### `.runtime/sensors/running_sensors.json`

```json
{
  "version": 1,
  "entries": [
    {
      "sensor_id": "run-project",
      "pid": 12345,
      "pgid": 12345,
      "watcher_pid": 12346,
      "started_at": "2026-05-09T15:30:00Z",
      "command": "npm run dev",
      "held_by": ["manual", "smoke-tests"],
      "log_dir": ".runtime/sensors/run-project"
    }
  ]
}
```

Atomic write: marshal, write to `running_sensors.json.tmp`, `os.Rename`. flock held during read-modify-write.

### `.runtime/sensors/<sensor.id>/raw.log`

Append-only. Subprocess's stdout+stderr concatenated. No structure assumed.

### `.runtime/sensors/<sensor.id>/signals.log`

Append-only. Each line is a complete Signal JSON. Lines are 1-based for cursor purposes (cursor=0 means "I have not read any lines yet"; first call returns lines 1..N, next_cursor=N).

### `.gitignore`

```
.runtime/
```

## Out of scope

Deferred to follow-up specs. Each is a real concern but adds scope this design can do without:

- **Log rotation / disk pressure.** `raw.log` and `signals.log` grow without bound. Cursor-based tail mitigates token cost, but disk grows. Acceptable for a tool whose users are devs running locally; needs a story for CI eventually.
- **Idle timeout / safety max lifetime.** A sensor could hang forever with no output. The user explicitly rejected `idle_timeout_ms` and `safety_max_lifetime_ms` as part of this design. A future spec could reintroduce them as opt-in fields.
- **Sentinel-driven exit (`exit_when` patterns).** Discussed and rejected for now in favor of explicit stop. Could be added as a parallel mechanism later without breaking this design.
- **Cross-session resilience.** If the agent's session dies between `/start-sensor` and `/stop-sensor`, the subprocess and watcher continue running; `/list-sensors` from a new session can see them. There is no automatic adoption or signaling — the user manually `/stop-sensor`s. Adoption protocols (e.g. a daemon) are out of scope.
- **Manual holder release.** If `held_by` contains a dead sensor (orchestrator crashed mid-run), there's no `/stop-sensor --force` or `--release-holder`. Workaround: edit `running_sensors.json` by hand. A force-flag is straightforward to add later.
- **Multi-host / multi-machine.** `.runtime/` is local-fs only. No remote registry.
- **`detect-sensors` updates for blocking sensors.** Skill should learn to detect when an archetype implies a blocking sensor (dev server commands like `npm run dev`, `make watch`, `cargo watch`). Tracked separately.

## Migration

- `execution.long_running` is removed without alias. Search confirmed no sensor in `sensors/` uses it; only the schema description and `lib/signal/aggregate.go` reference it. Plugin version bumps (`.claude-plugin/plugin.json`).
- `lib/signal/aggregate.go::AggregateInput.LongRunning` → `Blocking`. All callers (`lib/orchestrator/lifecycle.go`, `lib/signal/aggregate_test.go`) updated in the same PR.
- Existing sensors are unaffected (none used `long_running`). All current tests continue to pass.
- `/run-sensor` argument change (`<path>` → `<id>`) requires updating `skills/run-sensor/SKILL.md` and the two runner scripts. Path-style argument is removed in the same PR (consistent contract, no compatibility burden).

## Testing strategy

- **Unit:** every new Go file has a `_test.go` neighbor (regra 8). `lib/registry/` tests use temp dirs; flock tests fork goroutines and assert serialization.
- **Integration:** new fixture sensors under `sensors/fixtures/`:
  - `blocking-echo-loop.json` — `while true; do echo TICK; sleep 0.5; done`. Pattern matches `TICK` → individual signal. Used to test the full start/tail/stop dance.
  - `consumer-of-blocking.json` — non-blocking sensor with `depends_on: ["blocking-echo-loop"]`. Asserts dep attach/detach and that `blocking-echo-loop` actually runs concurrently.
- **Orphan paths:** test that killing the watcher between start and stop produces `watcher_died=true` aggregate. Test that killing the subprocess externally produces `exit_code_unknown=true` if the watcher's reaper missed it.
- **Concurrency:** spawn two `/start-sensor` invocations in goroutines; assert exactly one wins and the other gets `already running`.

## Implementation sequence (suggested for the plan)

1. `lib/registry/` (state, lock, liveness, held_by, paths) + tests.
2. `lib/sensor/path.go::ResolveByID` + tests; update `/run-sensor` to use it (no schema change yet — feature-flagged on a `<id>` heuristic that detects path-style for backward compat during the PR).
3. `lib/subprocess/detach.go` + `watcher.go` + tests.
4. `skills/start-sensor/`, `skills/stop-sensor/`, `skills/tail-sensor/`, `skills/list-sensors/` + tests.
5. Schema bump: rename `long_running` → `blocking`, add `graceful_timeout_ms`, narrow `cost.latency.timeout_ms` requirement. Schema validation tests updated.
6. `lib/signal/aggregate.go` rename + callers.
7. `lib/orchestrator/live_deps.go` + integration with `RunOne`.
8. Update `/run-sensor` SKILL.md and scripts to refuse blocking sensors and to honor live deps.
9. Fixture sensors + integration tests.
10. Plugin version bump.
