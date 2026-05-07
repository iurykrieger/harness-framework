# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A Claude Code plugin that implements a **sensor harness** for AI coding agents. Vocabulary follows Boeckeler/Fowler ("Harness Engineering", martinfowler.com) and Lopopolo ("Harness engineering: leveraging Codex in an agent-first world", openai.com, 2026-02-11):

- **Sensor** — a feedback control. Observes the system *after* the agent acts and emits a Signal optimized for self-correction.
- **Signal** — the standardized JSON output of a sensor invocation. Cross-sensor uniformity is the point: routing, retry, and chaining must work without knowing the sensor type.
- Sensors come in two flavors:
  - **computational** — deterministic, CPU-bound, fast, cheap (linters, type checkers, structural tests, fixed-threshold log/metric queries).
  - **inferential** — probabilistic, LLM-based, slow, expensive (LLM-as-judge, semantic checks, AI code review).

## Project rules

Durable conventions. Apply them to every change.

1. **Language: en-US.** All source files, comments, identifiers, docs, and commit messages.
2. **Schemas are versioned with the plugin.** They live in `schemas/` and are the source of truth for entities. Skills and scripts MUST resolve schema paths relative to the plugin root (`schemas/<name>.json`); never copy a schema elsewhere. Bumping a schema is a plugin-version event.
3. **Scripts are written in Go.** No Python, Bash, or Node for non-trivial logic. One-line shell glue is fine; anything with branches, parsing, or I/O orchestration goes in Go.
4. **Scripts are skill-local; libraries can be shared.** Each script lives under `skills/<skill-name>/scripts/` and stays self-contained — never share *scripts* across skills via a top-level `scripts/`. Duplicate scripts before coupling. A top-level `lib/` package is permitted for stable, schema-tied primitives that several skills genuinely need (schema validation, envelope construction, subprocess streaming, exit-code mapping, template rendering); skill-specific logic does not belong there.
5. **Prefer explicit files over folders for scripts.** A script is `scripts/<name>.go` plus `scripts/<name>_test.go`, not `scripts/<name>/main.go`. The file path is the script's identity. Subdirectories under `scripts/` are reserved for shared helpers within the skill, not for individual commands.
6. **Deterministic logic belongs in Go, never in skill markdown.** Anything that produces the same output for the same input — path resolution, schema validation, envelope construction, exit-code mapping, slot substitution, calibration, audit persistence, file I/O, timestamps, IDs — MUST live in a Go script under `skills/<skill>/scripts/`. SKILL.md prose is reserved for (a) orchestration (deciding which subcommand to call, in what order, with what arguments) and (b) genuinely non-deterministic reasoning (the LLM's own judgment in inferential sensors, the choice of remediation phrasing for opaque tool output). If you catch yourself writing imperative steps with branching logic in a skill body, that's a sign the logic should be extracted to Go.
7. **One script, one clear job.** Each script does a single thing. No subcommands, no `--mode` flags, no positional dispatch arguments. If the same Go file would be invoked as `tool foo` and `tool bar` for different operations, split it into `foo.go` and `bar.go`. Required positional inputs and one-shot toggles (e.g. `--schemas-dir`) are fine; mode/operation switches are not. Shared logic lives in a sibling `lib/` subpackage; build tags on each main file (e.g. `//go:build foo` on `foo.go`) let several `package main` scripts coexist in the same directory.
8. **Every Go script ships with Go tests** in the same `scripts/` directory. Tests cover behavior, functional requirements, declared use cases, and acceptance criteria. Use the standard `testing` package; table-driven tests are the default.

## Architecture

### The two schemas

Both are JSON Schema **Draft 2020-12**. Validators must support that draft and resolve `$ref` across files.

- `schemas/signal.json` — the output contract. Defines the canonical `Verdict` and `Severity` enums under `$defs/`. **Edit enum values here only.**
- `schemas/sensor.json` — the definition contract. References `signal.json` two ways:
  - `#/$defs/Signal` is `{ "$ref": "signal.json" }`, so tooling can dereference a sensor's runtime output contract by chained `$ref`.
  - Enum sites inside `sensor.json` (`execution.exit_code_map[].{verdict,severity}`, `verification.golden_cases[].{expected_verdict,expected_severity}`) use `{ "$ref": "signal.json#/$defs/Verdict" }` and `…/Severity`. Adding a new verdict or severity value means editing `signal.json` only — `sensor.json` picks it up automatically.

### Type discrimination

`sensor.type ∈ {computational, inferential}` is enforced by `sensor.json`'s top-level `allOf` with `if/then/else`:

- **computational** → `cost.compute` and `execution.{command, exit_code_map}` required; `cost.tokens` and inferential `execution` fields forbidden.
- **inferential** → `cost.tokens`, `execution.{model, system_prompt, user_prompt_template, decoding}`, and the top-level `calibration` block required; `cost.compute` and computational `execution` fields forbidden.

When adding fields to either branch, update the `allOf` so the mutual exclusion stays watertight. The `if/then` blocks use `not: { required: [...] }` to forbid the wrong-branch fields — same idiom for new fields.

### Skills

Each `skills/<name>/SKILL.md` has YAML frontmatter (`name`, `description`) read by Claude Code's skill loader. The body is procedural prose addressed to whichever agent invokes the skill.

`skills/run-sensor/` is the canonical sensor runner. Both runners (computational and inferential) follow the same model: spawn `sh -c <execution.command>`, scan stdout+stderr line-by-line against `execution.output_parsing.patterns` (when declared), emit one Signal per match as JSONL on stdout, then end with one aggregate Signal as the LAST JSONL line. The aggregate's verdict is the worse of `exit_code_map[exitCode]` and the highest-rank verdict observed in the stream. The inferential runner additionally exposes the rendered `user_prompt_template` to the subprocess via the `HARNESS_PROMPT` env var and applies the calibration `fail → warn` downgrade when the subprocess emits a `HARNESS_AGGREGATE_CONFIDENCE=<float>` line on its stdout below `calibration.confidence_threshold`. Both runners are thin CLI wrappers; the deterministic pipeline (path resolution, schema validation, envelope construction, pattern matching, subprocess streaming, aggregation, signal validation) lives in the top-level `lib/` package.

## Build, validate, test

Single Go module at the repo root: `module github.com/iurykrieger/harness-framework` (Go 1.25). Per-skill modules only if a skill needs an isolated dependency graph.

```bash
go test ./lib/...                                     # the shared library
go test -tags=run_computational ./skills/...          # the computational runner
go test -tags=run_inferential   ./skills/...          # the inferential runner
go vet -tags=run_computational  ./...
go vet -tags=run_inferential    ./...

# Run a sensor end-to-end:
go run -tags=run_computational ./skills/run-sensor/scripts <sensor>.json
go run -tags=run_inferential ./skills/run-sensor/scripts [--slot k=v]... <sensor>.json
```

The `run-sensor` skill ships two scripts under `skills/run-sensor/scripts/`, each gated by a build tag so they coexist:

- **`run-computational.go`** (`//go:build run_computational`) — thin CLI wrapper over `lib.StreamSubprocess`. Resolves the sensor path, validates against `schemas/sensor.json`, compiles `execution.output_parsing.patterns` (when present), spawns `sh -c <command>`, streams JSONL individual Signals, computes the aggregate via `MapExitCode` × `MaxStreamVerdict` × `Aggregate`, validates and prints.
- **`run-inferential.go`** (`//go:build run_inferential`) — thin CLI wrapper that uses the same streaming pipeline. The sensor's `command` is an LLM CLI; the runner exposes the rendered `user_prompt_template` as `HARNESS_PROMPT` and post-processes a `HARNESS_AGGREGATE_CONFIDENCE=<float>` line (if present on the subprocess stdout) to drive the calibration `fail → warn` downgrade. No HTTP and no Anthropic-specific knowledge — any LLM CLI that respects the env-var prompt protocol works.

Shared logic lives in the top-level `lib/` package (`schema.go`, `envelope.go`, `path.go`, `exitcode.go`, `template.go`, `patterns.go`, `aggregate.go`, `stream.go`), importable as `github.com/iurykrieger/harness-framework/lib`. Both runner scripts auto-discover `schemas/` by walking up from `cwd`; pass `--schemas-dir=<path>` to override. Exit codes: `0` Signal printed, `1` schema/pattern/slot failure, `2` usage or I/O error. Dependency: `github.com/santhosh-tekuri/jsonschema/v5` (Draft 2020-12 with cross-file `$ref`).

## References

- Boeckeler, B. et al. *Harness Engineering*. martinfowler.com.
- Lopopolo, R. *Harness engineering: leveraging Codex in an agent-first world*. openai.com, 2026-02-11.
