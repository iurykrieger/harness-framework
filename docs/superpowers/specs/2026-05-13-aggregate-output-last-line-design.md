# Aggregate output is the last line of the stream — design

Status: proposed
Date: 2026-05-13
Issue: https://github.com/iurykrieger/harness-framework/issues/19
Related: `lib/orchestrator/run.go`, `lib/orchestrator/lifecycle.go`, `lib/orchestrator/live_deps.go`, `skills/run-sensor/scripts/run-inferential.go`, `skills/run-sensor/SKILL.md`, `README.md`

## Why

`/run-sensor X` publishes a JSONL stream on stdout. The framework's public contract — documented in `skills/run-sensor/SKILL.md`, in the README, and assumed by `skills/detect-sensors/SKILL.md`'s smoke-test pattern `tail -n 1 | jq` — guarantees that the **last** line on stdout is the aggregate Signal of the requested sensor. Every existing consumer of `/run-sensor`'s output relies on this invariant to recover the verdict, either by `tail -n 1` or by parsing bottom-up.

Issue #19 reproduces a path where this contract is violated. When the requested sensor declares a `requires[kind=sensor]` dep whose own `execution.blocking: true`, the orchestrator attaches a holder onto that dep through `AttachLiveDep` and stacks a `defer DetachLiveDep(...)` to release the holder. Detach is a real side-effect: when the holder being removed is the **last** one on a blocking entry, `DetachLiveDep` calls `stopBlockingDep`, which terminates the dep's process group and emits an additional aggregate Signal naming the **dep** (not the requested sensor) on stdout. Because the `defer` fires after `RunOneWithRoot` has already written the requested sensor's aggregate, the stream ends with the dep's stop-aggregate instead of the requested sensor's aggregate.

The reproduction in the issue:

```
{aggregate dependencies-up-docker-compose}    verdict=fail (non-blocking dep)
{dep_started run-api-local}                   blocking dep attach
{aggregate health-check-ready}                requested sensor — should be LAST
{aggregate run-api-local}                     dep stop — appears AFTER  ✗
```

`tail -n 1 | jq .sensor_id` returns `run-api-local` (the dep) instead of `health-check-ready` (the requested sensor). Every consumer downstream is silently misled.

The same defect exists in **both** paths that orchestrate blocking deps:

1. `lib/orchestrator/run.go::runWithDepsImpl` — the computational runner's entry point, also used by `RunWithDepsRoot` (`live_deps.go:50`) and indirectly by `replay-fixture.go`. The `defer` block is at `run.go:99-103`; aggregate emission happens inside `RunOneWithRoot` at `run.go:124`.
2. `skills/run-sensor/scripts/run-inferential.go::run()` — the inferential runner has its own ad-hoc deps loop. The `defer` block is at `run-inferential.go:171-175`; aggregate emission happens at `run-inferential.go:322` (and at the cascade and gate early-return paths).

The cascade-skipped target path inside `runWithDepsImpl` (`run.go:115-122`) has the same shape: emit the cascade Signal, return, then the `defer` fires and emits dep teardown signals after. A target cascaded because of a fail/error in a non-blocking dep may still have blocking deps already attached upstream in the topo order, and those generate teardown signals during the deferred detach.

The fix described in this spec preserves the public contract by moving the aggregate emission of the requested sensor (or its cascade surrogate) to a single explicit emit point that happens **after** all blocking-dep detaches have completed and any signals they emit have already flushed.

## What changes

1. **New helper `orchestrator.RunOneWithRootCapture`** in `lib/orchestrator/lifecycle.go`. Signature:
   ```go
   func RunOneWithRootCapture(
       ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator,
       root *registry.Root, stdout, stderr io.Writer,
   ) (sig map[string]interface{}, code int)
   ```
   Identical behavior to `RunOneWithRoot` except that the final aggregate Signal is **not** written to stdout — it is only returned. Individual Signals emitted during streaming (stream-mode pattern matches) continue to flow through `stdout` in real time, unchanged. Schema validation of the aggregate still happens inside the helper before the return; on validation failure the helper returns `(nil, 1)` exactly like `RunOneWithRoot` does today. Preflight gate failures are returned the same way (`sig, 0`) without internal emission. The existing `RunOneWithRoot` becomes a thin wrapper: `sig, code := RunOneWithRootCapture(...); if sig != nil && code == 0 { json.NewEncoder(stdout).Encode(sig) }; return sig, code`.

   No symmetric `RunOneCapture` for the no-persistence path is added: every caller that needs capture semantics already passes a `root` (either through `runWithDepsImpl`, which always builds one, or through the inferential runner, which builds its aggregate inline rather than via `RunOne`).

2. **`runWithDepsImpl` rewritten to capture-then-detach-then-emit** (`lib/orchestrator/run.go:99-126`). The current `defer DetachLiveDep` block is replaced by an explicit sequence:
   - On the normal-target path: `sig, code := RunOneWithRootCapture(...)`, then explicit `detachAll()`, then `if sig != nil { json.NewEncoder(stdout).Encode(sig) }`, then `return code`.
   - On the cascade-target path: capture the cascade Signal in a local, run explicit `detachAll()`, then emit the cascade Signal, then return 1.
   - On the pre-target `pre.ExitCode != 0` path: explicit `detachAll()` then return; deps have already emitted whatever they emit during `RunDeps`.
   - The `defer detachAll()` is **kept** as an idempotent safety net for panic / mid-function early-return paths. After the explicit call, the deferred call is a no-op because `LiveStack` is drained inside `detachAll()` (the function consumes the stack rather than just iterating it), and `DetachLiveDep` is already idempotent against missing registry entries.

3. **`skills/run-sensor/scripts/run-inferential.go::run()` rewritten with the same pattern** (`run-inferential.go:165-326`). The current `defer { for ... DetachLiveDep }` at line 171 is replaced by a `detachAll` closure called explicitly at each exit point that needs to emit a final Signal:
   - Normal target path: build the aggregate `sig` as today (existing inline logic from `run-inferential.go:243-322`), then call `detachAll()`, then `json.NewEncoder(stdout).Encode(sig)`, then `return 0`. Schema validation of `sig` happens before `detachAll()` so a validation failure returns 1 without writing anything.
   - Cascade path at `run-inferential.go:217-223` (non-blocking dep cascade) and `run-inferential.go:301-309` (requested-sensor cascade after deps loop): capture the cascade Signal in a local, call `detachAll()`, then emit, then return.
   - Gate-failure path at `run-inferential.go:235-241`: capture the gate Signal, call `detachAll()`, then emit, then return.
   - The `defer detachAll()` is **kept** as a safety net (same rationale as runWithDepsImpl).

4. **`detachAll` is consuming, not idempotent-by-iteration.** Both `runWithDepsImpl` and `run-inferential.go::run` define `detachAll` as a closure that:
   - Iterates `liveStack` in reverse order, calling `DetachLiveDep` on each entry.
   - Sets `liveStack = nil` at the end.

   The deferred call invokes the same closure; the second invocation does nothing because the slice is empty. This is preferred over a `var detached bool` flag because it makes the post-condition obvious: after `detachAll` returns, there is nothing left to detach.

5. **Documentation updated to reflect the new stream order:**
   - `skills/run-sensor/SKILL.md`: the JSONL stream example in the "Dependency resolution" section is rewritten to show that, when the requested sensor depends on a blocking dep, `dep_detached` / dep-stop-aggregate Signals appear between the requested sensor's individual Signals and its aggregate. The "aggregate of requested sensor is the LAST line" claim is preserved verbatim; the example clarifies *what else* can appear right before it.
   - `README.md` (if it shows a stream example with blocking deps): same update. If it only shows the non-blocking-dep case, no change is needed.

6. **New tests asserting the LAST-line invariant under blocking deps.** Three test additions, one per affected path:
   - `lib/orchestrator/run_test.go`: a table case `TestRunWithDepsImpl_blockingDep_aggregateLast` that builds a temp project with three sensors — non-blocking-dep, blocking-dep, requested — where the requested sensor depends on the blocking one. Runs through `RunWithDepsRoot`. Asserts the last JSONL line on stdout has `sensor_id == "<requested>"` and `metadata.kind == "aggregate"`. A second case `TestRunWithDepsImpl_blockingDep_cascade_aggregateLast` runs the same fixture but with the non-blocking dep returning `verdict=fail`, asserts the last line is the cascade Signal for the requested sensor.
   - `lib/orchestrator/integration_runtime_logs_test.go`: extends the existing integration coverage with a parallel case that asserts the runtime layout under blocking-dep detach (signals.log of the dep records its own stop-aggregate; stdout records the requested sensor's aggregate last).
   - `skills/run-sensor/scripts/run-inferential_test.go`: a case `TestRunInferential_blockingDep_aggregateLast` mirroring the orchestrator test but exercising the inferential runner's inline deps loop. The inferential runner cannot be exercised end-to-end without a fake LLM CLI; the test uses the same `--slot`-rendered stub command pattern already established in the existing `run-inferential_test.go` cases.

7. **No schema changes.** No new fields are added to `signal.json` or `sensor.json`. The fix is purely about emission ordering on stdout. The dep-stop-aggregate emitted by `stopBlockingDep` continues to carry `metadata.kind = "aggregate"` and `sensor_id = <depID>`; consumers that need to distinguish "dep stop-aggregate vs requested sensor's aggregate" should read `sensor_id`, which has always been authoritative.

## Architecture

### Current (broken)

```
runWithDepsImpl(ctx, sensorPath, ...)
  └─ RunDeps(...)                           ─→ emit dep signals to stdout
  └─ defer { detachAll() }                   (registered)
  └─ RunOneWithRoot(target, ..., stdout)    ─→ emit individuals to stdout (stream mode)
                                            ─→ emit target aggregate to stdout  ◀──┐
  └─ return code                                                                    │
  └─ defer fires:                                                                   │
     └─ DetachLiveDep(D2)                                                           │
        └─ if last holder: stopBlockingDep(D2)                                      │
           └─ emit dep-stop-aggregate to stdout  ◀── AFTER target aggregate ✗ ─────┘
```

### After this spec

```
runWithDepsImpl(ctx, sensorPath, ...)
  └─ RunDeps(...)                                    ─→ emit dep signals to stdout
  └─ defer { detachAll() }                            (safety net only — no-op after explicit call)
  └─ sig, code := RunOneWithRootCapture(             ─→ emit individuals to stdout (stream mode)
       target, ..., stdout)                          ─→ aggregate captured in sig, NOT written
  └─ detachAll()                                     ─→ DetachLiveDep(D2)
                                                        └─ stopBlockingDep(D2)
                                                           └─ emit dep-stop-aggregate to stdout
  └─ if sig != nil: emit sig to stdout                ◀── NOW LAST ✓
  └─ return code
```

The same shape applies to `run-inferential.go::run`: the inline-built `sig` is captured, `detachAll()` runs, `sig` is emitted last.

### Why not move detach into `RunOne`?

Approach B (rejected): make `RunOne` aware of `liveStack` and call detach inside it, before its own aggregate emission.

This would couple `RunOne` to orchestration concerns it has no business knowing about. `RunOne` is "execute one sensor's full lifecycle"; live deps are a property of the orchestrated graph, not of any individual sensor in it. Threading `liveStack` through `RunOne`'s signature would also force every other caller of `RunOne` (the inferential runner's deps loop at `run-inferential.go:208`, `RunDeps`'s non-blocking branch at `preflight.go:118`, the standalone-target test fixtures) to pass an empty stack. Capture-then-detach localizes the change to the two callers that genuinely orchestrate blocking deps.

### Why not just update the contract (issue's option 3)?

Approach C (rejected): change the documentation to say "consumers must filter by `sensor_id`, not use `tail -n 1`."

This is cheap to write but expensive to absorb. The current contract is referenced from:
- `skills/run-sensor/SKILL.md` (twice — once in "Dependency resolution", once in the closing "Output contract" section that says "the aggregate stays unambiguously identifiable" via bottom-up parsing).
- `skills/detect-sensors/SKILL.md`, whose Phase B sensor smoke-test instructions show `… | tail -n 1 | jq -c '{verdict, severity, ...}'` as the canonical example.
- The README, which uses the same `tail -n 1 | jq` pattern in two places.
- Any external consumer that has integrated against this contract since the streaming-sensors design landed (the contract is older than the blocking-sensors design, so external integrations predate the bug).

Updating the contract breaks every one of those usage sites. Preserving the contract fixes one ordering bug in two files.

### `detachAll` closure shape

In both files, the closure looks like:

```go
detachAll := func() {
    for i := len(liveStack) - 1; i >= 0; i-- {
        DetachLiveDep(liveStack[i], projectRoot, rootID, v, stdout, stderr)
    }
    liveStack = nil
}
defer detachAll()
// ... explicit detachAll() call before final emit ...
```

The reverse-order iteration mirrors the original `defer` semantics: deps that were attached last are detached first, preserving the topological ordering of the hold/release lifecycle.

`liveStack = nil` after the loop is what makes the deferred call a no-op when control reaches the explicit call. This is preferred over `var alreadyDetached bool` because the slice-clearing operation is what `DetachLiveDep` would test for anyway if we asked the question "is there anything left to detach?"

### Interaction with `runOneWithPersistence`'s own defer

`runOneWithPersistence` (called from `RunOneWithRoot` when `root != nil`) has its **own** `defer` block at `lifecycle.go:363-375` that removes the run-id registry entry after the target's command returns. This defer:
- Runs **before** the `runOneWithPersistence` function returns (per Go semantics, defers run on the way out of the function that registered them).
- Does **not** emit anything to stdout — it only deletes a registry row under flock.

Therefore the persistence defer is unaffected by this spec: it runs in its current position relative to the aggregate emission inside `runOneWithPersistence` (which itself is the line we are moving out into `RunOneWithRootCapture`'s caller). The persistence defer continues to fire before `RunOneWithRootCapture` returns, which is correct — the registry entry is cleaned up before any caller, including `runWithDepsImpl`, gets a chance to start detaching live deps.

### Interaction with watcher binary (for live blocking deps)

When `AttachLiveDep` spawn-fresh path runs, `startBlockingDep` does **not** spawn a watcher — orchestrator-managed deps run unobserved (signals.log stays empty for the dep). This is unchanged by this spec. The dep's stop-aggregate emitted by `stopBlockingDep` is a synthesized one-shot Signal built in-memory in `live_deps.go:283`; it is not routed through the watcher.

The other live-dep flow — `/start-sensor` — does spawn a watcher and is **not** part of this spec's scope, because `/start-sensor` does not run `/run-sensor`'s aggregate-emission code path: it returns its own `started` Signal and exits, leaving the watcher to tail signals.log independently. The contract that "/run-sensor's last line is the requested sensor's aggregate" does not apply to `/start-sensor`.

### Cascade with blocking deps already attached

Walking the cascade path in detail: suppose the topo order is `[D-nonblocking, D-blocking, target]`. `RunDeps` runs `D-nonblocking` first; it returns `verdict=fail`. The cascade machinery (`FirstFailedDep` + `BuildCascadeSignal`) flags downstream sensors. `D-blocking` is downstream of `D-nonblocking`? Only if it declares `D-nonblocking` as its own `requires[kind=sensor]`. If yes, `D-blocking` is not attached, and there is no detach to defer — the spec's fix is moot for this sub-case.

The interesting sub-case is: `D-blocking` does NOT depend on `D-nonblocking`, but `target` depends on both. `RunDeps` runs `D-nonblocking` (fails), then `D-blocking` (attaches and starts), then evaluates `target` against `FirstFailedDep` — sees `D-nonblocking` failed, builds a cascade Signal for `target`, sets `pre.CascadeSig`. Back in `runWithDepsImpl`, the current code emits `pre.CascadeSig` then the defer fires and detaches `D-blocking`, which emits its stop-aggregate **after** the cascade Signal. Same bug, same fix: capture the cascade Signal, detach explicitly, emit cascade last.

The new test `TestRunWithDepsImpl_blockingDep_cascade_aggregateLast` covers this exact topology.

## Acceptance criteria

Binary, runnable:

1. `go test ./lib/orchestrator/...` passes, including:
   - `TestRunWithDepsImpl_blockingDep_aggregateLast` (new, in `run_test.go`).
   - `TestRunWithDepsImpl_blockingDep_cascade_aggregateLast` (new, in `run_test.go`).
   - Extended `integration_runtime_logs_test.go` case for blocking-dep detach ordering (new).
   - All existing tests in `lib/orchestrator/` (no regression).

2. `go test -tags=run_computational ./skills/run-sensor/...` passes, including all existing cases (computational runner is exercised end-to-end through `runWithDepsImpl`).

3. `go test -tags=run_inferential ./skills/run-sensor/...` passes, including:
   - `TestRunInferential_blockingDep_aggregateLast` (new, in `run-inferential_test.go`).
   - All existing cases in `run-inferential_test.go`.

4. `go vet -tags=run_computational ./...` and `go vet -tags=run_inferential ./...` are clean.

5. Manual reproduction from the issue body becomes the documented good behavior: running `/run-sensor health-check-ready` with the chain `dependencies-up-docker-compose` → `run-api-local` (blocking) → `health-check-ready`, `tail -n 1` of the stdout returns the Signal whose `sensor_id == "health-check-ready"` and `metadata.kind == "aggregate"`, regardless of whether `health-check-ready` itself passed, failed, errored on timeout, or was cascade-skipped because an upstream dep failed.

6. No schema changes: `git diff schemas/` is empty.

## Anti-scope

- **No refactor of `RunOne` / `RunOneWithRoot` call sites that do not orchestrate blocking deps.** `preflight.go::RunDeps` non-blocking branch, `run-inferential.go`'s non-blocking-dep loop, and any test fixture that calls `RunOne` directly keep their current emit-from-inside behavior. The capture variant is opt-in for callers that genuinely need it.

- **No change to the dep-stop-aggregate Signal's shape.** `stopBlockingDep` continues to emit a Signal with `metadata.kind = "aggregate"`, `verdict = "pass"`, `sensor_id = <depID>`. Consumers wanting to filter dep teardowns out of a stream should match on `sensor_id != <requestedSensorID>` (or use `tail -n 1` and trust the contract this spec restores).

- **No retroactive deduplication or reordering of dep aggregates already emitted by `RunDeps`.** Non-blocking dep aggregates emitted during the deps phase keep their current position at the start of the stream. This spec is only about the final-line invariant for the requested sensor's aggregate.

- **No change to `/start-sensor`, `/stop-sensor`, `/list-sensors`, or `/tail-sensor`.** These skills have their own emission contracts that do not overlap with `/run-sensor`'s last-line invariant.

- **No CHANGELOG entry framed as a "breaking change."** This is a bug fix; consumers using `tail -n 1` were relying on documented behavior that the orchestrator failed to uphold. The fix restores the documented behavior. The SKILL.md update is a clarification (what other Signals can appear in the stream, and where), not a contract change.

## Definition of Done

Each item is a binary check the reviewer can verify mechanically:

1. `lib/orchestrator/lifecycle.go` exports `RunOneWithRootCapture` with the signature specified in "What changes" §1, and `RunOneWithRoot` is a thin wrapper that calls Capture and emits when `sig != nil && code == 0`.

2. `lib/orchestrator/run.go::runWithDepsImpl` no longer has a `defer DetachLiveDep` loop that races with the aggregate emission. The function captures the target sensor's aggregate (via `RunOneWithRootCapture`) or the cascade Signal, calls an explicit `detachAll()`, then emits the captured Signal as the final stdout write. A deferred `detachAll()` remains as a safety net.

3. `skills/run-sensor/scripts/run-inferential.go::run` follows the same capture-then-detach-then-emit pattern at every exit point that emits a final Signal (normal target, cascade, gate failure).

4. The new tests listed in Acceptance §1, §3 exist and pass.

5. All existing tests in `lib/orchestrator/`, `skills/run-sensor/scripts/`, and any test that exercises blocking deps via `RunWithDepsRoot` continue to pass without modification (no test should need its assertions weakened to accommodate the new ordering — the only acceptable test changes are added cases and trivial reorderings of "expected emitted signals" lists where the test already asserts on ordering).

6. `skills/run-sensor/SKILL.md`'s "Dependency resolution" section's JSONL example reflects the new ordering: when a blocking dep is involved, dep-detach / dep-stop-aggregate Signals appear between the requested sensor's individual Signals and its aggregate. The "aggregate is the LAST JSONL line" sentence is preserved verbatim.

7. `git diff schemas/` is empty after the change.

8. The hand-reproduction from the issue body (the chain `dependencies-up-docker-compose` → `run-api-local` → `health-check-ready`) yields a stdout whose last JSONL line satisfies `sensor_id == "health-check-ready"` and `metadata.kind == "aggregate"`.
