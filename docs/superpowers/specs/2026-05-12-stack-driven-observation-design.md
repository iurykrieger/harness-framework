# Stack-driven observation sensors design

Status: proposed
Date: 2026-05-12
Issue: https://github.com/iurykrieger/harness-framework/issues/27
Related: `schemas/sensor.json`, `schemas/signal.json`, `schemas/stack.json` (new), `skills/detect-sensors/`, `lib/registry/`, `lib/sensor/`, `hooks/error-issue-autofiler.go`

## Why

Today, observation sensors (`kind=observation`, typically `output=stream`) surface signals from a running service exclusively through hand-written regexes in `execution.output_parsing.patterns[]`. That pipeline works for crash markers and a handful of structured log lines, but misses the dense observability surface a real running service emits — HTTP request/response middleware lines, structured logs at every severity, panic traces, trace/span identifiers.

The triggering case (issue #27) was bootstrapping a harness for `stone-payments/charge-api` (Go service, Zap logger, chi middleware). The blocking sensor `run-project-charge-api` (v0.2.0) booted the service cleanly, but on a batch of seven `POST /v1/charges` calls — 4× validation 400, 2× upstream 401, 1× degraded-engine — the `signals.log` captured **exactly one signal**: the boot marker `INFO Starting HTTP server on port :3000`. The middleware emitted one `WARN "message":"response"` per request with the full payload, latency, status_code, trace_id — none of which surfaced because v0.2.0's patterns only matched `INFO Starting`, `FATAL`, `ERROR`. To capture per-request data, the sensor author would have to bump to v0.3.0 with a `\"severity\":\"WARN\".*\"message\":\"response\".*\"status_code\":(4\\d\\d|5\\d\\d)` regex.

That work is redone by every team observing a Go-with-Zap service, then redone again by teams on Java/Logback, Node/Pino, Python/structlog. The root failure is that the framework treats "what shape does this project's stdout have?" as **implicit knowledge in the head of whoever writes the sensor**. The encoder shape is a fact about the project — it should be a first-class, discoverable, versioned artifact, not a tribal regex.

The corollary failure: a hypothetical "canonical pattern catalog" maintained inside the plugin doesn't scale. The combinatorial space `(language × framework × logger × encoder × middleware × custom-config)` is unbounded; whoever maintains the catalog becomes the bottleneck the plugin is supposed to remove. The work belongs in the LLM-driven detection pass, not in plugin maintenance.

This spec moves the work to the right layer: `/detect-sensors` becomes responsible for **synthesizing a project-specific stack description** — a structured, schema-validated artifact that captures the project's languages, components, and the canonical shapes of stdout lines those components emit. Sensors authored against that project then **derive their `output_parsing.patterns[]` from the stack** instead of from memory.

## What changes

1. **New entity: Stack** with its own schema `schemas/stack.json` (third entity-schema alongside `sensor.json` and `signal.json`). A Stack describes a project's runtime-observable stack: `languages[]`, `components[]` (role + name + version + config_summary + evidence), and — the heart of the contract — `log_shapes[]` capturing the canonical shape of stdout lines (format, fields with semantic meaning, severity_values, sample).
2. **New top-level project directory: `.harness/`** consolidates all framework artifacts under a single namespace. `<project>/sensors/` → `<project>/.harness/sensors/`. `<project>/.runtime/sensors/` → `<project>/.harness/runtime/`. The detected stack lives at `<project>/.harness/stack.json`. **Clean cutover, no fallback.**
3. **`/detect-sensors` gains a two-phase flow.** Phase A: synthesize and persist the stack (reused across invocations; explicit `--refresh-stack` regenerates). Phase B: for each capability, draft sensors, deriving `output_parsing.patterns[]` from `log_shapes[]` for `kind=observation` + `output=stream` sensors.
4. **New Go package `lib/stack/`** owns Stack load, validate, persist, lookup, and shape-helper functions. Mirrors `lib/sensor/` and `lib/registry/` conventions.
5. **New script `skills/detect-sensors/scripts/write-stack.go`** is the deterministic write-path: validate against `schemas/stack.json`, cross-check referential integrity (`produced_by[] ⊂ components[].name`), persist atomically.
6. **`lib/registry/lookup.go`** changes its walk-up sentinel from `sensors/` to `.harness/`. All registry paths internally rebase from `<root>/.runtime/sensors/` to `<root>/.harness/runtime/`. `HARNESS_REGISTRY_ROOT` still names the project root; the runtime subdirectory remains an internal detail.
7. **`lib/sensor/path.go`** resolves `<id>` to `.harness/sensors/<id>.json` instead of `sensors/<id>.json`.
8. **`lib/orchestrator/`** removes its hardcoded path literals. `run.go` and `live_deps.go` doc comments update to reference `.harness/sensors/<id>.json`. `lifecycle.go:350` and `live_deps.go:222` stop building `filepath.Join(".runtime", "sensors", ...)` directly — they consume `registry.Root.SignalsLogRun()` (or a sibling accessor for the per-run directory) instead. `cascade.go:46` builds its error-evidence `file` field via `registry.Root.SensorFile(id)`. After this PR, no Go file under `lib/orchestrator/` contains the string literals `".runtime"`, `"sensors"` (as a path segment), or `"sensors/%s.json"`.
9. **`hooks/error-issue-autofiler.go`** stores its dedup cache at `.harness/runtime/auto-issues.json` instead of `.runtime/auto-issues.json`.
10. **`skills/detect-sensors/scripts/write-sensor.go`** gains the build tag `//go:build write_sensor`. Without this, the new `write-stack.go` (with `//go:build write_stack`) cannot coexist as a second `package main` in the same directory — Go would compile the untagged `write-sensor.go` on every build and fail with duplicate `main`. The skill's `go run` invocation in `SKILL.md` updates to `go run -tags=write_sensor ./skills/detect-sensors/scripts <draft.json>`.
11. **All five blocking-sensor SKILL.md descriptions are updated.** `start-sensor`, `stop-sensor`, `list-sensors`, `tail-sensor`, `run-sensor` — every reference in their frontmatter `description:` or body prose to `<projectRoot>/.runtime/sensors/...` or `<root>/sensors/<id>.json` migrates to the `.harness/` paths. Mechanical but enumerated to make the diff explicit.
12. **Framework's own `.gitignore`**: replace `.runtime/` with `.harness/runtime/`. Do NOT add `.harness/sensors/` or `.harness/stack.json` — those are committed artifacts.
13. **Dogfooded migration:** the framework's own repository moves `sensors/` → `.harness/sensors/` and `.runtime/` (if present) → `.harness/runtime/` in the same PR that lands the registry change.
14. **CHANGELOG documents the breaking change.** First thing an outdated project sees on upgrade is a clear "registry not found at .harness/runtime/" error pointing at the migration steps.

## Architecture

```
┌────────────────────────────────────────────────────────────────────┐
│  /detect-sensors  [project-path]  [--refresh-stack]                │
└───────────────────────────────┬────────────────────────────────────┘
                                ▼
                  ┌───────────────────────────┐
                  │  Phase A — Stack discovery │  (LLM judgment)
                  └─────────────┬─────────────┘
                                ▼
   .harness/stack.json exists & no --refresh-stack ──▶  reuse
                                ▼
   1. Inspect manifests (go.mod, package.json, requirements.txt, ...)
   2. Open initialization sites (cmd/server/main.go, src/main.ts, ...)
      and extract concrete logger / middleware config
   3. Assemble components[] (role, name, version, config_summary,
      evidence)
   4. Derive log_shapes[] (format, fields[]+meaning, severity_values,
      sample) from (a) library defaults the LLM knows, (b) config
      overrides observed, (c) sample lines if accessible
   5. Call write-stack.go ─────────▶ validates against stack.json
                                   ─▶ persists to <project>/.harness/stack.json
                                ▼
                  ┌───────────────────────────┐
                  │  Phase B — Sensor authoring│  (LLM judgment + Go validation)
                  └─────────────┬─────────────┘
                                ▼
   Per capability (lint, build, unit-test, run-project, ...):
     • Pick kind/type/output as today
     • If kind=observation AND output=stream:
        - Read .harness/stack.json#/log_shapes[]
        - Filter relevant shapes (run-* / watch-* / tail-*)
        - For each shape, derive 2–6 patterns mapping severity_values
          → verdict (ERROR/FATAL → fail/high; WARN+status_code∈4xx/5xx
          → fail/medium; WARN → warn/low; INFO+boot-marker → pass/info)
        - Anchor patterns to the shape's sample (regex MUST match
          sample) and cite the shape.id in the sensor's description
     • Other sensors (lint, build, test): current behavior
     • Call write-sensor.go to validate & persist

                                                         ┌──────────────────┐
   .harness/                                             │ stack.json       │
   ├── sensors/<id>.json   ← was <project>/sensors/      │   languages[]    │
   ├── runtime/                                          │   components[]   │
   │   ├── running_sensors.json   ← was .runtime/...     │   log_shapes[]   │
   │   ├── running_sensors.lock                          └──────────────────┘
   │   ├── auto-issues.json                                       ▲
   │   └── <sensor>/<run_id>/{raw.log, signals.log}               │ derives
   └── stack.json   (new)                                         │ patterns
```

### Phase split rationale

- **Reuse across invocations.** The first `/detect-sensors` call synthesizes the stack; subsequent calls (add a sensor, re-detect after restructure) reuse the persisted artifact. Idempotent default; `--refresh-stack` forces regeneration.
- **Audit.** A pattern that fails to match becomes diagnosable in two layers: was the *shape* wrong (re-check `stack.log_shapes[].sample`) or was the *regex* wrong (re-check the sensor)?
- **Heal-friendly (future).** `/heal-sensor` can detect stack staleness by comparing `stack.detected_at` against the mtimes of `components[].evidence[].file`. Out of scope here; the architecture leaves the door open.
- **Failure isolation.** If Phase A produces a minimal stack (logger unidentified), Phase B still emits lint/build/test sensors normally; only observation+stream sensors degrade gracefully with generic patterns + a `blind_spots[]` annotation pointing at the empty stack.

## Schema: `schemas/stack.json`

Authoritative file lives at the path. Quick reference:

- **Top-level:** `version` (SemVer), `detected_at` (date-time), `detected_by` (model id or `"manual"`), `languages[]`, `components[]`, `log_shapes[]`.
- **`Component`:** `role` (enum: `http-server | http-router | http-middleware | logger | log-encoder | tracer | metrics | queue-consumer | queue-producer | db-client | rpc | test-runner`), `name`, `version`, `config_summary`, `evidence[]`.
- **`LogShape`:** `id` (kebab-case slug, unique within stack), `produced_by[]` (Component names), `format` (enum: `json | logfmt | plain | stack-trace | combined-log-format`), `fields[]` (only meaningful for json/logfmt; each has `key`, `meaning` enum, optional `example_values[]`), `severity_values[]` (when any field has meaning=`severity`), `sample` (one real example line).
- **`FieldMeaning` enum:** `severity | message | timestamp | trace_id | span_id | status_code | latency_ms | method | path | user_id | request_id | service | version | other`.
- **Conventions match existing schemas:** Draft 2020-12, `additionalProperties: false` everywhere, `$defs` for reusable types, every field carries a `description`.

The schema does NOT cross-reference `sensor.json` or `signal.json` — Stack is consumed by humans/LLMs to derive sensors, never directly emitted as a runtime artifact. Decoupled.

## Library contracts: `lib/stack/`

Organized by context per project rule 9. Files:

| File | Responsibility |
|---|---|
| `lib/stack/load.go` + `load_test.go` | Read `<project>/.harness/stack.json`, deserialize into typed `Stack` struct, validate against `schemas/stack.json` via `lib/schema`. |
| `lib/stack/persist.go` + `persist_test.go` | Write `Stack` to disk: validate, marshal, write-temp-then-rename. Atomic. 0o644. |
| `lib/stack/lookup.go` + `lookup_test.go` | `Lookup(cwd) (Result, error)` — uses `lib/registry.Lookup` to resolve project root, then resolves `<root>/.harness/stack.json`. Mirrors the registry result pattern: `{Path, Exists, Stack}`. |
| `lib/stack/shape.go` + `shape_test.go` | Helpers consumed by tests, audits, and future heal logic: `Stack.ShapesByRole(role)`, `Stack.ShapesProducedBy(componentName)`, `LogShape.FieldsByMeaning(meaning)`, `LogShape.HasSeverity()`. |

Typed structs mirror the schema shape; JSON tags on every field; nullable fields use pointers so absence is distinguishable from zero.

## Script: `skills/detect-sensors/scripts/write-stack.go`

Build tag `//go:build write_stack`. Two `package main` files in the same directory require **both** to carry mutually-exclusive build tags. Today `write-sensor.go` has no build tag — this spec adds `//go:build write_sensor` to it as part of the same PR that introduces `write-stack.go`. After this change, the `skills/detect-sensors/scripts/` directory follows the same pattern as `skills/run-sensor/scripts/` (where `run-computational.go` and `run-inferential.go` each carry their own build tag).

CLI:
```
write-stack <project-root> < stack-payload.json
```

Procedure:
1. Read payload from stdin (or `--payload-file` for tests).
2. Validate against `schemas/stack.json` via `lib/schema.Validator`.
3. Cross-check referential integrity: every `log_shapes[].produced_by[]` entry MUST match some `components[].name` exactly. A reference to a non-existent component returns exit 1 with `metadata.kind=stack_produced_by_orphan`.
4. Persist via `lib/stack.Persist(<project-root>, stack)`.
5. Emit Signal on stdout: `verdict=pass`, `metadata.kind=stack_written` on success; `verdict=error` with detailed evidence on validation or cross-check failure.

Exit codes follow the existing scripts' convention: `0` Signal printed successfully, `1` schema/cross-check failure, `2` usage or I/O error.

Tests in `write-stack_test.go`:
- Golden payload → exit 0, stack.json on disk byte-identical.
- Missing required field → exit 1, Signal `kind=schema_validation_failed`.
- `produced_by` referencing missing component → exit 1, Signal `kind=stack_produced_by_orphan`.
- Idempotency: write same payload twice, second write byte-identical to first.

## Registry & path migration

Single conceptual change: the walk-up sentinel becomes `.harness/` instead of `sensors/`. Everything else cascades.

**`lib/registry/lookup.go`** (and its tests):
- The walk-up loop in `Lookup(cwd)` checks `<dir>/.harness/` for `IsDir()` (not `<dir>/sensors/`). Empty `.harness/` is acceptable as a marker, same as today's empty `sensors/`.
- `HARNESS_REGISTRY_ROOT` env var contract unchanged: it names the **project root** containing `.harness/`, not `.harness/` itself.
- Discovery error metadata (`metadata.kind=registry_discovery_failed`) updates its evidence wording to reference `.harness/` instead of `sensors/`. The two diagnose strategies (`env`, `walk_up`) keep their names.

**`lib/registry/Root` accessors**:
- `RegistryFile()` → `<root>/.harness/runtime/running_sensors.json`
- `LockFile()` → `<root>/.harness/runtime/running_sensors.lock`
- `SignalsLogRun(id, runID)` → `<root>/.harness/runtime/<id>/<runID>/signals.log`
- `LegacySignalsLog(id)` → `<root>/.harness/runtime/<id>/signals.log` (internal "legacy" still names the per-sensor flat path, just under the new namespace)
- New: `RunDir(id, runID)` → `<root>/.harness/runtime/<id>/<runID>/` — directory containing `raw.log`, `signals.log`, and any other per-run artifacts. Replaces the `filepath.Join(".runtime", "sensors", id, runID)` call sites in `lib/orchestrator/{lifecycle.go, live_deps.go}`.
- New: `SensorFile(id)` → `<root>/.harness/sensors/<id>.json` — replaces hard-coded `sensors/<id>.json` in `lib/sensor/path.go` and `lib/orchestrator/cascade.go`.

**`lib/sensor/path.go`**: `Resolve(id)` walks up via `lib/registry.Lookup` and returns `Root.SensorFile(id)`. The current logic that joins `sensors/<id>.json` directly is removed.

**`hooks/error-issue-autofiler.go`**: cache path moves to `<root>/.harness/runtime/auto-issues.json`. Same `lib/registry.Lookup(cwd)` resolution. No change to the env-disable knob (`HARNESS_AUTOFILE_ISSUES=0`).

**`lib/orchestrator/`** (registry path callers):
- `run.go`: doc comment at top now reads "sensorPath must be located at `<projectRoot>/.harness/sensors/<id>.json`". The "run is registered under" comment updates to `.harness/runtime/<id>/<run-id>/`.
- `lifecycle.go:350` builds the per-run log directory via a new `registry.Root` accessor (e.g. `RunDir(id, runID)`) instead of `filepath.Join(".runtime", "sensors", envelope.SensorID, runID)`. The accessor centralizes the layout in `lib/registry` where the rest of the rebase lives.
- `live_deps.go:31` doc comment updates to `<root>/.harness/sensors/<id>.json`. `live_deps.go:222` uses the same new `RunDir` accessor.
- `cascade.go:46` builds the evidence `file` field via `Root.SensorFile(failedID)` (returning a relative path string from project root) instead of `fmt.Sprintf("sensors/%s.json", failedID)`.

Invariant: after this PR, `grep -rn '"\.runtime"\|"sensors/"\|"sensors\\/%s' lib/orchestrator/` returns no matches.

**Verdict semantics on missing registry** (per the table in CLAUDE.md "Registry root discovery"): unchanged. `/start-sensor`=pass (creates it), `/list-sensors`=warn, `/stop-sensor`=error, `/tail-sensor`=error. Only the path inside the diagnostic text changes.

## Skill prose updates: `/detect-sensors`

`skills/detect-sensors/SKILL.md` gains an explicit Phase A / Phase B partition before the existing per-capability authoring guidance. New prose, in order:

1. **§0 (new): Stack discovery.** Self-contained instructions for what the LLM does in Phase A: which manifests to read, which initialization files to open, how to identify the encoder config concretely (not "Zap is used" but "`zap.NewProductionConfig()` is called in `cmd/server/main.go:42`"), how to map `(library × config) → log_shape`. Concrete examples for the four most common stacks (Go+Zap, Node+Pino, Python+structlog, Java+Logback) embedded in the prose so the LLM has anchors.
2. **§0.5 (new): Stack-discovery degraded path.** If the LLM cannot identify any logger after a thorough search, persist a minimal stack with `languages[]` populated but `components: []` and `log_shapes: []`. Phase B will degrade gracefully (see §4 in the skill).
3. **§1 (existing, expanded): Schema awareness** — also reads `schemas/stack.json` if Phase A is going to be exercised.
4. **§4 (existing, expanded): Drafting each sensor.** For `kind=observation` + `output=stream` sensors, the prose explicitly directs the LLM to consult `.harness/stack.json#/log_shapes[]` and derive patterns from the shape, anchored on the shape's `sample` (every drafted regex MUST match the sample). The shape's `id` goes in the sensor's `description` for audit ("derived from log_shape 'zap-prod-json'"). Existing prose for lint/build/test (`output: stream` but `kind: assertion`) remains — those continue to use compiler/linter output patterns, not log_shapes.
5. **§7 (existing): Iteration loop.** Clarification: if Phase B yields a sensor with patterns that match nothing in the project's actual stdout, the first remediation is to inspect the stack (`bat .harness/stack.json`), then rerun `/detect-sensors --refresh-stack`.

The skill remains LLM-judgment-heavy by design. The deterministic work (validation, persistence, cross-checks) sits in `write-stack.go` and `write-sensor.go`.

The five blocking-sensor skills (`/start-sensor`, `/stop-sensor`, `/list-sensors`, `/tail-sensor`, `/run-sensor`) each have a description or body that references `<projectRoot>/.runtime/sensors/...` or `<projectRoot>/sensors/<id>.json` — each of those references migrates to its `.harness/` equivalent. Frontmatter `description:` lines are read by the Claude Code skill loader, so the migration is user-visible.

## CHANGELOG and CLAUDE.md updates

**CHANGELOG.md** — new entry:

> ### Breaking: `.harness/` layout
>
> All framework artifacts now live under `<project>/.harness/`:
> - Sensor definitions: `<project>/.harness/sensors/<id>.json` (was `<project>/sensors/<id>.json`)
> - Runtime state: `<project>/.harness/runtime/` (was `<project>/.runtime/sensors/`)
> - Detected stack (new): `<project>/.harness/stack.json`
>
> To migrate an existing project:
> ```bash
> mkdir -p .harness && git mv sensors .harness/sensors && git mv .runtime .harness/runtime
> # update .gitignore: change /.runtime to /.harness/runtime
> ```
> No fallback to the previous layout. `lib/registry.Lookup` searches for `.harness/` only.

**CLAUDE.md** — two sections to update:

- **"Registry root discovery"**: all path references shift from `<projectRoot>/.runtime/sensors/...` to `<projectRoot>/.harness/runtime/...` and from "walk-up looking for `sensors/`" to "walk-up looking for `.harness/`". Verdict-on-missing-file table unchanged.
- **"Auto issue opening"**: dedup cache path reference shifts from `<projectRoot>/.runtime/auto-issues.json` to `<projectRoot>/.harness/runtime/auto-issues.json`.

**Project rule §2 (CLAUDE.md "Project rules")** gains a one-line acknowledgment that there are now three entity schemas (`sensor.json`, `signal.json`, `stack.json`), not two.

## Verification

Each Go artifact ships with Go tests (project rule §8). Coverage minimum:

**`lib/stack/`**:
- `load_test.go` — golden fixture: valid stack → decodes; invalid payload (extra field, missing required, malformed `format` enum) → schema error.
- `persist_test.go` — round-trip: load → mutate → persist → reload → `reflect.DeepEqual`. Atomic write (write-temp-then-rename observable via filesystem). 0o644 permissions.
- `lookup_test.go` — project with `<root>/.harness/stack.json` → `Exists=true`, content matches. Project with no `.harness/` → `Exists=false`, no error. `HARNESS_REGISTRY_ROOT` overrides walk-up.
- `shape_test.go` — table-driven over a multi-shape fixture exercising `ShapesByRole`, `ShapesProducedBy`, `FieldsByMeaning`, `HasSeverity`.

**`write-stack.go` test** (`write-stack_test.go`):
- Happy path: golden payload → exit 0, stack.json on disk matches payload byte-for-byte.
- Missing required field → exit 1, Signal `verdict=error`, `metadata.kind=schema_validation_failed`.
- `produced_by` referencing nonexistent component → exit 1, Signal `metadata.kind=stack_produced_by_orphan`.
- Idempotency: second write of identical payload produces byte-identical file.

**`lib/registry/lookup_test.go`** (updated):
- Existing tests that scaffolded `tmp/sensors/` now scaffold `tmp/.harness/sensors/`.
- New test: project that has the legacy `sensors/` directory but no `.harness/` → `Exists=false` (no fallback, per the explicit choice).
- `HARNESS_REGISTRY_ROOT` env continues pointing at project root (not `.harness/`); internal path resolution lands at `<root>/.harness/runtime/`.

**Downstream skill tests** (`start-sensor`, `stop-sensor`, `list-sensors`, `tail-sensor`, `run-sensor`):
- Fixtures that created `<tmp>/sensors/<id>.json` or `<tmp>/.runtime/sensors/...` migrate to `<tmp>/.harness/sensors/<id>.json` and `<tmp>/.harness/runtime/...`. No behavioral assertions change.

**End-to-end fixture** (`test/fixtures/stack-discovery/`):
- Mini Go HTTP service: `cmd/server/main.go` initializing `zap.NewProductionConfig()` and chi router with `middleware.Logger`. One endpoint that responds 200/400/500 based on query param.
- `expected-stdout.log`: 5–10 captured lines from running it.
- `expected-stack.json`: a hand-written stack that a correct LLM Phase A would produce. Includes components for `go.uber.org/zap` (role: `logger`), `go.uber.org/zap/zapcore` (role: `log-encoder`), `github.com/go-chi/chi/middleware.Logger` (role: `http-middleware`). One `log_shape` per distinct emit shape.
- Go test that loads `expected-stack.json` via `lib/stack.Load`, derives the regex patterns that the skill's Phase B prose describes (using a deterministic helper in the test, not an LLM call), and verifies they collectively match every line in `expected-stdout.log` with the expected verdict distribution.

The fixture test does **not** exercise the LLM. It tests that **given a well-formed stack, the deterministic pipeline downstream produces patterns that work** and that **the stack schema can faithfully describe a real service**.

## Phasing

Four phases, ideally four PRs:

1. **Schema + lib/stack/ + write-stack.go + write_sensor build tag.** Adds the new entity with no consumers yet, plus the one-line `//go:build write_sensor` on the existing `write-sensor.go`. The build tag MUST land in this phase, not in phase 2 — without it, the two `package main` files in `skills/detect-sensors/scripts/` collide on the untagged default build and phase 1 would fail to compile on its own. Mergeable independently; exercised only by golden-case tests. Low blast radius.
2. **Layout migration.** All path-touching call sites updated atomically:
   - `lib/registry` (new accessors, walk-up sentinel), `lib/sensor/path`, `lib/orchestrator/{run.go, lifecycle.go, live_deps.go, cascade.go}`, `hooks/error-issue-autofiler`.
   - Five blocking-sensor `SKILL.md` files (`start-sensor`, `stop-sensor`, `list-sensors`, `tail-sensor`, `run-sensor`) have their frontmatter and body prose updated to `.harness/` paths.
   - Repo dogfood: `git mv sensors .harness/sensors`, `git mv .runtime .harness/runtime` (if present), `.gitignore` swap.
   - CLAUDE.md "Registry root discovery" + "Auto issue opening" updated; project rule §2 acknowledges three entity schemas.
   - CHANGELOG entry with the `git mv` migration recipe for downstream projects.
   - Downstream skill tests (`start`, `stop`, `list`, `tail`, `run`) fixture paths migrated.
   - Mechanical, no behavior change beyond layout.
3. **detect-sensors prose update.** SKILL.md gains §0 Phase A and the §4 Phase B branch. Cites `schemas/stack.json` and the degraded path. No Go changes.
4. **End-to-end fixture.** `test/fixtures/stack-discovery/` lands with one fully worked example (Go+Zap+chi). Validates that the schema + library + script + prose work together.

Bundling phases 1 + 2 is acceptable if the diff stays reviewable; bundling 3 + 4 is fine too. Splitting 2 alone is recommended because the layout change is breaking and benefits from a dedicated commit-history landmark.

## Out of scope

Each is a candidate standalone issue after this lands:

1. **`/tail-sensor --filter <gojq>`** — gojq-based JSONL filter over signals.log at tail time. Independent value; should ship soon after this spec because stack-derived sensors will produce dense streams.
2. **`captures.metadata.*` writing to `Signal.metadata`** — currently `captures` only writes to `evidence[].{file, line_start, line_end, excerpt, rationale}`. Extending to `metadata` would unlock trace_id correlation in Signals (the issue's "trace/span correlation" ask). Today's workaround: rationale receives the whole matched line verbatim; consumers JSON-parse downstream.
3. **New sensor types (http-proxy / log-stream / trace-sink)** — the "tap, don't poll" model. Not needed once stack-derived patterns cover the structured-log case effectively.
4. **`/heal-sensor` stack-staleness detection** — compares `stack.detected_at` against mtimes of `components[].evidence[].file`; proposes `--refresh-stack` when sensors match zero lines for N seconds AND files have moved.
5. **`/detect-sensors --diff-stack`** — diff between the persisted stack and a freshly-discovered one. Useful when project evolves and a subset of patterns still match.

The two bugs at the foot of issue #27 (`/tail-sensor` not honoring `<run_id>` subdir; `/stop-sensor` aggregate `counts` zero) are unrelated to stack-driven observation and should be filed as separate issues — they don't belong in this spec's deferral list because they're not deferred from *this* spec, they're orthogonal pre-existing defects surfaced by the same charge-api bootstrapping exercise.

## Acceptance criteria

A reader of this spec should be able to verify the work is done when:

- `schemas/stack.json` exists with the contract described in this spec, and validates the fixture stack at `test/fixtures/stack-discovery/expected-stack.json`.
- `lib/stack/` exists with the four files listed in §Library contracts and `go test ./lib/stack/...` passes.
- `skills/detect-sensors/scripts/write-stack.go` exists with build tag `write_stack`; `skills/detect-sensors/scripts/write-sensor.go` carries the new build tag `write_sensor`; `go test -tags=write_stack ./skills/detect-sensors/scripts/...` and `go test -tags=write_sensor ./skills/detect-sensors/scripts/...` both pass.
- `lib/registry.Lookup` walks up looking for `.harness/`; `grep -rn '"\.runtime"\|"sensors/"' lib/registry/ lib/orchestrator/ lib/sensor/ hooks/` returns no path-literal hits. `go test ./lib/registry/... ./lib/orchestrator/... ./lib/sensor/...` passes.
- `<repo>/sensors/` is gone from the framework's own tree; `<repo>/.harness/sensors/` contains all dogfooded sensors. `<repo>/.gitignore` no longer references `.runtime/` and instead lists `.harness/runtime/`. `go test -tags=run_computational ./...` and `go test -tags=run_inferential ./...` pass against the new layout.
- `CHANGELOG.md` documents the breaking change with the `git mv` recipe.
- `CLAUDE.md` "Registry root discovery" and "Auto issue opening" reference `.harness/` paths. `CLAUDE.md` project rule §2 acknowledges three entity schemas (`sensor.json`, `signal.json`, `stack.json`).
- `skills/detect-sensors/SKILL.md` contains a "Stack discovery" section before the per-capability authoring guidance, and the per-capability section contains a branch beginning with the literal phrase "For kind=observation + output=stream sensors:". Both grep cleanly. The prose cites `schemas/stack.json` and `.harness/stack.json` by exact filename.
- `skills/{start-sensor,stop-sensor,list-sensors,tail-sensor,run-sensor}/SKILL.md` frontmatter `description:` and body prose reference `.harness/` paths only; no remaining mentions of `.runtime/sensors/` or `<projectRoot>/sensors/`.
- `test/fixtures/stack-discovery/` exists with the Go service, `expected-stdout.log`, `expected-stack.json`, and the end-to-end test passes.

## References

- Issue #27 — observation sensors should surface HTTP requests, structured logs, and traces as first-class Signals.
- Boeckeler, B. et al. *Harness Engineering*. martinfowler.com.
- Lopopolo, R. *Harness engineering: leveraging Codex in an agent-first world*. openai.com, 2026-02-11.
- Prior framework specs in `docs/superpowers/specs/` — particularly the layout, lifecycle, and registry conventions established by the blocking-sensors and registry-root-discovery designs.
