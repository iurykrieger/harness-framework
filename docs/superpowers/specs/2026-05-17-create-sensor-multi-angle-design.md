# /create-sensor multi-angle authoring — design

**Status:** Draft
**Date:** 2026-05-17
**Spec set:** B of {A: complex-commands (PR #58 merged), B: authoring, C: detection}
**Breaking change:** Yes — `verification.golden_cases[]` removed from `schemas/sensor.yaml`; replaced by top-level `use_cases[]`. No migration path; users regenerate.

---

## Problem

The current `/create-sensor` skill (specified in `docs/superpowers/specs/2026-05-13-create-sensor-design.md` and implemented in `skills/create-sensor/SKILL.md`) produces **the simplest possible sensor**: one shell command, one `exit_code_map`, one `pass.txt` / `fail.txt` pair. Three concrete consequences:

1. **It ignores `.harness/usecases/`.** `/detect-usecases` has been producing a rich, structured ledger of journey variations (57 usecases across 9 journeys at the time of this spec), each carrying `trigger.fixture`, `behavior.business_rules[]`, `expected_outcome.fixture`, `evidence[]` and `regression_priority`. None of this informs `/create-sensor`.
2. **It does not use Spec A primitives.** Typed steps (`shell`/`http`/`assert`/`sensor`), top-level fixtures (`.harness/fixtures/`), step `outputs`, `${{ steps.X.outputs.Y }}` interpolation, and sensor composition via `type: sensor` are all available since PR #58 (merged 2026-05-15). The author has the toolkit; the skill does not wire it.
3. **It has no mock/stub strategy.** "Fixtures" today means one text file per declared verdict. There is no notion of payload mocks, downstream stubs, or recorded responses — even though the underlying `type: http` step + `with: { fixture: ... }` makes all three trivially expressible.

The user wants `/create-sensor` to produce **multi-angle** sensors that hit a requirement from several facets at once, while a single invocation may legitimately yield **multiple sensors** when the input usecase set spans heterogeneous angles. The "simplest possible sensor" mode is exactly what motivated the brainstorm.

## Goals

- Take the usecase ledger seriously as input. The new `/create-sensor` reads usecase YAMLs (by id, by journey, by file path, or inferred from free text) and treats `behavior.business_rules[]` as the authoritative source of internal multi-angle decomposition.
- Move all deterministic logic (grouping, kind/type/output inference, mock-strategy selection) into testable Go (Rule #6, Rule #7). Leave the LLM responsible only for synthesis — translating a `business_rule` into a concrete typed step.
- Honor an N:N relationship between usecases and sensors. One invocation can emit several sensors; one usecase can be referenced by several sensors.
- Replace `verification.golden_cases[]` (which duplicated `usecase.trigger.fixture` and `usecase.expected_outcome.fixture` and required a separate `run-golden` runtime) with declarative `use_cases[]` traceability. Step-level assertions inside `execution.steps[]` become the sole runtime contract.

## Non-goals

- No runtime auto-replay of usecases. `use_cases[]` is declarative. The sensor's own steps decide pass/fail. (See "Tradeoffs" below for the rationale.)
- No migration path for sensors built against the old schema. Per user direction: "no rollout, assume all plugin users will remove old entities and regenerate." The framework's own smoke sensors are regenerated as part of this change.
- No new mock-server infrastructure shipped by the plugin. When the heuristic selects `mock_strategy = setup-mock-infra`, the skill emits a `kind=setup` sensor whose `execution.command` is the project's own choice (`docker compose up wiremock`, `npm run mock-server`, etc.) — the LLM proposes; the user adapts. The framework provides composition, not the mocks themselves.
- No automatic refactor of duplicated fixtures between usecases and sensors. Acknowledged but deferred — when usecases get refactored to be the single source of truth for fixture payloads (anticipated direction), that is a separate change.

## Architecture

```
┌────────────────────────────────────────────────────────────────────────┐
│ /create-sensor <input>                                                 │
│                                                                        │
│   <input> ∈ { usecase-id | journey-id | path/to/usecase.yaml | "text" }│
└────────────────┬───────────────────────────────────────────────────────┘
                 ▼
       ┌─────────────────┐
       │ Phase 1: Parse  │ (markdown — LLM)
       │   invocation    │   classify input shape; resolve usecase set
       └────────┬────────┘
                ▼
       ┌─────────────────────────────┐
       │ Phase 2: Load ledger        │ (Go — read-usecases.go)
       │  - usecase YAMLs            │   loads usecase.yaml(s) + stack.yaml
       │  - stack.yaml (read-only)   │   + catalog of existing sensors
       │  - sensor catalog           │   emits a single JSON ledger on stdout
       └────────┬────────────────────┘
                ▼
       ┌─────────────────────────────┐
       │ Phase 3: Plan sensor set    │ (Go — plan-sensors.go)
       │  deterministic grouping +   │   input: ledger JSON
       │  kind/type/output           │   output: JSONL plan, one line per
       │  inference                  │   proposed sensor + step_outline[]
       └────────┬────────────────────┘
                ▼
       ┌─────────────────────────────┐
       │ Phase 4: Synthesize         │ (markdown — LLM)
       │  expand step_outline[] to   │   typed steps (shell/http/assert/sensor)
       │  typed steps;               │   + write fixtures
       │  pick fixtures; wire deps   │
       └────────┬────────────────────┘
                ▼
       ┌─────────────────────────────┐
       │ Phase 5: Persist + report   │ (Go — write-sensor.go, existing)
       │  schema-validate + write    │   one Signal per persisted sensor
       │  each sensor                │
       └─────────────────────────────┘
```

**Go ↔ LLM boundary:**

| Phase | Lives in | Why |
|---|---|---|
| 1 — Parse | LLM | Non-deterministic: input may be free text. |
| 2 — Load | Go (`read-usecases.go`) | Deterministic YAML I/O + schema validation. |
| 3 — Plan | Go (`plan-sensors.go`) | Deterministic heuristic — Rule #6. |
| 4 — Synth | LLM | Genuinely non-deterministic (prose → typed step). |
| 5 — Persist | Go (`write-sensor.go`) | Atomic schema-validated write. |

## Schema changes — `schemas/sensor.yaml`

Conceptual diff (abridged — `properties` and `required` show only the affected fields; the existing 15-item `required` list keeps all other entries):

```diff
 properties:
   ...
+  use_cases:
+    description: |
+      Usecase ids this sensor validates. Pure traceability — the runtime
+      does NOT auto-replay these. Steps under execution authoritatively
+      decide pass/fail. Required so every sensor declares its purpose.
+    type: array
+    minItems: 1
+    items:
+      type: string
+      pattern: ^[a-z][a-z0-9-]*$
-  verification:
-    description: Sensors are code; they need their own harness...
-    additionalProperties: false
-    properties:
-      golden_cases:
-        items:
-          properties:
-            expected_severity: { $ref: signal.yaml#/$defs/Severity }
-            expected_verdict:  { $ref: signal.yaml#/$defs/Verdict }
-            fixture:           { type: string }
-            notes:             { type: string }
-          required: [fixture, expected_verdict, expected_severity]
-        minItems: 1
-      self_test_command: { type: string }
-    required: [golden_cases]
 required:
   - id
   - version
   - name
   - description
   - kind
   - type
   - regulation
   - phase
   - determinism
   - output
   - cost
   - triggers
   - execution
-  - verification
+  - use_cases
```

The top-level `allOf` discriminators (keyed off `type` and `output`) are unaffected — they reference `cost.*` and `execution.*` only.

Cascade through the codebase:

1. **`lib/sensor/shape.go`** — drop `GoldenCase` struct and the `Verification` field. Add `UseCases []string` on `Sensor`. Helpers like `Sensor.HasGoldenCases()` are removed.
2. **`lib/sensor/load.go`** — `Normalize` stops initializing `verification`. The `use_cases[]` pattern is validated at parse time; existence-on-disk is validated in Phase 5.
3. **`lib/sensor/validate.go`** — no `golden_cases` cross-field rules exist here today (grep returns zero hits). Additions only: add `use_cases_files_exist` (when the validator has a `projectRoot`). Schema-level `use_cases_non_empty` is covered by the `minItems: 1` constraint and needs no Go counterpart.
4. **`lib/sensor/persist.go`** — currently extracts `sensorMap["verification"]["golden_cases"][].fixture` to drive the `RequireFixturesOnDisk` check (lines 34, 57, 149–153). Reroute the fixture-existence check to read `use_cases[]` and validate each one resolves to a real `.harness/usecases/**/<id>.yaml`; rename the option to `RequireUseCaseFilesOnDisk`. The dual-purpose comment on the `Rel` field is rewritten accordingly.
5. **`skills/detect-sensors/scripts/run-golden.go`** — deleted along with its `_test.go`. No more golden replay path.
6. **`skills/detect-sensors/SKILL.md`** — currently has nine references to `verification.golden_cases[]` as authoring guidance (Phase B drafting examples + the persistence step). Rewritten in the same PR so `/detect-sensors` produces sensors with `use_cases[]` referencing usecase ids it co-produces (or that already exist under `.harness/usecases/**`). When `/detect-sensors` runs against a project with no usecases yet, it emits sensors with empty `use_cases: []` and a warn — schema rejects this, so the user is funneled to run `/detect-usecases` first.
7. **`.github/workflows/test.yml`** — remove the `run_golden` step; replace with a step that runs the smoke sensors via `run-computational`.
8. **`.harness/sensors/smoke-*.yaml`** — both gain `use_cases: ["framework-smoke-typed-pipeline"]` / `["framework-smoke-with-setup"]`. Those usecases are created as part of this PR under `.harness/usecases/framework/`.
9. **`schemas/usecase.yaml`** — unchanged.

Cross-validation in Phase 5 (`write-sensor.go`): for each `use_case_id` in `use_cases[]`, fail if no file matches `<projectRoot>/.harness/usecases/**/<use_case_id>.yaml`. A `--skip-usecase-existence-check` flag exists but is reserved for the script's own tests; the skill never sets it.

**`/heal-sensor`:** no current references to `verification.golden_cases[]` in the skill body or its scripts (confirmed by grep at spec time). The skill's existing tests cover unchanged paths; no refactor needed here. If `/heal-sensor` later wants to surface usecase context in its diagnostics, that is a separate change.

## Component: `read-usecases.go`

**Location:** `skills/create-sensor/scripts/read-usecases.go` + `read-usecases_test.go`. Build tag `read_usecases`.

**Responsibility:** Resolve a list of usecase identifiers (by id, by journey, or by file path) and emit a single JSON ledger on stdout. Read-only.

**CLI:**

```bash
go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=read_usecases \
  ./skills/create-sensor/scripts \
  --usecases "<id>,<id>,..." \
  [--journey "<journey-id>"] \
  [--list-only] \
  [--include-stack] \
  [--include-catalog]
```

**Flag interactions (single rule):** `--list-only` produces a thin index (id + name + tags only) and is mutually exclusive with `--include-stack` and `--include-catalog`. Combining `--list-only` with either inclusion flag returns `verdict=error, metadata.kind=usage`. When neither inclusion flag is set and `--list-only` is absent, the ledger contains just `usecases[]` (full shape, no stack, no catalog).

Output shape on success:

```json
{
  "usecases": [
    {
      "id": "tail-sensor-no-registry",
      "journey_id": "tail-sensor",
      "name": "/tail-sensor errors when the registry file is absent",
      "regression_priority": "high",
      "tags": ["error-handling", "registry-discovery"],
      "trigger":           { "shape": "CLI invocation", "fixture": {...} },
      "behavior":          { "summary": "...", "business_rules": [...] },
      "expected_outcome":  { "shape": "...", "fixture": {...}, "invariants": [...], "side_effects": [] },
      "evidence":          [{"file": "...", "line_start": 72, "rationale": "..."}],
      "source_path":       ".harness/usecases/tail-sensor/tail-sensor-no-registry.yaml"
    }
  ],
  "stack": { ... },
  "catalog": [
    { "id": "smoke-typed-pipeline", "kind": "assertion", "type": "computational",
      "output": "single", "blocking": false, "path": ".harness/sensors/..." }
  ],
  "project_root": "/abs/path/to/project"
}
```

Errors are emitted as warn/error Signals on stdout BEFORE the ledger. If any usecase is `usecase_schema_invalid`, a warn Signal is emitted for that file and the usecase is skipped — the ledger still produces. If `usecase_not_found` for an explicitly-requested id, an error Signal is emitted and the script exits non-zero with no ledger (the skill aborts).

## Component: `plan-sensors.go`

**Location:** `skills/create-sensor/scripts/plan-sensors.go` + `plan-sensors_test.go`. Build tag `plan_sensors`. Shared types (`Ledger`, `Plan`, `StepOutline`) live in `skills/create-sensor/scripts/lib/` (per Rule #4: scripts stay skill-local; helpers within a skill go in a sub-`lib/`).

**Responsibility:** Read the ledger from stdin, apply the deterministic grouping heuristic, emit a JSONL plan on stdout. Pure CPU. No file I/O.

**CLI:**

```bash
cat /tmp/ledger.json | \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=plan_sensors \
  ./skills/create-sensor/scripts
```

Output shape — one line per planned sensor, then one aggregate Signal as the last line (framework convention):

```json
{
  "sensor_id": "assert-tail-sensor-error-handling",
  "kind": "assertion",
  "type": "computational",
  "output": "stream",
  "use_cases": ["tail-sensor-no-registry", "tail-sensor-invalid-cursor",
                "tail-sensor-cursor-beyond-eof"],
  "step_outline": [
    {
      "step_id": "rule-1-exit-code-is-1",
      "source_usecase": "tail-sensor-no-registry",
      "source_rule": "Verdict is error and exit code is 1.",
      "suggested_step_type": "shell",
      "mock_strategy": "stub-deterministic",
      "evidence": [{"file": "skills/tail-sensor/scripts/tail.go", "line_start": 72}]
    }
  ],
  "rationale": "Grouped by trigger.shape=CLI + journey_id=tail-sensor + tag=error-handling. 3 usecases × 3 business_rules = 9 steps."
}
{
  "aggregate": true, "verdict": "pass", "severity": "info",
  "sensors_planned": 4, "usecases_consumed": 9
}
```

`step_outline[]` is **propositional** — concrete assertions (e.g., `assert.expect`) are deferred to Phase 4 because they require the LLM to bridge `business_rule` prose to a specific check.

### Grouping heuristic (deterministic)

Multi-axis decision; order matters:

| Axis | Rule | Effect |
|---|---|---|
| 1. `journey_id` | Usecases from different journeys NEVER group. | Partitions the input. |
| 2. `trigger.shape` | Same `journey_id` + same `trigger.shape` → candidate to share a sensor. | "CLI invocation" vs "HTTP request" yield distinct sensors. |
| 3. `tags` overlap | ≥1 tag in common → reinforces grouping; disjoint tags → split. | `error-handling` and `happy-path` do not coexist. |
| 4. `evidence` file proximity | Usecases whose `evidence[].file` sits in the SAME directory (or one level up) → same sensor. | `skills/tail-sensor/scripts/tail.go` groups; `lib/registry/lookup.go` separates. |
| 5. `regression_priority` | Does not influence grouping — informative only (feeds `cost.class`). | No cardinality impact. |

**Tie-breaking:** if a bucket holds >8 usecases, fission by dominant tag (each top-frequency tag becomes its own sensor). Hard limit: 8 usecases/sensor. If no tag dominates, the script emits warn `bucket_too_large` and divides into chunks of ≤8. **Deterministic ordering for the split:** sort usecases by `id` ascending, then chunk into contiguous groups of ≤8, naming them `<base>-part-1`, `<base>-part-2`, etc. Recorded in `rationale`. The `go test -count=10` determinism requirement (§Testing plan) depends on this rule.

### Inference: `kind` / `type` / `output`

Applied per planned sensor after grouping:

| Signal | → `kind` |
|---|---|
| `trigger.shape` contains "setup" OR `behavior.summary` mentions "idempotent" | `setup` |
| `expected_outcome.shape` is "stream of events" / "log lines while running" | `observation` |
| (default) | `assertion` |

`expected_outcome.invariants` is always populated on well-formed usecases (the schema does not currently mandate it, but `/detect-usecases` produces it consistently) and therefore is not a useful discriminator — the row above tests `expected_outcome.shape` instead, which IS distinct between assertion-shaped and observation-shaped scenarios.

| Signal | → `type` |
|---|---|
| `business_rules[]` contains semantic adjectives ("semantically equivalent", "team voice", "no PII") | `inferential` (and emits warn — calibration must come from the user) |
| (default) | `computational` |

| Signal | → `output` |
|---|---|
| `expected_outcome.shape` mentions "one line per X" / "stream" / "log lines" | `stream` |
| ≥2 independent `business_rules` all assertable in a single run | `stream` |
| (default) | `single` |

### Canonical cost defaults

The schema requires `cost.compute` for `type=computational` and `cost.tokens` for `type=inferential`. `plan-sensors.go` emits canonical defaults so Phase 4 (LLM) does not have to invent them. The LLM may override during synthesis when the requirement clearly implies different scaling.

| Field | Default for `computational` | Default for `inferential` |
|---|---|---|
| `cost.compute` | `{ cpu: "low", memory_mb: 64 }` | — (forbidden by schema) |
| `cost.tokens` | — (forbidden by schema) | `{ model: "<from user/calibration>", input_avg: 4000, output_avg: 1000, max_output: 4096 }` |
| `cost.class` | `cheap` if all steps are local shell/file; `medium` if any HTTP step or `setup-mock-infra`; else `expensive` | `expensive` |
| `cost.latency` | `{ p50_ms: 10, p95_ms: 100, timeout_ms: 5000 }` for shell-only; `{ p50_ms: 200, p95_ms: 2000, timeout_ms: 30000 }` if any HTTP step | `{ p50_ms: 3000, p95_ms: 20000, timeout_ms: 60000 }` |

`cost.tokens.model` cannot be defaulted — it is the model the inferential sensor will call. `plan-sensors.go` emits the row with `model: ""` and pairs it with the same `inferential_calibration_required` warn that already covers the calibration block; Phase 4 blocks until the user supplies the model id along with the calibration set.

The computational defaults mirror the canonical example at `lib/sensor/persist_test.go:289` (`TestPersistCanonicalIndependentOfDraftStyle`) so existing test fixtures keep round-tripping.

### Inference: `mock_strategy` (the hybrid)

Per step, not per sensor — a sensor may legitimately mix strategies across its steps.

| Signal on `evidence[].file` | `mock_strategy` |
|---|---|
| Path starts with `lib/`, file is `*.go` (not `_test.go`) | `stub-deterministic` — invoke via `go test -run` or a thin wrapper |
| Path points to an HTTP handler (`*.go` importing `net/http`), and `trigger.fixture` carries a request body | `fixture-http-step` — Spec A `type: http`, `with: { fixture: ... }` |
| `expected_outcome.side_effects[]` mentions "DB write", "kafka publish", "external API call" | `setup-mock-infra` — propose a `kind=setup` sensor as auxiliary |
| Nothing clear | `stub-deterministic` (conservative default) + warn recorded in `rationale` |

Multiple `evidence[]` entries with divergent signals: the most specific wins (handler > lib > unknown). Within the same tier (e.g., two `lib/` entries from different subdirs, or two HTTP handlers), the first entry in `evidence[]` order (the order the usecase YAML records them) wins. The ordering is stable and reproducible across runs.

## SKILL.md flow (Phases that live in markdown)

### Phase 1 — Parse invocation

Classifies input into one of four shapes:

| Input | Resolution |
|---|---|
| `/create-sensor <usecase-id>` | Locate `.harness/usecases/**/<usecase-id>.yaml` recursively. Multiple matches → fail asking for qualification by journey. |
| `/create-sensor <journey-id>` | Read all `.harness/usecases/<journey-id>/*.yaml`. |
| `/create-sensor path/to/file.yaml` | Read the file (schema validation deferred to Phase 2). |
| `/create-sensor "<free text>"` | No usecase resolved yet — jump to Phase 1.5. |

No arg → block: *"Which usecase, journey, or requirement do you want to cover? Pass an id, a path, or a free-text description."*

### Phase 1.5 — Free-text inference (only when input is text)

The LLM does semantic matching against the existing usecase catalog:

1. Call `read-usecases.go --list-only` to enumerate ids + names + tags.
2. For each usecase, judge: does this requirement reasonably correspond to this usecase? Build the matched id set.
3. **No matches:** propose to the user two options — run `/detect-usecases` first to populate the ledger, or proceed with an inline synthetic usecase that gets persisted as part of the PR. No default; block on the user.

Semantic matching is the only place the LLM has free judgment in this phase. Everything downstream consumes the resolved id list.

### Phase 2 — Load ledger (Go)

Invoke `read-usecases.go` with the resolved id list, `--include-stack`, and `--include-catalog`. If warns appeared, surface them to the user inline before continuing.

### Phase 3 — Plan sensors (Go)

Pipe the ledger to `plan-sensors.go`. Read the JSONL plan + the aggregate.

### Phase 3.5 — Report plan + confirm

Echo the plan to the user as one-line summaries per sensor (id, kind, type, use_cases count, step count, rationale). Ask: *"Proceed?"* — accept yes/no only; no editing here, because editing the deterministic plan defeats the purpose of having it in Go. If the user wants different grouping, they invoke with a narrower input (single usecase, or specific journey).

If the user says no, the skill aborts cleanly without persisting anything.

### Phase 4 — Synthesize (LLM)

For each planned sensor, in order (not parallel — fixture writes need path coordination):

1. **Read** `step_outline[]` + the source usecase YAMLs referenced.
2. **For each step_outline entry**, expand to a typed step per the matrix:

   | `suggested_step_type` | `mock_strategy` | Expansion |
   |---|---|---|
   | `shell` | `stub-deterministic` | `type: shell`, `run: <command inferred from evidence>`, `exit_code_map: {0: pass, "*": fail}` |
   | `http` | `fixture-http-step` | `type: http`, `with: { fixture: <persisted fixture name> }`, `expect.status: <from invariants>` |
   | `assert` | any | `type: assert`, `expect: { value: "${{ steps.X.outputs.Y }}", contains: "<from business_rule>" }` |
   | any | `setup-mock-infra` | LLM ALSO generates a `kind=setup` sensor (e.g., `setup-wiremock`), declares it as `requires[kind=sensor]` on the main sensor, persists setup first |

3. **Fixtures.** When a step needs one and the source is `trigger.fixture` of the usecase, serialize the fixture (JSON or text) and persist via `write-fixture.go` (existing). Path: `.harness/fixtures/<sensor-id>/<step-id>.<ext>`.
4. **Self-coherence check** before persisting: confirm (a) every `${{ steps.X.outputs.Y }}` reference points to a prior step that declared that output; (b) every `with: { fixture: ... }` points to a file written in step 4.3.

### Phase 5 — Persist + report (Go)

Per sensor, invoke `write-sensor.go --out .harness/sensors --schemas-dir <plugin>/schemas <draft>`. Outcomes are the existing set plus:

- **`verdict=error, metadata.kind=usecase_not_found`** — sensor references a `use_case` that does not exist on disk. The skill aborts the entire invocation (no partial persist of subsequent sensors) and surfaces which ids are missing.

### Final report

```
Created 3 sensors covering 9 usecases:

  ✓ assert-tail-sensor-error-handling
    use_cases: [tail-sensor-no-registry, tail-sensor-invalid-cursor, tail-sensor-cursor-beyond-eof]
    steps: 9 (one per business_rule)
    deps: setup-tmp-registry (auto-created)

  ✓ assert-tail-sensor-happy-path
    use_cases: [tail-sensor-cursor-zero-read-all, tail-sensor-cursor-mid-stream]
    steps: 6
    deps: —

  ✓ setup-tmp-registry  (auxiliary, created to satisfy mock_strategy=setup-mock-infra)

Next: run `/run-sensor assert-tail-sensor-error-handling` to exercise the sensor.
```

### What this skill still does NOT do

- Does not exercise sensors after creation — `/run-sensor` stays manual.
- Does not modify existing sensors — id collisions surface as warn during plan; user decides.
- Does not interpret free text as "create a usecase" — Phase 1.5 proposes and blocks.

## Error handling — Signals

| Origin | `metadata.kind` | `verdict` | Handling |
|---|---|---|---|
| `read-usecases.go` cannot find an id | `usecase_not_found` | `error` | Abort before plan. |
| `read-usecases.go` flag misuse (`--list-only` + `--include-*`) | `usage` | `error` | Abort with help message. |
| usecase YAML violates `schemas/usecase.yaml` | `usecase_schema_invalid` | `warn` | Skip that usecase; continue with the valid ones; surface to user. |
| `plan-sensors.go` collides id with existing sensor | `sensor_id_collision` | `warn` | Plan proceeds with warn; user decides at confirmation. |
| `plan-sensors.go` infers `type=inferential` but no calibration provided | `inferential_calibration_required` | `warn` | Plan emits the sensor as pending; Phase 4 blocks until user supplies calibration. |
| `plan-sensors.go` bucket >8 with no dominant tag | `bucket_too_large` | `warn` | Apply id-sorted chunk split (§Grouping heuristic); record in `rationale`. |
| Phase 4 LLM cannot infer a concrete shell `run` from `evidence[]` (e.g. evidence points to an internal lib function with no shell surface) | `command_inference_failed` | `warn` | Skill blocks and asks the user: "What shell command exercises `<file>:<line>`? (or: should I wrap it as `go test -run <Test>`?)". Sensor is NOT persisted until resolved. |
| LLM produces YAML that fails schema | `schema_invalid` | `error` | Retry with surgical diff (max 2x); then hand draft to user for manual edit. |
| Fixture write escapes `.harness/fixtures/` | `fixture_path_escape` | `error` | Abort the whole invocation. |
| `write-sensor.go` fails on sensor N of M (with N-1 already persisted) | `partial_persist` | `error` | NO rollback of the N-1 (they are valid in themselves); report what landed and what failed. |

## Edge cases (explicit in spec)

1. **Free text that matches no usecase.** Phase 1.5 asks user: run `/detect-usecases` or proceed with inline synthetic usecase. No default.
2. **Journey with 0 valid usecases.** Abort with `verdict=error, metadata.kind=empty_journey`.
3. **Usecase referenced by `use_cases[]` is deleted later.** Next load fails schema (`use_cases_files_exist` rule). No self-heal; user fixes.
4. **Existing sensor with `verification.golden_cases` (pre-Spec B).** No automatic migration. Load fails. User regenerates. Consistent with "no rollout".
5. **9+ usecases fall into the same bucket.** Fission by tag; if no tag dominates, warn `bucket_too_large` + arbitrary `-part-1` / `-part-2` split with rationale.
6. **Usecase without `evidence[]`.** Schema requires `minItems: 1` — caught in Phase 2 as `usecase_schema_invalid`. No further handling.
7. **Sensor classified as `kind=observation`.** Phase 4 uses Spec A `output: stream` + patterns. No deterministic assertion against trigger — sensor is "tail and classify". Recorded in `rationale` as observation-only; deterministic regression is future work.

## Testing plan

### `lib/sensor/` (shape, load, validate)

- Table-driven: `use_cases: []` rejected (minItems 1), `use_cases: ["valid-id"]` accepted, `use_cases: ["Invalid_ID"]` rejected (pattern).
- Confirm `verification` is rejected by the new schema (`additionalProperties: false`).
- Round-trip load preserves `use_cases[]`.

### `skills/create-sensor/scripts/read-usecases.go` + `_test.go` (new)

- Load 1 usecase by id; load all of a journey; reject ambiguous id; emit warn on schema-invalid but continue ledger.
- `--list-only` mode returns thin index only.
- `testdata/` covers 3 journeys × multiple variations.

### `skills/create-sensor/scripts/plan-sensors.go` + `_test.go` (new)

- All scenarios from the grouping table + the kind/type/output matrix + the mock_strategy matrix.
- `testdata/ledger-*.json` fixtures mirror realistic inputs (~10 usecases covering each combination).
- Snapshot tests for JSONL output (golden files under `testdata/plan-output/`).
- Determinism: `go test -count=10` must produce identical output (no `rand`, no `time.Now()` in the plan).

### `skills/create-sensor/scripts/write-sensor.go` (existing, modified)

- Add `use_case_not_found_on_disk` to the test suite.
- Confirm sensors carrying `verification` are rejected.

### Deletions

- `skills/detect-sensors/scripts/run-golden.go` + `_test.go` removed.
- `.github/workflows/test.yml`: `run_golden` step replaced by direct `/run-sensor smoke-typed-pipeline` and `/run-sensor smoke-with-setup` invocations.

### Acceptance sensors + framework usecases

- New `.harness/usecases/framework/framework-smoke-typed-pipeline.yaml` and `.harness/usecases/framework/framework-smoke-with-setup.yaml` documenting trigger/behavior/expected_outcome for what these sensors should emit.
- Both smoke sensors regenerated with `use_cases: ["framework-..."]`.
- New `assert-create-sensor-multi-angle` sensor that exercises `/create-sensor` itself against a real usecase from `tail-sensor`. **The assertion is strictly structural** to survive LLM nondeterminism: (a) the resulting YAML parses against `schemas/sensor.yaml`, (b) `len(execution.steps) ≥ 2`, (c) `len(use_cases) ≥ 1`, (d) at least one step references a real fixture under `.harness/fixtures/`. Do NOT diff YAML bytes or assert specific step contents — the LLM's synthesis is allowed to vary. Acts as the plugin's self-test for this spec.

### Out of scope for automated tests

- Phase 4 (LLM synthesis). Determinism tests stop at the Go boundary. The LLM's behavior is exercised via the acceptance sensor described above.
- `/heal-sensor` refactor is mechanical (replacing one prose template); a quick smoke run is enough; existing `/heal-sensor` tests cover the unchanged paths.

## Tradeoffs

- **Why declarative `use_cases[]` instead of auto-replay.** The user explicitly rejected auto-replay: "duplicação de fixtures será resolvida com refatoração nos usecases. Sensores podem ser mais complexos e terem mais fixtures que um usecase." A sensor's steps are richer than the usecase's trigger/expected_outcome by design — observability assertions, intermediate state checks, setup orchestration. Coupling runtime replay to the usecase shape would force sensors back into the "simplest possible" mold the spec is trying to leave behind. The cost is acknowledged duplication of payload data between usecase fixtures and sensor fixtures; that is a separate cleanup track.
- **Why deterministic grouping in Go instead of LLM-driven.** Two reasons. First, Rule #6: "Deterministic logic belongs in Go, never in skill markdown." Grouping by `journey_id`, `trigger.shape`, `tags`, and evidence proximity is purely deterministic; it does not benefit from LLM judgment. Second, reproducibility: a user re-invoking `/create-sensor <journey>` after the catalog changes should get an answer that differs only for documentable reasons, not because the LLM sampled differently this turn.
- **Why no editing of the plan at confirmation time.** Editing the deterministic plan undoes the benefit of having it deterministic. If the user wants different grouping, narrower input gives it — invoke per-usecase, or one journey at a time. The skill stays focused.
- **Why fission by tag at 8 usecases.** Empirical pick. The largest existing journey (`run-sensor`) has 10 usecases; an 8-cap forces a clean split there into two sensors, each still semantically coherent. Lower would over-fragment; higher would let single sensors grow unwieldy.
- **Why no automatic mock-server infrastructure.** Different projects want different mock layers (wiremock, mockoon, msw, recorded fixtures, lambda-stub). Picking one would create friction with the rest. The framework's value is composition — `requires[kind=sensor]` + Spec A typed steps — not the mock library.

## Migration / breaking changes summary

Cumulative impact on a project using this plugin:

1. Every existing sensor file under `.harness/sensors/<id>.yaml` must be regenerated. Old `verification.golden_cases[]` is hard-rejected by the new schema.
2. Every existing sensor must declare `use_cases[]` (≥1) referencing real usecase ids under `.harness/usecases/**/`.
3. Projects without usecases yet must run `/detect-usecases` before `/create-sensor` produces useful output.
4. CI workflows that invoked `go test -tags=run_golden` must update to `/run-sensor <smoke-id>` calls.
5. `/heal-sensor` evidence prose changes; no action required from end users beyond observing different messages.

No rollout. Users on the plugin take the cut at upgrade time and regenerate.

## Implementation order (anticipated)

This spec hands off to `/writing-plans`; the plan will sequence tasks. The ordering below is the spec author's anticipation and is not binding. **The sequence matters** because the schema change invalidates the existing smoke sensors AND the existing `run-golden.go` + its tests AND the existing CI workflow. `.github/workflows/test.yml` triggers on push to `main` and on every pull request — so any step that leaves `go test` red would block all subsequent PRs. Everything that the schema flip invalidates must land in a single atomic commit.

1. **Delete** `skills/detect-sensors/scripts/run-golden.go` + `_test.go` only (not the workflow yet). After this commit the workflow's `run_golden` step compiles against the missing files and fails — so this step is itself part of the next atomic commit unless the workflow step is also patched.

   *Better:* fold step 1 into step 2 as a single atomic commit. Listed separately here for clarity of what changes within it.

2. **Single atomic commit covering everything the schema change touches:**
   - `schemas/sensor.yaml`: remove `verification`, add `use_cases`.
   - `lib/sensor/{shape,load,validate,persist}.go`: cascade per §Schema changes.
   - `skills/create-sensor/scripts/write-sensor.go`: support `use_cases[]`, add `use_cases_files_exist` check.
   - Delete `skills/detect-sensors/scripts/run-golden.go` + `_test.go`.
   - `.github/workflows/test.yml`: replace the `run_golden` step with `/run-sensor smoke-typed-pipeline` + `/run-sensor smoke-with-setup` invocations.
   - `.harness/usecases/framework/framework-smoke-{typed-pipeline,with-setup}.yaml`: create.
   - `.harness/sensors/smoke-{typed-pipeline,with-setup}.yaml`: regenerate with `use_cases[]`.

   After this commit, `go test ./...` passes, the smoke sensors work via `/run-sensor`, and CI is green.

3. `read-usecases.go` + tests.
4. `plan-sensors.go` + tests with all heuristic scenarios.
5. `skills/create-sensor/SKILL.md` rewrite (Phases 1, 1.5, 4, 5; orchestration prose for Phases 2, 3, 3.5).
6. `skills/detect-sensors/SKILL.md` rewrite (rip out `verification.golden_cases[]` authoring guidance; replace with `use_cases[]` flow).
7. Acceptance sensor `assert-create-sensor-multi-angle` + its framework usecase.
