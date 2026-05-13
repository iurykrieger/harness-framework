# /create-sensor skill design

Status: proposed
Date: 2026-05-13
Related: `schemas/sensor.json`, `schemas/signal.json`, `schemas/stack.json`, `lib/sensor/`, `lib/schema/`, `skills/detect-sensors/`, `skills/run-sensor/`

## Why

Today the harness offers two ways for a sensor JSON to land on disk:

1. **`/detect-sensors`** — a project-wide sweep. The LLM reads the entire repo, classifies the project's archetypes, and drafts one sensor per inferred capability (lint, build, unit-test, run-project, …). Optimized for **bootstrapping a harness from zero**.
2. **Hand-authored** — the user opens `.harness/sensors/` in an editor and writes the JSON themselves. Optimized for **anyone who knows the schema by heart**, which is no one.

There is no path between those two extremes. Once the harness exists, the most common authoring request is the opposite of a sweep: *"I have a single acceptance criterion / functional requirement / use case; produce one targeted sensor that validates it deterministically, and have it reuse the sensors I already have."* That request currently means re-reading `schemas/sensor.json`, picking the right discriminators, choosing `depends_on` vs `requires[kind=sensor]` based on the dep's `execution.blocking` flag, writing patterns from memory, hand-authoring fixtures that match the schema's `verification.golden_cases` minItems-1 constraint, and finally piping it through `write-sensor.go`. It is the kind of work the plugin exists to automate — and it is the only common sensor-authoring shape with no skill behind it.

The corollary failure: when developers need a new sensor for a single requirement, they reach for `/detect-sensors` (which re-scans the whole project and emits sensors orthogonal to what they wanted) or they skip the harness entirely (adding an ad-hoc shell command somewhere). Both outcomes waste the composability that `depends_on` / `requires[kind=sensor]` were designed to enable.

This spec adds a skill, `/create-sensor`, that takes a single requirement-shaped prompt as input and produces one targeted sensor, biased toward **composing existing sensors** rather than re-deriving primitives.

## What changes

1. **New skill: `skills/create-sensor/`** with `SKILL.md` and three skill-local Go scripts. No new schemas; reuses `schemas/sensor.json`, `schemas/signal.json`, and `schemas/stack.json` as the source of truth for shape.
2. **New script `skills/create-sensor/scripts/catalog-sensors.go`** (build tag `catalog_sensors`) — read-only catalog of existing sensors in `<project>/.harness/sensors/`. Emits JSONL to stdout, one digest per sensor (`{id, kind, type, output, blocking, description, path}`). Used as context in the clarification dialogue so the LLM proposes deps from the user's actual sensor inventory, not from memory.
3. **New script `skills/create-sensor/scripts/write-fixture.go`** (build tag `write_fixture`) — atomic write of a single fixture file under `<project>/.harness/sensors/fixtures/<sensor-id>/<case-name>.<ext>`. Rejects paths that escape the fixtures root. Creates parent dirs. Idempotent.
4. **New script `skills/create-sensor/scripts/write-sensor.go`** (build tag `write_sensor`) — thin CLI over `lib/sensor.ValidateAndPersist`. Strict by default: refuses to persist a sensor JSON whose `verification.golden_cases[].fixture` paths do not exist on disk; refuses to overwrite an existing `<id>.json`. Counterpart of (but duplicated from) `skills/detect-sensors/scripts/write-sensor.go` — rule 4 "duplicate scripts before coupling".
5. **`SKILL.md` orchestrates a 7-phase flow** (parse → catalog → classify → draft → clarify → fixtures → persist). No e2e verification; user runs `/run-sensor` after.
6. **No changes to `lib/`, no new schemas, no changes to existing skills.** The skill is purely additive.

## Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  /create-sensor "<requirement-as-text>"                             │
└────────────────────────────────┬────────────────────────────────────┘
                                 ▼
              ┌────────────────────────────────────┐
              │  Phase 1 — Parse invocation        │  (SKILL.md)
              └────────────────┬───────────────────┘
                               ▼
              ┌────────────────────────────────────┐
              │  Phase 2 — Catalog + Stack         │  (Go: catalog-sensors.go)
              │  • list existing sensors as digest │
              │  • read .harness/stack.json (opt)  │
              └────────────────┬───────────────────┘
                               ▼
              ┌────────────────────────────────────┐
              │  Phase 3 — Classify                │  (LLM judgment)
              │  kind = assertion (always)         │
              │  type = computational by default,  │
              │         inferential iff fuzzy      │
              │  output = single | stream          │
              └────────────────┬───────────────────┘
                               ▼
              ┌────────────────────────────────────┐
              │  Phase 4 — Draft v0                │  (LLM judgment)
              │  • command, exit_code_map          │
              │  • requires[kind=env]              │
              │  • deps: dep.blocking=true →       │
              │      requires[kind=sensor];        │
              │      else depends_on               │
              └────────────────┬───────────────────┘
                               ▼
              ┌────────────────────────────────────┐
              │  Phase 5 — Clarification loop      │  (LLM ↔ user)
              │  ONE question per turn, only on    │
              │  gaps the LLM cannot infer         │
              └────────────────┬───────────────────┘
                               ▼
              ┌────────────────────────────────────┐
              │  Phase 6 — Fixture synthesis       │  (LLM + Go)
              │  • user-provided samples verbatim  │
              │  • else LLM synthesizes per verdict│
              │  • call write-fixture.go × N       │
              └────────────────┬───────────────────┘
                               ▼
              ┌────────────────────────────────────┐
              │  Phase 7 — Persist + report        │  (Go: write-sensor.go)
              │  • validates against schemas/      │
              │  • verifies fixture paths exist    │
              │  • atomic write to <id>.json       │
              │  • emits final Signal              │
              └────────────────────────────────────┘
```

### Boundary with `/detect-sensors`

| Skill | Input | Output | When used |
|---|---|---|---|
| `/detect-sensors` | A project directory | Many sensors covering all inferred capabilities + `stack.json` | Bootstrap: harness does not exist yet, or the project gained a major new capability |
| `/create-sensor` | One requirement-as-text | One targeted assertion sensor (+ its fixtures) | Day-to-day: a specific behavior needs deterministic regression coverage |

The two skills never call each other. `/create-sensor` reads (but does not write) `stack.json` when present and gracefully degrades when absent.

### Boundary with `lib/`

`lib/sensor.ValidateAndPersist` is the only `lib/` entry point invoked from `/create-sensor` (via `write-sensor.go`). The skill does not add new helpers to `lib/`; all skill-local concerns (cataloging, fixture writing) live under `skills/create-sensor/scripts/`. This keeps `lib/` tied to stable schema-bound primitives as rule 4 requires.

## Components

### `catalog-sensors.go` (`//go:build catalog_sensors`)

**Job.** Read `<project>/.harness/sensors/*.json`. Emit one JSONL line per sensor on stdout with the digest below. Exit 0 even when the directory is absent or empty (emit nothing). On per-file parse failure: emit a `verdict=warn` Signal envelope describing the malformed file and continue with the remaining files.

**Args.**
- positional 1 (optional): target directory. Defaults to `<projectRoot>/.harness/sensors/` resolved via `lib/registry.Lookup(cwd)`.
- `--schemas-dir <path>` (optional): when set, each sensor JSON is schema-validated before being included in the digest; schema-invalid sensors are skipped with a `verdict=warn` envelope.

**Digest schema** (one JSON object per stdout line):

```jsonc
{
  "id":          "run-project-nest",
  "kind":        "observation",
  "type":        "computational",
  "output":      "stream",
  "blocking":    true,
  "description": "Boots the NestJS app locally...",
  "path":        ".harness/sensors/run-project-nest.json"
}
```

Notes:
- `blocking` is `false` when `execution.blocking` is absent or false in the source JSON.
- `path` is relative to the project root for log-friendliness.

### `write-fixture.go` (`//go:build write_fixture`)

**Job.** Atomically write a fixture payload to `<projectRoot>/.harness/sensors/fixtures/<sensor-id>/<case-name>.<ext>`.

**Args.**
- positional 1: target relative path under `.harness/sensors/fixtures/`. The script rejects (`verdict=error`, `metadata.kind=fixture_path_escape`) any path that, after cleaning, does not have `<projectRoot>/.harness/sensors/fixtures/` as a prefix.
- positional 2 (optional) `--from-file <src>`: read payload from a file; otherwise read from stdin.

**Behavior.**
- `os.MkdirAll` on parent directories with mode `0o755`.
- Atomic write: `os.CreateTemp(parentDir, ".tmp-*")` → write → `fsync` → `os.Rename`.
- Idempotent: re-writing the same content to the same path is allowed; the script returns `verdict=pass` either way.
- Emits a single Signal envelope on stdout.

### `write-sensor.go` (`//go:build write_sensor`)

**Job.** Validate a sensor draft against `schemas/sensor.json`, verify fixture paths exist on disk, and atomically write the JSON to `<projectRoot>/.harness/sensors/<id>.json`.

**Args.**
- `--schemas-dir <path>`: path to `<plugin>/schemas/`.
- positional 1: path to the draft JSON file.

**Behavior.**
1. Read draft JSON. Decode into `lib/sensor.Sensor` (or equivalent strongly-typed shape).
2. Schema-validate via `lib/schema.Validate(draft, "<schemas-dir>/sensor.json")`. On failure, emit `verdict=error, metadata.kind=schema_invalid` with the JSON-pointer of the violation in `evidence`. Exit 2.
3. For each `verification.golden_cases[].fixture`, resolve relative to `<projectRoot>` and check the file exists. On any missing fixture, emit `verdict=error, metadata.kind=missing_fixture` listing the offending path. Exit 2.
4. Compute the target path `<projectRoot>/.harness/sensors/<id>.json`. If it already exists, emit `verdict=error, metadata.kind=sensor_already_exists` referencing the existing path. **No `--force` flag.** Exit 2.
5. Atomic write (tmp+rename) via `lib/sensor.ValidateAndPersist`.
6. Emit `verdict=pass, metadata.{path, id, kind, type}`. Exit 0.

**Why duplicated from `/detect-sensors`'s `write-sensor.go`.** Rule 4: "Scripts are skill-local; duplicate before coupling." The two scripts will inevitably diverge as each skill's idempotency policy and validation strictness evolve. The shared logic (schema validation, atomic write) lives in `lib/sensor`; the CLI wrappers are skill-local and short.

### `SKILL.md` body

The skill body is procedural prose addressing the LLM. Sections:

#### Frontmatter

```yaml
---
name: create-sensor
description: Use when the user invokes /create-sensor or asks to create a single sensor that validates a specific acceptance criterion, functional requirement, or use case. Takes a free-text requirement as input, runs an interactive clarification dialogue, composes existing sensors as dependencies, synthesizes fixtures, and persists one new assertion sensor to <project>/.harness/sensors/<id>.json via the schema validator. Distinct from /detect-sensors, which sweeps the whole project; /create-sensor produces exactly one targeted sensor per invocation.
---
```

#### Phase 1: Parse invocation

If the user supplied a textual requirement as the skill argument, use it as the seed. If empty, prompt the user once: *"What is the requirement / acceptance criterion / use case you want this sensor to validate? Paste it as free text."* Block on the user's reply before proceeding.

#### Phase 2: Catalog + stack

Invoke `catalog-sensors.go`:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=catalog_sensors \
  ./skills/create-sensor/scripts
```

Parse JSONL output. If a `<projectRoot>/.harness/stack.json` exists, read it as additional context. Both inputs feed the LLM's reasoning in Phase 3 and 4.

#### Phase 3: Classify

Decide the three discriminators:

- `kind` — always `assertion`. `/create-sensor` does not produce `observation` or `setup` sensors. If the user's requirement is shaped like an observation ("watch the logs while X runs") or a setup ("install dependencies before Y"), the skill explains the boundary and recommends `/detect-sensors` for those shapes.
- `type` — `computational` by default. Escalate to `inferential` only when the requirement is genuinely probabilistic and no deterministic shell command can verify it (examples: *"the API response should be semantically equivalent to the spec example"*, *"the log line should not contain personally identifiable information"*). Inferential implies a `calibration` block; the LLM populates it with a documented default (`confidence_threshold: 0.8`, an empty `calibration_set: []` plus a `blind_spots[]` entry noting the calibration set is empty pending real samples).
- `output` — `stream` when the underlying tool naturally emits one independently-actionable observation per line (multi-record verification, batch tests). `single` otherwise (the common case for HTTP-200 / jq-equals / file-exists checks).

#### Phase 4: Draft v0

LLM produces a first-pass JSON in memory (not yet on disk) containing:

- `id` — kebab-case, prefixed `assert-` (matches the `assertion` kind convention from `/detect-sensors`).
- `version: "0.1.0"`.
- `name`, `description` — one-sentence summaries citing the requirement source.
- `cost.{class, latency}` — sensible defaults for the inferred check (cheap+fast for HTTP probe; medium for multi-step setup; expensive for inferential).
- `triggers: [{ on: "manual" }]` by default.
- `requires[kind=env]` — one entry per env var the command references.
- Deps (the composability heart of the skill):
  - For each existing sensor in the catalog the LLM judges relevant: if its `blocking` field is `true`, the dep becomes `requires[kind=sensor]` (orchestrator holds it live); otherwise `depends_on` (orchestrator runs it once and chains the verdict).
  - The mapping `blocking → requires` vs `one-shot → depends_on` is deterministic and stated in the skill body so the LLM applies it consistently.
- `execution.command` — the shell invocation.
- `execution.exit_code_map` — `[{exit_code: 0, verdict: pass, severity: info}, {exit_code: "*", verdict: fail, severity: high}]` by default.
- `execution.output_parsing.patterns` — only when `output=stream`. One pattern per actionable verdict.
- `verification.golden_cases` — placeholder array; populated in Phase 6.

#### Phase 5: Clarification loop

Identify gaps the LLM cannot resolve from the requirement + catalog + stack. Common gaps:

| Gap | Sample question |
|---|---|
| Command target | *"What URL / file path / process should the check target? If localhost, which port?"* |
| Auth | *"Does the check need auth? If yes, which env var holds the token, and what's the header format?"* |
| Inputs | *"What inputs does the command need (request body, query params, CLI flags)? Are these stable values or should we templatize them via env vars?"* |
| Expected output | *"What does success look like in the command's output? What does failure look like?"* |
| Deps | *"The catalog has [X, Y]. Should this sensor depend on X to bring the system up first?"* |
| Failure modes | *"Beyond the obvious exit code, are there other failures you want this to catch (timeouts, specific error messages)?"* |

**One question per turn.** The skill does not batch questions. After each user answer, the skill updates the in-memory draft and re-evaluates remaining gaps. The loop ends when no more gaps remain, or when the user signals *"that's enough, generate it"*.

#### Phase 6: Fixture synthesis

Each `golden_case` needs a fixture file. For each verdict the sensor declares (at minimum `pass`; usually also `fail`):

1. If the user's requirement or clarification answers explicitly contained a sample output for that verdict, use that verbatim as the fixture content.
2. Otherwise, LLM synthesizes a plausible fixture based on the requirement and the command. For example, for `curl -w '%{http_code}'`, the pass fixture is `200\n`; the fail fixture is `404\n` (or whatever failure mode the requirement implies).

Fixture files land at `<projectRoot>/.harness/sensors/fixtures/<sensor-id>/<case-name>.txt`. The skill invokes `write-fixture.go` once per fixture:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_fixture \
  ./skills/create-sensor/scripts \
  ".harness/sensors/fixtures/<id>/pass.txt" \
  --from-file <(printf '%s' "<payload>")
```

The draft's `verification.golden_cases[].fixture` paths reference these files.

#### Phase 7: Persist + report

Write the draft JSON to a temp file (`os.CreateTemp`), then invoke `write-sensor.go`:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_sensor \
  ./skills/create-sensor/scripts \
  --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas" \
  /tmp/draft.json
```

On success, the skill emits a final summary message to the user:

> Created sensor `assert-post-users-id-200` at `.harness/sensors/assert-post-users-id-200.json`.
> Dependencies wired: `run-project-nest` (via `requires[kind=sensor]`, blocking).
> Fixtures: `pass.txt` (200), `fail-404.txt` (404).
> Next: run `/run-sensor assert-post-users-id-200` to exercise the sensor.

On failure (schema invalid, id collision, missing fixture), the skill surfaces the error envelope from `write-sensor.go` verbatim and offers two recovery paths: *re-draft* (LLM patches the draft based on the error and retries) or *abort* (user keeps the draft as-is for manual edit).

## Data flow worked example

**Input prompt:** `"POST /v1/users/:id returns 200 when the user exists"`

```
catalog-sensors.go output (JSONL):
  {"id":"run-project-nest","blocking":true,"kind":"observation",...}
  {"id":"lint-eslint","blocking":false,"kind":"assertion",...}
  {"id":"unit-test-jest","blocking":false,"kind":"assertion",...}

.harness/stack.json present:
  components: [{role:"http-server", name:"nest", ...}]
  log_shapes: [...]  (not consumed by /create-sensor — assertion sensor)

Phase 3 classification:
  kind: assertion
  type: computational  (HTTP status is deterministic)
  output: single       (only the http_code line matters)

Phase 4 draft v0:
  id: assert-get-users-id-200
  command: curl -fsS -o /dev/null -w '%{http_code}\n' \
           -H "Authorization: Bearer $AUTH_TOKEN" \
           "$BASE_URL/v1/users/$USER_ID"
  requires[kind=sensor]: [{kind: "sensor", id: "run-project-nest"}]
  requires[kind=env]:    [BASE_URL, USER_ID, AUTH_TOKEN]
  exit_code_map: [{0,pass,info},{*,fail,high}]

Phase 5 clarification:
  Q1: "USER_ID — id existente ou criado pelo teste?"
  A1: "Existente. id=12345 num env de dev."
  Q2: "Auth necessária?"
  A2: "Sim, AUTH_TOKEN é um JWT."
  (no more gaps)

Phase 6 fixtures (synthesized):
  .harness/sensors/fixtures/assert-get-users-id-200/pass.txt    → "200\n"
  .harness/sensors/fixtures/assert-get-users-id-200/fail-404.txt → "404\n"

Phase 7 persist:
  write-sensor.go validates → writes .harness/sensors/assert-get-users-id-200.json
  → Signal {verdict: pass, metadata.path: ".harness/sensors/assert-get-users-id-200.json"}
```

## Error handling

| Situation | Component | Response |
|---|---|---|
| `<project>/.harness/sensors/` absent | `catalog-sensors.go` | Emit nothing, exit 0. Skill proceeds with empty catalog. |
| Malformed sensor JSON in catalog | `catalog-sensors.go` | Emit one `verdict=warn` Signal naming the file; skip; continue. |
| `stack.json` absent or invalid | `SKILL.md` | Ignore. Proceed without stack context. |
| Fixture path escapes fixtures root | `write-fixture.go` | `verdict=error, metadata.kind=fixture_path_escape`. Exit 2. |
| Schema invalid on final draft | `write-sensor.go` | `verdict=error, metadata.kind=schema_invalid` with JSON-pointer of violation. Skill offers LLM-driven redraft. |
| `<id>.json` already exists | `write-sensor.go` | `verdict=error, metadata.kind=sensor_already_exists`. User picks new id or removes existing file. No `--force`. |
| `verification.golden_cases[].fixture` path missing on disk | `write-sensor.go` | `verdict=error, metadata.kind=missing_fixture`. Skill regenerates Phase 6. |
| `CLAUDE_PLUGIN_ROOT` empty | all three scripts | `verdict=error, metadata.cause=plugin_root_missing`. Exit 2. Consistent with rest of plugin. |
| User abandons mid-dialogue | `SKILL.md` | Nothing persisted (all writes are in Phase 6+7). |

No retry loops, no auto-heal, no safety-net abstractions beyond what's listed. The user re-invokes `/create-sensor` or edits the draft manually.

## Testing

Per rule 8, each Go script has `_test.go` adjacent. All tests are table-driven (rule 8) and use the `testing` package.

### `catalog-sensors_test.go`

| Case | Input | Expected |
|---|---|---|
| Empty directory | `testdata/empty/` | stdout empty; exit 0 |
| Missing directory | path that doesn't exist | stdout empty; exit 0 |
| One valid sensor | `testdata/one-sensor/` | one JSONL line with full digest |
| Multiple valid sensors | `testdata/multi-sensor/` | N JSONL lines, sorted by id |
| Mixed: valid + malformed JSON | `testdata/mixed/` | valid sensor digest on stdout + `verdict=warn` Signal for the malformed file |
| `--schemas-dir` set, schema-invalid sensor | `testdata/schema-invalid/` | `verdict=warn` Signal naming the file; sensor omitted from output |
| `blocking` flag derivation | sensor with `execution.blocking: true` | digest `blocking: true` |
| `blocking` absent | sensor without `execution.blocking` | digest `blocking: false` |

### `write-fixture_test.go`

| Case | Input | Expected |
|---|---|---|
| Happy path, stdin | path under fixtures/, stdin payload | file exists with payload; `verdict=pass` |
| Happy path, `--from-file` | `--from-file <src>` | file exists with payload from src |
| Parent dirs missing | nested path under fixtures/ | parent dirs created; file exists |
| Path escapes fixtures root | `../outside.txt` | `verdict=error, metadata.kind=fixture_path_escape`; nothing written |
| Idempotent re-write | same path twice, same content | both writes succeed; `verdict=pass` on both |
| Atomic write under failure | (simulated via `chmod` on tmp dir; if reliably testable) | no partial file remains |

### `write-sensor_test.go`

| Case | Input | Expected |
|---|---|---|
| Happy path | valid draft + fixtures on disk | `<id>.json` written atomically; `verdict=pass` |
| Schema invalid | draft missing required field | `verdict=error, kind=schema_invalid`; JSON-pointer in evidence; nothing written |
| `<id>.json` already exists | valid draft, target file present | `verdict=error, kind=sensor_already_exists`; nothing written |
| Fixture path missing | draft references absent fixture | `verdict=error, kind=missing_fixture`; nothing written |
| Atomic write (interrupt mid-rename) | (simulated) | no partial `<id>.json` file remains |
| `CLAUDE_PLUGIN_ROOT` empty | env unset | `verdict=error, cause=plugin_root_missing` |

### SKILL.md acceptance

The skill body is procedural prose without unit tests. Acceptance is **end-to-end manual**:

1. Set up a small fixture project with at least one blocking sensor (e.g., `run-project-nest`) already in `.harness/sensors/`.
2. Invoke `/create-sensor "POST /v1/users/:id returns 200"`.
3. Answer Phase 5 questions.
4. Verify:
   - One new file at `.harness/sensors/assert-get-users-id-200.json`, schema-valid.
   - `requires[kind=sensor]` references `run-project-nest`.
   - Fixture files exist at the paths declared in `verification.golden_cases[].fixture`.
   - `/run-sensor assert-get-users-id-200` executes the sensor (it may fail if the dev environment isn't up — that's expected and outside the skill's contract per the "schema + persist only" decision).
5. Run `/create-sensor` again with the same id-implying prompt — verify the `sensor_already_exists` error surfaces.

## Acceptance criteria

Binary checks that gate "spec implemented":

1. ☐ `skills/create-sensor/SKILL.md` exists with the frontmatter from the design and the 7-phase body.
2. ☐ `skills/create-sensor/scripts/catalog-sensors.go` and `catalog-sensors_test.go` exist; `go test -tags=catalog_sensors ./skills/create-sensor/...` passes.
3. ☐ `skills/create-sensor/scripts/write-fixture.go` and `write-fixture_test.go` exist; `go test -tags=write_fixture ./skills/create-sensor/...` passes.
4. ☐ `skills/create-sensor/scripts/write-sensor.go` and `write-sensor_test.go` exist; `go test -tags=write_sensor ./skills/create-sensor/...` passes.
5. ☐ `go vet -tags=catalog_sensors ./...`, `-tags=write_fixture`, `-tags=write_sensor` all pass.
6. ☐ The skill is listed in `/help` (i.e., Claude Code's skill loader sees the frontmatter).
7. ☐ End-to-end manual acceptance from the "SKILL.md acceptance" section passes against a fixture project.
8. ☐ No new files under `lib/`. No changes to existing schemas. No changes to other skills' `SKILL.md` or scripts.

## Anti-scope

Explicitly not in this spec:

- **End-to-end verification.** The skill stops at schema validation + persistence. It does not invoke `/run-sensor` after creating. (Decided during brainstorming.)
- **Multi-sensor authoring.** One invocation produces one sensor. Multi-AC prompts ("here are five acceptance criteria") require five invocations. The skill explicitly does not split a multi-AC prompt into multiple sensors.
- **`kind=observation` and `kind=setup`** sensors. `/create-sensor` only produces `kind=assertion`. The boundary is enforced in Phase 3 (skill recommends `/detect-sensors` for those shapes).
- **Auto-heal on failure.** If `write-sensor.go` rejects the draft, the skill offers a redraft path *via LLM*, not via an automated heal loop. No retry budget, no patch templates.
- **Force-overwrite of existing sensor.** No `--force` flag on `write-sensor.go`. User collisions are user-decisions.
- **New `lib/` helpers.** All deterministic logic this skill needs is already in `lib/sensor` and `lib/schema`; the skill-local scripts are thin CLI wrappers.
- **Caching `catalog-sensors.go` output across invocations.** Each invocation re-reads the directory. The catalog is small (typically < 50 sensors); caching is premature.

## Risks and blind spots

1. **Catalog quality depends on the user's existing sensors.** If `.harness/sensors/` has shallow `description` fields, the LLM's dep proposals in Phase 4 will be weak. Mitigation: the dep proposals are surfaced as questions in Phase 5; the user confirms.
2. **Fixture synthesis is LLM-driven.** Synthesized fixtures may not exactly match what the real command would emit. The fixtures serve `verification.golden_cases` (which `/run-sensor` doesn't auto-execute; they're documentation + regression hooks). The user reviews and tightens after first real run. Documented in `blind_spots[]` of generated sensors.
3. **Inferential escalation criteria are subjective.** "Genuinely probabilistic" is a judgment call. The skill body provides examples; edge cases land on the user during Phase 3. Acceptable for v0.1.0.
4. **No automated way to detect when the catalog drifts** (sensor file is deleted but a freshly-created sensor was wired to it). The `requires[kind=sensor]` reference becomes a hanging pointer. Mitigation: `/run-sensor` already surfaces unknown-dep errors via `lib/orchestrator`; the failure mode is loud and immediate. Future work could add a `validate-deps` script; out of scope here.
5. **Duplication with `/detect-sensors`'s `write-sensor.go`.** Two scripts with overlapping logic. Mitigation: rule 4's "duplicate before coupling" is deliberate; the shared logic lives in `lib/sensor.ValidateAndPersist`. The duplication is the CLI wrapper only (~50 lines).
