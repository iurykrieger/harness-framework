# Heal-sensor cascade through dead blocking deps (closes #47)

## Problem

When a blocking dep dies shortly after attach (build error, panic, port-in-use,
toolchain failure), the dependent sensor runs anyway and times out. The
visible aggregate is `verdict=fail, evidence=[], metadata.kind=aggregate` —
nothing the `setup-failure-detector` hook can classify, so `/heal-sensor` is
never injected even though the real error in the dep's `raw.log` is obvious.

Real-world repro from the issue: `/run-sensor
assert-health-check-live-returns-200-health` whose blocking dep
`run-project-charge-api` fails `docker buildx` because `go work sync` points
at a missing module. Dep's `raw.log` contains:

```
go: cannot load module charge-worker-conciliation listed in go.work file: open charge-worker-conciliation/go.mod: no such file or directory
failed to solve: process "/bin/sh -c go work sync" did not complete successfully: exit code: 1
```

But the dependent's aggregate reaching the hook is empty:

```json
{
  "sensor_id":"assert-health-check-live-returns-200-health",
  "verdict":"fail",
  "severity":"high",
  "evidence":[],
  "metadata":{"exit_code":1,"kind":"aggregate","output_mode":"single"}
}
```

Three sub-bugs combine to produce this gap.

## Root cause

1. **`stopBlockingDep` (`lib/orchestrator/live_deps.go:291-330`) hard-codes
   `verdict=pass` for every blocking dep on detach.** The detached subprocess
   has no exit-code capture mechanism, so even when the dep is already dead
   the orchestrator emits a clean `pass` aggregate.

2. **No post-attach liveness gate.** After `AttachLiveDep` succeeds, the
   orchestrator (`runWithDepsImpl`, `lib/orchestrator/run.go:131-138`) goes
   straight to `RunOneWithRootCapture` for the dependent. If the dep died in
   the milliseconds between attach and the dependent's command, the
   dependent runs against a corpse and produces irrelevant timeout output.

3. **Single-output sensors emit `evidence=[]`.** When `output=single` and
   exit_code is non-zero, `lib/orchestrator/lifecycle.go` (`RunOne` and
   `RunOneWithRoot`) builds the aggregate from `exit_code_map` alone — the
   subprocess's stdout/stderr is captured but not folded into the aggregate's
   `evidence[]`. The hook's `case "aggregate":` then has nothing to feed the
   `stderr_pattern` rule, even when the failing sensor has direct stderr
   output that names the cause.

These three add up: cascade Signals (the natural carrier for dep failures)
are never emitted because (1) and (2) both lie about dep liveness, and the
fallback aggregate path is blind because (3) gives the classifier no input.
The hook's `case "cascade":` branch
(`hooks/setup-failure-detector.go:119-132`) is correct code that has never
been reachable in this scenario.

This is distinct from issue #20 (`2026-05-13-cascade-through-blocking-dep-design.md`),
which moved `FirstFailedDep` above the `blocking` branch in `RunDeps` so that
an **upstream non-blocking** failure cascades through a blocking dep. That
fix handles failures *known before* `AttachLiveDep`. The present spec handles
failures discovered *after* `AttachLiveDep` succeeds — when the blocking dep
itself dies post-spawn.

## Fix

Five coordinated changes, each independently testable and committable.

### 1. Capture dep exit status via wrapper

In `startBlockingDep`, wrap the dep's command so its exit code is written to
a sidecar file before the subprocess group ends:

```
sh -c '( <command> ); ec=$?; echo $ec > <exit_code_file>; exit $ec'
```

The file lives at
`<projectRoot>/.harness/runtime/<dep_id>/<run_id>/exit_code` (sibling of
`raw.log`, which already lives in the same per-run directory). `mkdir -p` of
that directory happens immediately before the spawn, mirroring the existing
`raw.log` / `signals.log` setup.

The wrapper is portable POSIX shell and survives `sh -c` already being the
spawn shell — `sh -c 'sh -c "…"'` is well-defined. The wrapper does NOT
inherit `set -e` from the parent: it runs the user's command inside a
subshell `( <command> )`, then captures `$?` unconditionally before
re-exiting with the original code. A unit fixture asserts that a command
exiting with code 42 produces `42` in the `exit_code` file and propagates
exit 42 to the wrapper itself.

### 2. Honest `stopBlockingDep` aggregate

`stopBlockingDep` reads the `exit_code` file at teardown:

- **Missing or `=0`** → keep the current `verdict=pass` aggregate. Empty
  `exit_code` file (the subprocess was killed by SIGTERM/SIGKILL before
  writing it) is treated as `=0` because the kill came from the orchestrator
  itself, not from the dep failing.
- **Non-zero** → emit `verdict=fail` (severity `high`), populate `evidence[]`
  with the last 20 non-empty lines of `raw.log` as `excerpt` entries, and
  set `metadata.exit_code = <N>`. The aggregate keeps `metadata.kind =
  "aggregate"`, mirroring how every other aggregate Signal is constructed.

Hard-coded tail size of 20 lines. Not configurable — keeps the API surface
minimal and matches the conservative tail sizes used elsewhere in the
codebase.

### 3. Post-attach liveness gate

A new function `awaitDepLiveness(deps []LiveDep, projectRoot string,
v *schema.Validator, stdout, stderr io.Writer) (deadDepSignal map[string]interface{})`
lives in `lib/orchestrator/live_deps.go`. It iterates the `LiveStack`
contents; if any dep's PID is no longer alive (via existing
`registry.IsPIDAlive`), it constructs that dep's aggregate the same way
`stopBlockingDep` does (exit_code + raw.log tail) and returns it. Returns
`nil` when all deps are alive.

`runWithDepsImpl` (`lib/orchestrator/run.go`) calls `awaitDepLiveness`
**between** `RunDeps` and `RunOneWithRootCapture` (right after the existing
`detachAll` defer, before line 132). When a dead dep is returned:

1. Emit the dep's aggregate Signal to stdout (validated).
2. Build the cascade Signal for the target via
   `BuildCascadeSignal(target, depAggregate)`, emit it, and skip
   `RunOneWithRootCapture`.
3. Return exit code 1 (same as the existing DAG-cascade path), letting
   `defer detachAll()` clean up the dead dep's registry entry on the way out.

This places the dep-liveness gate at the same architectural layer as the
issue #20 fix (one gate at one call site, mirroring `PreflightGate`).

### 4. Populate evidence in single-output aggregates

In `RunOne` and `RunOneWithRoot` (`lib/orchestrator/lifecycle.go`), when
`output == "single"` and the subprocess exit code is non-zero, populate
`evidence[]` with the last 20 non-empty lines of captured stdout+stderr as
`excerpt` entries. The line tail is sourced from the same buffer that the
subprocess streamer (`lib/subprocess/stream.go`) already maintains for raw
log persistence — the implementation will expose a `Tail(n int)` method on
the streamer rather than re-reading the file.

This is identical mechanically to the stderr/stdout tail used in (2) for
deps, just applied to the running sensor rather than a separate dep. It
unlocks the `stderr_pattern` rule for non-dep failures (standalone single
sensors that fail with informative output).

### 5. Classification: new shape, patterns, rule

**`lib/heal/classify.go`** — extend the closed Shape enum:

```go
ShapeSubprocessFailed Shape = "subprocess-failed"
```

Update `IsKnown()`.

**`lib/heal/patterns.go`** — add curated non-capturing patterns:

```go
{re: regexp.MustCompile(`failed to solve:`),                        shape: ShapeSubprocessFailed},
{re: regexp.MustCompile(`did not complete successfully: exit code: \d+`), shape: ShapeSubprocessFailed},
{re: regexp.MustCompile(`cannot load module .* listed in go\.work`), shape: ShapeSubprocessFailed},
{re: regexp.MustCompile(`COPY failed:`),                            shape: ShapeSubprocessFailed},
```

**`lib/heal/rules/subprocess_failed.go`** (new) — implements `heal.Rule`.
Match condition: `signal.Metadata.ExitCode != nil && *signal.Metadata.ExitCode != 0`
AND some `evidence[i].Excerpt` (or `Rationale`) matches one of the patterns
above. Detail is the matching line.

**`lib/heal/rules/registry.go`** — append `subprocessFailed{}` after
`stderrPatternRule{}`. Order matters: `stderrPatternRule` covers
`command not found` / `connection refused` / `ENOENT .env` patterns that map
to existing shapes with auto-apply actions; `subprocessFailed` is the
catch-all for non-auto-applyable build/toolchain failures.

**`skills/heal-sensor/scripts/diagnose.go`** — when `shape ==
ShapeSubprocessFailed`, the LLM-flavored plan returns `propose_only[]` only
(zero `auto_apply[]`):

```json
{
  "diagnosis": {"failed_sensor_id": "<dep>", "shape": "subprocess-failed", ...},
  "propose_only": [
    {"kind": "manual-inspect", "command": "cat .harness/runtime/<dep>/<run_id>/raw.log", "rationale": "..."},
    {"kind": "manual-rebuild", "command": "docker compose build --no-cache <service>", "rationale": "..."}
  ]
}
```

No changes to `lib/heal/apply.go`: `ShapeSubprocessFailed` has no allowlisted
actions, so the propose-only lane is the only emission path. The user sees
the proposals in the heal Signal's `remediation` field.

## Definition of Done

Binary checks. Each must be objectively verifiable.

1. **Dep exit captured**: after `/run-sensor` of a target whose blocking dep
   intentionally exits 1, the file
   `<projectRoot>/.harness/runtime/<dep_id>/<run_id>/exit_code` contains
   `1`.
2. **Honest stopBlockingDep aggregate**: in the same scenario, the JSONL
   line for the dep's aggregate has `verdict=fail`, `severity=high`,
   `metadata.exit_code=1`, and at least one `evidence[]` entry with a
   non-empty `excerpt`.
3. **Liveness gate emits cascade**: when the dep dies before the dependent
   runs, the stream contains exactly two signals after the dep aggregate, in
   order:
   1. dep's aggregate (`verdict=fail`),
   2. cascade Signal of the dependent (`metadata.kind=cascade`,
      `metadata.failed_dep_id=<dep>`).
   The dependent's aggregate does NOT appear in the stream (asserted as
   JSONL line count == 2 + count of preceding dep aggregates, not via
   raw.log absence). The dependent's command is never spawned.
4. **Hook classifies cascade end-to-end**: replaying the JSONL stream of (3)
   through `setup-failure-detector` produces an injection whose `rule` is
   `subprocess-failed` and whose `--sensor=` argument points at the dep's
   sensor file (not the dependent's).
5. **Single-output evidence**: a sensor with `output=single, blocking=false`
   that exits 1 with stderr containing `failed to solve: ...` has at least
   one `evidence[].excerpt` line carrying that message in its aggregate
   Signal.
6. **Shape enum extended**: `heal.ShapeSubprocessFailed.IsKnown()` returns
   true; `heal.ParsePlan` accepts plans whose `diagnosis.shape ==
   "subprocess-failed"`.
7. **Heal action allowlist unchanged**: no new kinds appear in `apply.go`'s
   switch; `ShapeSubprocessFailed` plans contain `propose_only[]` only.
8. **Existing suites stay green**: `go test ./lib/...`, `go test
   -tags=run_computational ./skills/...`, `go test -tags=run_inferential
   ./skills/...`, and every existing skill-tag suite pass with no changes.
9. **Schema validation**: every Signal emitted by the new paths validates
   against `schemas/signal.json` through the existing `validateOrFallback`
   helper.

## Scope

In:

- `lib/orchestrator/live_deps.go`: `startBlockingDep` exit-code wrap,
  `stopBlockingDep` honest aggregate, new `awaitDepLiveness` function.
- `lib/orchestrator/run.go`: call `awaitDepLiveness` between `RunDeps` and
  `RunOneWithRootCapture`; on dead dep, emit cascade and skip target.
- `lib/orchestrator/lifecycle.go`: populate `evidence[]` from captured
  stdout/stderr tail when `output=single` and `exit_code != 0`.
- `lib/subprocess/stream.go`: expose `Tail(n int) []string` (or equivalent)
  on the streamer if not already available — the streamer already buffers
  recent lines for raw-log persistence.
- `lib/heal/classify.go`: extend Shape enum + `IsKnown`.
- `lib/heal/patterns.go`: four new curated patterns mapping to
  `ShapeSubprocessFailed`.
- `lib/heal/rules/subprocess_failed.go` (new) and `registry.go` (one new
  slice entry).
- `skills/heal-sensor/scripts/diagnose.go`: `propose_only[]` template for
  the new shape.
- Tests: see "Verification plan" below.

Out:

- Auto-applying `docker compose build --no-cache`, `go work sync`, or any
  build/toolchain command. Heal stays diagnostic-only for this shape.
- Changing how detached deps are supervised in general (no watcher, no
  PR_SET_CHILD_SUBREAPER, no `/proc` polling).
- `/start-sensor` registry behaviour (the `awaitDepLiveness` gate runs on
  the `/run-sensor` path only — `/start-sensor` already has separate
  liveness semantics via the registry holder model).
- Configuration knobs (tail size, liveness timeout) — hardcode for now,
  promote to env var only if real cases demand it.
- Backward-compat shim for old `exit_code` files: there are none, the file
  is new.

## Anti-scope

- Do not change `BuildCascadeSignal`'s envelope or `signal.json` schema.
- Do not add new heal action kinds.
- Do not change `AttachLiveDep` / `DetachLiveDep` signatures.
- Do not introduce a watcher for orchestrator-managed deps.
- Do not modify the existing issue-#20 cascade gate (`FirstFailedDep` lift
  in `RunDeps`) — the new gate is downstream of it and independent.

## Technical decisions

- **Exit code via wrapper, not OS-level supervision.** A
  `sh -c '( cmd ); echo $? > file'` wrapper is portable across Linux and
  macOS, requires no new system calls, and survives commands that themselves
  start subshells. Alternatives considered: `PR_SET_CHILD_SUBREAPER` (Linux
  only), polling `/proc/<pid>/status` (Linux only), reading wait status via
  zombie reaping (requires the parent to be alive when the dep dies — not
  guaranteed across `/run-sensor` invocations). The wrapper is dumb and
  durable.
- **Tail size hardcoded at 20 lines.** Two tails (raw.log for deps,
  stdout/stderr for the running sensor) both use this number. Conservative
  — enough to capture a typical Go panic stack or docker build error
  signature, small enough to keep Signal JSON reasonable.
- **Cascade Signal payload is unchanged.** The existing `BuildCascadeSignal`
  already accepts an aggregate map and produces a fully-formed cascade
  Signal. The new path simply supplies a real `verdict=fail` aggregate
  instead of the placeholder `verdict=pass`.
- **One gate, one call site.** `awaitDepLiveness` runs once in
  `runWithDepsImpl`, mirroring `PreflightGate` and the issue-#20 cascade
  gate. No scatter of "is the dep alive?" checks across the codebase.
- **`subprocess-failed` is a `propose_only[]` shape from day one.** The
  failure surface (docker, go toolchain, npm, cargo) does not have safe,
  idempotent auto-apply actions; auto-running `--no-cache` rebuilds is
  expensive and potentially destructive. The shape exists to surface the
  failure clearly to the user with curated remediation suggestions, not to
  auto-heal.
- **Patterns are non-capturing.** Unlike `Required tool "X" is not on PATH`,
  these failure lines don't have a single canonical "detail" token. The
  rule reports the matching line itself as the detail, which is informative
  and avoids regex fragility.
- **Item 3 (single-output evidence) bundled in this PR.** Cohesive — the
  same `Tail(n)` primitive serves both dep and direct-failure paths.
  Splitting it forces two PRs that touch overlapping code in
  `lifecycle.go`.

## Verification plan

1. **Unit — orchestrator**
   - `lib/orchestrator/live_deps_test.go` (extend): test `stopBlockingDep`
     with (a) missing `exit_code` file → pass; (b) `exit_code=0` → pass;
     (c) `exit_code=1` + raw.log with 5 lines → fail aggregate with 5
     evidence entries; (d) `exit_code=2` + raw.log with 50 lines → fail
     aggregate with 20 evidence entries (tail).
   - `awaitDepLiveness` test: stack of two LiveDeps, one alive, one dead →
     returns the dead one's aggregate.
2. **Integration — orchestrator end-to-end**
   - `lib/orchestrator/integration_runtime_logs_test.go` (extend): blocking
     dep that exits 1 immediately → expect three JSONL lines (dep aggregate
     `verdict=fail`, cascade for target, no target aggregate) and exit code
     1.
3. **Unit — heal**
   - `lib/heal/rules/subprocess_failed_test.go` (new): table-driven match
     cases for each of the four patterns, plus negative cases (exit_code=0,
     missing exit_code, patterns absent).
   - `lib/heal/classify_test.go` (extend): assert
     `ShapeSubprocessFailed.IsKnown() == true`.
4. **End-to-end — heal pipeline**
   - `lib/heal/heal_e2e_test.go` (extend): build a cascade Signal of the
     shape produced by (3), run `ClassifyWith(rules.Registered(), …)` →
     expect `Result{Rule: "subprocess-failed", Shape: ShapeSubprocessFailed, …}`.
   - `skills/heal-sensor/scripts/diagnose_test.go` (extend): when input is
     `shape=subprocess-failed`, output plan has non-empty `propose_only[]`
     and empty `auto_apply[]`.
5. **Single-output evidence**
   - `skills/run-sensor/scripts/run-computational_test.go` (extend): sensor
     with `output=single`, command `sh -c 'echo "failed to solve: oops" 1>&2;
     exit 1'` → aggregate has at least one `evidence[].excerpt` containing
     `failed to solve: oops`.
6. **Hook integration (manual smoke)**
   - Construct the issue's reproduction fixtures in `.harness/sensors/` and
     run `/run-sensor assert-health-check-live-returns-200-health`. Confirm
     the hook injects `/heal-sensor --signal-from=transcript
     --sensor=.harness/sensors/run-project-charge-api.json` with `rule=subprocess-failed`.

## Sequencing

Five commits, in order. Each is buildable and testable in isolation.

1. **Exit-code wrap** — `startBlockingDep` writes to `exit_code` file;
   `stopBlockingDep` still emits `verdict=pass`. Tests assert the file
   exists with the right contents.
2. **Honest stopBlockingDep** — read the file, emit real verdict. Tests
   extend to cover the new emission.
3. **`awaitDepLiveness` + run.go integration** — adds the gate; tests cover
   the dead-dep cascade flow.
4. **Shape enum, patterns, rule, diagnose template** — pure heal-side
   changes. Tests new + extended.
5. **Single-output evidence** — `Tail(n)` on streamer, lifecycle.go
   integration. Tests extended.

## References

- Issue: https://github.com/iurykrieger/harness-framework/issues/47
- Sibling spec (upstream-cascade through blocking deps):
  `docs/superpowers/specs/2026-05-13-cascade-through-blocking-dep-design.md`
  (issue #20).
- Existing cascade machinery: `lib/orchestrator/cascade.go`,
  `lib/orchestrator/preflight.go`, `lib/orchestrator/run.go`.
- Existing heal contracts: `lib/heal/`, `lib/heal/rules/`,
  `skills/heal-sensor/SKILL.md`.
- Hook entrypoint: `hooks/setup-failure-detector.go:93-144`
  (`findFailingInvocation`, `case "cascade":` branch).
