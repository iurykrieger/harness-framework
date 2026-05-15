# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Claude Code plugin that implements a **sensor harness** for AI coding agents. Vocabulary follows Boeckeler/Fowler ("Harness Engineering", martinfowler.com) and Lopopolo ("Harness engineering: leveraging Codex in an agent-first world", openai.com, 2026-02-11):

- **Sensor** — a feedback control. Observes the system *after* the agent acts and emits Signals optimized for self-correction.
- **Signal** — the standardized JSON output of a sensor invocation. Cross-sensor uniformity is the point: routing, retry, and chaining must work without knowing the sensor type.
- Sensors are classified along two independent dimensions:
  - **type** — what the runner spawns:
    - **computational** — deterministic, CPU-bound, fast, cheap. Linters, type checkers, structural tests, fixed-threshold log/metric queries.
    - **inferential** — probabilistic, LLM-based, slow, expensive. LLM-as-judge, semantic checks, AI code review.
  - **output** — how many Signals per invocation:
    - **single** — one Signal. The runner reports the final verdict only; the subprocess's lines on stdout are ignored. Suitable when the only useful observation is the exit status (e.g. `make build`, `go vet ./...`).
    - **stream** — many individual Signals during the run plus one aggregate Signal at the end (JSONL on stdout, aggregate is the LAST line). Suitable when each observation is independently actionable: test cases, lint findings, log lines. Requires `execution.output_parsing.patterns` to declare how lines map to verdicts.

## Project rules

Durable conventions. Apply them to every change.

1. **Language: en-US.** All source files, comments, identifiers, docs, and commit messages.
2. **Schemas are versioned with the plugin.** They live in `schemas/` and are the source of truth for entities (`sensor.yaml`, `signal.yaml`, `stack.yaml`). Skills and scripts MUST resolve schema paths relative to the plugin root (`schemas/<name>.yaml`); never copy a schema elsewhere. Bumping a schema is a plugin-version event.
3. **Scripts are written in Go.** No Python, Bash, or Node for non-trivial logic. One-line shell glue is fine; anything with branches, parsing, or I/O orchestration goes in Go.
4. **Scripts are skill-local; libraries can be shared.** Each script lives under `skills/<skill-name>/scripts/` and stays self-contained — never share *scripts* across skills via a top-level `scripts/`. Duplicate scripts before coupling. A top-level `lib/` package is permitted for stable, schema-tied primitives that several skills genuinely need (schema validation, envelope construction, subprocess streaming, exit-code mapping, template rendering); skill-specific logic does not belong there.
5. **Prefer explicit files over folders for scripts.** A script is `scripts/<name>.go` plus `scripts/<name>_test.go`, not `scripts/<name>/main.go`. The file path is the script's identity. Subdirectories under `scripts/` are reserved for shared helpers within the skill, not for individual commands.
6. **Deterministic logic belongs in Go, never in skill markdown.** Anything that produces the same output for the same input — path resolution, schema validation, envelope construction, exit-code mapping, slot substitution, calibration, audit persistence, file I/O, timestamps, IDs — MUST live in a Go script under `skills/<skill>/scripts/`. SKILL.md prose is reserved for (a) orchestration (deciding which subcommand to call, in what order, with what arguments) and (b) genuinely non-deterministic reasoning (the LLM's own judgment in inferential sensors, the choice of remediation phrasing for opaque tool output). If you catch yourself writing imperative steps with branching logic in a skill body, that's a sign the logic should be extracted to Go.
7. **One script, one clear job.** Each script does a single thing. No subcommands, no `--mode` flags, no positional dispatch arguments. If the same Go file would be invoked as `tool foo` and `tool bar` for different operations, split it into `foo.go` and `bar.go`. Required positional inputs and one-shot toggles (e.g. `--schemas-dir`) are fine; mode/operation switches are not. Shared logic lives in a sibling `lib/` subpackage; build tags on each main file (e.g. `//go:build foo` on `foo.go`) let several `package main` scripts coexist in the same directory.
8. **Every Go script ships with Go tests** in the same `scripts/` directory. Tests cover behavior, functional requirements, declared use cases, and acceptance criteria. Use the standard `testing` package; table-driven tests are the default.
9. **`lib/` organizes by context, not by type.** Subdirectories under `lib/` are contexts — entities (`sensor/`, `signal/`) or runtime concerns (`schema/`, `subprocess/`, `template/`, `cli/`). Each file inside a context is named for the action or aspect it implements (`validate.go`, `load.go`, `aggregate.go`, `render.go`), not for the function or struct it contains. Two extremes to avoid: a single sprawling file per package, and one public function per file. Aim for 2–6 cohesive files per context, each with a sibling `_test.go`. Promote a context to its own subdirectory only when it has enough cohesive code to justify the boundary; until then, fold related actions together. Cross-package test fixtures follow the taxonomy in rule 11.
10. **No temporal or version-history content inside `skills/<name>/SKILL.md` (or any other Skill instruction).** Skill bodies are durable agent instructions; they describe the current behavior of the plugin in present tense and nothing else. Never reference issue numbers, closed/open PRs, prior plugin versions, "legacy" paths, migration notes for upgraders, or "after PR #X" annotations inside a Skill — that prose decays the moment the surrounding code moves and silently misleads future agents. The same restraint applies to Go script package comments and runtime evidence text read by other skills. If a fact only makes sense in light of a particular point in the project's timeline, it belongs in a commit message, a CHANGELOG entry, a PR description, or a generic project-rule line in this CLAUDE.md — not inside a Skill.
11. **Test data and test helpers are split by purpose.** Three locations, three purposes:
    - `<pkg>/testdata/` — Go-convention static fixtures (JSON, txt, jsonl, nested go.mod sub-modules). Per-package; consumed by `_test.go` of the same package or by another package's `<pkg>test` helper via relative path. Ignored by `go build`.
    - `lib/<pkg>/<pkg>test/` — Go test helpers (functions taking `*testing.T`) that load/decorate testdata for cross-package use. Each `<pkg>test` package is owned by exactly one `<pkg>` and depends only on `<pkg>` and the standard library / testing. Convention follows `net/http/httptest` and `testing/iotest`. The package is importable from production code in principle; do not do so.
    - `.harness/sensors/fixtures/` — sensor-domain fixture data referenced by `verification.golden_cases[].fixture` in sensor YAML. NOT a Go test fixture. Lives in the user project tree (under `.harness/`) and is consumed at sensor runtime.

    A single shared "fixtures" or "testhelpers" package across the whole `lib/` tree is explicitly disallowed.
12. **Sensor spawn is gated, no exceptions.** Every call to `subprocess.StreamSubprocess`, `subprocess.Start`, or `subprocess.SpawnDetached` that executes a sensor's `execution.command` MUST be preceded by `orchestrator.PreflightGate` in the same file. On gate failure, the caller emits the canonical signal returned (`metadata.kind="failed", cause="preflight_failed"`) and aborts the spawn. Legitimate exceptions — because they do not execute the sensor's own command — are allowlisted in `lib/orchestrator/gate_invariant_test.go`: `lib/watcher/` (spawns the watcher binary), `lib/subprocess/step.go` (prepare/teardown step commands), and the files of `lib/subprocess/` itself. The single helper is `orchestrator.PreflightGate(s Sensor, env sensor.Envelope, outputMode string) → (sig, failed)`. Do not call `sensor.CheckRequiresGate` or `sensor.BuildRequiresGateSignal` directly — they are implementation details encapsulated by the helper.

## Architecture

### The four schemas

All four are JSON Schema **Draft 2020-12**, authored as YAML and converted to JSON bytes at validator construction time via `sigs.k8s.io/yaml`. Validators must support that draft and resolve `$ref` across files.

- `schemas/signal.yaml` — the output contract. Defines the canonical `Verdict` and `Severity` enums under `$defs/`. **Edit enum values here only.**
- `schemas/sensor.yaml` — the definition contract. References `signal.yaml` two ways:
  - `#/$defs/Signal` is `{ "$ref": "signal.yaml" }`, so tooling can dereference a sensor's runtime output contract by chained `$ref`.
  - Enum sites inside `sensor.yaml` (`execution.exit_code_map[].{verdict,severity}`, `verification.golden_cases[].{expected_verdict,expected_severity}`) use `{ "$ref": "signal.yaml#/$defs/Verdict" }` and `…/Severity`. Adding a new verdict or severity value means editing `signal.yaml` only — `sensor.yaml` picks it up automatically.
- `schemas/stack.yaml` — the project-stack contract. Produced by `/detect-sensors` Phase A; consumed by Phase B when authoring `kind=observation` + `output=stream` sensors. Independent of `signal.yaml` and `sensor.yaml` (no cross-`$ref`).
- `schemas/usecase.yaml` — the use-case contract. Describes one observable journey variation of the project (trigger as narrative + fixture, behavior, expected_outcome with invariants and side_effects, file:line evidence pointing at the implementation). Produced by `/detect-usecases`; consumed by a future `/create-sensor` skill to synthesize deterministic regression sensors. References `stack.yaml` indirectly via `journey_id` (validated in Go, not JSON Schema).

**Comments in YAML artifacts are not preserved on round-trip.** `sigs.k8s.io/yaml` discards comments when marshalling, so any `# comment` lines added to a sensor or use case will be lost the next time the framework rewrites the file (`/heal-sensor`, re-running `/detect-sensors`). Durable explanations belong in commit messages, the design doc, or this CLAUDE.md — never in the artifact body.

### Discriminators

`sensor.yaml`'s top-level `allOf` enforces both classification dimensions with `if/then/else` blocks.

`sensor.type ∈ {computational, inferential}`:

- **computational** → `cost.compute` and `execution.{command, exit_code_map}` required; `cost.tokens` and inferential `execution` fields forbidden.
- **inferential** → `cost.tokens`, `execution.{command, model, system_prompt, user_prompt_template, decoding}`, and the top-level `calibration` block required; `cost.compute` forbidden.

`sensor.output ∈ {single, stream}`:

- **single** → `execution.output_parsing` forbidden. The runner ignores subprocess stdout/stderr lines and emits exactly one Signal whose verdict comes from `exit_code_map` (computational) or the calibration-aware default (inferential).
- **stream** → `execution.output_parsing.patterns` required (≥1 pattern). The runner streams one Signal per matched line plus a final aggregate Signal (JSONL on stdout, aggregate is the LAST line). Aggregate verdict is the worst of `exit_code_map[exitCode]` and the highest-rank verdict observed in the stream.

When adding fields, update the `allOf` so each branch's mutual exclusion stays watertight. The `if/then` blocks use `not: { required: [...] }` to forbid the wrong-branch fields — same idiom for new fields.

### Skills

Each `skills/<name>/SKILL.md` has YAML frontmatter (`name`, `description`) read by Claude Code's skill loader. The body is procedural prose addressed to whichever agent invokes the skill.

`skills/run-sensor/` is the canonical sensor runner. Both runners (computational and inferential) follow the same model: spawn `sh -c <execution.command>`, scan stdout+stderr line-by-line against `execution.output_parsing.patterns` (when declared), emit one Signal per match as JSONL on stdout, then end with one aggregate Signal as the LAST JSONL line. The aggregate's verdict is the worse of `exit_code_map[exitCode]` and the highest-rank verdict observed in the stream. The inferential runner additionally exposes the rendered `user_prompt_template` to the subprocess via the `HARNESS_PROMPT` env var and applies the calibration `fail → warn` downgrade when the subprocess emits a `HARNESS_AGGREGATE_CONFIDENCE=<float>` line on its stdout below `calibration.confidence_threshold`. Both runners are thin CLI wrappers; the deterministic pipeline (path resolution, schema validation, envelope construction, pattern matching, subprocess streaming, aggregation, signal validation) lives in the top-level `lib/` package.

`skills/detect-usecases/` scans the project, augments `stack.yaml` with `purpose`/`archetypes`/`journeys` when missing, then drafts one descriptive UseCase per observable journey variation and persists each via `skills/detect-usecases/scripts/write-usecase.go` to `<project>/.harness/usecases/<id>.yaml`.

### Dependencies and lifecycle

A sensor's top-level `kind` is one of `observation`, `assertion`, `setup`. The first two are regulatory; the third is auxiliary (idempotent, makes a precondition true: `start-postgres`, `setup-env-from-example`, `install-deps-pnpm`).

A sensor's `depends_on: [<id>...]` declares ids that must run and pass before it. The runner resolves the transitive closure topologically (deps first, requested sensor last), runs each sensor's full lifecycle (prepare → command → teardown), and propagates failures: dependents of a failed sensor never run and emit "cascade" Signals (`metadata.kind = "cascade"`) instead.

Lifecycle phases live under `execution`:

- `prepare[]` — silent, fail-fast (first non-pass step aborts and skips command, but teardown still runs). Use for sensor-specific setup that isn't worth a reusable setup sensor.
- `command` — the observed step (existing streaming pipeline; emits individual JSONL Signals for matched output lines).
- `teardown[]` — silent, best-effort, finally semantics. Runs regardless of prepare/command outcome. Per-step failures contribute warn evidence but do NOT downgrade the aggregate verdict.

Per-step lifecycle results fold into the aggregate Signal under `metadata.lifecycle.{prepare,teardown}` (free-form per signal.yaml). The aggregate Signal of the requested sensor remains the LAST JSONL line on stdout — deps' aggregates appear earlier in the stream.

The orchestrator lives in `lib/orchestrator/` (DAG resolution + lifecycle execution + cascade construction) and is reused by both `run-computational` and `run-inferential` runner scripts.

Blocking deps in the orchestrator path run **with a watcher** — the same watcher binary `/start-sensor` already spawns for top-level blocking sensors. `startBlockingDep` lays out the per-run directory (`.harness/runtime/<id>/<run-id>/{raw.log,signals.log,watcher.log}`), spawns the subprocess detached, then spawns the watcher via `lib/watcher.Spawn`. The registry entry records both the subprocess PID and the watcher PID. `AttachLiveDep` then runs a synchronous health gate via `lib/watcher.WaitForReady(signalsLogPath, subprocessPID, timeout)` against the live `signals.log`: a `verdict ∈ {pass, warn}` individual signal returns `ready` and emits `dep_started`; a `verdict ∈ {fail, error}` individual returns `failed` and emits `dep_start_failed` (verdict=fail, recorded into `res.Signals` so `FirstFailedDep` cascades the dependent); subprocess death before any signal returns `died_silently` and behaves like `failed` with a tail of `raw.log` as evidence; a timeout with the subprocess still alive emits `dep_started` carrying `metadata.health_gate="timed_out_proceeding"`. `stopBlockingDep` reads the dep's `signals.log` at detach, computes the aggregate verdict via `lib/signal.MaxStreamVerdict` crossed with a liveness check on the subprocess; a subprocess found dead before SIGTERM forces `verdict=fail` and stamps `metadata.subprocess_state="died_before_stop"`. Liveness uses `lib/watcher.IsSubprocessAlive`, which combines `kill(pid, 0)` with `Wait4(WNOHANG)` to distinguish a genuinely running subprocess from a zombie waiting to be reaped.

The `requires[kind ∈ {tool,context,env}]` gate is evaluated by `orchestrator.PreflightGate` before **any** spawn of a sensor's command — `RunOne`/`RunOneWithRoot` Phase 0, `/start-sensor` before the detach, `AttachLiveDep` in the spawn-fresh branch under the lock (not on re-attach), and `run-inferential.go` before the LLM spawn. Gate failure emits a canonical signal (`metadata.kind="failed", cause="preflight_failed"`) attributed to the sensor whose gate failed; dependents cascade via `FirstFailedDep` + `BuildCascadeSignal` as with any other failure.

### Registry root discovery

The blocking-sensor registry (`<projectRoot>/.harness/runtime/running_sensors.json`) lives in the user's project tree, NOT in the plugin tree. To make this resolution deterministic and cwd-independent, the four registry-touching skills (`/start-sensor`, `/list-sensors`, `/stop-sensor`, `/tail-sensor`) call `lib/registry/Lookup(cwd)` which resolves the project root in this order:

1. **`HARNESS_REGISTRY_ROOT` env var.** Must be an absolute path to an existing directory. The env var names the **project root** — i.e., the directory that contains `.harness/`, not `.harness/` itself. Symlinks are resolved via `EvalSymlinks`.
2. **Walk-up from `cwd` looking for `.harness/`.** The first ancestor whose `.harness/` child is itself a directory is the project root. Empty `.harness/` is acceptable.
3. **Failure.** No fallback to `cwd`. The skill emits an error Signal `metadata.kind=registry_discovery_failed` whose evidence names both strategies tried.

Every signal emitted by the four skills carries `metadata.{registry_path, registry_source, registry_exists}` for diagnose. `registry_source` is `"env"` or `"walk_up"`; `registry_exists` is `true` only when `running_sensors.json` is on disk.

Verdict semantics by skill when the registry file is absent (`registry_exists: false`):

| Skill | Verdict on missing file | Why |
| --- | --- | --- |
| `/start-sensor` | `pass` (canonical first-start) | Creating a registry is the point of `/start-sensor`. |
| `/list-sensors` | `warn` | Likely "wrong cwd" or no live sensors yet. |
| `/stop-sensor` | `error` | A sensor cannot be running if there is no registry file. |
| `/tail-sensor` | `error` | Same reasoning as `/stop-sensor`. |

The watcher subprocess inherits the resolved root via `HARNESS_WATCHER_REGISTRY_ROOT` (set by `/start-sensor` from `Result.ProjectRoot`); that env var is a precise absolute path, not a discovery hint.

### Auto issue opening

A `PostToolUse(Bash)` hook (`hooks/error-issue-autofiler.go`, build tag `error_autofiler`) observes every Bash invocation and opens a GitHub issue when a framework Go script panics, fails to compile, or emits a Signal with `verdict=error` plus an internal `metadata.kind`. Per-fingerprint dedup uses a 3-layer cascade: local `<projectRoot>/.harness/runtime/auto-issues.json` cache, then `gh issue list --search "harness-fp:<fingerprint>"`, then `gh issue create`. The hook always exits 0 — internal failures (no `gh` auth, no GitHub remote, unparseable cache, …) degrade silently to stderr.

Disable per-shell with `HARNESS_AUTOFILE_ISSUES=0`. The repo it files against is derived from `git remote get-url origin` of the project root resolved by `lib/registry.Lookup(cwd)`; the framework expects a label `auto-filed` to exist on that repo (create once).

## Build, validate, test

Single Go module at the repo root: `module github.com/iurykrieger/harness-framework` (Go 1.25). Per-skill modules only if a skill needs an isolated dependency graph.

The plugin **does not ship pre-built binaries**. Every script — runners, registry skills, hooks, and the watcher — is invoked via `go run`. The canonical invocation contract is:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> \
  ./skills/<name>/scripts <args>
```

Three pieces, each load-bearing:

- `-C "${CLAUDE_PLUGIN_ROOT}"` chdirs the `go` process itself to the plugin checkout before any module resolution. The user's `go.mod`/`go.work` cannot interfere.
- `HARNESS_REGISTRY_ROOT="$(pwd)"` captures the agent's cwd (the user's project root) before `-C` moves `go`. Every registry-touching skill consults this via `lib/registry.Lookup`; the runner threads it into subprocess `Dir` so sensor commands keep running from the project root.
- `GOWORK=off` neutralizes any `go.work` in the user's tree.

`${CLAUDE_PLUGIN_ROOT}` is exposed by Claude Code to plugin-originated commands. Scripts emit `verdict=error metadata.cause=plugin_root_missing` if it is empty.

### Local verification

```bash
go test ./lib/...                                     # the shared library
go test -tags=run_computational ./skills/...          # the computational runner
go test -tags=run_inferential   ./skills/...          # the inferential runner
go test -tags=start_sensor      ./skills/...          # the start-sensor runner
go test -tags=stop_sensor       ./skills/...
go test -tags=list_sensors      ./skills/...
go test -tags=tail_sensor       ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go test -tags=write_usecase   ./skills/...          # the write-usecase script
go vet -tags=run_computational  ./...
go vet -tags=run_inferential    ./...

# Run a sensor end-to-end (from the user's project, with CLAUDE_PLUGIN_ROOT set):
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts <sensor-id>
```

### Watcher latency

`/start-sensor` spawns the watcher via `go run`, which means the watcher is compiled on every invocation. With a warm Go build cache, the compile-link step costs ~150–500ms; with a cold cache (fresh plugin install or after `go clean -cache`), it costs ~600ms–1s. The cost is paid once per `/start-sensor`; subsequent reads via `/tail-sensor` and `/list-sensors` are unaffected.

### Requirements

- Go 1.20+ (the `-C` flag arrived in 1.20; `go.mod` pins `go 1.25`).
- `CLAUDE_PLUGIN_ROOT` exposed by Claude Code.
- `go` on PATH (Claude Code's `go` toolchain is the default).

## References

- Boeckeler, B. et al. *Harness Engineering*. martinfowler.com.
- Lopopolo, R. *Harness engineering: leveraging Codex in an agent-first world*. openai.com, 2026-02-11.
