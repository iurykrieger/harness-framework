# Layers of Confidence — Design Spec

**Status:** Approved (brainstorming complete; ready for writing-plans)
**Date:** 2026-05-18
**Branch:** `layers-of-confidence`
**Replaces:** `/create-sensor` (singular) → `/create-sensors` (plural) + new `/validate-usecase`

## Context

The current `/create-sensor` skill (in `skills/create-sensor/`) takes one or more usecase ids — or a journey id, file path, or free-text requirement — and produces 1..N sensors that validate the implied behavior. Its planner (`lib/planning/`) groups usecases by `(journey_id, trigger.shape, shared tags, evidence proximity)` and emits ONE sensor per group. Variability lives on the **usecase axis** (a sensor covers N usecases that share a shape) and on the **kind/type/output axes** (assertion vs. observation vs. setup; computational vs. inferential; single vs. stream).

What is missing is variability on the **validation angle axis**. A single usecase is validated by only one sensor — even when the stack supports validating it from several independent angles (unit tests, e2e replay, DB inspection, log assertions, code-quality checks, security scans, etc.). When that single sensor passes, the agent's confidence that the implementation is correct rests entirely on one observation, with no cross-checking.

This design adds the missing axis. Each usecase is validated by **multiple narrow sensors composed across multiple layers**, where a layer is a closed-enum validation lens. The number of applicable layers (given the stack) is the usecase's **confidence ceiling**; the number actually generated is its **coverage**; the number passing at runtime is its **realized confidence**. More layers passing ⇒ higher confidence.

## Goals

1. **Replace `/create-sensor` (singular) with `/create-sensors` (plural)** — the new skill emits a per-usecase bundle of sensors instead of a per-group single sensor. No inter-usecase grouping.
2. **Introduce a `layer` field on `sensor.yaml`** that names the validation lens.
3. **Materialize each usecase bundle in its own folder** at `.harness/sensors/<usecase-id>/`.
4. **Co-locate narrow (single-objective) and composite (multi-objective) sensors** in the same folder; composites use `SensorStep` (`type: sensor`, `ref:`) to invoke narrows.
5. **Treat root-level sensors as platform primitives** (core capabilities): setup, build, lint, type-check, generic run/observe. These are produced by `/detect-sensors` and reused by per-usecase sensors via `requires[kind=sensor]`.
6. **Provide a new skill `/validate-usecase <usecase-id>`** that orchestrates the bundle's layer entrypoints and emits a confidence report (ceiling, coverage, realized).
7. **Remove `blind_spots` from the sensor schema.** Gap remediation is operational (iterate a sensor's version or generate a new sensor in another layer), not declarative.

## Non-goals

- Migration scripts. The framework is in active development; sensors that do not conform to the new schema are deleted manually and regenerated.
- Backwards compatibility for the old `/create-sensor` skill or the 62 sensors persisted by PR #70.
- Parameterized core sensors via `SensorStep.with:`. Per-usecase sensors implement their own observation/assertion logic; they reuse core sensors only as setup preconditions.
- Confidence weighting across layers. Every layer counts equally in `realized`. Weighting is future work.

## Concepts

### Sensor classes (per usecase folder)

| Class | Objective | Mechanics |
|---|---|---|
| **Narrow** | A single validation/observation objective | Any number of `shell`/`http`/`assert` steps in service of that one objective. No `SensorStep` required (but allowed for inline helpers). |
| **Composite** | Multiple objectives chained | Uses `SensorStep` (`type: sensor`, `ref:`) to delegate each objective to a narrow sensor; may include inline `assert` steps for cross-cutting concerns. Always carries the `layer:` of the camada it represents. |

The distinction is **scope of objective**, not step count. A narrow `observe-db-create-user` may have steps `pg_isready → psql query → assert row exists` and remain narrow because all three serve "row was persisted".

### Sensor tiers (core vs per-usecase)

| Tier | Location | Examples | Owner |
|---|---|---|---|
| **Core (platform primitives)** | `.harness/sensors/<id>.yaml` (root) | `run-project`, `setup-postgres`, `install-deps`, `seed-db`, `build`, `lint`, `type-check`, `run-all-tests`, `check-server-startup` | Produced by `/detect-sensors` |
| **Per-usecase (product validations)** | `.harness/sensors/<usecase-id>/<sensor-id>.yaml` | `observe-db-create-user`, `e2e-happy-create-user`, `http-replay-happy-create-user`, … | Produced by `/create-sensors` |

Per-usecase sensors invoke core sensors **only via `requires[kind=sensor]`** (transparent setup before the sensor runs). Observation and assertion logic is implemented in the per-usecase sensor itself — never parameterized into a core sensor via `SensorStep.with:`. This keeps core sensors self-contained and per-usecase sensors autonomous in their observation/assertion logic.

### Layer

A **Layer** is a validation lens: the angle from which the usecase is observed. Layers are orthogonal to `kind`, `type`, and `output`. The enum is closed (defined in `lib/planning/layer/` as Go constants); adding a new layer is a plugin-version event.

### Layer matrix

The 18 layers, grouped by family, each with their stack pre-condition:

| Family | Layer | Stack pre-condition |
|---|---|---|
| **Test execution** | `unit-test` | role=test-runner |
|  | `integration-test` | role=test-runner + (role=db-client OR role=queue-* OR role=external-integration) |
|  | `contract-test` | role=http-server OR role=rpc + OpenAPI/proto contract in repo |
|  | `e2e-happy` | journey has entry_points + sensor with id=run-project exists |
|  | `e2e-error` | same as `e2e-happy` |
| **Runtime observation** | `db-state` | role=db-client |
|  | `log-trace` | ≥1 log_shape in stack |
|  | `metric` | role=metrics |
|  | `event-emission` | role=queue-producer |
|  | `event-consumption` | role=queue-consumer |
| **Performance/resilience** | `performance` | archetype http-api OR queue-* |
|  | `resilience` | fault-injection tooling component on stack |
| **Static quality (inferential)** | `code-quality` | always applicable |
|  | `architecture` | always applicable |
|  | `security` | always applicable |
|  | `dependency-health` | always applicable |
| **Schema/contract** | `db-schema` | role=db-client + migrations folder |
|  | `accessibility` | archetype http-spa OR http-ssr |

### Layer recipe (interface)

Each layer is a Go file under `lib/planning/layer/<name>.go` implementing:

```go
type LayerRecipe interface {
    // Returns the canonical layer name (matches the enum value in sensor.yaml).
    Name() Layer

    // True when this stack + usecase + existing catalog supports the layer.
    // The string is the reason when false (surfaced in /create-sensors report).
    Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string)

    // Returns 1..N draft sensors that together validate the usecase
    // through this layer's lens. Drafts are persisted in the per-usecase
    // folder. Each draft carries layer=Name().
    Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft
}

type Draft struct {
    SensorID  string
    Layer     Layer
    Kind      string         // assertion | observation
    Type      string         // computational | inferential
    Output    string         // single | stream
    Requires  []sensor.Requirement
    Execution sensor.Execution
    // ... mirrors the sensor schema's required fields
}
```

A layer may emit a **single narrow draft** (e.g., `db-state` emits one `observe-db-<usecase>.yaml`) or **multiple drafts including a composite** (e.g., `e2e-happy` emits `http-replay-happy-<usecase>.yaml` plus `e2e-happy-<usecase>.yaml` composite). The runtime topologically sorts drafts so leaves persist before the composites that reference them.

### Confidence model

Three integer counts per usecase, all derived (no authorial fields):

| Count | Formula | When it changes |
|---|---|---|
| `ceiling` | `count(layer ∈ enum if LayerRecipe(layer).Applicable(stack, uc, catalog))` | Stack changes; layer enum changes |
| `coverage` | `count(unique sensor.layer values in .harness/sensors/<usecase-id>/)` | `/create-sensors` runs; manual edits |
| `realized` | `count(layer entrypoints with verdict=pass at latest /validate-usecase run)` | Each `/validate-usecase` invocation |

Three derived ratios:

- `completeness = coverage / ceiling` — how much of the stack's potential is generated
- `pass_rate = realized / coverage` — health of what is generated
- `confidence = realized / ceiling` — the headline score

`realized` is always fresh (re-executes sensors on each `/validate-usecase` invocation). The framework does not consult cached signals from `signals.log` when computing realized confidence — this removes TTL ambiguity.

### Confidence report (cache)

`/validate-usecase` optionally persists its output at `.harness/confidence/<journey-id>/<usecase-id>.yaml`:

```yaml
usecase_id: create-user
journey_id: users
computed_at: 2026-05-18T14:30:00Z
ceiling:
  value: 12
  applicable: [unit-test, integration-test, e2e-happy, e2e-error, db-state,
               log-trace, code-quality, architecture, security,
               dependency-health, db-schema, performance]
  not_applicable:
    - { layer: contract-test, reason: "no OpenAPI/proto contract in stack" }
    - { layer: metric, reason: "no role=metrics component" }
    - { layer: accessibility, reason: "archetype not http-spa/http-ssr" }
    - { layer: event-emission, reason: "no role=queue-producer" }
    - { layer: event-consumption, reason: "no role=queue-consumer" }
    - { layer: resilience, reason: "no fault-injection tooling component" }
coverage:
  value: 10
  generated: [unit-test, e2e-happy, e2e-error, db-state, log-trace,
              code-quality, architecture, security, dependency-health, db-schema]
  missing: [integration-test, performance]
realized:
  value: 8
  layer_verdicts:
    - { layer: unit-test, verdict: pass, sensor_id: unit-test-create-user, finished_at: 2026-05-18T14:25:00Z }
    - { layer: db-state, verdict: fail, sensor_id: observe-db-create-user, finished_at: 2026-05-18T14:29:00Z }
    # ... one entry per layer entrypoint executed in this invocation
  untested: []   # generated but skipped this run
ratios:
  completeness: 0.83
  pass_rate:    0.80
  confidence:   0.67
```

The cache is regenerable; deleting it loses no data.

## Schema changes (`schemas/sensor.yaml`)

```yaml
# REMOVE this property entirely:
# blind_spots:
#   description: Explicit limits — bug classes, inputs that produce unstable verdicts,
#     sensor interactions that mask issues. Required honesty for inferential sensors.
#   items: { type: string }
#   type: array

# ADD this property:
layer:
  description: |
    Validation angle this sensor takes. /create-sensors emits every per-usecase
    sensor with this field set. /detect-sensors emits root-level core sensors
    with this field omitted. The schema marks it optional; the producing skills
    enforce presence/absence based on file location.
  enum:
    - unit-test
    - integration-test
    - contract-test
    - e2e-happy
    - e2e-error
    - db-state
    - log-trace
    - metric
    - event-emission
    - event-consumption
    - performance
    - resilience
    - code-quality
    - architecture
    - security
    - dependency-health
    - db-schema
    - accessibility
  type: string
```

The enum value list mirrors the Go constants in `lib/planning/layer/layer.go` (single source of truth at the code level; mirrored in the schema for declarative discovery).

## Library structure

```
lib/planning/
├── layer/
│   ├── layer.go              # type Layer, const block, LayerRecipe interface,
│   │                         # registry, topological sort helper, Draft type
│   ├── applicability.go      # shared helpers: hasRole(stack, role),
│   │                         # hasLogShape(stack), hasCoreSensor(catalog, id),
│   │                         # hasArchetype(stack, …)
│   ├── unit_test.go          # 18 files, one per layer; each implements LayerRecipe
│   ├── integration_test.go
│   ├── contract_test.go
│   ├── e2e_happy.go
│   ├── e2e_error.go
│   ├── db_state.go
│   ├── log_trace.go
│   ├── metric.go
│   ├── event_emission.go
│   ├── event_consumption.go
│   ├── performance.go
│   ├── resilience.go
│   ├── code_quality.go
│   ├── architecture.go
│   ├── security.go
│   ├── dependency_health.go
│   ├── db_schema.go
│   ├── accessibility.go
│   ├── layer_test.go         # registry + ordering tests
│   ├── applicability_test.go # shared helpers tests
│   └── <name>_test.go        # 18 per-layer test files
└── (deleted: group.go, infer.go, shape.go, plan.go)
```

Rule 9 ("organize by context, not by type"): each layer is its own file with its own test sibling; shared helpers in `applicability.go`; registry/interface in `layer.go`.

## Skill: `/create-sensors`

**Invocation**: `/create-sensors <usecase-id | journey-id | path | "<free-text>">`

**Phases**:

```
Phase 1  Resolve input → list of usecase ids (reuse read-usecases.go)
Phase 2  Load context: stack.yaml + catalog (root + per-usecase folders,
         recursive walk)
Phase 3  Per usecase: apply layer matrix → plan
         - For each Layer in registry order, call recipe.Applicable()
         - Applicable → drafts to emit
         - Not applicable → reason recorded for the report
Phase 4  Report plan + confirm
         "Generating N drafts for usecase X (M applicable layers, K skipped).
          M layers: [unit-test, e2e-happy, …]
          K layers skipped: [contract-test: no OpenAPI; metric: no role=metrics]
          Proceed? (yes/no)"
Phase 5  Per usecase, per layer, in topological order (leaves first):
         recipe.Plan(stack, uc, catalog) → []Draft
         Synthesize YAML (reuse evidence + fixture writing pipeline)
         Inferential calibration gate (same as today's create-sensor)
Phase 6  Persist to .harness/sensors/<usecase-id>/<sensor-id>.yaml
         (reuse write-sensor.go with --out flag accepting per-usecase dirs)
Phase 7  Per-usecase report:
         "create-user: 10 sensors across 10 layers; ceiling=12 (2 skipped).
          Next: /validate-usecase create-user"
```

**Policy for an existing usecase folder**:

- Incremental by default: layers absent from the folder are generated; layers present are skipped silently
- `--force-layer <name>`: regenerate one specific layer (bump `sensor.version`)
- `--regenerate`: delete and regenerate the entire bundle

**Reuse vs new code**:

| Today (in `skills/create-sensor/scripts/`) | Tomorrow (in `skills/create-sensors/scripts/`) |
|---|---|
| `read-usecases.go` | Reused; `loadCatalog` walks recursively |
| `plan-sensors.go` (grouping + InferKind/Type/Output) | Deleted; replaced by `plan-and-emit.go` iterating the layer matrix |
| `write-sensor.go` | Reused with `--out .harness/sensors/<usecase-id>/` |
| `write-fixture.go` | Reused |
| `catalog-sensors.go` | Reused with recursive walk |
| `lib/planning/group.go`, `infer.go`, `shape.go`, `plan.go` | Deleted |
| `lib/planning/layer/` | NEW — 18 layer files + interface + helpers + tests |

## Skill: `/validate-usecase`

**Invocation**: `/validate-usecase <usecase-id | journey-id | --all>`

**Phases**:

```
Phase 1  Resolve input → list of usecase ids
Phase 2  Per usecase:
         a. Walk .harness/sensors/<usecase-id>/ → list every sensor with `layer:`
         b. Group by layer → identify layer entrypoints
            (composite if present; otherwise the solo narrow for that layer)
         c. Resolve dependency graph (composites have requires; narrows may too)
Phase 3  Per usecase, per layer entrypoint, in topological order:
         Invoke the runtime (reuse lib/orchestrator) to run the entrypoint
         Collect the aggregate signal from this invocation
Phase 4  Per usecase:
         a. Compute ceiling (stack + lib/planning/layer)
         b. Compute coverage (folder scan)
         c. Compute realized (signals from Phase 3)
         d. Emit signal with metadata.confidence_report = the YAML above
         e. Optionally persist cache at
            .harness/confidence/<journey-id>/<usecase-id>.yaml
```

`realized` always reflects the current invocation. Historical `signals.log` data is not consulted when computing the report.

## File layouts

```
.harness/
├── sensors/
│   ├── run-project.yaml          # core platform primitive (from /detect-sensors)
│   ├── setup-postgres.yaml
│   ├── install-deps.yaml
│   ├── build.yaml
│   ├── lint.yaml
│   ├── type-check.yaml
│   ├── run-all-tests.yaml
│   └── create-user/              # per-usecase bundle (from /create-sensors)
│       ├── unit-test-create-user.yaml             # layer: unit-test
│       ├── observe-db-create-user.yaml            # layer: db-state
│       ├── observe-log-create-user.yaml           # layer: log-trace
│       ├── http-replay-happy-create-user.yaml     # layer: e2e-happy (narrow)
│       ├── e2e-happy-create-user.yaml             # layer: e2e-happy (composite)
│       ├── http-replay-error-create-user.yaml     # layer: e2e-error (narrow)
│       ├── e2e-error-create-user.yaml             # layer: e2e-error (composite)
│       ├── code-quality-create-user.yaml          # layer: code-quality
│       ├── architecture-create-user.yaml          # layer: architecture
│       └── security-create-user.yaml              # layer: security
├── usecases/
│   └── users/
│       └── create-user.yaml
├── stack.yaml
├── runtime/
│   └── <sensor-id>/{raw.log, signals.log, watcher.log}
└── confidence/                   # optional cache (from /validate-usecase)
    └── users/
        └── create-user.yaml
```

## Sample composite sensor

```yaml
# .harness/sensors/create-user/e2e-happy-create-user.yaml
id: e2e-happy-create-user
version: 0.1.0
name: e2e happy path for create-user
description: |
  Validates the happy path of create-user by replaying the HTTP request and
  asserting on response, log emission, and DB persistence.
layer: e2e-happy
kind: assertion
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: stream
use_cases: [create-user]
requires:
  - { kind: sensor, id: run-project }
  - { kind: sensor, id: setup-postgres }
cost:
  class: medium
  compute: { cpu: low, memory_mb: 128 }
  latency: { p50_ms: 500, p95_ms: 5000, timeout_ms: 30000 }
triggers:
  - { on: manual }
execution:
  steps:
    - { id: replay, type: sensor, ref: http-replay-happy-create-user }
    - { id: check-log, type: sensor, ref: observe-log-create-user }
    - { id: check-db, type: sensor, ref: observe-db-create-user }
    - id: assert-status
      type: assert
      expect:
        value: "${{ steps.replay.outputs.status }}"
        equals: 201
    - id: assert-email
      type: assert
      expect:
        value: "${{ steps.replay.outputs.body.email }}"
        equals: "alice@example.com"
```

## Sample narrow sensor

```yaml
# .harness/sensors/create-user/observe-db-create-user.yaml
id: observe-db-create-user
version: 0.1.0
name: observe DB state after create-user
description: |
  Queries the users table to confirm the row produced by create-user
  was persisted with the expected fixture values.
layer: db-state
kind: observation
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: single
use_cases: [create-user]
requires:
  - { kind: sensor, id: setup-postgres }
cost:
  class: cheap
  compute: { cpu: low, memory_mb: 64 }
  latency: { p50_ms: 50, p95_ms: 500, timeout_ms: 5000 }
triggers:
  - { on: manual }
execution:
  command: psql -h localhost -U postgres -d app -c "SELECT email FROM users WHERE email='alice@example.com'" -t -A
  exit_code_map:
    - { exit_code: 0,   verdict: pass, severity: info }
    - { exit_code: "*", verdict: fail, severity: high }
```

(Same usecase folder; narrow has many steps internally if the recipe demands, but a single objective: "row persisted".)

## Migration

Tudo é deleção + recriação (zero backfill):

| Action | Path |
|---|---|
| DELETE | `skills/create-sensor/` (entire skill) |
| CREATE | `skills/create-sensors/` (new skill, scripts moved + adapted) |
| CREATE | `skills/validate-usecase/` (new skill) |
| DELETE | `lib/planning/{group,infer,shape,plan}.go` |
| CREATE | `lib/planning/layer/` (18 layer files + shared helpers + tests) |
| DELETE | `blind_spots` property from `schemas/sensor.yaml` |
| ADD | `layer` property to `schemas/sensor.yaml` |
| DELETE | `BlindSpots []string` from `lib/sensor.Sensor` struct |
| DELETE | All 62 sensors in `.harness/sensors/*.yaml` (PR #70 output) |
| RE-RUN | `/detect-sensors` to repopulate core platform primitives at root |
| RE-RUN | `/create-sensors <usecase>` for each of the 62 usecases (or journey-by-journey) |

`/detect-sensors` updates (out of scope for this design, but flagged): ensure it emits the full set of platform primitives (build, lint, type-check, run-all-tests, seed-db) even when not explicitly used by the stack, so per-usecase sensors can rely on their availability via `requires[kind=sensor]`.

## Open questions / future work

- **Core sensor capability gap**: when a layer's `Applicable` requires a core sensor that doesn't exist (e.g., `e2e-happy` needs `run-project`), the layer is skipped. A future enhancement could auto-invoke `/detect-sensors` to fill the gap. YAGNI for phase 1.
- **`/iterate-sensor` skill**: a future skill to formalize the "bump version, refine recipe" flow when a blind spot is detected operationally. YAGNI for phase 1.
- **Confidence weighting**: today every layer counts equally. A future enhancement could weight layers (e.g., e2e > unit > code-quality) per project preference. YAGNI for phase 1.
- **Cross-layer signal reuse**: if `e2e-happy` reuses `observe-db-<usecase>` (layer=db-state) via `SensorStep`, and `observe-db-<usecase>` fails, `e2e-happy` also fails. Both contribute to `realized` as separate layers. This double counting is intentional (cross-validation strengthens the realized score correctly) but worth monitoring once data accumulates.
- **Inferential calibration**: layers `security`, `architecture`, `code-quality` are inferential. Their sensors require model id + calibration set as today. The /create-sensors phase 5 calibration gate blocks until the user supplies these. No change from current `/create-sensor` behavior.

## References

- `CLAUDE.md` — project conventions; rules 8 (Go tests required), 9 (lib organized by context), 10 (no temporal content in skills), 13 (sensors are composable, stack-driven).
- `schemas/sensor.yaml` — current sensor contract; this design adds `layer` and removes `blind_spots`.
- `schemas/usecase.yaml` — current usecase contract; unchanged.
- `schemas/stack.yaml` — current stack contract; consumed by layer `Applicable` checks.
- `skills/create-sensor/SKILL.md` — current skill (to be deleted).
- `lib/planning/{group,infer,shape,plan}.go` — current planner (to be deleted).
