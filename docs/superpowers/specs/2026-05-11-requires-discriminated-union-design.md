# Spec — Unify dependency-shaped attributes under a single `requires[]` discriminated union

**Status:** draft (awaiting spec review and user approval)
**Issue:** https://github.com/iurykrieger/harness-framework/issues/10
**Author:** iurykrieger@stone
**Date:** 2026-05-11

## 1. Context

`schemas/sensor.json` currently spreads precondition-shaped data across five heterogeneous fields:

| Field | Type | Meaning |
|---|---|---|
| `depends_on` | `string[]` (sensor IDs) | Sensors that must run and pass before this one |
| `requires.tools` | `string[]` | Binaries/CLIs invoked |
| `requires.permissions` | `string[]` | Least-privilege scopes |
| `requires.context` | `string[]` | Repo paths read |
| `requires.env` | `{name, description?, optional?}[]` | Env vars forwarded by the runner |
| `execution.prepare` | `LifecycleStep[]` | Silent shell commands run before `execution.command` |

Conceptually all six are preconditions; only **how** the precondition is satisfied varies. The dispersion forces every consumer to know its own field path:

- `lib/orchestrator/dag.go::readDepsArray` reads `depends_on`.
- `lib/sensor/env.go::CheckRequiredEnv` reads `requires.env`.
- `hooks/setup-failure-detector.go::loadFailedSensorView` reads `requires.{env,tools,context}` (ignores the others).
- `lib/orchestrator/lifecycle.go` reads `execution.prepare`.

Adding a new precondition class (e.g. `requires.docker_service`) means extending the schema in another place and teaching every tool to look at another field. The overlap between `execution.prepare[]` (inline) and setup sensors referenced via `depends_on` is the most painful instance — the `detect-sensors` SKILL.md needs a full prose table to explain when to use which.

## 2. Goal

Collapse all six fields into a single `requires[]` array of discriminated-union elements keyed by `kind`. The runner's behaviour stays bit-identical; only the schema shape and the path consumers walk to read it change.

## 3. Non-goals

- No change to runtime behaviour (lifecycle phases, fail-fast semantics, cascade rules, teardown finally-semantics, exit code mapping).
- No change to the Signal contract (`schemas/signal.json`).
- No migration of sensors outside `harness-framework`. The migration script is reusable; downstream projects (e.g. `payment-card-api`) run it in their own PRs.
- No new precondition kinds beyond the six the issue lists. Future kinds (`port`, `service`, `secret-vault-path`) are explicitly out of scope for this spec.
- `execution.teardown[]` stays where it is — it is post-command lifecycle, not a precondition.

## 4. Decisions

Captured during brainstorming on 2026-05-11.

1. **Hard refactor** as the *final shipping state* of this PR. Issue #10's "Notas" section proposes an alternative — a computed `effective_requires[]` view that keeps the v1 fields and projects them at read time. We reject that alternative: it preserves the scattered shape as the source of truth and only papers over it, leaving every future precondition class still requiring a schema extension. Schema v2 is the only accepted shape once the PR lands; v1 sensors are rejected by the validator with an actionable message pointing at the migration script. Intermediate commits within the PR MAY transiently support both shapes for CI hygiene (§12), but the final commit closes the v1 path.
2. **Scope**: `harness-framework` only. Migration script is reusable and committed permanently.
3. **Ordering semantics**: **fixed precedence by kind**, independent of array order. Array order matters only between items of the same kind (relevant for `kind=step` whose order is significant).
4. **Plugin version bump**: `plugin.json` to 1.0.0; schema bump to 2.0.0 (documented in CHANGELOG, no version field inside the schema body).
5. **Lifecycle metadata key stays `prepare`**. The Signal-side `metadata.lifecycle.prepare` key (read by `hooks/setup-failure-detector.go::signalFromMap` and heal rules) is a phase name, not the schema field name. Renaming it to `step` would force changes across the heal classifier, the hook, and the Signal producers with no semantic gain. Keep `prepare` as the phase name; only the schema source field changes.

## 5. Schema v2 shape

`schemas/sensor.json` gains `$defs/Requirement` as a `oneOf` over six per-kind sub-schemas. Each kind has `additionalProperties: false` and its own required fields.

```jsonc
"requires": {
  "type": "array",
  "default": [],
  "items": { "$ref": "#/$defs/Requirement" }
}
```

```jsonc
"$defs": {
  "Requirement": {
    "oneOf": [
      { "$ref": "#/$defs/RequireSensor" },
      { "$ref": "#/$defs/RequireTool" },
      { "$ref": "#/$defs/RequireEnv" },
      { "$ref": "#/$defs/RequireContext" },
      { "$ref": "#/$defs/RequirePermission" },
      { "$ref": "#/$defs/RequireStep" }
    ]
  },
  "RequireSensor": {
    "type": "object",
    "additionalProperties": false,
    "required": ["kind", "id"],
    "properties": {
      "kind": { "const": "sensor" },
      "id":   { "type": "string", "pattern": "^[a-z][a-z0-9-]*$" }
    }
  },
  "RequireTool": {
    "type": "object",
    "additionalProperties": false,
    "required": ["kind", "name"],
    "properties": {
      "kind": { "const": "tool" },
      "name": { "type": "string" }
    }
  },
  "RequireEnv": {
    "type": "object",
    "additionalProperties": false,
    "required": ["kind", "name"],
    "properties": {
      "kind":        { "const": "env" },
      "name":        { "type": "string", "pattern": "^[A-Z_][A-Z0-9_]*$" },
      "description": { "type": "string" },
      "optional":    { "type": "boolean", "default": false }
    }
  },
  "RequireContext": {
    "type": "object",
    "additionalProperties": false,
    "required": ["kind", "path"],
    "properties": {
      "kind": { "const": "context" },
      "path": { "type": "string" }
    }
  },
  "RequirePermission": {
    "type": "object",
    "additionalProperties": false,
    "required": ["kind", "scope"],
    "properties": {
      "kind":  { "const": "permission" },
      "scope": { "type": "string" }
    }
  },
  "RequireStep": {
    "type": "object",
    "additionalProperties": false,
    "required": ["kind", "command"],
    "properties": {
      "kind":          { "const": "step" },
      "command":       { "type": "string", "description": "Shell invocation (sh -c). MUST be idempotent." },
      "timeout_ms":    { "type": "integer", "minimum": 1 },
      "exit_code_map": {
        "type": "array",
        "minItems": 1,
        "items": { "$ref": "#/$defs/ExitCodeMapEntry" }
      }
    }
  }
}
```

### Removed top-level fields

- `depends_on`
- `requires` (object form)
- `execution.prepare`

`execution.teardown[]` stays as is.

### Uniqueness rules

Validator-level checks added on top of JSON Schema:

- `(kind=sensor, id)` unique within `requires[]`.
- `(kind=tool, name)` unique.
- `(kind=env, name)` unique.
- `(kind=context, path)` unique.
- `(kind=permission, scope)` unique.
- `kind=step` duplicates allowed (the same shell command may legitimately run twice with different intent, or with different `timeout_ms`). The migration script (§9) MUST NOT dedupe step entries.
- Self-loop on `kind=sensor` (`requires[].id == this.id`) is detected by `lib/orchestrator/dag.go` and aborts with exit 1 (unchanged behaviour).

### Empty and missing `requires`

`requires` is **optional**. Omitting it is equivalent to `requires: []`: no preconditions, no env preflight, no prepare steps. The validator MUST NOT add `requires` to the top-level `required` list — sensors with truly no preconditions stay terse.

### Unknown `kind` values

JSON Schema's `oneOf` over six per-kind sub-schemas with `kind` as a `const` discriminator works under Draft 2020-12 and `santhosh-tekuri/jsonschema/v5`. The branches are mutually exclusive because each has `additionalProperties: false` and a distinct `const` on `kind`. The cost: failure messages on an unknown kind are opaque (`oneOf failed: 0 of 6 schemas matched`), the same readability problem §8 fixes for v1 fields. We extend the pre-schema sniff in §8 to also detect `requires[].kind` values not in the closed set and emit:

```
sensor <id> requires[<i>] has unknown kind <kind>. Valid kinds: sensor, tool, env, context, permission, step.
```

This keeps schema authoring forgiving without weakening the `oneOf` enforcement.

## 6. Execution semantics

Precedence is **fixed by kind**. Array order matters only between items of the same kind.

| Step | Source | Behaviour |
|---|---|---|
| 1 | `requires[kind=sensor]` | Orchestrator resolves topological closure (`lib/orchestrator/dag.go`); runs full lifecycle per dep; cascade on failure. |
| 2 | `requires[kind=env]` (non-optional) | Preflight check: env var must be set in runner environment. Missing var emits `verdict=error` Signal with remediation (same shape as today's `BuildMissingEnvSignal`). |
| 3 | `requires[kind=tool / permission / context]` | Declarative metadata. Not blocking. `tool` and `context` are consumed by the heal classifier (`heal.FailedSensor.Tools/Context`). `kind=permission` is parsed and schema-validated but has no runtime consumer today; it documents intent and is a forward-looking hook for future least-privilege tooling. |
| 4 | `requires[kind=step]` | Fail-fast in array order. Per-step result folded into `metadata.lifecycle.prepare[i]`. First non-pass step aborts the array and skips `execution.command`. |
| 5 | `execution.command` | Runs only if 1–4 passed. |
| 6 | `execution.teardown[]` | Unchanged. Finally-semantics: runs on prepare failure, command failure, and command timeout. |

### 1:1 mapping v1 → v2

| v1 | v2 |
|---|---|
| `depends_on: ["id1", "id2"]` | `requires: [{kind:"sensor", id:"id1"}, {kind:"sensor", id:"id2"}]` |
| `requires.env: [{name, description, optional}]` | `requires: [{kind:"env", name, description, optional}]` |
| `requires.tools: ["docker"]` | `requires: [{kind:"tool", name:"docker"}]` |
| `requires.context: ["docs/"]` | `requires: [{kind:"context", path:"docs/"}]` |
| `requires.permissions: ["repo:read"]` | `requires: [{kind:"permission", scope:"repo:read"}]` |
| `execution.prepare: [{command, timeout_ms?, exit_code_map?}]` | `requires: [{kind:"step", command, timeout_ms?, exit_code_map?}]` |

The migration script (§9) applies this mapping mechanically.

## 7. Code impact

Each consumer is rewritten to project `requires[]` by kind via a single new helper.

### New helper

`lib/sensor/project.go`:

```go
// Project returns all elements of requires[] whose `kind` equals the given
// kind, preserving array order. Returns nil when requires is absent, empty,
// or malformed. Schema validation is the caller's responsibility — Project
// silently skips entries that are not JSON objects or whose kind field is
// missing/non-string.
func Project(sensor map[string]interface{}, kind string) []map[string]interface{}
```

Used by every consumer below. Single source of read for `requires[]`.

### Per-file changes

Loaders (read sensor JSON directly):

| File | Change |
|---|---|
| `lib/orchestrator/dag.go::readDepsArray` | Replace `depends_on` read with `sensor.Project(s, "sensor")`; map `.id`. |
| `lib/orchestrator/run.go` | Update package/function doc comments that reference `depends_on` to name `requires[kind=sensor]`. No code change. |
| `lib/orchestrator/lifecycle.go` (prepare phase) | Replace `execution.prepare[]` read with `sensor.Project(s, "step")`; preserve array order. Lifecycle phase NAME stays `prepare` (per §4 decision 5); only the source field changes. |
| `lib/orchestrator/preflight.go` | Source paths change; structure unchanged. |
| `lib/sensor/env.go::CheckRequiredEnv` | Replace `requires.env` read with `sensor.Project(s, "env")`; same `MissingEnv` output. |
| `hooks/setup-failure-detector.go::loadFailedSensorView` | Rewrite `sensorRequiresView` to iterate `requires[]`; populate `heal.FailedSensor.{EnvNames, Tools, Context}` from items of the corresponding kinds. `heal.FailedSensor` shape unchanged downstream. `metadata.lifecycle.prepare` key (read by `signalFromMap`) keeps its name. |
| `lib/heal/apply.go`, `lib/heal/plan.go` | Update sensor-JSON reads to consume the new shape via `Project()`. |
| `skills/run-sensor/scripts/run-computational.go`, `run-inferential.go` | Replace `execution.prepare` consumption with `sensor.Project(s, "step")`. |
| `skills/start-sensor/scripts/start.go` | Reads `execution.prepare` indirectly via `orchestrator.RunPreparePhase`; no direct change there, but the lifecycle metadata it emits (`metadata.lifecycle.prepare`, `metadata.cause = "prepare_failed"`) stays untouched per §4 decision 5. |
| `skills/heal-sensor/scripts/diagnose.go` | Same projection update. |
| `skills/detect-sensors/SKILL.md` | Update user-facing prose (sections that name `depends_on`, `requires.env`, `requires.tools`, `prepare[]`) to use the v2 vocabulary. Docs only; no code. |
| Test fixtures across the repo (`skills/start-sensor/scripts/start_test.go`, `lib/orchestrator/run_test.go`, `lib/orchestrator/preflight_test.go`, `lib/orchestrator/live_deps_test.go`, `hooks/setup-failure-detector_test.go`, `lib/heal/apply_test.go`, `lib/heal/plan_test.go`) | Fixtures rewritten to v2. |

Consumers that do NOT need rewiring (their input contract is unchanged):

- `lib/heal/rules/*.go` (`missing_env.go`, `exit_code_127.go`, `prepare_template_copy.go`, `stderr_pattern.go`, `heal_hint.go`) — they read `heal.FailedSensor`, not sensor JSON. Their interface is unchanged.

### What stays the same

- `heal.FailedSensor` struct — internal stable contract; only the *loader* changes.
- All Signal shapes — sensor output is untouched.
- `metadata.lifecycle.prepare` Signal key — phase name, not field name (§4 decision 5).
- All `execution.*` fields except the removed `prepare`.
- All other top-level fields (`triggers`, `cost`, `calibration`, `verification`, `self_correction`, `blind_spots`, `references`).

## 8. Validator: rejecting v1 with an actionable message

The current validator (`lib/schema/validator.go`) uses `santhosh-tekuri/jsonschema/v5`. A naked JSON Schema rejection would emit `additional property "depends_on" not allowed`, which is unhelpful.

The pre-schema sniff persists **even after step 5 of §12** removes the v1 properties from `schemas/sensor.json`. Once the v1 fields are out of the schema, `additionalProperties: false` would already reject them, but with the opaque message above. The sniff exists to translate that opaque message into the actionable one below.

Add a **pre-schema sniff** in `lib/schema/validator.go`:

- Before running JSON Schema validation, inspect the top-level JSON. If any of `depends_on`, top-level `requires` as object (not array), or `execution.prepare` is present, short-circuit with:

```
sensor <id> uses v1 schema fields (depends_on, requires.tools, execution.prepare, ...).
Run `go run ./scripts/migrate-requires.go <path>` to upgrade to v2.
```

- Sniff function `detectLegacyShape(raw []byte) (legacyFields []string, ok bool)` is testable in isolation.

## 9. Migration script

`scripts/migrate-requires.go` — Go, **permanent** in the repo (reusable across projects).

### CLI

```
migrate-requires <sensor.json>...
migrate-requires --root sensors/
migrate-requires --dry-run [...]
```

### Behaviour

- **Idempotent**: if `requires` is already an array (v2), the file is left untouched and exits 0.
- **1:1 conversion** per the §6 table. Preserves the order of `execution.prepare[]` items into the corresponding `kind=step` entries; appends them after sensor/env/tool/context/permission entries in a stable, kind-grouped order (sensor → tool → env → context → permission → step). Step entries are NEVER deduplicated — two identical `prepare[]` commands stay as two `kind=step` entries.
- **Version bump**: increments the sensor's `version` (semver patch). Justification: shape changed, behaviour did not. (All 13 sensors get a synchronous patch bump in step 4 of §12; see §13 risks.)
- **Dry run**: `--dry-run` prints a `diff -u`-style unified diff to stdout, exit 0 if changes would apply.
- **Ambiguity**: if `requires` is already an array but contains a malformed item (missing `kind`, unknown `kind`, wrong type), exit 1 with the offending file path and the malformed entry. Does not attempt to "fix" partial migrations.
- **Recursive mode**: `--root <dir>` walks the directory, applies to every `*.json` whose top-level JSON looks like a sensor (has `id`, `kind`, `type`, `execution`).
- Standard exit codes: `0` success, `1` ambiguity / validation failure, `2` usage / I/O error.

### Tests

`scripts/migrate-requires_test.go` — table-driven, covering:

- Full v1 sensor with all six legacy fields → expected v2 output.
- Sensor already v2 → file unchanged.
- Partially-migrated sensor (some legacy fields, some new array) → exit 1 with diagnostic.
- Empty `requires` / missing `requires` / empty `depends_on` → v2 with `requires: []`.
- Dry-run mode prints diff, leaves file intact.
- Recursive mode picks up sensors and skips non-sensor JSONs.

## 10. Versioning and changelog

- `schemas/sensor.json` — conceptual bump to **2.0.0**. Documented in `CHANGELOG.md`.
- `plugin.json` — bump to **1.0.0**.
- Migrated sensors — version field bumped to next patch (e.g. `1.0.0` → `1.0.1`) by the script.
- `CHANGELOG.md` — created if missing; documents the breaking change, links to issue #10, includes the exact migration script command line (`go run ./scripts/migrate-requires.go --root sensors/`), and explains the v1 → v2 mapping at a glance.

## 11. Test coverage

| Test file | Purpose |
|---|---|
| `lib/sensor/project_test.go` (new) | Table-driven over `Project()` across all six kinds, empty `requires`, missing `requires`, malformed entries. |
| `lib/orchestrator/dag_test.go` | Fixtures rewritten to v2; identical assertions for cycle detection, topo sort, root-last invariant. |
| `lib/orchestrator/run_test.go` | Fixtures build `requires[kind=sensor]` instead of `depends_on`. |
| `lib/orchestrator/preflight_test.go` | Same fixture rewrite. |
| `lib/orchestrator/live_deps_test.go` | Same fixture rewrite. |
| `lib/orchestrator/lifecycle_test.go` | Prepare steps sourced from `requires[kind=step]`; fail-fast behaviour preserved. |
| `lib/sensor/env_test.go` | Env vars sourced from `requires[kind=env]`. |
| `hooks/setup-failure-detector_test.go` | Fixture sensors rewritten; `loadFailedSensorView` produces same `heal.FailedSensor` as before. |
| `scripts/migrate-requires_test.go` (new) | Migration coverage per §9. |
| `lib/schema/validator_test.go` | New cases for v1 sniff: each legacy field triggers the actionable error. |
| `sensors/*` golden cases | Run via existing `unit-test-skills-*` sensors after migration. |

## 12. Delivery plan

Single branch; commits ordered for incremental review. CI must be green at every commit.

1. **`feat(schema)`**: introduce v2 `requires[]` in `schemas/sensor.json` alongside the v1 `requires` object — both shapes accepted **for the duration of this PR only**. Concretely: the top-level `requires` property becomes `{ "oneOf": [ <v1 object schema>, <v2 array schema> ] }`. The v1 `depends_on` and `execution.prepare` properties stay declared. The schema's `additionalProperties: false` continues to hold. Test fixtures still pass; no sensor breaks.
2. **`feat(lib)`**: introduce `lib/sensor.Project()` with a **transitional fallback**: at the top of `Project()`, before any per-kind dispatch, a single `switch` on the runtime type of `requires` synthesizes the v2 array internally from `depends_on`, `requires.{env,tools,context,permissions}`, and `execution.prepare[]` whenever `requires` is an object (v1) or absent. From that point on, the function operates only on the synthesized v2 array — every downstream consumer sees the v2 shape. Update `orchestrator`, `sensor/env`, `heal`, hook, and runner skills to call `Project()`. Tests cover both shapes. The fallback is a single dispatch point with no scattered branching; it is removed in step 5 and has no maintenance cost beyond this PR. This intentionally diverges from §4 decision 1 *within the PR* and reconverges in step 5.
3. **`feat(scripts)`**: add `scripts/migrate-requires.go` + tests.
4. **`chore(sensors)`**: run the migrator on the 13 `sensors/*.json`; generated commit shows the rewrites.
5. **`feat(schema)`**: drop the v1 branch from the `oneOf` (schema now accepts only the v2 array); remove `depends_on` and `execution.prepare` declarations; add the pre-schema sniff in `lib/schema/validator.go` with the actionable error for both v1 fields and unknown `requires[].kind` values; remove the transitional fallback in `lib/sensor.Project()`.
6. **`chore(version)`**: bump `plugin.json` to 1.0.0; create/update `CHANGELOG.md`.

The final hard cut is commit 5+6. Up to commit 4, sensors continue to load under both shapes. From commit 5 onward, the v1 shape is rejected and the final state matches §4 decision 1.

## 13. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Downstream projects (`payment-card-api`) break when they pull the new plugin version. | Script is permanent and self-contained. CHANGELOG points at it. Plugin major bump (1.0.0) signals breaking change. |
| All 13 migrated sensors get a synchronous patch version bump in one commit. Downstream version-pinned consumers must rebase. | Acceptable. CHANGELOG documents the bump; the patch level signals shape-only change. If downstream pins prove brittle, future refactors can opt out of the version bump. |
| `Project()` silently swallowing malformed entries hides bugs. | Schema validation runs *before* any consumer calls `Project()`. By the time `Project()` is invoked, structure is guaranteed. |
| Migration script ambiguity rule (exit 1 on partial v2) is too strict in practice. | Acceptable: explicit failure with file path is more honest than guessing. Can be relaxed later if real cases demand. |
| Commit-ordered hard-cut leaves a window where two shapes coexist in `lib/` (commits 1–4). | The transitional fallback in `lib/sensor.Project()` is opt-in (single dispatch on `requires` type) and well-tested. Window is bounded by the PR lifetime; commit 5 removes the fallback. |

## 14. Acceptance criteria

- [ ] `schemas/sensor.json` v2 with `requires[]` discriminated union (six kinds); existing JSON Schema validator (`lib/schema/validator.go`) passes against all migrated sensors.
- [ ] `lib/sensor.Project()` introduced with table-driven tests across all six kinds + edge cases.
- [ ] `lib/orchestrator/dag.go`, `lib/sensor/env.go`, `lib/orchestrator/lifecycle.go`, `hooks/setup-failure-detector.go::loadFailedSensorView`, and heal rules updated to read via `Project()`. No remaining reads of `depends_on`, `requires.{tools,permissions,context,env}`, or `execution.prepare` in non-test, non-script code.
- [ ] `scripts/migrate-requires.go` performs idempotent 1:1 migration, bumps sensor version, supports `--dry-run` and `--root`, fails cleanly on ambiguity, never dedupes step entries.
- [ ] All 13 sensors in `sensors/` migrated; golden cases pass for each.
- [ ] `lib/schema/validator.go` rejects v1 sensors with an actionable message naming the migration script, and rejects unknown `requires[].kind` values with a message naming the valid kinds.
- [ ] `plugin.json` bumped to 1.0.0; `CHANGELOG.md` includes a v2 entry naming the migration script command line (`go run ./scripts/migrate-requires.go --root sensors/`) and the v1 → v2 mapping.
- [ ] `skills/detect-sensors/SKILL.md` prose updated to v2 vocabulary.
- [ ] CI green at every commit on the branch.

## 15. Open questions

One non-blocker, recorded for honesty rather than for resolution before implementation:

- **`kind=context` path existence**. Today the schema does not enforce that `requires.context[]` paths exist on disk at validation time; the heal classifier checks existence on demand. The spec inherits that behaviour (`kind=context` is declarative). If a future use case demands stricter validation, we add it as a follow-up — not in this PR.
