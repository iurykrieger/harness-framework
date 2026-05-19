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
5. **Treat root-level sensors as platform primitives** (core capabilities): setup, build, lint, type-check, generic run/observe. Primary producer is `/detect-sensors`; `/create-sensors` may also auto-create a missing primitive on demand via the new `lib/planning/coredetect/` package so layers like `e2e` aren't blocked by an empty catalog.
6. **Provide a new skill `/validate-usecase <usecase-id>`** that orchestrates the bundle's layer entrypoints and emits a confidence report (ceiling, coverage, realized). Aggregate verdict uses **worst-result aggregation** — any single failing layer poisons the whole verdict, regardless of how many other layers pass.
7. **Remove `blind_spots` from the sensor schema.** Gap remediation is operational (iterate a sensor's version via the future `/update-sensor` skill, or generate a new sensor in another layer), not declarative.

## Non-goals

- Migration scripts. The framework is in active development; sensors that do not conform to the new schema are deleted manually and regenerated.
- Backwards compatibility for the old `/create-sensor` skill or any sensors previously persisted under the old schema.
- Parameterized core sensors via `SensorStep.with:`. Per-usecase sensors implement their own observation/assertion logic; they reuse core sensors only as setup preconditions.
- Confidence weighting across layers. Every layer counts equally in `realized`. Weighting is future work.

## Concepts

### Sensor classes (per usecase folder)

| Class | Objective | Mechanics |
|---|---|---|
| **Narrow** | A single validation/observation objective | Any number of `shell`/`http`/`assert` steps in service of that one objective. No `SensorStep` required (but allowed for inline helpers). |
| **Composite** | Multiple objectives chained | Uses `SensorStep` (`type: sensor`, `ref:`) to delegate each objective to a narrow sensor; may include inline `assert` steps for cross-cutting concerns. Always carries the `layer:` of the lens it represents. |

The distinction is **scope of objective**, not step count. A narrow `observe-db-create-user` may have steps `pg_isready → psql query → assert row exists` and remain narrow because all three serve "row was persisted".

### Sensor tiers (core vs per-usecase)

| Tier | Location | Examples | Owner |
|---|---|---|---|
| **Core (platform primitives)** | `.harness/sensors/<id>.yaml` (root) | `run-project`, `setup-postgres`, `install-deps`, `seed-db`, `build`, `lint`, `type-check`, `run-all-tests`, `check-server-startup` | Produced by `/detect-sensors` |
| **Per-usecase (product validations)** | `.harness/sensors/<usecase-id>/<sensor-id>.yaml` | `observe-db-create-user`, `e2e-create-user`, `e2e-happy-path-create-user`, `e2e-duplicate-email-create-user`, … | Produced by `/create-sensors` |

Per-usecase sensors invoke core sensors **only via `requires[kind=sensor]`** (transparent setup before the sensor runs). Observation and assertion logic is implemented in the per-usecase sensor itself — never parameterized into a core sensor via `SensorStep.with:`. This keeps core sensors self-contained and per-usecase sensors autonomous in their observation/assertion logic.

### Layer

A **Layer** is a validation lens: the angle from which the usecase is observed. Layers are orthogonal to `kind`, `type`, and `output`. The enum is closed (defined in `lib/planning/layer/` as Go constants); adding a new layer is a plugin-version event.

### Layer matrix

The 17 layers, grouped by family, each with their stack pre-condition:

| Family | Layer | Stack pre-condition |
|---|---|---|
| **Test execution** | `unit-test` | role=test-runner |
|  | `integration-test` | role=test-runner + (role=db-client OR role=queue-consumer OR role=queue-producer OR role=external-integration) |
|  | `contract-test` | role=http-server OR role=rpc + OpenAPI/proto contract in repo |
|  | `e2e` | journey has entry_points + `run-project` core sensor available (auto-created if missing — see "Auto-creation of missing core sensors") |
| **Runtime observation** | `db-state` | role=db-client |
|  | `log-trace` | ≥1 log_shape in stack |
|  | `metric` | role=metrics |
|  | `event-emission` | role=queue-producer |
|  | `event-consumption` | role=queue-consumer |
| **Performance/resilience** | `performance` | archetype http-api OR queue-consumer OR queue-producer |
|  | `resilience` | fault-injection tooling component on stack |
| **Static quality (inferential)** | `code-quality` | always applicable |
|  | `architecture` | always applicable |
|  | `security` | always applicable |
|  | `dependency-health` | always applicable |
| **Schema/contract** | `db-schema` | role=db-client + migrations folder |
|  | `accessibility` | archetype http-spa OR http-ssr |

**Naming choices relative to the original brief**:

The 17 enum values diverge from the brief's "database, unit-test, e2e-test, logs, metrics, security, architecture, code" in two places, each deliberate:

- `database` → split into `db-state` (runtime row inspection) and `db-schema` (static migration safety) — these are independent angles.
- `code` → renamed to `code-quality` (duplication, complexity, idioms). Structural concerns are split out into `architecture` (layering, dependency direction) and `security` (vulns, exploits). The `code-quality` slug fully subsumes the brief's "code" entry; no validation angle from the original brief was dropped.

`e2e-test` from the brief becomes a single `e2e` layer whose recipe emits one composite **plus N scenario narrows derived from the usecase's `behavior.business_rules[]` and `expected_outcome.invariants[]`**. The happy path is one scenario; each rule that excludes a class of input (uniqueness, format, missing field, boundary value) becomes another. See "E2E scenario derivation" below.

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

A layer may emit a **single narrow draft** (e.g., `db-state` emits one `observe-db-<usecase>.yaml`) or **multiple drafts including a composite** (e.g., `e2e` emits N scenario narrows plus one `e2e-<usecase>.yaml` composite that orchestrates them). The runtime topologically sorts drafts so leaves persist before the composites that reference them.

### E2E scenario derivation

The `e2e` layer is the only layer whose recipe is intrinsically multi-scenario — it MUST cover every acceptance criterion declared on the usecase, not only the happy path. Scenarios are derived deterministically from the usecase YAML:

| Source field | What it produces |
|---|---|
| `trigger.fixture` + `expected_outcome.fixture` | One **happy scenario** (`e2e-happy-path-<usecase>`) replaying the canonical input and asserting the canonical output |
| Each entry in `behavior.business_rules[]` whose text expresses a constraint that can be violated (uniqueness, format, range, required field, etc.) | One **error scenario** (`e2e-<rule-slug>-<usecase>`) replaying a deliberately-violating fixture and asserting the canonical error response (status code + body shape) |
| Each entry in `expected_outcome.invariants[]` that is independently observable | One **invariant scenario** (`e2e-invariant-<slug>-<usecase>`) asserting the invariant directly (typically via side-effect observation) |

All scenarios carry `layer: e2e`. The composite `e2e-<usecase>.yaml` references them via `SensorStep` (or via `requires[kind=sensor]` when ordering matters) and its aggregate verdict is the WORST verdict observed across the scenarios (worst-result aggregation — see "Confidence model"). A usecase with 1 happy scenario + 3 error scenarios + 2 invariant scenarios produces 6 narrow sensors + 1 composite under `<usecase-id>/`, all labelled `layer: e2e`.

For confidence accounting, `e2e` still counts once in `ceiling` and `coverage`; it counts once in `realized` only when the composite (and therefore every scenario it orchestrates) passes. This is intentional: the e2e layer is a tighter bar than per-scenario layers would be, but it surfaces granular signals to the agent via the individual scenario sensors during a `/validate-usecase` run.

### Confidence model

Three integer counts per usecase, all derived (no authorial fields):

| Count | Formula | When it changes |
|---|---|---|
| `ceiling` | `count(layer ∈ enum if LayerRecipe(layer).Applicable(stack, uc, catalog))` | Stack changes; layer enum changes |
| `coverage` | `count(unique sensor.layer values in .harness/sensors/<usecase-id>/)` | `/create-sensors` runs; manual edits |
| `realized` | `count(layer entrypoints with verdict=pass at latest /validate-usecase run)` | Each `/validate-usecase` invocation |

Four derived ratios:

- `completeness = coverage / ceiling` — how much of the stack's potential is generated
- `pass_rate = realized / coverage` — health of the generated bundle (untested layers count against this)
- `executed_pass_rate = realized / (coverage − len(untested))` — health of the subset actually executed in this invocation
- `confidence = realized / ceiling` — the headline score

`realized` is always fresh (re-executes sensors on each `/validate-usecase` invocation). The framework does not consult cached signals from `signals.log` when computing realized confidence — this removes TTL ambiguity.

**Realized counting rule**: a layer entrypoint contributes to `realized` only when its aggregate Signal has `verdict=pass`. The four non-pass verdicts behave as follows:

| Aggregate verdict | Contributes to `realized`? | Notes |
|---|---|---|
| `pass` | yes | the layer is realized |
| `warn` | no | layer is generated but advisory issue surfaced; counted in `pass_rate` denominator |
| `fail` | no | layer is generated but the system-under-test failed the assertion; counted in `pass_rate` denominator |
| `error` | no | the sensor itself could not run (preflight failed, dep crashed, malformed output); counted in `pass_rate` denominator |
| timeout (treated as `error`) | no | the runtime emits `verdict=error metadata.kind=timeout`; same denominator treatment as other errors |

Layer-level breakdowns appear in `realized.layer_verdicts[]` (one entry per executed entrypoint, with its verdict + finished_at). Layers that were generated but skipped this invocation (e.g., the operator passed `--skip <layer>`) appear in `realized.untested[]`. **Untested layers still count toward `coverage`** (which is a static folder-scan count, not a runtime count), so they DO appear in the denominator of `pass_rate`; not running them effectively penalizes the ratio. This is intentional — `pass_rate` measures health of the GENERATED bundle, not just of the SUBSET that happened to run. To compute a "pass rate among layers actually executed", use the auxiliary ratio `executed_pass_rate = realized / (coverage − len(untested))` which the report surfaces alongside the headline ratios.

**Aggregate Signal verdict for `/validate-usecase`** — worst-result aggregation. The verdict reflects the most serious issue observed across all layer entrypoints, NOT a count or ratio. A single failing layer poisons the whole report:

| Condition (evaluated top to bottom — first match wins) | Aggregate `verdict` |
|---|---|
| `coverage == 0` (no per-usecase bundle or folder empty) | `error` with `metadata.kind=no_coverage` |
| any layer entrypoint emitted `verdict=error` (including timeouts, mapped to `error` per the realized-counting table above) | `error` |
| any layer entrypoint emitted `verdict=fail` (and none worse) | `fail` |
| any layer entrypoint emitted `verdict=warn` (and none worse) | `warn` |
| every layer entrypoint emitted `verdict=pass` | `pass` |

The headline ratio `confidence = realized/ceiling` is still surfaced in `metadata.confidence_report` for tracking coverage of the stack's potential, but the aggregate `verdict` is driven entirely by the worst observed result. This matches the agent's reading model: any single layer reporting trouble means the usecase is NOT validated, regardless of how many other layers reported green.

### Confidence report (cache)

`/validate-usecase` optionally persists its output at `.harness/confidence/<journey-id>/<usecase-id>.yaml`:

```yaml
usecase_id: create-user
journey_id: users
computed_at: 2026-05-18T14:30:00Z
ceiling:
  value: 11
  applicable: [unit-test, integration-test, e2e, db-state, log-trace,
               code-quality, architecture, security, dependency-health,
               db-schema, performance]
  not_applicable:
    - { layer: contract-test, reason: "no OpenAPI/proto contract in stack" }
    - { layer: metric, reason: "no role=metrics component" }
    - { layer: accessibility, reason: "archetype not http-spa/http-ssr" }
    - { layer: event-emission, reason: "no role=queue-producer" }
    - { layer: event-consumption, reason: "no role=queue-consumer" }
    - { layer: resilience, reason: "no fault-injection tooling component" }
coverage:
  value: 9
  generated: [unit-test, e2e, db-state, log-trace, code-quality, architecture,
              security, dependency-health, db-schema]
  missing: [integration-test, performance]
realized:
  value: 7
  layer_verdicts:
    - { layer: unit-test, verdict: pass, sensor_id: unit-test-create-user, finished_at: 2026-05-18T14:25:00Z }
    - { layer: e2e, verdict: pass, sensor_id: e2e-create-user, finished_at: 2026-05-18T14:27:00Z }
    - { layer: db-state, verdict: fail, sensor_id: observe-db-create-user, finished_at: 2026-05-18T14:29:00Z }
    # ... one entry per layer entrypoint executed in this invocation
  untested: []   # generated but skipped this run
ratios:
  completeness:        0.82   # 9/11
  pass_rate:           0.78   # 7/9
  executed_pass_rate:  0.78   # 7/(9-0) — all generated layers ran in this invocation
  confidence:          0.64   # 7/11
aggregate_verdict: fail        # worst-result aggregation: db-state failed
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
    - e2e
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
│   ├── unit.go               # 17 production files, one per layer; each
│   ├── integration.go        # implements LayerRecipe. Production filenames
│   ├── contract.go           # MUST NOT end in `_test.go` (Go would skip them
│   ├── e2e.go                # from the build). The three test-flavored
│   ├── db_state.go           # layers (unit-test, integration-test,
│   ├── log_trace.go          # contract-test) drop the `-test` suffix on
│   ├── metric.go             # the production file; the corresponding
│   ├── event_emission.go     # `_test.go` siblings hold their tests.
│   ├── event_consumption.go  # The `e2e` recipe is intrinsically
│   ├── performance.go        # multi-scenario — see "E2E scenario derivation".
│   ├── resilience.go
│   ├── code_quality.go
│   ├── architecture.go
│   ├── security.go
│   ├── dependency_health.go
│   ├── db_schema.go
│   ├── accessibility.go
│   ├── layer_test.go         # tests for layer.go (registry + ordering)
│   ├── applicability_test.go # tests for applicability.go
│   ├── <name>_test.go        # 17 per-layer test files (one per recipe)
│   └── testdata/             # static fixtures consumed by _test.go files:
│       ├── stack-http-api-postgres.yaml         # sample stacks
│       ├── stack-queue-consumer.yaml
│       ├── usecase-create-user.yaml             # sample usecases
│       └── catalog-with-run-project.json        # sample catalogs
├── coredetect/
│   ├── coredetect.go         # ScaffoldFunc(stack) Draft type + registry
│   ├── run_project.go        # one file per known platform primitive:
│   ├── setup_postgres.go     # run-project, setup-postgres, seed-db,
│   ├── seed_db.go            # install-deps, build, lint, type-check,
│   ├── install_deps.go       # run-all-tests, check-server-startup
│   ├── build.go
│   ├── lint.go
│   ├── type_check.go
│   ├── run_all_tests.go
│   ├── check_server_startup.go
│   ├── coredetect_test.go    # registry + scaffold ordering tests
│   ├── <name>_test.go        # per-scaffold tests
│   └── testdata/             # sample stacks → expected scaffold output
└── (deleted: group.go, infer.go, shape.go, plan.go)
```

Rule 9 ("organize by context, not by type"): each layer is its own file with its own test sibling; shared helpers in `applicability.go`; registry/interface in `layer.go`. Rule 11 (testdata layout): per-package `testdata/` holds the JSON/YAML fixtures the `_test.go` files load via relative path. `coredetect` mirrors the same structure for platform-primitive scaffolds.

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
          M layers: [unit-test, e2e (4 scenarios), db-state, …]
          K layers skipped: [contract-test: no OpenAPI; metric: no role=metrics]
          Auto-create missing core sensors: [run-project] (used by e2e)
          Proceed? (yes/no)"
Phase 5  Per usecase, per layer, in topological order (leaves first):
         recipe.Plan(stack, uc, catalog) → []Draft
         Synthesize YAML (reuse evidence + fixture writing pipeline)
         Inferential layers (security, architecture, code-quality,
         dependency-health) are emitted WITH DEFAULT CALIBRATION
         (model=anthropic/claude-sonnet-4-6, confidence_threshold=0.7,
         calibration_size=1, calibration_date=<today>, calibration_set="")
         so they pass schema validation immediately. The operator tunes
         these post-hoc via the future /update-sensor skill. No
         interactive calibration gate.
Phase 6  Persist to .harness/sensors/<usecase-id>/<sensor-id>.yaml
         (reuse write-sensor.go with --out flag accepting per-usecase dirs)
Phase 7  Per-usecase report:
         "create-user: 11 sensors across 9 generated layers
          (1 e2e composite + 4 e2e scenarios + 6 single-sensor layers).
          ceiling=11 (2 skipped: contract-test, performance).
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
| `lib/planning/layer/` | NEW — 17 layer files + interface + helpers + tests |
| `lib/planning/coredetect/` | NEW — scaffolds for known platform primitives (`run-project`, `setup-postgres`, `seed-db`, `install-deps`, `build`, `lint`, `type-check`, `run-all-tests`, `check-server-startup`) used by `/create-sensors` auto-creation |

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
│       ├── e2e-happy-path-create-user.yaml        # layer: e2e (scenario narrow)
│       ├── e2e-duplicate-email-create-user.yaml   # layer: e2e (scenario narrow)
│       ├── e2e-invalid-email-format-create-user.yaml # layer: e2e (scenario narrow)
│       ├── e2e-missing-required-field-create-user.yaml # layer: e2e (scenario narrow)
│       ├── e2e-create-user.yaml                   # layer: e2e (composite entrypoint)
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

**Sensor id naming convention** (enforced by `/create-sensors` when synthesizing drafts):

| Sensor class | Pattern | Example |
|---|---|---|
| Composite (layer entrypoint) | `<layer>-<usecase-id>` | `e2e-create-user` |
| Narrow that IS the layer's entrypoint (no composite) | `<observe-or-action>-<lens>-<usecase-id>` — `<lens>` collapses to the resource being observed | `observe-db-create-user` (db-state), `observe-log-create-user` (log-trace), `unit-test-create-user`, `architecture-create-user` |
| Narrow scenario inside a multi-scenario layer (e.g., `e2e`) | `<layer>-<scenario-slug>-<usecase-id>` — slug derived from `behavior.business_rules[]` or "happy-path" for the canonical fixture | `e2e-happy-path-create-user`, `e2e-duplicate-email-create-user`, `e2e-invalid-email-format-create-user`, `e2e-missing-required-field-create-user` |
| Narrow that is reused by a composite (not a layer entrypoint of its own; not a scenario) | `<action>-<flavor>-<usecase-id>` — `<flavor>` matches the calling composite's layer | `http-replay-happy-create-user`, `http-replay-error-create-user` (used by their respective `e2e-*` scenario narrows) |

The rule of thumb: layer composites take `<layer>-<usecase>`; solo entrypoints take `<verb>-<lens>-<usecase>`; scenarios inside a multi-scenario layer take `<layer>-<scenario-slug>-<usecase>`; sub-narrows that only exist to support a higher sensor take `<action>-<flavor>-<usecase>`. Slugs come from `lib/planning/layer.Slugify(business_rule_text)` (lowercase, kebab-case, max 32 chars).

## Sample composite sensor (e2e layer entrypoint)

The `e2e` layer's composite orchestrates ALL the scenarios derived from the usecase (happy path + every violation case from `behavior.business_rules[]` + every observable invariant). Its aggregate verdict is the WORST verdict across the scenarios — worst-result aggregation.

```yaml
# .harness/sensors/create-user/e2e-create-user.yaml
id: e2e-create-user
version: 0.1.0
name: e2e validation for create-user (all scenarios)
description: |
  Orchestrates every e2e scenario derived from create-user's behavior.business_rules
  and expected_outcome.invariants: the happy path plus each constraint-violation
  scenario. Aggregate verdict is the worst observed across scenarios.
layer: e2e
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
  latency: { p50_ms: 2000, p95_ms: 15000, timeout_ms: 60000 }
triggers:
  - { on: manual }
execution:
  steps:
    - { id: scenario-happy,             type: sensor, ref: e2e-happy-path-create-user }
    - { id: scenario-duplicate-email,   type: sensor, ref: e2e-duplicate-email-create-user }
    - { id: scenario-invalid-email,     type: sensor, ref: e2e-invalid-email-format-create-user }
    - { id: scenario-missing-field,     type: sensor, ref: e2e-missing-required-field-create-user }
```

Note the composite has NO inline `assert` steps — every scenario narrow already carries its own asserts. The composite's only job is to invoke each scenario; the runtime's worst-result aggregation across the scenario signals determines its aggregate verdict.

**Multi-dep `requires` semantics**: the existing orchestrator already supports an array of `requires[kind=sensor]` entries. For the composite above, `lib/orchestrator` topologically sorts both `run-project` and `setup-postgres`, brings each up via its own watcher (per the registry-discovery flow described in `CLAUDE.md`'s "Dependencies and lifecycle" section), runs the composite once both deps are live, and tears them down at teardown when no other dependent holds the lease. No new capability is required — the spec relies on the orchestrator's current contract for multi-blocking-dep coordination.

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

## Sample e2e scenario narrow

One of the scenarios the e2e composite above references — a deliberately-violating fixture to exercise the duplicate-email constraint:

```yaml
# .harness/sensors/create-user/e2e-duplicate-email-create-user.yaml
id: e2e-duplicate-email-create-user
version: 0.1.0
name: e2e duplicate-email rejection for create-user
description: |
  Replays create-user with a fixture whose email already exists in the seed,
  asserting the API rejects with 409 Conflict and no second row is persisted.
  Derived from behavior.business_rules entry "email must be unique".
layer: e2e
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
  - { kind: sensor, id: seed-db }
cost:
  class: medium
  compute: { cpu: low, memory_mb: 64 }
  latency: { p50_ms: 200, p95_ms: 2000, timeout_ms: 15000 }
triggers:
  - { on: manual }
execution:
  steps:
    - id: replay
      type: http
      method: POST
      url: "http://localhost:8080/users"
      body_from: { fixture: "duplicate-email" }    # .harness/fixtures/e2e-duplicate-email-create-user/payload.json
      expect:
        status: { equals: 409 }
        body: { jsonpath: "$.error", contains: "email already exists" }
    - id: assert-no-new-row
      type: shell
      run: 'psql -h localhost -U postgres -d app -tA -c "SELECT COUNT(*) FROM users WHERE email=''alice@example.com''"'
      parse:
        patterns:
          - { regex: '^1$', verdict: pass, severity: info }
          - { regex: '^[2-9]\d*$', verdict: fail, severity: high }
```

The scenario narrow has its OWN `requires` (the same setup deps the composite has, plus `seed-db` for the pre-existing row) so it can also be run standalone via `/run-sensor e2e-duplicate-email-create-user`. The composite reuses it via `SensorStep`.

## Auto-creation of missing core sensors

`/create-sensors` no longer skips layers whose `Applicable` would fail solely because of a missing core sensor. Instead, the skill auto-creates the missing primitive before running the layer's `Plan`. The flow:

1. `Layer.Applicable(stack, uc, catalog)` returns `(false, "core sensor <id> missing")` if it would otherwise be applicable.
2. `/create-sensors` Phase 3 collects the union of all missing-core-sensor reasons across all layers under consideration.
3. For each missing core sensor id, the skill consults a small in-spec registry mapping `<core-sensor-id> → ScaffoldFunc(stack) Draft` (`run-project`, `setup-postgres`, `seed-db`, `install-deps`, `build`, `lint`, `type-check`, `run-all-tests`, `check-server-startup` are the known primitives; more can be added).
4. The scaffolded draft is persisted at `.harness/sensors/<id>.yaml` (root tier) BEFORE the per-usecase layers run their `Plan`.
5. The catalog is rebuilt; `Applicable` is rechecked; the layer proceeds normally.

The scaffold functions live in `lib/planning/coredetect/` (a new sibling to `lib/planning/layer/`). Each scaffold is a thin Go function that produces a Draft for one platform primitive. The same scaffold logic should be the SOURCE that `/detect-sensors` calls when it sweeps the project — so `/create-sensors` and `/detect-sensors` agree byte-for-byte on the shape of any auto-created primitive. (Refactoring `/detect-sensors` to consume `coredetect` is out of scope for this design but flagged as the path to single-source-of-truth for platform primitives.)

If a layer's `Applicable` returns false for any reason OTHER than a missing core sensor (e.g., stack has no `role=metrics`), the layer is genuinely skipped and reported as not-applicable in Phase 4. Auto-creation is reserved for the narrow case of "the platform primitive isn't there yet".

## Migration

Everything is delete + recreate (zero backfill):

| Action | Path | Notes |
|---|---|---|
| DELETE | `skills/create-sensor/` | entire skill |
| CREATE | `skills/create-sensors/` | new skill; scripts moved + adapted from `create-sensor/` |
| CREATE | `skills/validate-usecase/` | new skill |
| DELETE | `lib/planning/{group,infer,shape,plan}.go` | the old inter-usecase grouper |
| CREATE | `lib/planning/layer/` | 17 layer files + `layer.go` + `applicability.go` + tests + `testdata/` |
| DELETE | `blind_spots` property in `schemas/sensor.yaml` | |
| ADD | `layer` property to `schemas/sensor.yaml` | |
| DELETE | `BlindSpots []string` from `lib/sensor.Sensor` struct | |
| AUDIT | All consumers of the deleted `BlindSpots` field | Search `BlindSpots` across `lib/`, `skills/`, and runtime envelope construction; delete every read/write reference to keep the build green. |
| DELETE | All sensors in `.harness/sensors/*.yaml` from the previous schema | run `rm .harness/sensors/*.yaml` (top-level + every `<usecase-id>/` subfolder created by the old `/create-sensor`) |
| **KEEP** | `.harness/usecases/**` | usecase YAMLs are NOT touched by the migration; they remain authoritative inputs for `/create-sensors` |
| RE-RUN | `/detect-sensors` (recommended FIRST, but no longer strictly required) | repopulates core platform primitives at root — running this first is the cleanest path, but if you skip it `/create-sensors` will auto-create the specific core sensors each applicable layer needs (see "Auto-creation of missing core sensors" below) |
| RE-RUN | `/create-sensors <usecase>` (per usecase, journey-by-journey) | safe to run at any time after the migration; auto-creates any missing core sensors a layer requires |

**Ordering invariant (revised)**: `/detect-sensors` is RECOMMENDED before `/create-sensors` to populate the full set of platform primitives in one pass, but it is NOT a hard precondition. When `/create-sensors` encounters a layer whose `Applicable` would have failed for "core sensor X missing", the skill now AUTO-CREATES X (a focused, idempotent scaffold) before proceeding with the layer's `Plan`. See "Auto-creation of missing core sensors".

**`/detect-sensors` updates** (out of scope for this design but flagged): ensure it emits the full set of platform primitives (`build`, `lint`, `type-check`, `run-all-tests`, `seed-db`) even when not explicitly used by the stack, so per-usecase sensors can rely on their availability via `requires[kind=sensor]`.

## Open questions / future work

- **Stack drift after a bundle exists**: this design assumes the stack components are up to date when `/create-sensors` runs. When a new component is added to `stack.yaml` after a bundle already exists, the next `/create-sensors` invocation picks up newly-applicable layers (incremental policy in Phase 5). When a component is removed and a layer becomes inapplicable, stale sensors stay in the folder until the operator runs `--regenerate`. Automated pruning is deferred.
- **`/update-sensor` skill**: a future skill to formalize the "bump version, refine recipe" flow when a sensor's coverage proves insufficient (operational blind spot detected). Tracked as a GitHub issue in this repository for follow-up; not part of this design.
- **Confidence weighting**: today every layer counts equally in `realized`. A future enhancement could weight layers (e.g., e2e > unit > code-quality) per project preference. The current worst-result aggregation for the aggregate verdict already gives e2e an outsized influence on `verdict` (since any e2e scenario failing fails the whole report), so weighting is a refinement, not a missing feature.
- **Cross-layer signal reuse (no double counting)**: a narrow sensor labelled `layer=X` may be invoked twice during `/validate-usecase` — once as the standalone entrypoint for layer X, and once inline as a `SensorStep` inside a composite whose own layer is Y ≠ X. The standalone invocation determines layer X's verdict; the inline invocation's signal flows into composite Y's aggregate but does NOT count separately for X. Each layer in `realized` is determined by the verdict of ITS entrypoint only — `coverage` and `realized` always count unique `sensor.layer` values, not unique sensor invocations. This means `observe-db-<usecase>` (layer=db-state) failing standalone counts once toward db-state's realized score, AND the same failure propagates to e2e's composite when e2e includes it as a SensorStep — but db-state still contributes only one increment to `realized` regardless of the composite outcome.

## References

- `CLAUDE.md` — project conventions; rules 8 (Go tests required), 9 (lib organized by context), 10 (no temporal content in skills), 13 (sensors are composable, stack-driven).
- `schemas/sensor.yaml` — current sensor contract; this design adds `layer` and removes `blind_spots`.
- `schemas/usecase.yaml` — current usecase contract; unchanged.
- `schemas/stack.yaml` — current stack contract; consumed by layer `Applicable` checks.
- `skills/create-sensor/SKILL.md` — current skill (to be deleted).
- `lib/planning/{group,infer,shape,plan}.go` — current planner (to be deleted).
