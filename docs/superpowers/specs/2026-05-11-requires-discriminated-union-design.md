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

1. **Hard refactor**, not computed view nor hybrid. Schema v2 is the only accepted shape; v1 sensors are rejected by the validator with an actionable message pointing at the migration script.
2. **Scope**: `harness-framework` only. Migration script is reusable and committed permanently.
3. **Ordering semantics**: **fixed precedence by kind**, independent of array order. Array order matters only between items of the same kind (relevant for `kind=step` whose order is significant).
4. **Plugin version bump**: `plugin.json` to 1.0.0; schema bump to 2.0.0 (documented in CHANGELOG, no version field inside the schema body).

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
- `kind=step` duplicates allowed (the same shell command may legitimately run twice with different intent).
- Self-loop on `kind=sensor` (`requires[].id == this.id`) is detected by `lib/orchestrator/dag.go` and aborts with exit 1 (unchanged behaviour).

## 6. Execution semantics

Precedence is **fixed by kind**. Array order matters only between items of the same kind.

| Step | Source | Behaviour |
|---|---|---|
| 1 | `requires[kind=sensor]` | Orchestrator resolves topological closure (`lib/orchestrator/dag.go`); runs full lifecycle per dep; cascade on failure. |
| 2 | `requires[kind=env]` (non-optional) | Preflight check: env var must be set in runner environment. Missing var emits `verdict=error` Signal with remediation (same shape as today's `BuildMissingEnvSignal`). |
| 3 | `requires[kind=tool / permission / context]` | Declarative metadata. Not blocking. Consumed by heal classifier (`heal.FailedSensor.Tools/Context`) and tooling. |
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

| File | Change |
|---|---|
| `lib/orchestrator/dag.go::readDepsArray` | Replace `depends_on` read with `sensor.Project(s, "sensor")`; map `.id`. |
| `lib/sensor/env.go::CheckRequiredEnv` | Replace `requires.env` read with `sensor.Project(s, "env")`; same `MissingEnv` output. |
| `lib/orchestrator/lifecycle.go` (prepare steps) | Replace `execution.prepare[]` read with `sensor.Project(s, "step")`; preserve array order. |
| `lib/orchestrator/preflight.go` | Source paths change; structure unchanged. |
| `hooks/setup-failure-detector.go::loadFailedSensorView` | Rewrite `sensorRequiresView` to iterate `requires[]`; populate `heal.FailedSensor.{EnvNames, Tools, Context}` from items of the corresponding kinds. `heal.FailedSensor` shape unchanged downstream. |
| `lib/heal/apply.go`, `lib/heal/plan.go`, `lib/heal/rules/missing_env.go`, `lib/heal/rules/exit_code_127.go` | Update field reads to consume the new shape via `Project()`; `heal.FailedSensor` itself stays the same. |
| `skills/run-sensor/scripts/run-computational.go`, `run-inferential.go` | Replace `execution.prepare` consumption with `sensor.Project(s, "step")`. |
| `skills/heal-sensor/scripts/diagnose.go` | Same projection update. |
| `skills/start-sensor/scripts/start_test.go` and other test fixtures | Fixtures rewritten to v2. |

### What stays the same

- `heal.FailedSensor` struct — internal stable contract; only the *loader* changes.
- All Signal shapes — sensor output is untouched.
- All `execution.*` fields except the removed `prepare`.
- All other top-level fields (`triggers`, `cost`, `calibration`, `verification`, `self_correction`, `blind_spots`, `references`).

## 8. Validator: rejecting v1 with an actionable message

The current validator (`lib/schema/validator.go`) uses `santhosh-tekuri/jsonschema/v5`. A naked JSON Schema rejection would emit `additional property "depends_on" not allowed`, which is unhelpful.

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
- **1:1 conversion** per the §6 table. Preserves the order of `execution.prepare[]` items into the corresponding `kind=step` entries; appends them after sensor/env/tool/context/permission entries in a stable, kind-grouped order (sensor → tool → env → context → permission → step).
- **Version bump**: increments the sensor's `version` (semver patch). Justification: shape changed, behaviour did not.
- **Dry run**: `--dry-run` prints a unified diff to stdout, exit 0 if changes would apply.
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
- `CHANGELOG.md` — created if missing; documents the breaking change, links to issue #10, and points readers at the migration script.

## 11. Test coverage

| Test file | Purpose |
|---|---|
| `lib/sensor/project_test.go` (new) | Table-driven over `Project()` across all six kinds, empty `requires`, missing `requires`, malformed entries. |
| `lib/orchestrator/dag_test.go` | Fixtures rewritten to v2; identical assertions for cycle detection, topo sort, root-last invariant. |
| `lib/orchestrator/lifecycle_test.go` | Prepare steps sourced from `requires[kind=step]`; fail-fast behaviour preserved. |
| `lib/sensor/env_test.go` | Env vars sourced from `requires[kind=env]`. |
| `hooks/setup-failure-detector_test.go` | Fixture sensors rewritten; `loadFailedSensorView` produces same `heal.FailedSensor` as before. |
| `scripts/migrate-requires_test.go` (new) | Migration coverage per §9. |
| `lib/schema/validator_test.go` | New cases for v1 sniff: each legacy field triggers the actionable error. |
| `sensors/*` golden cases | Run via existing `unit-test-skills-*` sensors after migration. |

## 12. Delivery plan

Single branch; commits ordered for incremental review. CI must be green at every commit.

1. **`feat(schema)`**: introduce v2 `requires[]` in `schemas/sensor.json` while *temporarily* leaving v1 fields in place (`allOf` accepts both). This commit alone breaks no sensor.
2. **`feat(lib)`**: introduce `lib/sensor.Project()` and update `orchestrator`, `sensor/env`, `heal`, hook, and runner skills to read from `requires[]` first, falling back to v1 paths (transitional layer). Tests cover both shapes.
3. **`feat(scripts)`**: add `scripts/migrate-requires.go` + tests.
4. **`chore(sensors)`**: run the migrator on the 14 `sensors/*.json`; generated commit shows the rewrites.
5. **`feat(schema)`**: remove v1 fields from `schemas/sensor.json`; add the pre-schema sniff in `lib/schema/validator.go` with the actionable error. Remove the transitional fallback in `lib/sensor.Project()`.
6. **`chore(version)`**: bump `plugin.json` to 1.0.0; create/update `CHANGELOG.md`.

The final hard cut is commit 5+6. Up to commit 4, sensors continue to load under both shapes.

## 13. Risks and mitigations

| Risk | Mitigation |
|---|---|
| Downstream projects (`payment-card-api`) break when they pull the new plugin version. | Script is permanent and self-contained. CHANGELOG points at it. Plugin major bump (1.0.0) signals breaking change. |
| `Project()` silently swallowing malformed entries hides bugs. | Schema validation runs *before* any consumer calls `Project()`. By the time `Project()` is invoked, structure is guaranteed. |
| Migration script ambiguity rule (exit 1 on partial v2) is too strict in practice. | Acceptable: explicit failure with file path is more honest than guessing. Can be relaxed later if real cases demand. |
| Commit-ordered hard-cut leaves a window where two shapes coexist in `lib/`. | The transitional fallback in `lib/sensor.Project()` is opt-in and well-tested. Window is bounded by the PR lifetime. |

## 14. Acceptance criteria

- [ ] `schemas/sensor.json` v2 with `requires[]` discriminated union (six kinds); existing JSON Schema validator (`lib/schema/validator.go`) passes against all migrated sensors.
- [ ] `lib/sensor.Project()` introduced with table-driven tests across all six kinds + edge cases.
- [ ] `lib/orchestrator/dag.go`, `lib/sensor/env.go`, `lib/orchestrator/lifecycle.go`, `hooks/setup-failure-detector.go::loadFailedSensorView`, and heal rules updated to read via `Project()`. No remaining reads of `depends_on`, `requires.{tools,permissions,context,env}`, or `execution.prepare` in non-test, non-script code.
- [ ] `scripts/migrate-requires.go` performs idempotent 1:1 migration, bumps sensor version, supports `--dry-run` and `--root`, fails cleanly on ambiguity.
- [ ] All 14 sensors in `sensors/` migrated; golden cases pass for each.
- [ ] `lib/schema/validator.go` rejects v1 sensors with an actionable message naming the migration script.
- [ ] `plugin.json` bumped to 1.0.0; `CHANGELOG.md` documents the breaking change.
- [ ] CI green at every commit on the branch.

## 15. Open questions

None at this point. All decisions captured in §4 and §6.
