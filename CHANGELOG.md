# Changelog

## [Unreleased] — Breaking: `.harness/` layout

All framework artifacts now live under `<project>/.harness/`:

- Sensor definitions: `<project>/.harness/sensors/<id>.json` (was `<project>/sensors/<id>.json`).
- Runtime state: `<project>/.harness/runtime/` (was `<project>/.runtime/sensors/`).
- Detected stack (new): `<project>/.harness/stack.json`.

To migrate an existing project:

```bash
mkdir -p .harness
git mv sensors .harness/sensors
[ -d .runtime ] && git mv .runtime .harness/runtime
# Update .gitignore: replace `/.runtime` with `/.harness/runtime`.
```

No fallback to the previous layout. `lib/registry.Discover` searches for `.harness/` only — projects with the old layout will see `registry root discovery failed: .harness/ marker not found walking up from ...`.

## 1.0.0 — 2026-05-11

### Breaking changes

`schemas/sensor.json` v2: `depends_on`, `requires.{tools,permissions,context,env}`, and `execution.prepare[]` are replaced by a single `requires[]` array of discriminated-union elements keyed by `kind`. Six kinds are accepted: `sensor`, `tool`, `env`, `context`, `permission`, `step`.

Refs: issue #10.

### Migration

For each project that ships sensor JSON files, run the migration tool from the harness-framework checkout:

```
go run ./scripts/migrate-requires.go --root sensors/
```

The tool is idempotent (already-v2 files are left untouched), fail-fast on ambiguity, and never dedupes step entries. It bumps each migrated sensor's `version` patch.

### v1 → v2 mapping

| v1 | v2 |
|---|---|
| `depends_on: ["id1", "id2"]` | `requires: [{kind:"sensor", id:"id1"}, {kind:"sensor", id:"id2"}]` |
| `requires.env: [{name, description, optional}]` | `requires: [{kind:"env", name, description, optional}]` |
| `requires.tools: ["docker"]` | `requires: [{kind:"tool", name:"docker"}]` |
| `requires.context: ["docs/"]` | `requires: [{kind:"context", path:"docs/"}]` |
| `requires.permissions: ["repo:read"]` | `requires: [{kind:"permission", scope:"repo:read"}]` |
| `execution.prepare: [{command, timeout_ms?, exit_code_map?}]` | `requires: [{kind:"step", command, timeout_ms?, exit_code_map?}]` |

### Runtime behaviour

Unchanged. Lifecycle phases, fail-fast semantics, cascade rules, teardown finally-semantics, exit code mapping, and Signal output all remain bit-identical. Only the schema shape and the consumer read path change. The Signal-side `metadata.lifecycle.prepare` key keeps its name (it is a phase name, not the schema field name).

### Validator

`lib/schema/validator.go` rejects v1 sensors with an actionable message naming the migration script. Unknown `requires[].kind` values produce a message listing the six valid kinds.
