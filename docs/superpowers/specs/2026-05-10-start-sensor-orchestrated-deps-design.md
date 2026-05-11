# /start-sensor orchestrated dependencies design

Status: proposed
Date: 2026-05-10
Related: `skills/start-sensor/`, `skills/stop-sensor/`, `skills/tail-sensor/`, `lib/orchestrator/`, `schemas/signal.json` (free-form metadata only — no schema diff)

Issue: [#7](https://github.com/iurykrieger/harness-framework/issues/7)

## Why

`/start-sensor` today does not honor `depends_on` and does not run `execution.prepare[]` of its target. The runner — `skills/start-sensor/scripts/start.go::runStart` — proceeds straight from schema-validate → singleton check → flock + spawn detached + spawn watcher + registry write. The orchestrator (`lib/orchestrator/dag.go::Resolve`, `lib/orchestrator/live_deps.go::AttachLiveDep`) is fully capable of resolving a sensor's dep graph and bringing blocking deps up — but only `/run-sensor` exercises it via `lib/orchestrator/run.go::RunWithDeps`.

The practical failure mode is documented in issue #7: a project's `run-api-local` blocking sensor declares `depends_on: ["dependencies-up-docker-compose"]` (a `kind=setup` sensor that runs `make dependencies-up`, bringing Kafka and friends up). When the user invokes `/start-sensor run-api-local`, the orchestrator is bypassed: the API container is spawned without Kafka up, fails immediately on `Failed to resolve 'kafka:29092'`, and the user only learns about it when a later `/stop-sensor` drains the signals log.

The fix brings `/start-sensor` to parity with `/run-sensor`: same DAG resolution, same dep iteration, same cascade semantics. Where the two diverge is in what happens at the root — `/run-sensor` runs the root via `RunOne` (lifecycle prepare → command → teardown, blocks the agent's turn until the command finishes); `/start-sensor` runs the root's prepare, spawns the command detached, spawns a watcher, writes the registry, and returns immediately so the agent's turn is unblocked.

This spec also expands scope to align `metadata.kind` naming across the lifecycle skills (`/start-sensor`, `/stop-sensor`, `/tail-sensor`). Today some skills carry redundant prefixes (`start_failed`, `stop_held`, `tail_envelope`) that repeat the command name. The rename drops those prefixes; status values become bare verbs (`failed`, `held`, `envelope`) with `metadata.cause` discriminating within `failed`. The aggregate-shape kind (`aggregate`) and orchestrator-internal kinds (`cascade`, `dep_attached`, `dep_started`, `dep_detached`) are kept unchanged because their names describe the signal's contract, not a redundant command prefix.

## Terminology

Two distinct senses of "blocking" are used throughout this spec:

- **`execution.blocking: true|false` in the sensor JSON** — describes whether the **command** of the sensor terminates on its own. Dev servers, log tailers, foreground containers → `blocking: true`. Compilers, linters, test runners, `make`-style targets → `blocking: false`.
- **"Blocks the agent's turn"** — describes the **invoking skill**: `/run-sensor` holds the agent's tool-use turn until the subprocess exits; `/start-sensor` returns as soon as the spawn-detach is registered.

The two senses are coupled by design: `/run-sensor` only accepts roots with `execution.blocking: false` (the alternative would block the agent's turn forever); `/start-sensor` only accepts roots with `execution.blocking: true` (the alternative — running a command that finishes on its own as a detached background process — is what `/run-sensor` is for).

When this spec says "blocking dep" / "blocking root", it always means `execution.blocking: true`. The agent-turn sense is referred to verbatim ("blocks the agent's turn").

## What changes

1. **New `lib/orchestrator/preflight.go`** — exports `RunDeps` (the loop currently inlined in `run.go::RunWithDeps`, factored out as a reusable building block) and `RunPreparePhase` (Phase 1 of `lifecycle.go::RunOne`, exposed so `/start-sensor` can run only prepare without the rest of the lifecycle).
2. **`lib/orchestrator/run.go::RunWithDeps` refactored** to call `RunDeps` for the dep loop and handle the root via `RunOne` after — same external behavior, ~40 lines deduplicated.
3. **`lib/orchestrator/live_deps.go::AttachLiveDep` signature change** — accepts `holderPID int` explicitly instead of hardcoding `os.Getpid()`. Existing call site in `run.go` passes `os.Getpid()`. Bonus: when adding a new holder, the function now reaps any pre-existing holder of the same `(kind=sensor, id=holderID)` whose pid is dead.
4. **New `lib/orchestrator/live_deps.go::RebindDepHolderPID`** — atomically updates a holder's pid in `held_by`. Used by `/start-sensor` after the root subprocess spawn to swap the placeholder pid for the actual subprocess pid.
5. **`skills/start-sensor/scripts/start.go::runStart` rewritten** as a linear composition: resolve → schema-validate → reject if non-blocking → `RunDeps` (deps + cascade detection) → `RunPreparePhase` of root → flock + singleton check + spawn detached + spawn watcher + registry write → rebind dep holder pids → emit `started` Signal.
6. **`metadata.kind` rename across lifecycle skills.** Drop redundant command-name prefixes from terminal kinds.
7. **`SKILL.md` updates** for `/start-sensor`, `/stop-sensor`, `/tail-sensor` reflecting the new kinds and (for start-sensor) the new pre-flight phases.
8. **Plugin version bump.** `metadata.kind` rename is a contract change; bump minor in `.claude-plugin/plugin.json`.

`schemas/sensor.json` and `schemas/signal.json` are **not** modified. `metadata` is free-form per `signal.json`; the rename is a convention change consumed by callers (heal-sensor classifier, list parsers).

## Architecture

```
                           ┌─────────────────────┐
                           │ /start-sensor <id>  │
                           └──────────┬──────────┘
                                      │
                                      ▼
                  ┌────────────────────────────────────────┐
                  │ skills/start-sensor/scripts/start.go   │
                  │  runStart:                              │
                  │   1. resolve + schema-validate root     │
                  │   2. assert execution.blocking=true     │
                  │   3. RunDeps(targetID, holderPID=Getpid)│
                  │   4. RunPreparePhase(root)              │
                  │   5. flock + spawn detach + watcher +   │
                  │      registry write (existing block)    │
                  │   6. RebindDepHolderPID for each in     │
                  │      LiveStack (Getpid → subproc PID)   │
                  │   7. emit `started` Signal              │
                  │  on any failure ≥ step 3:               │
                  │   detachAll(LiveStack) before return    │
                  └─────┬──────────────────────────────┬───┘
                        │                              │
                        ▼                              ▼
            ┌────────────────────────┐     ┌──────────────────────┐
            │ lib/orchestrator/       │     │ lib/registry/         │
            │  preflight.go::         │     │  WithFileLock         │
            │   RunDeps               │     │  Load / Save          │
            │   RunPreparePhase       │     │  IsPIDAlive           │
            │  live_deps.go::         │     │  AddHolder /          │
            │   AttachLiveDep         │     │   RemoveHolder        │
            │   DetachLiveDep         │     └──────────────────────┘
            │   RebindDepHolderPID    │
            │  run.go::               │
            │   RunWithDeps           │ ◄── /run-sensor still uses
            │  lifecycle.go::         │     this; refactored to use
            │   RunOne                │     RunDeps internally
            │  cascade.go::           │
            │   BuildCascadeSignal    │
            │   FirstFailedDep        │
            └────────────────────────┘
```

### Lifecycle of `/start-sensor run-api-local` with `depends_on: ["dependencies-up-docker-compose"]`

1. **Resolve & schema-validate root.** `libsensor.ResolveByID("run-api-local", projectRoot)` → `sensors/run-api-local.json`. Load JSON. Validate against `schemas/sensor.json`. On any failure: emit `failed` with `metadata.cause` ∈ {`resolve_failed`, `schema_invalid`}, exit 1–2.
2. **Assert `execution.blocking: true`.** Otherwise emit `failed` with `metadata.cause=not_blocking`, exit 2.
3. **Pre-flight via `RunDeps`.**
   - `Resolve("run-api-local", projectRoot)` returns `[dependencies-up-docker-compose, run-api-local]`.
   - Validate every sensor in the graph against `schemas/sensor.json`. Failure → `RunDepsResult{ExitCode: 1}`.
   - Iterate topo order, **skipping** the root:
     - `dependencies-up-docker-compose` is `kind=setup`, non-blocking → call `RunOne`. Aggregate signal emitted on stdout. If verdict ∈ {fail, error} → record in `Signals`, set `CascadeSig` for the root via `BuildCascadeSignal`, but continue iterating (other deps would also cascade if there were any).
   - For each blocking dep in the graph: `AttachLiveDep` with `holderID="run-api-local"`, `holderPID=os.Getpid()` (placeholder). Push dep id onto `LiveStack`. Each call emits `dep_attached` or `dep_started` to stdout.
   - Return `RunDepsResult{Order, Signals, LiveStack, CascadeSig, ExitCode: 0}`.
4. **Cascade check.** If `pre.CascadeSig != nil`, build a `failed` signal with `metadata.cause=dep_cascade`, copying `failed_dep_id`/`failed_dep_run_id`/`failed_dep_verdict`/`failed_dep_severity` from `pre.CascadeSig.metadata`. Emit. `detachAll(LiveStack)`. Exit 1.
5. **Run root's `prepare[]` fail-fast** via `RunPreparePhase`. On failure: emit `failed`, `metadata.cause=prepare_failed`, `metadata.lifecycle.prepare=results`. `detachAll(LiveStack)`. Exit 1.
6. **Flock + singleton + spawn + watcher + registry write.** Existing `start.go` block (lines 113–191), unchanged in shape. On `alreadyRunning`: `detachAll(LiveStack)` before emitting `rejected`. On spawn/watcher/registry I/O failure: emit `failed` with appropriate `metadata.cause`, `detachAll(LiveStack)`, exit 1.
7. **Rebind dep holder pids.** For each `depID` in `pre.LiveStack`, call `RebindDepHolderPID(depID, projectRoot, "run-api-local", oldPID=os.Getpid(), newPID=spawned.det.PID)`. Errors are not fatal — append to `metadata.rebind_warnings` and continue. The root subprocess is up; the holder pid mismatch is an audit-only concern.
8. **Emit `started`.** `metadata.kind="started"`, `metadata.lifecycle.prepare=prepResults`, `metadata.dep_chain=pre.LiveStack`, `metadata.rebind_warnings=warnings` (omitted if empty), `metadata.pid`/`metadata.watcher_pid`/`metadata.log_dir`/`metadata.next_cursor` per existing contract. Exit 0.

### Why the holder pid is the root subprocess pid (not start.go's pid)

In `/run-sensor`'s `RunWithDeps`, the holder pid is `os.Getpid()` — the run-sensor process pid — because run-sensor stays alive for the entire dependent's lifetime. When run-sensor exits, the deferred `DetachLiveDep` runs and the dep is properly detached.

In `/start-sensor`, the start.go process exits within milliseconds of the spawn (returns control to the agent's turn). If we used `os.Getpid()` as the holder pid, the holder would be dead immediately. `/list-sensors` would always show the dep as having a stale holder; `/stop-sensor dep` would always require `--reap-dead-holders`.

Instead, the holder pid is the root subprocess pid (e.g., the `docker compose up` pid). This pid stays alive as long as the root sensor is running. When the user later `/stop-sensor run-api-local`s the root, its pid dies, the dep's holder becomes stale, and `/stop-sensor dep --reap-dead-holders` cleans it up. The relationship is now correctly modeled: the dep is held by the actual process that needs it.

The two-phase mechanic exists because the dep must come up before the root spawn (otherwise the root tries to connect to a not-yet-ready Kafka). At dep-attach time we don't yet know the root subprocess pid. We use `os.Getpid()` as a placeholder and swap it post-spawn via `RebindDepHolderPID`.

## Library changes

### New: `lib/orchestrator/preflight.go`

```go
// Package orchestrator preflight functions: dependency resolution and
// per-phase lifecycle execution exposed for callers that need finer control
// than RunOne (notably /start-sensor, which must run only prepare and then
// hand off to a detached spawn).

// RunDepsResult carries the post-pre-flight state for the caller to decide
// the root's fate.
type RunDepsResult struct {
    // Order is the topo-sorted DAG (root last). Always populated when ExitCode==0.
    Order []Sensor

    // Signals maps non-root sensor id → its emitted signal (RunOne aggregate,
    // AttachLiveDep ack, or BuildCascadeSignal for skipped deps).
    Signals map[string]map[string]interface{}

    // LiveStack is the ordered list of blocking dep ids that AttachLiveDep
    // succeeded on. Caller iterates in reverse for detach.
    LiveStack []string

    // CascadeSig is non-nil when a dep of the root produced fail/error and
    // the root would cascade. Caller emits and detaches LiveStack.
    CascadeSig map[string]interface{}

    // ExitCode: 0 ok, 1 DAG/schema failure, 2 io error.
    ExitCode int
}

// RunDeps resolves targetID's depends_on graph, validates every sensor
// against schemas/sensor.json, and iterates topologically — emitting
// per-dep aggregate (non-blocking via RunOne) or attach acks (blocking via
// AttachLiveDep). Cascade signals for intermediate deps are emitted on
// stdout during the loop. The root is NOT processed; caller handles it.
//
// Intermediate cascade: a non-blocking dep whose own dep failed gets a
// cascade signal emitted in stdout (metadata.kind=cascade), recorded in
// Signals, and processing continues. The cascade chain propagates: any
// dependent of the cascade-marked dep also cascades.
//
// Root cascade: when iteration finishes, if FirstFailedDep returns non-nil
// for the root sensor, BuildCascadeSignal is built but NOT emitted —
// returned in CascadeSig so the caller can wrap it (e.g. /start-sensor
// translates it to a `failed` signal with `metadata.cause=dep_cascade`).
func RunDeps(
    ctx context.Context,
    targetID, projectRoot, schemasDir, holderID string,
    holderPID int,
    v *schema.Validator,
    stdout, stderr io.Writer,
) *RunDepsResult
```

```go
// RunPreparePhase runs sensor.execution.prepare[] fail-fast. Returns the
// per-step results (shaped for inclusion in metadata.lifecycle.prepare)
// and a bool indicating whether the phase failed (first non-pass step).
//
// Extracted from lifecycle.go::runLifecyclePhase("prepare", failFast=true).
// No stdout/stderr emission — the caller chooses how to surface the
// results in its terminal signal.
func RunPreparePhase(ctx context.Context, target Sensor, defaultTimeoutMS int) (results []interface{}, failed bool)
```

### Modified: `lib/orchestrator/live_deps.go::AttachLiveDep`

Signature gains `holderPID int`:

```go
func AttachLiveDep(
    ctx context.Context,
    dep Sensor,
    projectRoot, holderID string,
    holderPID int,                // NEW
    v *schema.Validator,
    stdout, stderr io.Writer,
) (string, error)
```

Behavior addition: when adding the new holder, scan `dep.held_by` for entries `(kind="sensor", id=holderID, pid=*)` whose pid is dead per `IsPIDAlive`. Remove them from the slice before adding the new holder. This keeps held_by from accumulating dead duplicates of the same logical holder across re-runs.

### New: `lib/orchestrator/live_deps.go::RebindDepHolderPID`

```go
// RebindDepHolderPID atomically updates the pid of a holder in
// dep.held_by. Match by (kind="sensor", id=holderID, pid=oldPID); if
// found, swap to newPID. Idempotent: no match → silent no-op (no error).
//
// Used by /start-sensor after spawning the root subprocess to swap the
// placeholder pid (os.Getpid() of start.go) for the actual subprocess pid.
func RebindDepHolderPID(depID, projectRoot, holderID string, oldPID, newPID int) error
```

Implementation: `registry.WithFileLock` + `registry.Load` + find dep entry + find holder + swap pid + `registry.Save`.

### Modified: `lib/orchestrator/run.go::RunWithDeps`

Refactored to delegate the dep loop to `RunDeps`. External behavior preserved (same signal order on stdout, same cascade semantics, same exit codes).

```go
func RunWithDeps(ctx context.Context, sensorPath, schemasDir string, stdout, stderr io.Writer) int {
    abs, err := filepath.Abs(sensorPath)
    if err != nil { fmt.Fprintln(stderr, "error: abs path:", err); return 2 }
    projectRoot := filepath.Dir(filepath.Dir(abs))
    targetID := StripJSONExt(filepath.Base(abs))

    v, code := schema.LoadValidator(schemasDir, stderr)
    if code != 0 { return code }

    holderPID := os.Getpid()
    pre := RunDeps(ctx, targetID, projectRoot, schemasDir, targetID, holderPID, v, stdout, stderr)

    defer func() {
        for i := len(pre.LiveStack) - 1; i >= 0; i-- {
            DetachLiveDep(pre.LiveStack[i], projectRoot, targetID, v, stdout, stderr)
        }
    }()

    if pre.ExitCode != 0 { return pre.ExitCode }
    if pre.CascadeSig != nil {
        if err := v.Validate(schema.TargetSignal, pre.CascadeSig); err != nil {
            schema.PrintValidationOrPlain(err, stderr); return 1
        }
        _ = json.NewEncoder(stdout).Encode(pre.CascadeSig)
        return 1
    }

    target := pre.Order[len(pre.Order)-1]
    _, code = RunOne(ctx, target, schemasDir, v, stdout, stderr)
    return code
}
```

## Output contract: `/start-sensor`

### Signals emitted on stdout (in order)

1. (during `RunDeps`) Aggregates of `kind=setup` or non-blocking deps that ran via `RunOne` (`metadata.kind="aggregate"`).
2. (during `RunDeps`) Acks of blocking deps (`metadata.kind` ∈ {`dep_attached`, `dep_started`}).
3. (during `RunDeps`) Cascade signals for intermediate deps that were skipped (`metadata.kind="cascade"`).
4. (at end) **EXACTLY ONE** terminal signal whose `metadata.kind` ∈ {`started`, `failed`, `rejected`}.

### Terminal signal shapes

**`metadata.kind="started"`** (verdict=pass, severity=info)

Root subprocess and watcher are up.

```json
{
  "verdict": "pass",
  "severity": "info",
  "metadata": {
    "kind": "started",
    "pid": 12345,
    "watcher_pid": 12346,
    "log_dir": ".runtime/sensors/run-api-local",
    "next_cursor": 0,
    "lifecycle": { "prepare": [] },
    "dep_chain": ["dependencies-up-docker-compose"],
    "rebind_warnings": []
  }
}
```

`lifecycle.prepare`, `dep_chain`, `rebind_warnings` are omitted from `metadata` when their values are empty.

**`metadata.kind="rejected"`** (verdict=error, severity=high)

Singleton check failed: a live entry already exists for this id in the registry. No `cause` field — single meaning.

```json
{
  "verdict": "error",
  "severity": "high",
  "metadata": {
    "kind": "rejected",
    "existing_pid": 12000
  }
}
```

**`metadata.kind="failed"`** (verdict=error, severity=high)

Anything else that prevents a `started` signal. Discriminator is `metadata.cause`:

| `metadata.cause` | Auxiliary `metadata` fields | When emitted |
|---|---|---|
| `dep_cascade` | `failed_dep_id`, `failed_dep_run_id`, `failed_dep_verdict`, `failed_dep_severity` | A dep of the root produced fail/error verdict. Built from `pre.CascadeSig`. |
| `prepare_failed` | `lifecycle.prepare` (per-step results, last is the failing step) | `RunPreparePhase` returned `failed=true`. |
| `spawn_failed` | `error_excerpt` | `os.StartProcess` of root subprocess failed. |
| `watcher_spawn_failed` | `error_excerpt` | `os.StartProcess` of watcher binary failed. |
| `registry_write_failed` | `error_excerpt` | `registry.Save` returned I/O error. |
| `schema_invalid` | `error_excerpt` | Schema validation of root sensor failed. |
| `resolve_failed` | `error_excerpt` | `libsensor.ResolveByID` failed. |
| `preflight_failed` | `error_excerpt` | `RunDeps` returned `ExitCode≠0` (DAG cycle, dep file missing, dep schema invalid). |
| `not_blocking` | — | Root sensor's `execution.blocking` is not `true`. |
| `bootstrap_failed` | `error_excerpt` | Pre-flight failures (cwd, validator init, missing arg). |

`evidence[].rationale` carries human-readable phrasing; `metadata.cause` is the stable key for programmatic routing (heal-sensor classifier, etc.).

### Single signal constructor

```go
// finalSignal builds the terminal signal of /start-sensor. cause is required
// for kind="failed" and ignored for "started"/"rejected". aux is merged into
// metadata, carrying kind-specific fields per the table above.
func finalSignal(
    id string,
    sensorJSON map[string]interface{},
    kind string,
    cause string,
    aux map[string]interface{},
    rationale string,
) map[string]interface{}
```

The current `errorSignal` (start.go lines 259–274) is removed; all paths funnel through `finalSignal`. `validateSignal` (lines 279–298) is preserved — it validates the constructed signal against `schemas/signal.json` before emission.

## `metadata.kind` rename across lifecycle skills

### Rename table

| Skill | Old `metadata.kind` | New `metadata.kind` | Notes |
|---|---|---|---|
| `/start-sensor` | `started` | `started` | unchanged |
| `/start-sensor` | `start_rejected` | `rejected` | `metadata.existing_pid` for evidence |
| `/start-sensor` | `start_failed` | `failed` | `metadata.cause` discriminator |
| `/stop-sensor` | `aggregate` | `aggregate` | **unchanged** — describes signal shape (rollup of stream individuals), not action |
| `/stop-sensor` | `stop_not_running` | `not_running` | verdict=warn |
| `/stop-sensor` | `stop_held` | `held` | verdict=warn. `metadata.held_by` lists remaining holders. |
| `/stop-sensor` | `stop_held_with_dead_holders` | `held` | **folded** into `held`. `metadata.dead_holders=[...]` (empty when none) |
| `/stop-sensor` | `stop_failed` | `failed` | `metadata.cause` discriminator |
| `/tail-sensor` | `tail_envelope` | `envelope` | verdict=pass, carries `next_cursor` |
| `/tail-sensor` | `tail_not_running` | `not_running` | verdict=error |
| `/list-sensors` | `list` | `list` | unchanged — no redundant prefix |
| `/run-sensor` | `aggregate` | `aggregate` | unchanged |
| orchestrator | `cascade` | `cascade` | unchanged |
| orchestrator | `dep_attached`, `dep_started`, `dep_detached`, `dep_orphan`, `dep_start_failed` | unchanged | `dep_` is a semantic discriminator (lifecycle of a dep), not a command prefix |
| any fallback | `signal_validation_failed`, `bootstrap_failed` | unchanged | error fallbacks |

### Why the asymmetry between `stopped` (not used) and `aggregate` (kept) for /stop-sensor success

The signal that `/stop-sensor` emits on the success path **is** an aggregate by every meaningful definition: it carries `counts` of stream individuals, `exit_code`, `output_mode`, and the verdict computed via `signal.Aggregate`. The `aggregate` kind names the **shape** of the signal — which is identical whether `/run-sensor` produces it (after `RunOne` finishes a non-blocking sensor) or `/stop-sensor` produces it (after draining a blocking sensor's signals.log on shutdown).

Renaming to `stopped` would conflate shape (aggregate) with action (stop). The shape is what consumers care about — `metadata.counts`, `metadata.exit_code` are present unconditionally for any kind=`aggregate`, and absent for terminal kinds like `started`/`rejected`/`failed` that don't roll up individuals. Keeping `aggregate` for /stop-sensor's success preserves that schema contract.

`stop_held` → `held` and `stop_not_running` → `not_running` get the rename because those names just repeat "stop" — they describe states of the stop attempt, not signal shapes.

### Signal validation

`schemas/signal.json` doesn't constrain `metadata.kind` (`metadata` is free-form). The rename doesn't require a schema diff. Plugin version bumps because consumers (heal classifier, list parsers, agent skills) read these values and a rename is a contract break.

## Edge cases

### Re-run `/start-sensor target` while target is dead, deps alive

Singleton check passes (target not in registry, or has dead pid). `RunDeps` iterates deps; `AttachLiveDep` finds the dep alive and adds a new `(kind=sensor, id=target-id, pid=os.Getpid())` holder. The reap-on-attach behavior added to `AttachLiveDep` removes any pre-existing `(kind=sensor, id=target-id, pid=DEAD)` entries from previous runs first, so held_by stays clean. Acceptance criterion #3 satisfied.

### Re-run `/start-sensor target` while target is alive

`RunDeps` runs first (paying for `kind=setup` deps, idempotent in practice — `make dependencies-up` is no-op when containers are already up). Then step 6 enters the flock callback; the singleton check happens **before** any spawn (lines 121–125 of current `start.go`), so no subprocess is created. Path: `alreadyRunning=true` (set inside the flock callback) → flock released → `detachAll(LiveStack)` removes the holders we just added → emit `rejected`. Cost is the wasted `RunOne` cycles for setup deps; acceptable given rejection is rare.

### Dep dies between `AttachLiveDep` and root spawn

The dep is in `LiveStack` but the dep's subprocess pid is dead. Two sub-cases for `RebindDepHolderPID`:

- The dep's registry entry still exists (no concurrent `/stop-sensor` reaped it). Rebind finds the holder slot and swaps pid normally; the dep entry now has a holder pointing to the live root subprocess pid, but the dep's own `pid` field is dead. `/list-sensors` will flag this with `pid_alive=false` on the dep.
- The dep's registry entry was already reaped (concurrent `/stop-sensor` removed it because all its holders were dead, our placeholder included). Rebind's idempotent contract kicks in: no match → silent no-op, no error. The root subprocess will spawn and try to connect to the dead dep, fail on its own, and surface the failure via stream individuals or aggregate.

We do not attempt mid-flight resuscitation — that would be a fragile heuristic.

### Concurrent `/start-sensor target` invocations

Both run `RunDeps` in parallel. `AttachLiveDep` is serialized by the registry flock — both successfully add their holder to the dep (different holderPID values). At the singleton stage, the flock serializes again: one wins (creates entry, rebinds holders to its subproc pid, emits `started`); the other sees `alreadyRunning` and runs `detachAll(LiveStack)` to remove its placeholder holder. Final state is consistent.

### `prepare[]` fails after fresh blocking deps were brought up

`detachAll(LiveStack)` walks reverse. For each dep where our `(kind=sensor, id=target-id, pid=os.Getpid())` holder was the only one, `DetachLiveDep` removes it, finds held_by empty, runs SIGTERM/SIGKILL on the dep subprocess, removes the dep's registry entry. End state matches pre-`/start-sensor`. Terminal signal: `failed` with `metadata.cause=prepare_failed`, `metadata.lifecycle.prepare=[steps]`.

### `RebindDepHolderPID` fails (registry I/O)

The root subprocess is already alive at this point (steps 6 → 7). We don't unwind. Append to `metadata.rebind_warnings=[{dep_id, error}]` and emit `started` with verdict=pass anyway. The cost is that the dep's holder pid stays as `os.Getpid()` (start.go's pid, dead immediately). `/list-sensors` shows pid_alive=false for that holder; `/stop-sensor dep --reap-dead-holders` cleans it later. Audit-only impact.

### DAG cycle / dep file missing / dep schema invalid

`RunDeps.ExitCode=1`. `/start-sensor` emits `failed` with `metadata.cause=preflight_failed`, `metadata.error_excerpt=<orchestrator's error>`. Exit 1.

### Watcher spawn fails

After root subprocess is up but before registry entry is fully written. The path runs `detachAll(LiveStack)`, kills the just-spawned root subprocess (via SIGTERM on its pgid since the registry write is partial), emits `failed` with `metadata.cause=watcher_spawn_failed`. This requires `start.go` to track the `det.PGID` outside the flock callback so it can be killed on this failure — already true in current code (`spawnResult.det.PGID` is captured). The kill-on-watcher-failure logic is added to the existing post-flock error branch.

### `start.go` is SIGKILL'd between `AttachLiveDep` and `RebindDepHolderPID`

No defer can run on SIGKILL. The dep's `held_by` retains the entry `(kind=sensor, id=target-id, pid=os.Getpid()_of_dead_start.go)`. The pid is dead, so:

- `/list-sensors` flags this holder with `pid_alive=false`.
- The next invocation of `/start-sensor target` (re-run after target is dead) reaches `AttachLiveDep` again. The reap-on-attach behavior added to `AttachLiveDep` removes the dead `(kind=sensor, id=target-id, pid=DEAD)` entry **before** adding the new placeholder. State self-heals on the next run; no manual intervention required.
- If the user instead does `/stop-sensor dep --reap-dead-holders` first, the dead holder is removed there.

This is the reason `AttachLiveDep` reaps stale same-id holders rather than blindly appending: combined with the SIGKILL scenario, blind append would let dead holders accumulate indefinitely across crashes.

## Testing strategy

### Acceptance criteria → tests

- **AC#1** "with setup dep PASS, sensor starts": `TestStart_WithSetupDepPASS` (start_test.go).
- **AC#2** "dep cascade emits failed before any spawn": `TestStart_WithSetupDepFAIL` — assert `metadata.kind="failed"`, `metadata.cause="dep_cascade"`, AND no registry entry / no raw.log / no subprocess pid.
- **AC#3** "re-run with alive blocking dep adds holder, doesn't relaunch": `TestStart_WithBlockingDepAttach` — pre-populate registry with alive dep, assert `dep_attached` (not `dep_started`), assert dep subprocess pid unchanged, assert held_by has 2 holders.
- **AC#4** "prepare[] of root runs": `TestStart_PrepareFAIL` (asserts `cause=prepare_failed`, `lifecycle.prepare` non-empty), and the happy-path coverage in `TestStart_WithSetupDepPASS` (asserts `lifecycle.prepare` present in `started`).
- **AC#5** "tests: setup PASS, setup FAIL, blocking ATTACH, blocking START fresh, prepare FAIL": all covered in start_test.go (see list below).

### Unit tests in `lib/orchestrator/preflight_test.go` (new file)

| Test | Scenario |
|---|---|
| `TestRunDeps_NoDeps` | Sensor without `depends_on`. `Order=[root]`, empty Signals/LiveStack, nil CascadeSig, ExitCode=0. |
| `TestRunDeps_SetupDepPASS` | Setup dep with passing command. `Signals[depID].verdict="pass"`, empty LiveStack. |
| `TestRunDeps_SetupDepFAIL` | Setup dep fails. `Signals[depID].verdict="fail"`, `CascadeSig != nil`. |
| `TestRunDeps_BlockingDepStartFresh` | Blocking dep not in registry. `LiveStack=[depID]`, registry entry has `pid=holderPID`. |
| `TestRunDeps_BlockingDepAttach` | Blocking dep alive in registry. Holder added without relaunch. Registry entry has 2 holders post. |
| `TestRunDeps_BlockingDepAttach_ReapStale` | Pre-existing `(kind=sensor, id=H, pid=DEAD)` reaped on attach. Final held_by has 1 entry, not 2. |
| `TestRunDeps_DAGCycle` | Self-referential `depends_on`. ExitCode=1. |
| `TestRunDeps_DepFileMissing` | `depends_on` references non-existent id. ExitCode=1. |
| `TestRunDeps_DepSchemaInvalid` | Dep file fails sensor.json validation. ExitCode=1. |
| `TestRunDeps_TransitiveCascade` | A → B → C; C fails; B and A both cascade. CascadeSig non-nil for the root A. |

### Unit tests in `lib/orchestrator/live_deps_test.go` (extend)

| Test | Scenario |
|---|---|
| `TestAttachLiveDep_PassesHolderPID` | Custom holderPID; registry entry's holder has that pid, not `os.Getpid()`. |
| `TestAttachLiveDep_ReapsStaleSameHolder` | Pre-existing dead `(kind=sensor, id=H, pid=999)`; AttachLiveDep with id=H removes it before adding new. |
| `TestRebindDepHolderPID_Match` | `(kind=sensor, id=H, pid=OLD)` → swap to NEW. |
| `TestRebindDepHolderPID_NoMatch` | No matching holder; idempotent no-op. |
| `TestRebindDepHolderPID_RegistryIOError` | Read-only dir; returns error. |

### Unit tests in `lib/orchestrator/lifecycle_test.go` (extend)

| Test | Scenario |
|---|---|
| `TestRunPreparePhase_AllPass` | 2 passing steps. failed=false, results length 2. |
| `TestRunPreparePhase_FirstFails_FailFast` | Step 1 fails, step 2 never runs. failed=true, results length 1. |
| `TestRunPreparePhase_NoSteps` | No `prepare` array. failed=false, results empty. |

### Integration tests in `skills/start-sensor/scripts/start_test.go` (extend)

Existing tests (`TestStart_RejectsNonBlocking`, `TestStart_RejectsAlreadyRunning`) get assertion updates: `metadata.kind == "rejected"` (was `start_rejected`), `metadata.kind == "failed"` with `cause == "not_blocking"` (was `start_failed`).

New tests:

| Test | Scenario |
|---|---|
| `TestStart_WithSetupDepPASS` | Setup dep aggregate emitted; `started` final with `lifecycle.prepare`, `dep_chain`. Registry has target entry. |
| `TestStart_WithSetupDepFAIL` | Setup dep fail aggregate emitted; `failed` final with `cause=dep_cascade`, `failed_dep_id` set. Registry has NO target entry. |
| `TestStart_WithBlockingDepStartFresh` | `dep_started` ack; `started` final. Registry has target + dep entries; dep holder pid == target subprocess pid (post-rebind). |
| `TestStart_WithBlockingDepAttach` | Pre-populate dep alive. `dep_attached` ack (not `dep_started`). Dep subprocess pid unchanged. Dep held_by has 2 holders. |
| `TestStart_PrepareFAIL` | Target with failing `prepare[0]`. `failed` with `cause=prepare_failed`, `lifecycle.prepare=[{verdict:fail}]`. No subprocess spawned, no registry entry. |
| `TestStart_PrepareFAIL_DetachesLiveStack` | Blocking dep attached fresh + target prepare fails. Dep is detached and stopped (registry has no dep entry). |
| `TestStart_PrepareSuccess_DetachesNothing` | Blocking dep alive with another holder + target prepare passes. Dep stays alive with 2 holders. |
| `TestStart_RebindWarning_DoesNotFail` | Simulate Rebind I/O error (read-only registry path mid-flight). `started` with verdict=pass; `metadata.rebind_warnings` non-empty. |

### Regression tests in `/run-sensor` runners

`skills/run-sensor/scripts/run-computational_test.go` and `run-inferential_test.go` get regression assertions to ensure the `RunWithDeps` refactor preserves: signal order in stdout, root aggregate as the LAST JSONL line, cascade signals in order, reverse detach of blocking deps in defer.

### Rename assertion tests

Each touched skill gets test updates asserting the new `metadata.kind`:

- `start_test.go`: assertions for `started`, `rejected`, `failed`.
- `stop_test.go`: assertions for `aggregate`, `not_running`, `held`, `failed`.
- `tail_test.go`: assertions for `envelope`, `not_running`.

### Fixtures

In `sensors/fixtures/`:

- `start-target-with-setup-pass.json` — blocking target, `depends_on=[setup-pass]`.
- `start-target-with-setup-fail.json` — blocking target, `depends_on=[setup-fail]`.
- `start-target-with-blocking-dep.json` — blocking target, `depends_on=[blocking-loop]`.
- `start-target-prepare-fail.json` — blocking target, `prepare[0].command="false"`.
- `setup-pass.json` — `kind=setup`, `make echo`, exit 0.
- `setup-fail.json` — `kind=setup`, `false`, exit 1.
- `blocking-loop.json` — already exists (`blocking-echo-loop.json`); reuse.

## Migration

### Plugin version

`.claude-plugin/plugin.json`: minor bump (e.g., 0.5.0 → 0.6.0). Changelog entry: "**breaking:** rename `metadata.kind` values in /start-sensor (`start_*` → bare), /stop-sensor (`stop_*` → bare), /tail-sensor (`tail_*` → bare). Add `/start-sensor` orchestrated dependency resolution and `prepare[]` execution."

### String literals to update

**`/start-sensor`:**
- `skills/start-sensor/SKILL.md` — Output contract section: replace `started`/`start_rejected`/`start_failed` with `started`/`rejected`/`failed`. Add tables for `failed` causes and signals emitted during deps. Remove the "Note: execution.prepare[] is not yet executed" disclaimer (lines 40–42 of start.go and any echo in SKILL.md).
- `skills/start-sensor/scripts/start.go` — 4 occurrences of `"start_rejected"`/`"start_failed"`; remove the docstring disclaimer.
- `skills/start-sensor/scripts/start_test.go` — 1 occurrence in assertion.

**`/stop-sensor`:**
- `skills/stop-sensor/SKILL.md` — Output contract section.
- `skills/stop-sensor/scripts/stop.go` — all `stop_*` literals; merge `stop_held_with_dead_holders` path into the `held` path with `metadata.dead_holders` field.
- `skills/stop-sensor/scripts/stop_test.go` — assertions.

**`/tail-sensor`:**
- `skills/tail-sensor/SKILL.md` — Output contract section.
- `skills/tail-sensor/scripts/tail.go` — `tail_envelope`, `tail_not_running` literals.
- `skills/tail-sensor/scripts/tail_test.go` — assertions.

**Schema/lib:** no source changes. `metadata.kind` is free-form per `signal.json`.

### Sensor JSON files

No changes — `metadata.kind` is a runtime concern, not a sensor-definition field.

## Out of scope

- **`/stop-sensor` walks `depends_on` to stop deps too.** Currently `/stop-sensor target` only stops the target. Deps brought up by `/start-sensor target` stay alive with stale holders pointing to the dead target subprocess pid. User cleans up via `/stop-sensor dep --reap-dead-holders`. A future spec could add `/stop-sensor target --cascade` or auto-cascade via the registry's holder graph.
- **`/run-sensor` and `/start-sensor` rename of all kinds to status verbs.** This spec keeps `aggregate` and `cascade` as shape descriptors. A future cleanup could either keep that distinction crisply (rename non-shape kinds to verbs everywhere) or fold everything into action verbs (rename `aggregate` to `ran`/`completed`). Out of scope here.
- **`/list-sensors` `list` → `listed`.** Cosmetic; no caller currently routes on this kind value differently than checking presence.
- **Holder lifetime models beyond `kind=sensor`.** This spec keeps the existing two-discriminator model (`manual`, `sensor`). Adding e.g. `started_for` (separate kind for /start-sensor's auto-attached deps) was considered and rejected for being more invasive than necessary.

## Implementation sequence

Each step compiles and tests in isolation; no step leaves the tree red.

1. **`lib/orchestrator/preflight.go`** — `RunDeps`, `RunPreparePhase`. Extract `runLifecyclePhase` body for prepare-only invocation. Tests in `preflight_test.go`. No callers yet — pure addition.
2. **`lib/orchestrator/live_deps.go`** — `AttachLiveDep` signature gains `holderPID int`; reap-on-attach behavior added; `RebindDepHolderPID` added. Single existing caller in `run.go::RunWithDeps` updated to pass `os.Getpid()`. Tests in `live_deps_test.go` extended.
3. **`lib/orchestrator/run.go::RunWithDeps`** — refactor to delegate to `RunDeps`. Verify regression suite (run-computational_test.go, run-inferential_test.go) passes.
4. **`/start-sensor` rewrite** — `skills/start-sensor/scripts/start.go::runStart` reorganized per Section "Lifecycle". Remove `errorSignal`, add `finalSignal`. Add `detachAll` helper. Update `start_test.go` assertions for renamed kinds and add new tests.
5. **`/start-sensor` SKILL.md** — Output contract, lifecycle phases, dep semantics. Remove the "execution.prepare[] is not yet executed" disclaimer.
6. **`metadata.kind` rename in `/stop-sensor`** — update stop.go, stop_test.go, SKILL.md. Fold `stop_held_with_dead_holders` into `held` with `dead_holders` array.
7. **`metadata.kind` rename in `/tail-sensor`** — update tail.go, tail_test.go, SKILL.md.
8. **Fixtures** — add the 4 new fixture JSON files in `sensors/fixtures/`.
9. **Plugin version bump** — `.claude-plugin/plugin.json`.

Steps 1–3 are zero-behavior-change (refactor). Step 4 is the issue's actual fix. Steps 5–9 are scope expansion (rename + docs).

## References

- Issue: [#7](https://github.com/iurykrieger/harness-framework/issues/7) — `/start-sensor: does not resolve depends_on (orchestrator bypassed)`.
- Prior art:
  - `docs/superpowers/specs/2026-05-08-sensor-dependencies-design.md` — original DAG/cascade contract.
  - `docs/superpowers/specs/2026-05-09-blocking-sensors-design.md` — blocking-sensor lifecycle and `held_by` model.
- Code:
  - `lib/orchestrator/run.go::RunWithDeps` — current dep loop (to be refactored into `RunDeps`).
  - `lib/orchestrator/live_deps.go::AttachLiveDep` — current attach logic (to gain `holderPID` parameter).
  - `lib/orchestrator/lifecycle.go::runLifecyclePhase` — current prepare runner (to be exposed as `RunPreparePhase`).
  - `skills/start-sensor/scripts/start.go::runStart` — current implementation (to be rewritten).
