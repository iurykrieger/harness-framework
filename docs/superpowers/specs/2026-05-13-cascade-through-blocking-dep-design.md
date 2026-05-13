# Cascade through blocking dep (closes #20)

## Problem

`/run-sensor` documents this contract (`skills/run-sensor/SKILL.md`):

> When a dep produces verdict=fail or verdict=error, every transitively-dependent
> sensor is **skipped** and emits a "cascade" Signal (verdict=error, severity=high)
> pointing at the failed dep. The skipped sensor never runs its `command` or its
> prepare/teardown — only the cascade Signal is emitted.

The current implementation honours this contract for **non-blocking** intermediate
deps only. A failing non-blocking setup that sits *upstream of a blocking dep* in
the chain does not cascade through the blocking dep: the blocking dep is still
attached as a live subprocess, its placeholder `verdict=pass` is stored in
`RunDepsResult.Signals`, and the root then sees no failed direct dep and runs
its own command.

Real-world repro from the issue:

```
dependencies-up-docker-compose (setup, non-blocking, fails)
    └── run-api-local         (blocking, dependent)
            └── health-check-ready (assertion, root)
```

Observed stream (the dependent and root both run despite the upstream `fail`):

```jsonl
{"sensor_id":"dependencies-up-docker-compose","verdict":"fail",...}
{"sensor_id":"run-api-local","verdict":"pass","metadata":{"kind":"dep_started"}}
{"sensor_id":"health-check-ready","verdict":"error","metadata":{"kind":"aggregate","timed_out":true,...},
 "cost_actual":{"latency_ms":60952}}
```

The root cost ~60s of wasted curl polling instead of emitting an instant cascade
Signal pointing at the actual failure.

## Root cause

`lib/orchestrator/preflight.go::RunDeps` iterates the topo-sorted DAG and for each
non-root dep splits on `execution.blocking`:

- **non-blocking** path (lines 107–123): first checks `FirstFailedDep`, emits
  cascade Signal when a transitive dep failed, otherwise calls `RunOne`.
- **blocking** path (lines 88–106): unconditionally calls `AttachLiveDep` and
  records `Signals[s.ID] = {"verdict": "pass"}` on success. **No cascade check.**

Because the blocking branch stores a placeholder pass, any downstream consumer
calling `FirstFailedDep` against `Signals` cannot distinguish "blocking dep
spawned cleanly" from "blocking dep should have cascaded". The cascade chain is
silently pruned at every blocking intermediate.

## Fix

Hoist the `FirstFailedDep` check above the `blocking` branch in `RunDeps`, so a
single cascade gate applies to both blocking and non-blocking deps. Any dep
(blocking or not) whose own (transitive) dep already failed:

1. Emits a `BuildCascadeSignal(s, blocker)` Signal on stdout (validated against
   `schemas/signal.json`).
2. Stores that cascade Signal in `Signals[s.ID]`. Because the cascade Signal
   carries `verdict=error`, downstream `FirstFailedDep` calls — including the
   final one against the root sensor — pick it up naturally and propagate.
3. Skips `AttachLiveDep` and `RunOne` entirely. The blocking dep's subprocess
   is never spawned; no entry is created in `running_sensors.json`; nothing is
   pushed onto `LiveStack`.

No other call site changes. `BuildCascadeSignal`, `validateOrFallback`, and the
detach machinery (`defer detachAll()` in `runWithDepsImpl`) keep their current
behaviour. `LiveStack` simply does not contain the cascaded blocking dep, which
is correct: nothing to detach.

### Why the fix is safe to apply unconditionally

- **Cascade Signals carry `verdict=error`.** Existing `FirstFailedDep` semantics
  (`verdict == "fail" || verdict == "error"`) already treat them as failures.
  No semantic shift in the failure detection.
- **No new schema fields.** `BuildCascadeSignal` already emits everything the
  blocking-dep case needs (`failed_dep_id`, `failed_dep_run_id`,
  `failed_dep_verdict`, `failed_dep_severity`, `metadata.kind=cascade`).
- **Existing tests stay green.** The existing cascade test
  (`TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast` in `live_deps_test.go`)
  models the *sibling* topology (root depends on both a failing non-blocking dep
  AND a blocking dep that does NOT itself depend on the failing one). After the
  fix, the blocking sibling still attaches (its own `FirstFailedDep` returns
  nil), and the root still cascades. Behaviour preserved.
- **`/start-sensor` path is unaffected.** It calls `RunDeps` through the same
  helper but cascades are caught later via the same `FirstFailedDep` + cascade
  Signal mechanism it already trusts.

## Definition of Done

Binary checks. Each must be objectively verifiable.

1. **Cascade through blocking dep**: in the chain
   `failing-setup (non-blocking, fail) → blocking-intermediate (blocking) → root`,
   `/run-sensor root` emits exactly three JSONL signals, in this order:
   1. aggregate Signal of `failing-setup` with `verdict=fail`,
   2. cascade Signal of `blocking-intermediate` with `metadata.kind=cascade`
      and `metadata.failed_dep_id="failing-setup"`,
   3. cascade Signal of `root` with `metadata.kind=cascade` and
      `metadata.failed_dep_id="blocking-intermediate"`.
2. **No subprocess for the cascaded blocking dep**: after the run completes,
   `.harness/runtime/running_sensors.json` contains no entry for
   `blocking-intermediate`. (Best-effort proxy for "never spawned".)
3. **No wasted cost**: the cascade Signal of `blocking-intermediate` has
   `cost_actual.latency_ms = 0`.
4. **Exit code is 1**: `RunWithDepsRoot` exits 1 (cascade-skipped root). The
   existing exit-code semantics are preserved.
5. **Existing tests still pass**: `go test ./lib/...` plus the run-sensor
   skill tags (`-tags=run_computational`, `-tags=run_inferential`) green.
6. **Schema validation**: the cascade Signal emitted for the blocking dep
   validates against `schemas/signal.json` (existing validator path runs
   inside the fix).

## Scope

In:

- `lib/orchestrator/preflight.go`: move the `FirstFailedDep` cascade check above
  the `if blocking` branch. ~10 lines moved.
- `lib/orchestrator/preflight_test.go` (or a sibling `cascade_blocking_test.go`):
  one new test asserting DoD items 1–4.

Out:

- `BuildCascadeSignal` shape, schema fields, exit codes, `AttachLiveDep` /
  `DetachLiveDep` mechanics, `/start-sensor` / `/stop-sensor` / `/tail-sensor`
  registry behaviour. None of these change.
- `skills/run-sensor/SKILL.md` prose. The current contract already describes the
  intended behaviour; the bug is implementation-only.
- The placeholder `{"verdict": "pass"}` stored in `Signals[s.ID]` after a
  successful `AttachLiveDep`. It stays as is — its only consumer is
  `FirstFailedDep`, and `pass` is the truthful answer for "the attach succeeded
  and no upstream dep had failed".

## Anti-scope

- Do not change the cascade Signal envelope.
- Do not change `AttachLiveDep` / `DetachLiveDep` signatures or contracts.
- Do not introduce new exit codes.
- Do not touch the `/start-sensor` cascade path beyond what falls out
  mechanically from the `RunDeps` fix.

## Technical decisions

- **Place the cascade gate at one site, not many.** The gate lives in `RunDeps`,
  applied to every non-root iteration before any branch on `blocking`. Mirrors
  the `PreflightGate` invariant that requires every spawn to be preceded by a
  single gate call.
- **Reuse the existing placeholder pass for successful attaches.** The fix does
  not introduce a new sentinel; it just guarantees the cascade check fires
  before `AttachLiveDep` so the placeholder is only written when the dep
  legitimately attached.
- **Test fixture lives alongside `live_deps_test.go` helpers.** Reuse
  `writeBlockingDep`, `writeNonBlockingFailingDep`, and add a new
  `writeBlockingDepDependingOn(failingID)` helper if the existing
  `writeBlockingDep` cannot express the chain. The new test uses the same
  `RunWithDepsRoot` entry point as the existing `BlockingDep_CascadeAggregateLast`
  test to keep coverage symmetric.

## Verification plan

1. `go test ./lib/orchestrator/... -run TestRunDeps_CascadeThroughBlockingDep` —
   new test must pass.
2. `go test ./lib/...` — full library suite stays green.
3. `go test -tags=run_computational ./skills/...` and
   `go test -tags=run_inferential ./skills/...` — runner integration suites
   stay green.
4. Manual smoke (optional): construct the issue's fixtures in `.harness/sensors/`
   and run `/run-sensor health-check-ready`; confirm three signals and 0ms
   latency on the cascaded blocking dep.

## References

- Issue: https://github.com/iurykrieger/harness-framework/issues/20
- Existing cascade machinery: `lib/orchestrator/cascade.go`,
  `lib/orchestrator/preflight.go`, `lib/orchestrator/run.go`.
- Sibling-topology cascade test:
  `lib/orchestrator/live_deps_test.go::TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast`.
