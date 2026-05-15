# Blocking-dep fail-fast and truthful aggregate (closes #46)

## Problem

`lib/orchestrator/live_deps.go::stopBlockingDep` (lines 314–329) hard-codes the
aggregate Signal of a blocking dep to `verdict=pass / severity=info` whenever
the dep is torn down at detach time. The function does not consult the
subprocess's exit state, the dep's `raw.log`, or the watcher's `signals.log`
(the latter is empty by construction — `startBlockingDep` does not spawn a
watcher for orchestrator-managed deps; see the comment at lines 234–238).

`AttachLiveDep` is symmetric in the other direction: after `SpawnDetached`
returns, the dep is unconditionally treated as healthy. A `dep_started` Signal
(verdict=pass) is emitted and the LiveStack is incremented even if the
subprocess dies milliseconds later.

Consequence: `FirstFailedDep` never observes a blocking dep as failed, the
cascade gate added in #20 never fires for blocking deps, and the dependent
sensor runs its full command (often a long curl wait-loop) against
infrastructure that never came up.

Real-world repro from the issue:

```
run-project-charge-api  (blocking dep — `docker compose --build api`)
    └── assert-health-check-live-returns-200-health  (root sensor)
```

The docker build fails in ~10s with `failed to solve: process "/bin/sh -c go
work sync" did not complete successfully: exit code: 1`. The current code
emits:

```jsonl
{"sensor_id":"run-project-charge-api","verdict":"pass","severity":"info",
 "evidence":[{"rationale":"blocking dep \"run-project-charge-api\" stopped on detach"}],
 "metadata":{"counts":{"pass":0,"warn":0,"fail":0,"error":0},"kind":"aggregate"}}
{"sensor_id":"assert-health-check-live-returns-200-health","verdict":"fail",
 "cost_actual":{"latency_ms":245321},...}
```

The dep aggregate is a lie and the root spent ~245s timing out a curl loop on a
container that never existed.

## Root cause

Two unrelated code paths in `lib/orchestrator/live_deps.go` assume the dep is
alive without checking:

1. **`AttachLiveDep` (lines 117–145)** — after `startBlockingDep` returns, it
   builds `dep_started` directly. There is no synchronous health gate, so a
   subprocess that dies between `SpawnDetached` and `AttachLiveDep`'s `Encode`
   is indistinguishable from one that is healthy.

2. **`stopBlockingDep` (lines 290–329)** — the aggregate is constructed before
   the SIGTERM call without checking whether the subprocess was already dead,
   and the function does not consult any external state about the dep's
   lifetime (no exit code via `Wait4`, no `signals.log` because no watcher was
   spawned, no `raw.log` inspection).

Both gaps share a root: orchestrator-managed blocking deps run **without a
watcher**, so the only observation channels (`signals.log` populated by pattern
matching, `subprocess_exit` written by the reaper) are not produced.

## Fix

Treat orchestrator-managed blocking deps with the same observability mechanism
that `/start-sensor` already uses for top-level blocking sensors: spawn the
watcher binary alongside the subprocess and let it populate `signals.log` and
the registry's `subprocess_exit` field. With that mechanism in place, the two
points where the aggregate is constructed become driven by observed signals
rather than hard-coded constants.

The change is contained to four areas:

1. **`startBlockingDep`** — adopt the per-run directory layout used by
   `/start-sensor` (staging-raw + atomic rename) and spawn the watcher via
   `lib/watcher.Spawn` after the subprocess is up. The registry entry stores
   the real `WatcherPID` (today's value is `0`).

2. **`AttachLiveDep`** — after `startBlockingDep` returns, call a new
   `lib/watcher.WaitForReady(SignalsLogPath, SubprocessPID, Timeout)` and
   branch on its outcome:

   | Outcome         | Trigger                                                                     | Action                                                                                                                              | Signal emitted                                                                                                |
   | --------------- | --------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------- |
   | `ready`         | First signal observed has `verdict ∈ {pass, warn}`                          | Proceed: push `LiveDep` onto stack.                                                                                                  | `dep_started` (verdict=pass, severity=info).                                                                  |
   | `failed`        | First signal observed has `verdict ∈ {fail, error}`                         | Abort: SIGTERM the subprocess group, drain watcher, remove registry entry.                                                          | `dep_start_failed` (verdict=fail, severity=high, `metadata.observed_signal` carries the watcher's signal).    |
   | `died_silently` | `IsPIDAlive(SubprocessPID) == false` before any signal appears              | Same cleanup as `failed`.                                                                                                            | `dep_start_failed` (`metadata.cause="subprocess_died_silently"`, evidence carries tail of `raw.log`).         |
   | `timed_out`     | Timeout elapses with no signal and PID still alive                          | Proceed (optimistic).                                                                                                                | `dep_started` with `metadata.health_gate="timed_out_proceeding"` for diagnose.                                |

   The `failed` and `died_silently` outcomes return their signal through the
   existing `AttachResult.GateSignal` field, so `lib/orchestrator/preflight.go`
   stores the verdict in `res.Signals[s.ID]` and the subsequent
   `FirstFailedDep` check propagates the cascade exactly as it already does
   for `PreflightGate` failures.

3. **`stopBlockingDep`** — replace the hard-coded aggregate with a worst-of
   computation over the dep's `signals.log` crossed with a pre-SIGTERM liveness
   check:

   ```
   liveBeforeStop := IsPIDAlive(entry.PID)
   if liveBeforeStop && entry.PGID > 0:
       SIGTERM → poll IsPIDAlive until deadline → SIGKILL if still alive
   signal watcher (SIGTERM on entry.WatcherPID) and wait briefly for it to drain
   read signals.log: individuals := JSONL lines that are not envelope/aggregate
   verdict, severity := lib/signal.MaxStreamVerdict(individuals)  // pass/info if empty
   if !liveBeforeStop:
       verdict, severity = "fail", "high"
       metadata.subprocess_state = "died_before_stop"
       evidence += {rationale, snippet = tail(raw.log, N)}
   else:
       metadata.subprocess_state = "stopped_on_detach"
   counts := lib/signal.CountVerdicts(individuals)
   emit aggregate
   ```

   The watcher-drain step uses the same primitive `/stop-sensor` already uses
   (SIGTERM the watcher, give it a short grace period, then read
   `signals.log`). Cap the drain wait at e.g. 1s — a misbehaving watcher must
   not block detach indefinitely.

4. **`docs/CLAUDE.md`** — update the "Dependencies and lifecycle" section: the
   sentence "No watcher process is spawned for orchestrator-managed deps — the
   dep runs unobserved (signals.log stays empty)" is now stale and must be
   replaced.

### What does NOT change

- **`schemas/sensor.json`, `schemas/signal.json`** — `metadata` is free-form
  per `signal.json:122-125`. New `metadata.health_gate`,
  `metadata.subprocess_state`, `metadata.observed_signal` keys are accepted as
  is.
- **`lib/subprocess/detach.go::SpawnDetached`** — it continues to `Release()`
  the Process handle. The fix does not use `syscall.Wait4`; aggregate
  decisions are driven by `signals.log` + `IsPIDAlive`, which work for both
  the orchestrator-as-parent case and the re-attach case where the
  orchestrator is not the parent.
- **The non-blocking dep path** in `lib/orchestrator/preflight.go::RunDeps`.
  Already correct, untouched.
- **Re-attach path in `AttachLiveDep`** (`existing != nil && IsPIDAlive`). The
  watcher is already running (spawned by the first holder); re-attach skips
  the health gate and reuses the existing infrastructure. Justified by the
  same reasoning the spawn-fresh gate has at `live_deps.go:108–111` (gating
  with the current holder's env when the dep was spawned with a different env
  is wrong).
- **`lib/orchestrator/cascade.go::BuildCascadeSignal`,
  `lib/orchestrator/run.go::FirstFailedDep`,
  `lib/orchestrator/gate.go::PreflightGate`** — all consume verdicts via
  `res.Signals`; once the verdicts are truthful, the cascade chain works
  without modification.

### Explicitly deferred

**Mid-run cancellation.** When a blocking dep survives the health gate, the
dependent starts its `command`, and the dep dies *during* the dependent's
execution, the dependent runs to its own timeout. The aggregate at detach is
now correct (records the dep as `verdict=fail` with
`subprocess_state=died_before_stop`), but the dependent's wall-clock is
wasted. Cancellation requires the orchestrator to watch each live dep's
`signals.log` / liveness in a goroutine and propagate `context.Context`
cancellation to the dependent's runner — invasive enough to warrant its own
issue. Tracked in #49.

## Error handling

### `startBlockingDep`

| Failure                                  | Cleanup                                                                  | Returned                  |
| ---------------------------------------- | ------------------------------------------------------------------------ | ------------------------- |
| `os.MkdirAll(SensorDir)` fails           | none                                                                     | `error`                   |
| `os.WriteFile(staging raw.log)` fails    | none                                                                     | `error`                   |
| `SpawnDetached` fails                    | remove staging raw.log                                                   | `error`                   |
| `os.MkdirAll(runDir)` fails              | kill subprocess group, remove staging raw.log                             | `error`                   |
| `os.Rename(staging → runDir)` fails      | kill subprocess group, remove staging raw.log, remove runDir              | `error`                   |
| `os.WriteFile(signals.log)` fails        | kill subprocess group, remove runDir                                      | `error`                   |
| `libsensor.BuildEnvelope` fails          | kill subprocess group, remove runDir                                      | `error`                   |
| `watcher.Spawn` fails                    | kill subprocess group, remove runDir                                      | `error`                   |

Any error returned from `startBlockingDep` is bubbled up through
`AttachLiveDep` to `preflight.go::RunDeps` (existing code), which emits a
`dep_start_failed` cascade signal attributed to the requesting target and
returns `ExitCode=1`.

### `AttachLiveDep` (health gate)

| `WaitForReady` outcome  | Cleanup                                                                                                   | Signal emitted by `AttachLiveDep`                                                                |
| ----------------------- | --------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `ready`                 | none                                                                                                       | `dep_started` (verdict=pass)                                                                     |
| `failed`                | SIGTERM subprocess group, SIGTERM watcher, wait drain, remove registry entry                              | `dep_start_failed` (verdict=fail), `metadata.observed_signal` carries the watcher's signal      |
| `died_silently`         | SIGTERM watcher (subprocess already dead), wait drain, remove registry entry                              | `dep_start_failed` (verdict=fail), `metadata.cause="subprocess_died_silently"`, evidence has `raw.log` tail |
| `timed_out`             | none                                                                                                       | `dep_started` (verdict=pass), `metadata.health_gate="timed_out_proceeding"`                     |

For `failed` and `died_silently`, the signal is returned via
`AttachResult.GateSignal` (same channel used for `PreflightGate` failures), so
the existing `preflight.go::RunDeps` loop stores the verdict in
`res.Signals[s.ID]` and `FirstFailedDep` propagates the cascade.

### `stopBlockingDep`

| Failure                                                  | Behaviour                                                                                                |
| -------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| Reading `signals.log` fails (file missing/permission)    | Fall back to old aggregate shape with verdict driven solely by `liveBeforeStop`; evidence cites the error |
| `signals.log` contains malformed JSONL line              | Skip that line (continue parsing the rest)                                                               |
| Watcher does not exit within drain grace period          | Read `signals.log` at whatever state it is in; do not block detach further                               |
| `os.Remove(runDir)` not attempted                        | Run logs remain on disk for diagnose; matches `/start-sensor` behaviour                                  |

## Constants

| Constant                       | Initial value      | Why                                                                                                       |
| ------------------------------ | ------------------ | --------------------------------------------------------------------------------------------------------- |
| `healthGateTimeout`            | `5 * time.Second`  | Long enough to absorb fsnotify event latency on macOS; short enough to feel responsive on the boot path.   |
| `healthGatePollInterval`       | `100ms`            | Matches `lib/watcher/reaper`'s polling cadence. Trade-off: latency vs. CPU.                              |
| `watcherDrainTimeout`          | `1 * time.Second`  | Matches `/stop-sensor`'s expectation. Watcher's drain is bounded by `fsnotify` event handling.            |
| `rawLogTailLines`              | `40`               | Enough to capture the operative error from a build tool (e.g. docker buildx). Subject to follow-up tuning. |

Each is a private Go constant in the relevant file (`lib/watcher/health_gate.go`,
`lib/orchestrator/live_deps.go`). No new sensor-schema fields. If users
demand per-sensor overrides later, that becomes a separate plugin-version event
(per project rule #2 in CLAUDE.md).

## Components

| Layer            | Path                                       | Change                                                                                   |
| ---------------- | ------------------------------------------ | ---------------------------------------------------------------------------------------- |
| Library          | `lib/watcher/health_gate.go`               | **new**. `WaitForReady(SignalsLogPath, SubprocessPID, Timeout) → HealthGateResult`.       |
| Library tests    | `lib/watcher/health_gate_test.go`          | **new**. Table-driven covering 4 outcomes + JSONL parsing edge cases.                    |
| Orchestrator     | `lib/orchestrator/live_deps.go`            | `startBlockingDep`: run-dir layout + `watcher.Spawn`. `AttachLiveDep`: 4-way branch.    |
|                  |                                            | `stopBlockingDep`: worst-of + liveness-driven verdict.                                  |
| Orchestrator     | `lib/orchestrator/live_deps_test.go`       | New tests: ready, failed, died_silently, timed_out, died_before_stop, drained signals.   |
| Skill (no code)  | `CLAUDE.md`                                | Update "Dependencies and lifecycle" paragraph about orchestrator-managed deps.            |

No changes to `lib/subprocess`, `lib/registry`, `lib/signal` (`MaxStreamVerdict`,
`CountVerdicts` are reused), `lib/cli`, or any skill in `skills/`. The
`/start-sensor` path is unaffected.

## Test plan

`lib/watcher/health_gate_test.go` — table-driven:

| Scenario                                                                          | Expected                                                                            |
| --------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------- |
| `signals.log` starts empty, write `verdict=pass` signal within timeout            | `Outcome=ready`, `Signal=<that signal>`                                             |
| `signals.log` starts empty, write `verdict=warn` signal within timeout            | `Outcome=ready` (warn counts as healthy)                                            |
| `signals.log` starts empty, write `verdict=fail` signal within timeout            | `Outcome=failed`, `Signal=<that signal>`                                            |
| `signals.log` starts empty, write `verdict=error` signal within timeout           | `Outcome=failed`                                                                    |
| Subprocess dies (PID gone) before any signal                                      | `Outcome=died_silently`                                                             |
| Timeout elapses with no signal and PID still alive                                | `Outcome=timed_out`                                                                 |
| Malformed JSONL line followed by valid `verdict=pass` line                        | `Outcome=ready` (malformed line skipped, no error)                                  |
| `signals.log` already has a `verdict=pass` line at start (pre-existing)           | `Outcome=ready` immediately                                                         |

`lib/orchestrator/live_deps_test.go` — end-to-end through `RunWithDepsImpl`:

| Scenario                                                                                                                            | Expected                                                                                                                              |
| ----------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| Blocking dep boots fast and emits `verdict=pass` (pattern matches "Listening on")                                                  | `dep_started` (pass) emitted; root runs and its aggregate is the last line                                                            |
| Blocking dep dies in 50ms (command `false`)                                                                                         | `dep_start_failed` emitted (verdict=fail); root cascades; aggregate carries `metadata.cause="subprocess_died_silently"`; root not run |
| Blocking dep emits a `verdict=fail` signal (pattern matches "FATAL")                                                                | `dep_start_failed` emitted; root cascades                                                                                              |
| Blocking dep stays alive but emits no matching signal within `healthGateTimeout`                                                    | `dep_started` (pass) emitted with `metadata.health_gate="timed_out_proceeding"`; root runs                                            |
| Blocking dep stays alive throughout root execution; dep is alive at detach                                                          | Aggregate emits with verdict driven by `MaxStreamVerdict(signals.log)` (typically pass); `metadata.subprocess_state="stopped_on_detach"` |
| Blocking dep dies after root starts (during the root's command)                                                                     | Aggregate at detach emits `verdict=fail`, `metadata.subprocess_state="died_before_stop"`, evidence carries `raw.log` tail            |
| Re-attach to live dep (second invocation of the same dep via a different root)                                                      | No new health gate run; existing watcher continues; behavior matches pre-change for re-attach                                         |
| `signals.log` is unreadable at detach (e.g. simulate fs error)                                                                      | Aggregate still emitted; evidence cites the read failure; no panic                                                                    |

Existing tests that assert against the synthetic `verdict=pass` aggregate need
updating to reflect the new shape. Search target:
`TestRunWithDepsImpl_BlockingDep_AggregateLast` and friends — likely need to
expect `metadata.subprocess_state` and `MaxStreamVerdict`-driven verdicts.

Run locally:

```
go test ./lib/watcher/...
go test ./lib/orchestrator/...
go test -tags=run_computational ./skills/...
go test -tags=start_sensor      ./skills/...
go vet -tags=run_computational  ./...
```

## Acceptance criteria

A1. `lib/orchestrator/live_deps.go::stopBlockingDep` no longer hard-codes
    `verdict="pass"` / `severity="info"`. Verdict is derived from
    `MaxStreamVerdict(signals.log)` crossed with the liveness check.

A2. `AttachLiveDep` for a fresh-spawn blocking dep performs a synchronous
    health gate via `lib/watcher.WaitForReady`, and emits `dep_start_failed`
    (carried through `AttachResult.GateSignal`) when the watcher reports
    `failed` or `died_silently`.

A3. `lib/orchestrator/preflight.go::RunDeps` requires no changes to honour
    the new path — the `GateSignal` channel and `FirstFailedDep` work
    unchanged.

A4. The schema files in `schemas/` are not modified.

A5. The `/start-sensor` path (top-level blocking sensors) is functionally
    unchanged. `skills/start-sensor/scripts/start.go` may receive shared-lib
    refactors but its observable behavior is identical.

A6. The repro from the issue (`/run-sensor
    assert-health-check-live-returns-200-health` with a failing docker build
    dep) now produces a cascade chain instead of the silent 245-second curl
    wait-loop, given any of:
    - dep boots fast enough that `output_parsing.patterns` is matched (ideal),
    - dep dies within `healthGateTimeout` (caught by `died_silently`),
    - dep survives the gate but dies during the dependent's run (caught at
      detach; aggregate is truthful but wall-clock is still spent — fix in #49).

A7. `go test ./lib/orchestrator/... ./lib/watcher/...` passes.

A8. Documentation: `CLAUDE.md` "Dependencies and lifecycle" no longer says
    orchestrator-managed deps run unobserved.

## Cross-refs

- Issue: <https://github.com/iurykrieger/harness-framework/issues/46>
- #20 — Cascade through non-blocking dep (already resolved).
- #19 — Aggregate ordering (related; the new aggregate continues to be the
  LAST JSONL line on stdout of the dep).
- #9 — `/start-sensor` early-death probe (analogous; this design extends the
  same mechanism into the orchestrator path).
- #36 — Preflight skip for blocking deps (downstream of the same
  fire-and-forget design that #46 fixes).
- #49 — Mid-run cancellation (the deferred half of the wall-clock-recovery
  story).
