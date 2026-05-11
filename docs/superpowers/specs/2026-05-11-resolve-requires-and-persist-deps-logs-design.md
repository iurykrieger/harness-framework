# Spec — Resolve full `requires[]` set and persist per-dep logs under `.runtime/`

**Status:** draft (awaiting spec review and user approval)
**Author:** iurykrieger@stone
**Date:** 2026-05-11
**Branch / worktree:** `install-sensor-requirements`

## 1. Context

Two related gaps surfaced while running sensors with declared dependencies.

### Gap 1 — `requires[]` is partially resolved

`schemas/sensor.json` now models preconditions as a discriminated-union `requires[]` array keyed by `kind` (sensor / tool / context / permission / env / step). At runtime, only three of those kinds are honoured today:

| `kind` | Handled? | Where |
|---|---|---|
| `sensor` | yes | `lib/orchestrator/{dag.go,live_deps.go,preflight.go}` — DAG resolved, blocking deps attached, cascades emitted |
| `step` | yes | `lib/orchestrator/lifecycle.go::runPreparePhase` — fail-fast prepare loop |
| `env` | partially | `lib/sensor/env.go::CheckRequiredEnv` — verifies non-optional vars exist; emits `verdict=error` with per-var rationale |
| `tool` | **no** | silently ignored |
| `context` | **no** | silently ignored |
| `permission` | **no** | silently ignored |

A sensor declaring `requires[kind=tool].name = "docker"` runs anyway; if the binary is missing, the failure surfaces inside the command (e.g. `sh: docker: command not found`), which the heal classifier sometimes catches via stderr patterns and sometimes doesn't. The check belongs in pre-flight, not in stderr archaeology.

### Gap 2 — Dependency observations are not persisted

Today, the only path that writes `.runtime/sensors/<id>/{raw,signals}.log` is `/start-sensor`. Concretely:

- A **non-blocking sensor dep** invoked via `RunDeps → RunOne` emits JSONL signals straight to stdout. Nothing lands on disk; there is no `.runtime/sensors/<dep-id>/` directory at all.
- A **blocking sensor dep** brought up by `orchestrator.startBlockingDep` (`lib/orchestrator/live_deps.go:161`) creates `<id>/raw.log` and `<id>/signals.log` but **does not spawn a watcher**. `raw.log` receives the subprocess stdout; `signals.log` stays empty by design (see the inline comment at `live_deps.go:158-160`).

Result: `/run-sensor e2e-tests` with `requires[kind=sensor].id = "run-project"` shows the dep's aggregate Signal on stdout but leaves no per-dep trace under `.runtime/`. `/tail-sensor run-project` returns nothing useful. Cross-skill diagnostics (heal loops, post-mortem analysis) lose half the story.

## 2. Goal

Two converging changes inside `lib/orchestrator/lifecycle.go::RunOne` and `lib/orchestrator/live_deps.go::startBlockingDep`:

1. **Unified `requires[]` gate (Phase 0).** Replace `CheckRequiredEnv` with `CheckRequiresGate`, which collects all pre-execution failures across tool / context / env in one pass and emits a single `verdict=error` Signal with one `evidence[]` entry per failure and a `metadata.heal_hint` shaped to drive `/heal-sensor`.
2. **Uniform `.runtime/` persistence.** Every sensor invocation (root, non-blocking dep, blocking dep) writes `raw.log` and `signals.log` under `.runtime/sensors/<id>/<run_id>/`. Stdout JSONL stays the canonical wire protocol; the file is the canonical at-rest record.

## 3. Non-goals

- **No schema bump.** `schemas/sensor.json` and `schemas/signal.json` are untouched. The new code reads only fields the schema already declares.
- **No auto-fix for missing tool / context.** The gate emits `heal_hint`; `/heal-sensor` decides remediation (or doesn't — both paths are valid for now). No install attempts, no path creation.
- **No permission check.** `requires[kind=permission]` is declarative-only. Claude Code's built-in permission engine prompts the user the first time the command touches an unauthorised scope — re-implementing detection in the runner would duplicate work and diverge. The gate silently ignores `kind=permission` entries; SKILL.md and CLAUDE.md document this contract.
- **No `.env` auto-sourcing.** `requires[kind=env]` continues to verify-only. Heal-hint parity with the new kinds is the only env-side change.
- **No retention / GC of `.runtime/sensors/<id>/<run_id>/`.** Run-isolation by `run_id` is owned by a parallel task; this spec only ensures the path layout is `<id>/<run_id>/` so that retention work can hook in without re-touching the orchestrator.
- **No change to runner scripts.** `skills/run-sensor/scripts/run-{computational,inferential}.go` keep their current shape; the orchestrator absorbs every change.

## 4. Decisions

Captured during brainstorming on 2026-05-11.

1. **Gate semantics: check + heal_hint, no inline remediation.** The runner verifies and reports; `/heal-sensor` owns repair. This keeps each skill's responsibility focused and matches the current `missing-env` flow.
2. **Single Signal per gate failure batch.** Collect all failures, emit one `verdict=error` Signal with multiple `evidence[]` entries. `metadata.heal_hint` carries the shape of the **first** failure in declaration-order priority (tool → context → env). Matches today's `heal_hint` protocol (one hint per signal) — the classifier walks evidence rationales to discover the rest.
3. **`requires[kind=permission]` is declarative.** Not checked, not echoed in evidence. Sensors may declare it for human documentation; the runtime ignores it.
4. **Run-id is a path segment.** `.runtime/sensors/<id>/<run_id>/{raw,signals}.log` — not flat `<id>/{raw,signals}.log` and not `<id>/<run_id>.{raw,signals}.log`. Retention work owns symlinks (`latest/`), pruning, and rotation.
5. **`signals.log == stdout JSONL`, byte-for-byte.** Both come from the same `MultiWriter`; the invariant is load-bearing for `/tail-sensor` and post-hoc diagnostics. If a Signal fails validation, the fallback Signal lands in both places (the tee is blind to validation).
6. **Cascade and dep_attached / dep_detached signals are persisted too.** Helper `emitSignalWithPersistence` applies the invariant at every emission point in the orchestrator.
7. **Watcher is extracted, not duplicated.** `lib/watcher/spawn.go` is the single producer of watcher subprocesses; `/start-sensor` and `orchestrator.startBlockingDep` both call it.

## 5. Architecture overview

```
                         ┌───────────────────────┐
                         │  RunOne (orchestrator)│
                         └──────────┬────────────┘
                                    │
        ┌───── Phase 0 ─────┐       │
        │ requires gate     │◄──────┤
        │ (tool/ctx/env)    │       │
        └─────────┬─────────┘       │
                  │ fail            │ pass
                  │                 │
                  ▼                 ▼
       emit verdict=error   ┌───── Phase 1 ─────┐
       with heal_hint;      │ prepare[] (step)  │
       abort lifecycle.     └─────────┬─────────┘
       Tee → signals.log              │ pass
       Tee → raw.log (empty)          ▼
                            ┌───── Phase 2 ─────┐
                            │ command           │
                            │  · stdout → tee   │──► raw.log
                            │  · patterns →     │──► signals.log
                            │    individual     │      &
                            │    signals        │     stdout JSONL
                            │  · aggregate      │
                            └─────────┬─────────┘
                                      ▼
                            ┌───── Phase 3 ─────┐
                            │ teardown[]        │
                            └───────────────────┘
```

For blocking deps the same `raw.log` / `signals.log` invariant holds, but the watcher subprocess (not the runner) produces them: it tails `raw.log`, applies the dep's patterns, and writes `signals.log`.

### Touched modules

| Layer | File | Change |
|---|---|---|
| `lib/sensor` | `requires.go` (new) | `Failure`, `Gate`, `CheckRequiresGate`, `BuildRequiresGateSignal` |
| `lib/sensor` | `env.go` | `BuildMissingEnvSignal` deprecated → wrapper |
| `lib/heal` | `classify.go` | add `ShapeMissingContext` |
| `lib/heal/rules` | `missing_context.go` (new) | regex on rationale matching `checkContext` output |
| `lib/watcher` | `spawn.go` (new) | extract from `start.go::runStart` |
| `lib/subprocess` | `stream.go` | `StreamConfig.RawLogPath` optional field |
| `lib/orchestrator` | `lifecycle.go` | Phase 0 swap + tee wiring; new `prepareRuntimeDir` |
| `lib/orchestrator` | `live_deps.go` | `startBlockingDep` calls `watcher.Spawn`; `emitSignalWithPersistence` everywhere |
| `lib/orchestrator` | `preflight.go` | use `emitSignalWithPersistence` for cascade signals |
| `skills/start-sensor/scripts` | `start.go` | call `watcher.Spawn` instead of inline `os.StartProcess` |
| `skills/run-sensor/scripts` | `run-{computational,inferential}.go` | **no change** |
| `schemas/` | — | **no change** |

## 6. `lib/sensor/requires.go`

```go
package sensor

import "os/exec"

// Failure describes one unsatisfied precondition.
type Failure struct {
    Kind       string // "tool" | "context" | "env"
    Identifier string // tool: name; context: path; env: var name
    Rationale  string // formatted text for evidence[].rationale
    HealShape  string // value of lib/heal.Shape encoded as string to avoid import cycle
}

// Gate aggregates failures from all checks.
type Gate struct {
    Failures []Failure
}

func (g Gate) Failed() bool { return len(g.Failures) > 0 }

// GateOpts injects external dependencies for testability.
type GateOpts struct {
    LookupEnv func(string) (string, bool) // default: os.LookupEnv via package-level LookupEnvFn
    LookPath  func(string) (string, error) // default: exec.LookPath
    Stat      func(string) error           // default: stat helper around os.Stat
}

// CheckRequiresGate runs each check in fixed order — tool → context → env —
// and collects all failures (no fail-fast). kind=sensor / kind=step / kind=permission
// entries are ignored: sensor is handled by the orchestrator DAG, step by the
// prepare phase, permission by Claude Code's permission engine.
func CheckRequiresGate(sensorJSON map[string]interface{}, opts GateOpts) Gate

// BuildRequiresGateSignal constructs the verdict=error Signal emitted by the
// runner when Gate.Failed() is true. One evidence[] entry per failure, in the
// same order as Gate.Failures. metadata.heal_hint = "<HealShape>:<Identifier>"
// derived from the first failure. remediation.instructions aggregates a human
// summary listing every failure.
func BuildRequiresGateSignal(env Envelope, outputMode string, gate Gate) map[string]interface{}
```

### Per-check rationale format

| Kind | Rationale | HealShape |
|---|---|---|
| `tool` | `Required tool "docker" is not on PATH` | `binary-not-found` (existing) |
| `context` | `Required context path "./.env" does not exist` | `missing-context` (new) |
| `env`  | `Required environment variable FOO is not set` (with optional `: <description>` suffix) | `missing-env` (existing) |

The env shape preserves `lib/heal/rules.missingEnvRegex` verbatim. The tool shape reuses the existing `binary-not-found` rule (the rationale is added to the regex's allowlist of phrasings — currently it matches `\bcommand not found\b`; we extend with `Required tool ".*" is not on PATH`). The new context rule is added in §7.

### `BuildMissingEnvSignal` compat

The existing function becomes a thin wrapper that converts `[]MissingEnv` to `Gate{Failures: ...}` and delegates to `BuildRequiresGateSignal`. Tests that assert on its output continue to pass.

## 7. `lib/heal` additions

`lib/heal/classify.go`:

```go
const (
    ShapeMissingEnv         Shape = "missing-env"
    ShapeBinaryNotFound     Shape = "binary-not-found"
    ShapeEnvFileAbsent      Shape = "env-file-absent"
    ShapeServiceUnavailable Shape = "service-unavailable"
    ShapeMissingContext     Shape = "missing-context" // NEW
)
```

`Shape.IsKnown()` adds `ShapeMissingContext`.

`lib/heal/rules/missing_context.go` (new):

```go
package rules

import (
    "regexp"
    "github.com/iurykrieger/harness-framework/lib/heal"
)

var missingContextRegex = regexp.MustCompile(`Required context path "([^"]+)" does not exist`)

type missingContextRule struct{}

func (missingContextRule) Name() string { return "missing_context" }

func (missingContextRule) Match(sig heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
    for _, ev := range sig.Evidence {
        if m := missingContextRegex.FindStringSubmatch(ev.Rationale); m != nil {
            return true, heal.ShapeMissingContext, m[1] // detail = path
        }
    }
    return false, "", ""
}
```

Register in the same place the existing rules are registered.

## 8. `lib/watcher/spawn.go` (extracted)

```go
package watcher

import (
    "fmt"
    "os"
    "path/filepath"
)

// SpawnOpts captures everything needed to launch a watcher subprocess.
type SpawnOpts struct {
    ProjectRoot     string
    SensorID        string
    RunID           string
    RawLogPath      string
    SignalsLogPath  string
    EnvelopeJSON    []byte
    PatternsJSON    []byte
    SubprocessPID   int
}

// Spawn launches the watcher binary detached, returning its PID.
// Owns: opening the watcher log file, building env vars, calling
// os.StartProcess, capturing PID before Release.
func Spawn(opts SpawnOpts) (pid int, err error)

// BinaryPath returns the absolute path of the watcher binary.
// Lifted from start.go::watcherBinaryPath.
func BinaryPath() (string, error)
```

`skills/start-sensor/scripts/start.go::runStart` is refactored to call `watcher.Spawn(opts)` instead of the inline 30-line `os.StartProcess` block. Behaviour is identical; tests pass unchanged.

## 9. `lib/subprocess/stream.go` tee hook

`StreamConfig` gains:

```go
type StreamConfig struct {
    // ... existing fields ...
    RawLogPath string // optional; when non-empty, stdout+stderr are tee-written to this file
}
```

Implementation: when `RawLogPath != ""`, open with `O_CREATE|O_WRONLY|O_TRUNC` mode `0644` and wrap the existing combined-output reader in an `io.TeeReader`. On write errors, log to stderr and keep streaming — never abort. The `metadata.lifecycle.runtime_persistence = "degraded"` flag (§10) records the degradation.

## 10. `lib/orchestrator/lifecycle.go::RunOne` changes

### Phase 0 (gate)

```go
gate := sensor.CheckRequiresGate(s.JSON, sensor.GateOpts{
    LookupEnv: sensor.LookupEnvFn,
})
if gate.Failed() {
    sig := sensor.BuildRequiresGateSignal(envelope, output, gate)
    // emit + persist via emitSignalWithPersistence (§11)
    return sig, 0
}
```

### Runtime dir + tee wiring

A new helper:

```go
func prepareRuntimeDir(projectRoot, sensorID, runID string) (rawLogPath, signalsLogPath string, err error) {
    dir := filepath.Join(projectRoot, ".runtime", "sensors", sensorID, runID)
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return "", "", fmt.Errorf("runtime_dir: %w", err)
    }
    return filepath.Join(dir, "raw.log"), filepath.Join(dir, "signals.log"), nil
}
```

`RunOne` calls this once at the top (after env-elope build). On error: emit `verdict=error` Signal `metadata.kind = "runtime_dir_failed"` and return (signals.log == stdout invariant is load-bearing; we don't degrade silently).

`stdout` (the `io.Writer` parameter) is wrapped:

```go
signalsLogFile, _ := os.OpenFile(signalsLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
defer signalsLogFile.Close()
multiStdout := io.MultiWriter(stdout, signalsLogFile)
```

All subsequent emissions in `RunOne` use `multiStdout` instead of `stdout`. `subprocess.StreamSubprocess` receives `RawLogPath: rawLogPath` in its `StreamConfig`.

### Phase 0 vs runtime dir ordering

Runtime dir is created BEFORE gate check. Rationale: a failing gate produces a Signal — we want that Signal in `signals.log` too. The cost is one `mkdir` per run even when the gate fails; negligible.

If `prepareRuntimeDir` itself fails, no `signals.log` is available; the runtime_dir_failed Signal goes to stdout only. This is the only path that breaks the invariant, and we accept it (the alternative is to abort silently, which is worse).

## 11. `emitSignalWithPersistence` helper

New helper in `lib/orchestrator/`:

```go
// emitSignalWithPersistence writes a Signal to stdout JSONL and appends a copy
// to .runtime/sensors/<sensorID>/<runID>/signals.log. Used by every emission
// point in the orchestrator: cascade signals, dep_attached, dep_detached,
// dep_started, aggregate, RunOne aggregate, runtime_dir_failed.
//
// stdout is wrapped in a MultiWriter only for the duration of one Encode call;
// the helper takes care of opening/closing signals.log to avoid keeping FDs
// across nested calls.
func emitSignalWithPersistence(sig map[string]interface{}, stdout io.Writer, projectRoot, sensorID, runID string) error
```

All current `json.NewEncoder(stdout).Encode(...)` calls in `live_deps.go` and `preflight.go` are replaced with this helper. Tests verify that each emission point produces the same byte sequence in stdout and signals.log.

### Cascade run_id

`BuildCascadeSignal` already includes `run_id` in the Signal envelope. The helper extracts `sig["run_id"]` as the path segment when emitting a cascade Signal for sensor X. If `run_id` is absent (shouldn't happen post-schema-validation), the helper falls back to `time.Now().UTC().UnixNano()` formatted as a sortable string and logs a warning to stderr.

## 12. `lib/orchestrator/live_deps.go::startBlockingDep` changes

The function currently:

1. Creates `raw.log` and `signals.log` files.
2. Spawns subprocess detached via `subprocess.SpawnDetached`.
3. Writes registry entry with `WatcherPID: 0`.

The change:

```go
func startBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry) error {
    runID := uuid.NewString()
    rawLogPath, signalsLogPath, err := prepareRuntimeDir(r.ProjectRoot(), dep.ID, runID)
    if err != nil {
        return err
    }

    det, err := subprocess.SpawnDetached(subprocess.DetachConfig{Command: command, LogFile: rawLogPath})
    if err != nil {
        return fmt.Errorf("spawn: %w", err)
    }

    envelopeJSON, _ := json.Marshal(libsensor.Envelope{
        SensorID: dep.ID, Version: stringField(dep.JSON, "version"),
        RunID: runID, StartedAt: time.Now().UTC().Format(...),
    })
    patternsJSON := readPatternsAsJSON(dep.JSON)

    watcherPID, err := watcher.Spawn(watcher.SpawnOpts{
        ProjectRoot:     r.ProjectRoot(),
        SensorID:        dep.ID,
        RunID:           runID,
        RawLogPath:      rawLogPath,
        SignalsLogPath:  signalsLogPath,
        EnvelopeJSON:    envelopeJSON,
        PatternsJSON:    patternsJSON,
        SubprocessPID:   det.PID,
    })
    if err != nil {
        _ = killGroup(det.PGID) // don't orphan
        return fmt.Errorf("watcher: %w", err)
    }

    rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
        SensorID:   dep.ID,
        PID:        det.PID,
        PGID:       det.PGID,
        WatcherPID: watcherPID,   // was 0
        // ...
        LogDir:     filepath.Join(".runtime", "sensors", dep.ID, runID),
    })
    return registry.Save(r, *rs)
}
```

The inline comment `"No watcher process is spawned for orchestrator-managed deps — the dep runs unobserved"` is deleted along with the behaviour it documented.

## 13. Error handling

| Situation | Behaviour |
|---|---|
| `exec.LookPath` returns non-`ErrNotFound` error (rare) | Treat as missing — register `Failure` with `Rationale = Required tool "X" is not on PATH` |
| `os.Stat` returns non-`ErrNotExist` (e.g. parent permission denied) | `Failure` with custom rationale `Required context path "X": cannot stat: <err>`; HealShape stays `missing-context` |
| `watcher.Spawn` fails in `startBlockingDep` | Kill subprocess process group; return error so `AttachLiveDep` emits `dep_start_failed` cascade (existing path at `preflight.go:90`) |
| `prepareRuntimeDir` fails | RunOne emits `verdict=error` Signal `metadata.kind = "runtime_dir_failed"`, evidence carries `error_excerpt` |
| `signalsLogFile.Write` fails mid-stream | `subprocess.StreamSubprocess` logs to stderr, continues. Aggregate Signal carries `metadata.lifecycle.runtime_persistence = "degraded"` |
| Tee write to `raw.log` fails | Same — log to stderr, continue. Same `runtime_persistence = "degraded"` flag |

## 14. Definition of Done

Each binary check below must pass before this spec is considered shipped.

1. `go test ./lib/sensor/...` covers `CheckRequiresGate` with: tool missing, tool present, context path missing, context path exists, env missing, multiple failures (Gate.Failures order = tool → context → env), `kind=permission` ignored, `requires[]` absent, `requires[]` empty.
2. `go test ./lib/heal/...` validates `ShapeMissingContext.IsKnown() == true` and the new `missing_context` rule matches the formatted rationale.
3. `go test ./lib/watcher/...` covers `Spawn` happy path + error cleanup.
4. `go test ./lib/subprocess/...` covers `StreamConfig.RawLogPath` empty (compat) and set (file written, content matches stdout).
5. `go test -tags=start_sensor ./skills/start-sensor/...` passes unchanged (watcher extraction is behaviour-preserving).
6. `go test ./lib/orchestrator/...` covers:
   - RunOne with gate failure: signal emitted to stdout AND appended to `.runtime/sensors/<id>/<run_id>/signals.log`.
   - RunOne happy path: `raw.log` matches captured subprocess output; `signals.log` matches stdout JSONL byte-for-byte.
   - startBlockingDep: `WatcherPID` != 0; signals.log non-empty after dep emits a line matching its patterns.
   - Cascade signal at the `RunDeps` level lands in signals.log of the cascade target.
7. Two fixture sensors land under `sensors/fixtures/` (`requires-tool-missing.json`, `requires-context-missing.json`) with `golden_cases[0].expected_verdict = "error"`.
8. `go run -tags=run_computational ./skills/run-sensor/scripts <e2e-tests-fixture>` produces `.runtime/sensors/run-project/<run_id>/{raw.log, signals.log}` populated (smoke test, scripted).
9. `go vet -tags=run_computational ./...` and `go vet -tags=run_inferential ./...` pass.

## 15. Implementation order

Each step is a self-contained commit on the `install-sensor-requirements` worktree.

1. **`lib/heal` additions**: `ShapeMissingContext` + `IsKnown` + `rules/missing_context.go` + tests. Independent; mergeable in isolation.
2. **`lib/sensor/requires.go`**: API + checkTool + checkContext + checkEnv + `BuildRequiresGateSignal` + tests. `BuildMissingEnvSignal` becomes wrapper. No call-site changes yet.
3. **`lib/orchestrator/lifecycle.go::RunOne` Phase 0 swap**: `CheckRequiredEnv` → `CheckRequiresGate`. Orchestrator tests cover tool/context/env failure paths. Stdout protocol unchanged.
4. **`lib/watcher/spawn.go`**: extract from `start.go`. `start.go` refactored to call `watcher.Spawn`. Start-sensor tests pass unchanged.
5. **`lib/subprocess/stream.go` tee hook**: `RawLogPath` field; tests for empty + populated paths.
6. **`lib/orchestrator/lifecycle.go::RunOne` runtime-dir + tee wiring**: `prepareRuntimeDir`, MultiWriter for signals, `RawLogPath` for raw. Tests assert `signals.log == stdout`.
7. **`lib/orchestrator/live_deps.go::startBlockingDep`**: call `watcher.Spawn`; record `WatcherPID`. Tests.
8. **`emitSignalWithPersistence` helper + apply in `preflight.go` + `live_deps.go`**: every cascade / dep_attached / dep_detached signal lands in signals.log.
9. **Fixtures + smoke test**: two fixture sensors, smoke script in `Makefile` or `scripts/`.

## 16. References

- Boeckeler, B. et al. *Harness Engineering*. martinfowler.com.
- Lopopolo, R. *Harness engineering: leveraging Codex in an agent-first world*. openai.com, 2026-02-11.
- Sibling specs: `2026-05-11-requires-discriminated-union-design.md`, `2026-05-09-sensor-self-heal-design.md`, `2026-05-09-blocking-sensors-design.md`, `2026-05-10-start-sensor-orchestrated-deps-design.md`.
