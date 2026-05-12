# Unified preflight gate design

Status: proposed
Date: 2026-05-12
Issue: https://github.com/iurykrieger/harness-framework/issues/36
Related: `lib/orchestrator/preflight.go`, `lib/orchestrator/lifecycle.go`, `lib/orchestrator/live_deps.go`, `lib/sensor/requires.go`, `lib/sensor/env.go`, `skills/start-sensor/scripts/start.go`, `CLAUDE.md`

## Why

The framework's `requires[kind ∈ {tool, context, env}]` preflight gate exists so that a sensor's command never runs against an environment that cannot satisfy its preconditions. The gate is `O(microseconds)`, side-effect free, and the difference between a clean `verdict=error cause=preflight_failed missing_envs=[...]` signal (which `/heal-sensor` knows how to fix) and a 45-second timeout with a confused watcher capturing nothing.

PR #34 (issue #33) unified the gate entry point across two callers: `/run-sensor` (via `RunOne` Phase 0) and `/start-sensor` (via the explicit check at start.go:137). That fixed the failure mode for the **root** sensor across both runners.

It missed the third entry point: the **blocking dep** path inside `RunDeps`. When a sensor declares `requires[kind=sensor]` whose target has `execution.blocking: true`, `RunDeps` routes the dep through `AttachLiveDep` → `startBlockingDep` (live_deps.go:197) which spawns the subprocess *without* evaluating the dep's own `requires[]`. Issue #36 reproduces this exactly: a non-blocking `health-check` sensor depends on a blocking `run-project-nest` which declares 12 mandatory envs (`BANKING_BASE_URL`, `RSA_PRIVATE_KEY`, ...). With those envs unset in the shell, `RunDeps` spawns the Nest subprocess, the Nest subprocess crashes in ~50ms during `BankingAdapter` OAuth bootstrap (`new URL(undefined)`), the watcher captures nothing because the dep never bound to its port, and the root times out at 45s with `verdict=error timed_out=true`.

The diagnostic is wrong twice over: (a) attributed to the **root**, not the dep that actually has the unmet precondition, and (b) no structured signal naming the missing envs — just a timeout. `/heal-sensor`'s classifier has nothing to route on. The author must dig into `BankingAdapter`'s source to even know that `BANKING_BASE_URL` is the missing piece.

There is a second, related, problem the issue exposes by side-effect: the gate-failure signal has **two divergent shapes** across the codebase. `RunOne` Phase 0 emits via `sensor.BuildRequiresGateSignal` (`metadata.kind="aggregate"`, per-failure `evidence[]`, `remediation.instructions`). `/start-sensor` emits via `startSignal(... "failed", "preflight_failed", ...) + requiresGateAux` (`metadata.kind="failed"`, `metadata.cause="preflight_failed"`, machine-readable `missing_envs[] / missing_tools[] / missing_contexts[]`, coarse `evidence`). Both work, but they carry different payloads, and any consumer (the heal classifier, the auto-issue filer, a human reading the JSONL stream) sees a different shape depending on which entry point fired. Fixing #36 by reusing whatever shape is convenient at the new call site would entrench the divergence further.

The fix in this spec is therefore not just "add a gate call in one more place." It establishes a single invariant — *gate evaluation is inseparable from sensor spawn* — and routes every existing call site through a single canonical helper that produces a single canonical signal shape.

## What changes

1. **New invariant in CLAUDE.md (Project rule 11).** Every call to `subprocess.StreamSubprocess`, `subprocess.Start`, or `subprocess.SpawnDetached` that executes a sensor's `execution.command` is preceded by `orchestrator.PreflightGate(s, env, outputMode)` in the same file. Step commands (`subprocess.RunStep` for prepare/teardown) and the watcher spawn (`lib/watcher/`) are explicit allowlisted exceptions — they do not execute the sensor's own command. The allowlist lives in `lib/orchestrator/gate_invariant_test.go`.

2. **New canonical helper `orchestrator.PreflightGate`** in `lib/orchestrator/gate.go`. Signature:
   ```go
   func PreflightGate(s Sensor, env sensor.Envelope, outputMode string) (sig map[string]interface{}, failed bool)
   ```
   Returns `(nil, false)` when the gate passes; `(canonicalSignal, true)` when it fails. Internally calls `sensor.CheckRequiresGate` + the enriched `sensor.BuildRequiresGateSignal`. Accepts `orchestrator.Sensor` directly (not raw `map[string]interface{}`) — every production caller already has the struct.

3. **Canonical signal shape — single across all callers.** `sensor.BuildRequiresGateSignal` is enriched to emit:
   - `metadata.kind = "failed"` (was `"aggregate"`)
   - `metadata.cause = "preflight_failed"` (new)
   - `metadata.missing_envs[]`, `metadata.missing_tools[]`, `metadata.missing_contexts[]` (new; omitted when empty)
   - `metadata.output_mode` (existing)
   - `metadata.heal_hint` (existing, from the first failure)
   - `evidence[]` per-failure (existing, rich rationale)
   - `remediation.instructions` (existing)

4. **Four call sites refactored** to use `PreflightGate`:
   - `RunOne` (`lifecycle.go:62-64`)
   - `RunOneWithRoot` (`lifecycle.go:246-248`)
   - `/start-sensor` (`start.go:137-142`), removing the local `requiresGateAux` and `requiresGateRationale` helpers (~55 LoC)
   - `RunDeps` blocking branch (`preflight.go:89`) — **the fix for #36**. Gate is evaluated immediately before `AttachLiveDep`; on failure, the canonical signal is emitted on stdout and recorded in `res.Signals[s.ID]`, after which the existing cascade machinery (`FirstFailedDep` + `BuildCascadeSignal`) handles dependents and the root on the next iterations.

5. **`AttachLiveDep` is not modified.** The gate runs in the caller (`RunDeps`), not under the file lock inside `AttachLiveDep`. Justification: keeping gate in `RunDeps` avoids changing `AttachLiveDep`'s `(LiveDep, error)` signature; gate is pure and idempotent; running it in both spawn-fresh and re-attach paths keeps the invariant uniform without measurable cost.

6. **Removed:**
   - `orchestrator.RunRequiresGate` (`preflight.go:142-157`) — superseded by `PreflightGate`
   - `orchestrator.emitRequiresGateAggregate` (`lifecycle.go:688-702`) — absorbed into `PreflightGate`; the validation/emit half is renamed `emitPreflightSignal` (~15 LoC, stays in `lifecycle.go`)
   - `sensor.BuildMissingEnvSignal` (`env.go`) — deprecated wrapper, last caller goes away
   - `start.go::requiresGateAux` and `start.go::requiresGateRationale` — replaced by direct use of the canonical signal

7. **CHANGELOG entry** records the breaking change to `metadata.kind` for preflight-failure signals from `/run-sensor` paths: was `"aggregate"`, now `"failed"`. Consumers reading `metadata.kind` to distinguish gate-fail from runtime-aggregate should switch to reading `metadata.cause == "preflight_failed"` (more precise and works for any future `kind="failed"` shape too).

## Architecture

```
┌────────────────────────────────────────────────────────────────────────┐
│  Any caller that intends to spawn a sensor command                     │
└─────────────────────────────────┬──────────────────────────────────────┘
                                  ▼
              ┌─────────────────────────────────────────┐
              │  orchestrator.PreflightGate(s, env, om) │
              └───────────────────┬─────────────────────┘
                                  ▼
             gate.Failed() ?
                  │
        ┌─────────┴──────────┐
        ▼ no                 ▼ yes
   caller spawns         caller receives canonical signal:
   the command           {
                           "sensor_id":  <s.ID>,
                           "verdict":    "error",
                           "severity":   "high",
                           "evidence":   [per-failure rationale, ...],
                           "remediation":{"instructions": "Resolve ..."},
                           "metadata": {
                             "kind":             "failed",
                             "cause":            "preflight_failed",
                             "output_mode":      <sensor.output>,
                             "missing_envs":     [...],
                             "missing_tools":    [...],
                             "missing_contexts": [...],
                             "heal_hint":        "<shape>:<id>"
                           }
                         }
                         │
                         ▼
                         caller validates against signal.json,
                         emits on stdout (or terminal envelope),
                         aborts spawn.
                         dependents cascade via FirstFailedDep +
                         BuildCascadeSignal in the next iteration
                         (existing machinery, unchanged).
```

### End-to-end for issue #36's repro

Project state: `health-check.json` (non-blocking, depends on `run-project-nest`); `run-project-nest.json` (blocking, declares 12 required envs); shell has none of those envs exported.

`RunDeps` iterates the topo-sorted order `[run-project-nest, health-check]`:

1. `s = run-project-nest`, `blocking=true` →
   - `env, _ := sensor.BuildEnvelope(s.JSON)`
   - `sig, failed := PreflightGate(s, env, "stream")` → `failed=true`
   - Validate `sig` against `signal.json`; emit on stdout; `res.Signals["run-project-nest"] = sig`; `continue`.
   - **No subprocess spawned.**

2. `s = health-check` is the target → loop body's `if s.ID == targetID { continue }` skips it.

3. After the loop, `FirstFailedDep(rootSensor, res.Signals)` finds `run-project-nest` with `verdict="error"`; builds `res.CascadeSig` for `health-check` (`metadata.kind="cascade", failed_dep_id="run-project-nest"`). NOT emitted on stdout — returned to the caller (`/run-sensor` runner) which wraps it appropriately.

Stdout JSONL:

```jsonl
{"sensor_id":"run-project-nest","verdict":"error","severity":"high","evidence":[{"rationale":"Required environment variable BANKING_BASE_URL is not set"},{"rationale":"Required environment variable RSA_PRIVATE_KEY is not set"},...],"remediation":{"instructions":"Resolve the following preconditions before re-running: set env BANKING_BASE_URL; set env RSA_PRIVATE_KEY; ..."},"cost_actual":{"latency_ms":0},"metadata":{"kind":"failed","cause":"preflight_failed","output_mode":"stream","missing_envs":["BANKING_BASE_URL","RSA_PRIVATE_KEY",...],"heal_hint":"missing-env:BANKING_BASE_URL"}}
{"sensor_id":"health-check","verdict":"error","severity":"high","metadata":{"kind":"cascade","failed_dep_id":"run-project-nest","failed_dep_verdict":"error","failed_dep_severity":"high",...}}
```

Total latency: low single-digit milliseconds. Registry: empty for `run-project-nest`. `/heal-sensor`'s classifier reads the first signal's `heal_hint=missing-env:BANKING_BASE_URL`, routes to the env fixer, and operates against the **dep** sensor (correct attribution) rather than the root.

### Re-attach semantics

`AttachLiveDep` distinguishes spawn-fresh (existing entry absent or PID dead) from re-attach (existing entry alive) under the registry file lock. Gate evaluation happens in the **caller** (`RunDeps`), *before* `AttachLiveDep`, so it cannot peek at this distinction and runs uniformly in both paths. In re-attach, this means the gate re-evaluates preconditions that the dep already satisfied when it was first started. This is intentional: gate evaluation is pure and `O(microseconds)`; running it in re-attach maintains the invariant uniformly; and if the current shell environment fails the gate, that's a real signal that the holder (the dependent that's about to use the dep) is in an environment that won't satisfy the dep's contract. The holder's own gate (in `RunOne` Phase 0 or wherever) will likely flag the same problem; flagging it here too gives the agent an attributable signal at the dep level.

### Why `kind="failed"` and not `kind="preflight_failed"`

The signal's `metadata.kind` carries the *envelope* class, not the cause. The codebase already establishes `metadata.kind ∈ {failed, started, rejected}` for envelopes that report decisions (no execution) and `metadata.kind ∈ {aggregate, cascade, dep_started, dep_attached, dep_detached}` for envelopes that report execution outcomes. Preflight failure decides *not to spawn*; semantically it belongs in the first family. `metadata.cause` carries the specific reason, matching the existing `/start-sensor` convention for `bootstrap_failed`, `plugin_root_missing`, `resolve_failed`, `registry_write_failed`, `prepare_failed`. Using the same `kind="failed", cause=<reason>` shape across all decision-only signals keeps the schema consumer-friendly: filter by `kind` to ask "did the sensor produce a runtime result?", filter by `cause` to ask "why was this decision made?".

## Tests

Organized by layer.

### A. `lib/sensor/requires_test.go`

Existing test `TestBuildRequiresGateSignal_Shape` updates assertions to the new shape:
- `metadata.kind == "failed"` (was `"aggregate"`)
- `metadata.cause == "preflight_failed"` (new)
- `metadata.missing_envs == ["FOO"]` (new)
- `metadata.heal_hint == "missing-env:FOO"` (unchanged)
- `evidence[]` per-failure rationale (unchanged)
- `remediation.instructions` non-empty (unchanged)

New: `TestBuildRequiresGateSignal_PopulatesAllMissingLists` — gate with one env failure + one tool failure + one context failure → `missing_envs`, `missing_tools`, `missing_contexts` all populated with the right identifiers.

New: `TestBuildRequiresGateSignal_OmitsEmptyMissingLists` — gate with only env failures → `missing_tools` and `missing_contexts` keys absent from `metadata` (not present as empty arrays).

### B. `lib/orchestrator/gate_test.go` (new file)

- `TestPreflightGate_PassReturnsNilSignal` — sensor without `requires[]` returns `(nil, false)`.
- `TestPreflightGate_FailReturnsCanonicalSignal` — sensor with unset env returns `(sig, true)` with `metadata.kind="failed"`, `cause="preflight_failed"`, populated `missing_envs`, `heal_hint`.
- `TestPreflightGate_UsesProvidedEnvelope` — confirms the returned signal carries the caller's `env.SensorID`, `env.Version`, `env.RunID`, `env.StartedAt` (not a freshly-generated envelope).

### C. `lib/orchestrator/preflight_test.go` — closes #36

Existing tests (`TestRunDeps_NoDeps`, `_SetupDepPASS`, `_SetupDepFAIL_RootCascadesViaCascadeSig`, `_BlockingDepStartFresh`, `_DAGCycle`, `_DepFileMissing`, `_TransitiveCascade`) remain green. New:

- `TestRunDeps_BlockingDepGateFails_EmitsPreflightSignalAndCascadesRoot`:
  - Setup: root `target` (non-blocking) depends on `blocking-dep` (blocking) which declares `requires: [{kind: "env", name: "REQUIRED_FOO_36"}]`.
  - Test deliberately does NOT `t.Setenv("REQUIRED_FOO_36", ...)`.
  - Runs `RunDeps(ctx, "target", root, ...)`.
  - Asserts:
    - `res.ExitCode == 0`
    - `res.Signals["blocking-dep"]["verdict"] == "error"`
    - `res.Signals["blocking-dep"]["metadata"]["kind"] == "failed"`
    - `res.Signals["blocking-dep"]["metadata"]["cause"] == "preflight_failed"`
    - `res.Signals["blocking-dep"]["metadata"]["missing_envs"]` contains `"REQUIRED_FOO_36"`
    - `res.Signals["blocking-dep"]["metadata"]["heal_hint"] == "missing-env:REQUIRED_FOO_36"`
    - `len(res.LiveStack) == 0` (no spawn)
    - `res.CascadeSig != nil`; `res.CascadeSig["metadata"]["failed_dep_id"] == "blocking-dep"`
    - stdout contains the dep's preflight-failed signal
    - registry has no entry for `blocking-dep` (no orphan PID, no `.harness/runtime/.../blocking-dep/` directory)

- `TestRunDeps_BlockingDepGatePasses_AttachesNormally`:
  - Sensor with `requires: [{kind: "env", name: "PRESENT_FOO_36"}]`; `t.Setenv("PRESENT_FOO_36", "x")`.
  - Gate passes; `AttachLiveDep` is called; `LiveStack` populated; `dep_started` signal emitted on stdout.
  - Cleanup detaches via `DetachLiveDep` in reverse.

### D. Existing tests that pattern-match on `metadata.kind == "aggregate"` for preflight paths

Audit and update. Expected impact (to be verified during implementation):
- `lib/orchestrator/lifecycle_test.go` — any test asserting `metadata.kind` on a Phase 0 fail (likely a small number).
- `skills/run-sensor/scripts/*_test.go` — possibly affected, depending on integration coverage of the gate path.
- `skills/start-sensor/scripts/*_test.go` — `metadata.kind == "failed"` and `cause == "preflight_failed"` are preserved; tests should remain green. `missing_envs/missing_tools/missing_contexts` payload shape may differ from the old `requiresGateAux` output (e.g., omitted-when-empty vs. always-present-as-empty-array); update tests where this matters.
- `test/heal-e2e/heal_e2e_test.go` — heal classifier routes on `heal_hint`, not on `kind`. Expected neutral.

The audit happens before merging the spec implementation; this section lists the expected blast radius, not a guess.

### E. `lib/orchestrator/gate_invariant_test.go` (new file)

Static-style test that grep-scans the repo for calls to `subprocess.StreamSubprocess`, `subprocess.Start`, and `subprocess.SpawnDetached` and verifies that each call site's file also contains an `orchestrator.PreflightGate` call (or appears in the allowlist):

```
allowlist = {
  "lib/watcher/spawn.go",            // spawns watcher binary, not sensor command
  "lib/subprocess/step.go",          // RunStep — prepare/teardown step commands
  "lib/subprocess/stream.go",        // primitive itself
  "lib/subprocess/detach.go",        // primitive itself
  "*_test.go",                       // tests stub subprocess directly
  "lib/orchestrator/testdata/*",
}
```

Coarse but cheap. Catches the "added a new spawn site without remembering the gate" regression. If false positives become noisy, replace with a `go/analysis`-based analyzer; for now, grep is enough.

### F. Manual end-to-end against the issue's reproduction (no automated e2e in this PR)

In a real project laid out like the one in the issue:

1. `health-check.json` (non-blocking, `requires: [{kind: "sensor", id: "run-project-nest"}, {kind: "tool", name: "curl"}]`).
2. `run-project-nest.json` (blocking, `requires: [{kind: "env", name: "BANKING_BASE_URL"}, {kind: "env", name: "RSA_PRIVATE_KEY"}, ...]`).
3. Shell with **none** of those envs exported.
4. Run: `HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off go run -C "$HARNESS_FRAMEWORK_ROOT" -tags=run_computational ./skills/run-sensor/scripts health-check`
5. Verify stdout: two JSONL signals; first with `sensor_id="run-project-nest"`, `verdict="error"`, `metadata.kind="failed"`, `metadata.cause="preflight_failed"`, `metadata.missing_envs` listing the unset envs; second with `sensor_id="health-check"`, `metadata.kind="cascade"`, `failed_dep_id="run-project-nest"`.
6. Verify total elapsed time: low milliseconds (vs. ~45 s pre-fix).
7. Verify `.harness/runtime/running_sensors.json`: no entry for `run-project-nest` (no orphan PID, no leftover `<run-id>/` directory).

This roteiro is recorded in the design doc for future regression checks and is not gated as an automated test in this PR; the unit test in section C covers the same logical path without requiring a real Nest project.

## Migration and compatibility

- **`metadata.kind` for preflight failures from `/run-sensor`** changes from `"aggregate"` to `"failed"`. Consumers reading `kind` to detect gate-fail should switch to `metadata.cause == "preflight_failed"`. `/heal-sensor`'s classifier routes on `metadata.heal_hint` (unchanged shape), so it is neutral.
- **`/start-sensor` preflight signal payload** stays the same on the outside (`kind="failed"`, `cause="preflight_failed"`, `missing_envs/tools/contexts`, `heal_hint`) but is now built by the canonical helper. The flat `aux` keys (`missing_envs[]`, etc.) are preserved; the only observable difference is the added richer `evidence[]` per-failure (was a single coarse summary in `requiresGateRationale`) and `remediation.instructions`. Tests that inspect `evidence[0].rationale` for the old summary string must update.
- **`orchestrator.RunRequiresGate` is removed.** No callers exist outside the four refactored sites. External users of `lib/orchestrator/` (if any beyond this repo) call `PreflightGate` instead.
- **`sensor.BuildMissingEnvSignal` is removed** (`env.go`). It was a deprecated wrapper around `BuildRequiresGateSignal` since the unified gate landed; final cleanup.
- **Schema compatibility**: `signal.json`'s `metadata` is `additionalProperties: true` (no enum on `metadata.kind`), so no schema changes are needed.

## Rejected alternatives

### Alt 1 — gate inside `subprocess.{StreamSubprocess,Start,SpawnDetached}` primitives

Push the gate down into the lowest-level spawn primitives so callers cannot bypass it. Rejected: `lib/subprocess/` is a generic process-spawn package that does not (and should not) know about `sensor.json` schema, `requires[]`, or `Envelope`. Adding that knowledge couples a generic primitive to a domain concept and prevents the same primitive from being reused for the watcher spawn, prepare-step spawn, and any future non-sensor subprocess. The pinch-point belongs at the orchestrator layer where sensor identity is already in scope.

### Alt 2 — generic guard `SpawnWithGate[T any](s Sensor, env Envelope, output string, spawnFn func() (T, error))`

Higher-order function that wraps every spawn call so it is structurally impossible to call `spawnFn` without first passing the gate. Rejected: the three callers with a `prepare → command → teardown` lifecycle (`RunOne`, `RunOneWithRoot`, `/start-sensor`) do not have a "single spawn function" to wrap — they have a lifecycle with multiple subprocess phases (prepare runs step commands, command runs the sensor command, teardown runs step commands). Wrapping the whole lifecycle in `spawnFn` blurs the invariant ("gate is about the sensor command, not the step commands"); wrapping only the command call requires a separate `PreflightGate` call to keep the gate at Phase 0 anyway. The guard adds ceremony without preventing the only realistic regression (forgetting to add `PreflightGate` to a new call site), which the static invariant test (E) catches more cheaply.

### Alt 3 — push gate into `AttachLiveDep` under the file lock

Move the gate evaluation inside `AttachLiveDep`'s `WithFileLock` callback, between "is the dep alive?" and `startBlockingDep`. Rejected: would require changing `AttachLiveDep`'s `(LiveDep, error)` signature to also carry the gate-fail signal, propagate it back through `RunDeps`, and reason about whether the signal counts as "an error" for the existing `attachErr != nil` branch (it does not — it's a structured failure, not an attach error). Keeping the gate in the caller preserves the existing AttachLiveDep contract and matches the pattern already established by `RunOne` and `/start-sensor` (gate before the spawn primitive call, in the same function as the spawn).

### Alt 4 — collapse `metadata.kind` and `metadata.cause` into a single richer `kind` enum

E.g. `metadata.kind ∈ {aggregate, cascade, preflight_failed, prepare_failed, registry_write_failed, ...}`. Considered and rejected for this PR. The codebase already commits to the two-axis split (`kind` = envelope class, `cause` = specific reason); collapsing them requires a coordinated rewrite of every signal-emitting call site and every consumer (heal classifier, auto-issue filer, run-sensor signal handler) — strictly out of scope for fixing #36. The dual-axis approach is consistent for this PR's use case; revisiting it is a separate design exercise if it becomes painful.

## Open questions

None blocking. Implementation surfaces two minor decisions to make during coding:

1. Whether to keep `cost_actual.latency_ms: 0` on preflight signals or omit `cost_actual` entirely. Current behavior keeps it at `0`; spec preserves that for consistency with `signal.json`'s `required` list (if it includes `cost_actual`). To be verified against the schema during implementation.

2. Whether `lib/orchestrator/gate_invariant_test.go` should use Go's `go/ast`/`go/parser` (compile-correct) or plain regex over file bytes (simpler). Default to regex; switch to AST only if false positives become noisy.

Both are implementation details that do not affect the design contract.
