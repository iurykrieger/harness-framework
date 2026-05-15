# Complex commands: typed steps, fixtures, and inline sensor composition

Status: proposed
Date: 2026-05-15
Related: `schemas/sensor.yaml`, `lib/exec/` (new), `lib/step/` (new), `lib/fixture/` (new), `lib/template/`, `lib/sensor/`, `lib/schema/`, `lib/orchestrator/`, `lib/signal/`, `skills/run-sensor/`, `skills/detect-sensors/`, `skills/heal-sensor/`, `CLAUDE.md`

## Why

A sensor's `execution.command` is a single string passed to `sh -c`. That collapses three different concerns into one shell line: preparing the environment, executing the observable action, and extracting/comparing values. Authoring becomes unreadable as soon as a sensor needs more than one operation. Concrete pains today:

- **No multi-line commands.** Authors encode sequences as inline `;` and `&&` chains (see `.harness/sensors/assert-run-sensor-rejects-blocking.yaml:34-40`). Quoting, escaping, and conditional logic pile up in one string. Diagnosing the failing operation requires reading the whole pipe.
- **No first-class HTTP.** A sensor that validates a REST endpoint must run `curl … | jq … | grep …` in a single shell line. The matchers are buried in shell idioms; the exit code is the only thing the runner sees.
- **No runtime fixture references.** Fixtures live in `.harness/sensors/fixtures/<group>/<case>.*` and are only consumed by `verification.golden_cases` — and even there, only by substituting the whole `execution.command` with `cat <fixture>`. A sensor cannot say "POST this fixture as request body" without inlining it into the YAML string.
- **No inline sensor composition.** `depends_on` runs another sensor as a prerequisite, but its outputs are invisible to the dependent. A sensor cannot say "run smoke-check-X with payload Y, then use its response to drive step Z."
- **Blind spots as escape valves.** Because the schema cannot express richer execution, authors write `blind_spots: ["does not test malformed payload"]` instead of writing a second sensor that does. (Spec B addresses the authoring side; this spec gives Spec B something to author against.)

This design reshapes `execution` into a typed pipeline of steps modeled after GitHub Actions — opt-in for new sensors, with a single-line `command:` shortcut preserved as syntactic sugar. The runtime gains a small library of step types (`shell`, `http`, `assert`, `sensor`), an explicit data-flow contract between steps (`with:`/`outputs:`/`${{ … }}`), and a shared fixture pool at `.harness/fixtures/`.

This spec is the first of three:

- **Spec A (this document):** capability of execution — schema and runtime.
- **Spec B (future):** authoring — `/create-sensor` emits multi-angle sensors with mocks/stubs, and blind_spots become a second-class citizen.
- **Spec C (future):** detection — `/detect-sensors` deep-scans observability libraries and synthesizes telemetry validation sensors.

Specs B and C depend on the vocabulary established here.

## What changes

1. **`schemas/sensor.yaml` is reshaped (single version, no bump).** `execution` admits two mutually-exclusive shapes:
   - `command: string` (legacy-shaped shortcut) — preserved for trivial one-liners; behaves exactly like a single `shell` step under the hood.
   - `steps: [<Step>, …]` — ordered, typed pipeline. `command` and `output_parsing` are forbidden alongside `steps`.
2. **Four step types are first-class:** `shell`, `http`, `assert`, `sensor`. Each is defined under a new `$defs/Step` discriminator. Per-type fields are described in the Schema section.
3. **`with:` / `outputs:` / `${{ … }}` data flow.** Every step optionally declares what it consumes (`with:`) and what it exposes (`outputs:`). Other steps reference outputs via `${{ steps.<id>.outputs.<key> }}`. Identical model to GitHub Actions, with a deliberately reduced expression vocabulary (no operators, no functions — values only).
4. **Top-level fixture pool: `.harness/fixtures/<name>.<ext>`.** Fixtures are discovered at load time, validated for existence and size (1 MiB default, `HARNESS_FIXTURE_MAX_BYTES` override), and referenced from steps via `with: { fixture: <name> }` or interpolation `${{ fixtures.<name> }}`. Sub-paths are allowed (`orders/valid.json`); the resolver matches by literal name. Legacy fixtures at `.harness/sensors/fixtures/<group>/<case>.*` are assumed absent — downstream projects clean and regenerate.
5. **Per-step `parse:` for streaming.** Each `shell` step may declare `parse: { patterns: [<Pattern>, …] }`. Steps of type `http` and `assert` emit exactly one structured signal each. The top-level `output: single|stream` remains required; the schema validates coherence (a `single` sensor cannot have any step with `parse:`).
6. **Fail-fast sequential execution.** Steps run in declared order. The first step whose verdict is `fail` or `error` aborts the chain; `teardown` still runs. The sensor's aggregate verdict equals the verdict of the step that decided (last successful or first failing).
7. **`type: sensor` invokes a full sub-run.** The referenced sensor runs its complete lifecycle (`prepare → command/steps → teardown`); the step's outputs are the sub-run's aggregate (`verdict`, `severity`, `signals[]`). Cycle detection at load time; max depth 5. `blocking: true` sensors cannot appear in `type: sensor` refs (steps are synchronous; sub-runs are not detached).
8. **Top-level `requires[]` (preflight) is unchanged.** `orchestrator.PreflightGate` continues to run once per sensor, before any step, exactly as today. No per-step `requires:` — the invariant in `lib/orchestrator/gate_invariant_test.go` is preserved without additions.
9. **`verification.golden_cases` keep their shape; semantics change.** A golden case no longer substitutes the command with `cat <fixture>`. Instead, the sensor runs for real (consuming the optional `fixture` via `${{ fixtures.<name> }}` if and only if it declared the binding) and the aggregate signal is compared against `expected_verdict`/`expected_severity`. External-service mocking is deferred to Spec B.
10. **`lib/exec/`, `lib/step/`, `lib/fixture/` are new packages.** `lib/template/` gains an Actions-style strict renderer. `lib/sensor/`, `lib/schema/`, `lib/orchestrator/`, `lib/signal/` see surgical edits.
11. **No rollout, no schema versioning, no coexistence.** The schema has a single shape. Existing sensors in `.harness/sensors/` are removed before this work ships and regenerated against the new schema. Downstream plugin users do the same.
12. **`signal.yaml` is unchanged.** New `metadata.kind` values (`http_observation`, `assertion`, `sub_run`) reuse the existing free-form `metadata.kind` slot.

This design **does not** change:

- The streaming wire format. `signals.log`, runner stdout, and hook stdin/stdout remain JSONL with the aggregate as the last line.
- `signal.yaml`. `Verdict`/`Severity` enums are intact.
- `stack.yaml` and `usecase.yaml`. Spec C will touch `stack.yaml`.
- The watcher, registry, auto-issue hook, or any blocking-sensor machinery. Sensors with `blocking: true` continue to use `command:` only; declaring `blocking: true` together with `steps:` is a validation error.
- `/start-sensor`, `/stop-sensor`, `/list-sensors`, `/tail-sensor`. Their contracts and scripts are untouched.
- `/create-sensor`'s authoring logic. It continues to emit `command:`-shaped sensors; Spec B teaches it the new shape.
- `orchestrator.PreflightGate` semantics or its invariant test.
- The plugin's command invocation contract (`HARNESS_REGISTRY_ROOT`, `-C "${CLAUDE_PLUGIN_ROOT}"`, `GOWORK=off`).

## Architecture

The new pipeline lives behind a single entry point: `lib/exec.Run(sensor, env) → ([]Signal, error)`. Both runner scripts and `lib/orchestrator/run.go` call it. The legacy `command: string` shape is normalized to a single-element `steps[]` in memory at load time, so the engine sees only one shape.

```
                ┌─────────────────────────────────────────────┐
                │  sensor YAML on disk                        │
                │    execution.command: "go vet ./..."        │
                │      OR                                     │
                │    execution.steps: [<typed step>, …]       │
                └────────────────────┬────────────────────────┘
                                     │  lib/schema.Validate → lib/sensor.Load
                                     │    + lib/fixture.Discover(projectRoot)
                                     ▼
                ┌─────────────────────────────────────────────┐
                │  *Sensor (in memory)                        │
                │    .Execution.Steps: []Step (always)        │
                │    .Fixtures:       map[name]absPath        │
                └────────────────────┬────────────────────────┘
                                     │  lib/orchestrator.RunOne
                                     │    → PreflightGate (top-level requires[])
                                     │    → prepare[]  (existing semantics)
                                     ▼
                ┌─────────────────────────────────────────────┐
                │  lib/exec.Run(sensor, env)                  │
                │    ExecContext = { fixtures, env, steps:{}} │
                │                                             │
                │    for each step in steps:                  │
                │      rendered = template.RenderActions(...) │
                │      result   = step.Execute(rendered, ctx) │
                │      emit result.Signals                    │
                │      if result.Verdict ∈ {fail,error}:      │
                │        break (fail-fast)                    │
                │                                             │
                │    aggregate = build from observed verdicts │
                │    emit aggregate (LAST JSONL line)         │
                └────────────────────┬────────────────────────┘
                                     │  teardown[] (always)
                                     ▼
                              ─── done ───
```

Each step implementation lives in its own subpackage and conforms to one interface (`step.Step`). The engine knows nothing about HTTP, regex, or jsonpath — it only orchestrates the loop, renders inputs, and folds outputs into the context.

## Schema

`schemas/sensor.yaml` (single shape, no `version:` field, no migration layer):

### Top-level discriminator

```yaml
properties:
  execution:
    oneOf:
      - required: [command]
        not: { required: [steps] }
      - required: [steps]
        not: { required: [command, output_parsing] }
```

The existing top-level `allOf` over `sensor.type` (computational/inferential) and `sensor.output` (single/stream) continues to gate `cost`, `calibration`, and exit-code shape. The new third axis is independent.

### `$defs/Step`

```yaml
$defs:
  Step:
    type: object
    required: [id, type]
    properties:
      id:    { type: string, pattern: '^[a-z][a-z0-9-]*$' }
      type:  { enum: [shell, http, assert, sensor] }
      with:  { $ref: '#/$defs/StepInputs' }
    oneOf:
      - $ref: '#/$defs/ShellStep'
      - $ref: '#/$defs/HttpStep'
      - $ref: '#/$defs/AssertStep'
      - $ref: '#/$defs/SensorStep'

  StepInputs:
    type: object
    additionalProperties:
      oneOf:
        - type: string                 # literal or "${{ … }}"
        - type: object
          required: [fixture]
          properties:
            fixture: { type: string }
        - type: number
        - type: boolean

  ShellStep:
    properties:
      type: { const: shell }
      run:  { type: string }           # multi-line via YAML | scalar
      exit_code_map:
        type: object
        additionalProperties:
          $ref: signal.yaml#/$defs/Verdict
      parse:
        type: object
        properties:
          patterns:
            type: array
            minItems: 1
            items: { $ref: '#/$defs/Pattern' }
      outputs: { $ref: '#/$defs/StepOutputs' }
    required: [run]

  HttpStep:
    properties:
      type:    { const: http }
      method:  { enum: [GET, POST, PUT, PATCH, DELETE, HEAD] }
      url:     { type: string }
      headers: { type: object, additionalProperties: { type: string } }
      body_from:
        oneOf:
          - required: [fixture]
            properties: { fixture: { type: string } }
          - required: [template]
            properties: { template: { type: string } }
          - required: [inline]
            properties: { inline: {} }
      timeout: { type: string, pattern: '^[0-9]+(ms|s|m)$', default: '30s' }
      expect:  { $ref: '#/$defs/HttpExpect' }
      outputs: { $ref: '#/$defs/StepOutputs' }
    required: [method, url]

  AssertStep:
    properties:
      type:   { const: assert }
      expect: { $ref: '#/$defs/Matcher' }
    required: [expect]

  SensorStep:
    properties:
      type:                { const: sensor }
      ref:                 { type: string, pattern: '^[a-z][a-z0-9-]*$' }
      outputs_passthrough: { type: boolean, default: false }
      outputs:             { $ref: '#/$defs/StepOutputs' }
    required: [ref]
```

### `$defs/StepOutputs`, `Matcher`, `HttpExpect`

```yaml
StepOutputs:
  type: object
  additionalProperties:
    type: object
    required: [from]
    properties:
      from:     { type: string }       # stdout|stderr|response.body|response.status|response.duration_ms|response.headers.<name>
      regex:    { type: string }
      jsonpath: { type: string }
      trim:     { type: boolean }
    oneOf:                              # extraction modifiers mutually exclusive
      - required: [regex]
      - required: [jsonpath]
      - required: [trim]
      - not:
          anyOf:
            - required: [regex]
            - required: [jsonpath]
            - required: [trim]

Matcher:
  type: object
  properties:
    value:       {}                     # any (can be "${{ … }}")
    equals:      {}
    matches:     { type: string }       # regex
    contains:    { type: string }
    gte:         { type: number }
    lte:         { type: number }
    type:        { enum: [string, number, boolean, array, object, 'null'] }
    min_length:  { type: integer, minimum: 0 }
    max_length:  { type: integer, minimum: 0 }
    jsonpath:    { type: string }       # extract before comparing
  additionalProperties: false

HttpExpect:
  type: object
  properties:
    status:  { $ref: '#/$defs/Matcher' }
    headers: { type: object, additionalProperties: { $ref: '#/$defs/Matcher' } }
    body:
      oneOf:
        - { $ref: '#/$defs/Matcher' }
        - type: array
          items: { $ref: '#/$defs/Matcher' }
```

### `verification.golden_cases`

```yaml
verification:
  golden_cases:
    type: array
    items:
      type: object
      required: [name, expected_verdict, expected_severity]
      properties:
        name:              { type: string, pattern: '^[a-z][a-z0-9-]*$' }
        fixture:           { type: string }
        expected_verdict:  { $ref: signal.yaml#/$defs/Verdict }
        expected_severity: { $ref: signal.yaml#/$defs/Severity }
        notes:             { type: string }
```

The `fixture` field is recorded but no longer transforms execution. A sensor that wants to consume the fixture must declare an explicit binding via `with: { fixture: <name> }` in one of its steps (or `${{ fixtures.<name> }}` inline in the `command:` shortcut).

### Validation rules (in `lib/sensor/validate.go`)

Cross-field rules that JSON Schema cannot enforce alone:

1. `output: single` together with any step where `parse:` is set → error.
2. `output: stream` together with zero steps that have `parse:` → error.
3. `blocking: true` together with `steps:` → error.
4. Duplicate step `id` within one sensor → error.
5. `with: { fixture: X }` for X not present in the discovered fixture pool → error, citing `.harness/fixtures/`.
6. `${{ steps.<id>.outputs.<key> }}` referencing a step `id` that does not exist or appears later in the order → error.
7. `${{ steps.<id>.outputs.<key> }}` where `<id>` exists but never declares `outputs.<key>` → error.
8. `type: sensor` cycle detection — DFS over `ref` edges; cycles or depth > 5 → error.
9. `type: sensor` pointing to a sensor with `blocking: true` → error.

## Data flow

### Execution context

Each `exec.Run` invocation maintains an `ExecContext`:

```go
type ExecContext struct {
    Fixtures map[string]string                  // name → absolute path
    Env      map[string]string                  // inherited + HARNESS_*
    Steps    map[string]StepResult              // populated as each step completes
}

type StepResult struct {
    Verdict   signal.Verdict                    // pass | warn | fail | error
    Status    string                            // "completed" | "aborted"
    Outputs   map[string]string                 // declared outputs only
    Stdout    string                            // shell only
    Response  *HttpResponse                     // http only
    Signals   []signal.Signal                   // emitted during this step
}
```

The context is read-only to step implementations except for the slot keyed by the running step's own id, which is written exactly once when the step returns.

### Interpolation accessors

Before each step executes, the engine renders every string field in the step through `template.RenderActions(input, ctx)`. Accepted accessors:

| Accessor                                       | Resolves to                                  |
|------------------------------------------------|----------------------------------------------|
| `${{ fixtures.<name> }}`                       | Absolute path of the fixture                 |
| `${{ steps.<id>.outputs.<key> }}`              | Output value from a prior step               |
| `${{ steps.<id>.verdict }}`                    | Verdict of a prior step                      |
| `${{ steps.<id>.response.status }}`            | HTTP status from a prior `http` step         |
| `${{ steps.<id>.response.headers.<name> }}`    | Response header value                        |
| `${{ env.<NAME> }}`                            | Inherited environment variable               |

The parser is deliberately restrictive: any token that is not an identifier (or `.`-separated chain of identifiers) inside `${{ … }}` is a render-time error. No operators, no function calls, no conditionals. The escape valve is `type: shell` — authors who need expression evaluation move that logic into a shell step and use its stdout via `outputs:`.

### `with:` landing semantics

How `with:` values reach each step type:

| Step type | `with: { fixture: X }`                                          | `with: { foo: "${{ … }}" }`                            |
|-----------|------------------------------------------------------------------|--------------------------------------------------------|
| `shell`   | `HARNESS_FIXTURE_PATH=<abs>` (singular) + `HARNESS_FIXTURE_<NAME>=<abs>` for every fixture | `HARNESS_INPUT_<KEY>=<value>` (uppercase, `-` → `_`)  |
| `http`    | Not used here — fixtures land via `body_from: { fixture: … }`     | String fields (`url`, `headers.*`, `body_from.template`) are rendered |
| `assert`  | Invalid (assert does not touch disk; schema rejects)              | `expect.value` is rendered                             |
| `sensor`  | Forwarded as the sub-run's fixture pool override                  | Forwarded as the sub-run's env                         |

The singular `HARNESS_FIXTURE_PATH` is the common case where one shell step consumes one fixture; the per-name form handles the multi-fixture case.

### Outputs extraction

```yaml
outputs:
  order_id:
    from: response.body
    jsonpath: $.id
  duration_ms:
    from: response.duration_ms       # built-in scalar, no modifier
  sentinel:
    from: stdout
    regex: '^DONE: (.+)$'             # group 1
  status:
    from: response.status             # built-in
```

Modifiers (`regex`, `jsonpath`, `trim`) are mutually exclusive; the absence of any modifier yields the raw source value. Extraction failure (regex with no match, jsonpath with no result, non-JSON body for jsonpath) sets the step's verdict to `error` with a message citing the modifier and the source.

All outputs are stringified at the boundary. Matchers convert back to their target type (`equals: 201` against a stringified `"201"` succeeds; `gte: 500` against `"503"` succeeds via numeric coercion when the modifier output is parseable as a number).

## Step types in detail

### `type: shell`

```yaml
- id: seed
  type: shell
  with:
    fixture: order-valid.json
    upstream: "${{ steps.fetch.outputs.url }}"
  run: |
    set -euo pipefail
    psql -f "$HARNESS_FIXTURE_PATH"
    curl -sS "$HARNESS_INPUT_upstream" > /tmp/cached.json
  exit_code_map:
    0: pass
    1: fail
    2: warn
  parse:
    patterns:
      - { match: 'ERROR:', verdict: fail }
      - { match: 'WARN:',  verdict: warn }
  outputs:
    cached_path:
      from: stdout
      regex: '^WROTE: (.+)$'
```

Runs through `lib/subprocess.RunStep` (existing). The runner streams stdout and stderr line by line; `parse:` patterns match against each line and emit individual signals. The step's terminal verdict is the worst of the exit-code mapping and the highest-rank verdict observed in the stream — identical to current single-command semantics, now scoped to one step.

### `type: http`

```yaml
- id: create
  type: http
  method: POST
  url: http://localhost:8080/orders
  headers:
    content-type: application/json
  body_from:
    fixture: order-valid.json
  timeout: 5s
  expect:
    status: { equals: 201 }
    headers:
      content-type: { contains: 'json' }
    body:
      - { jsonpath: $.id,    matches: '^[a-f0-9-]+$' }
      - { jsonpath: $.items, type: array, min_length: 1 }
  outputs:
    order_id:    { from: response.body, jsonpath: $.id }
    duration_ms: { from: response.duration_ms }
```

Executes via Go's `net/http` client (no external dependency). The step emits one structured signal with `metadata.kind: http_observation`; evidence includes the method, URL, status code, duration, and the subset of `expect:` results.

Default behavior with no `expect:` declared:

- 2xx → verdict `pass`
- 3xx → verdict `pass` (redirect followed)
- 4xx → verdict `fail`
- 5xx → verdict `fail`
- network error or timeout → verdict `error`

`body_from` is mutually exclusive: `fixture` reads from `.harness/fixtures/`, `inline` accepts a YAML object/array/scalar that is JSON-encoded when `content-type` is JSON, and `template` accepts a string with `${{ … }}` interpolation (sent as-is, no encoding wrap).

### `type: assert`

```yaml
- id: gate
  type: assert
  expect:
    value: "${{ steps.create.outputs.duration_ms }}"
    lte: 500
```

The thinnest step. Renders `expect.value` through the interpolator, applies one matcher, emits one signal with `metadata.kind: assertion`. Used when the comparison is too small to justify a shell step but the prior step does not have a built-in matcher.

### `type: sensor`

```yaml
- id: setup-db
  type: sensor
  ref: setup-postgres-clean
  with:
    fixture: catalog-baseline.sql

- id: probe
  type: sensor
  ref: smoke-health-check
  outputs_passthrough: true        # signals from child appear in parent's stream

- id: gate
  type: assert
  expect:
    value: "${{ steps.probe.outputs.verdict }}"
    equals: pass
```

`exec.Run` recognizes `type: sensor` and re-enters `orchestrator.RunOne` for the referenced sensor with a fresh context. `with:` becomes the sub-run's fixture pool and env. The sub-run executes its full lifecycle (`requires` → `prepare` → `steps`/`command` → `teardown`); its aggregate signal becomes the step's `outputs` namespace.

Outputs always available:

```
aggregate.verdict           pass | warn | fail | error
aggregate.severity          info | warn | error | critical
aggregate.evidence          formatted evidence string
aggregate.metadata.<k>      arbitrary aggregate metadata fields
signals[0..N-1].<field>     individual signals (when output: stream)
signals.count               integer
```

When `outputs_passthrough: true`, every individual signal emitted by the sub-run is re-emitted by the parent runner with `metadata.from_sensor: <ref>` added; when `false` (default), the sub-run's signals are consumed internally and only the step result is visible.

Cycle detection runs at load time over the static `ref` graph (Spec A does not support dynamic `ref` resolution from outputs). Maximum sub-run depth is 5; deeper graphs are rejected with a clear error.

## Fixtures

A fixture is a static file under `.harness/fixtures/<name>.<ext>` in the user project tree (resolved through `lib/registry.Lookup(cwd)`, identical to today's blocking-sensor registry root discovery).

```
.harness/
  fixtures/
    order-valid.json
    order-malformed.json
    seed-baseline.sql
    metrics-snapshot-ok.txt
    orders/
      large-order.json          # sub-paths allowed
```

Discovery happens once per `lib/sensor.Load` call. The discovered map is attached to the loaded `*Sensor` and threaded into `ExecContext`. Sensors that reference fixtures unknown to the pool fail validation, not runtime — authors learn about typos at load time.

Size limit: 1 MiB per fixture, overridable via `HARNESS_FIXTURE_MAX_BYTES`. Larger files are rejected at discovery with a message citing the path. The cap is a guardrail against fixture bloat in the repo; relax via env var when there is a legitimate reason.

Fixtures are read-only to sensors. A step that needs to mutate a fixture must copy it elsewhere first (typically a `tmpdir` from a shell step). The runner does not snapshot or version fixtures; they are static project artifacts.

## Verdict aggregation and streaming

Sequential fail-fast (decided in brainstorm):

1. Steps execute in declared order.
2. Each step produces a verdict (`pass`, `warn`, `fail`, `error`).
3. After each step, the engine computes the running aggregate as the worst verdict seen so far.
4. If the just-completed step produced `fail` or `error`, the engine **aborts**: remaining steps do not run; `teardown` still runs.
5. The final aggregate verdict equals the running aggregate at the time of abort (or after the last step, if no abort).

The aggregate signal is constructed by `lib/exec/aggregate.go` and emitted as the **last JSONL line** on the runner's stdout. Order of emission within one sensor run:

```
{prepare step 1 signal — if it ran}
{prepare step 2 signal}
{individual signal from step 1 — one per parse: match, OR one structured for http/assert}
{individual signal from step 1 — ditto}
{individual signal from step 2}
...
{aggregate signal}                            ← LAST LINE
```

`metadata.steps[]` on the aggregate carries `[{id, type, verdict, duration_ms}, …]` for diagnostic and for `/heal-sensor` to localize failures to a specific step.

## Library changes

### New packages

```
lib/exec/
  context.go            # ExecContext + StepResult
  context_test.go
  engine.go             # Run(ctx, *Sensor, env) → ([]Signal, error)
  engine_test.go
  render.go             # delegates to lib/template.RenderActions
  render_test.go
  aggregate.go          # builds aggregate Signal
  aggregate_test.go
  testdata/             # sample sensors covering all step types

lib/step/
  step.go               # interface Step + StepResult; dispatch by type
  step_test.go

  shell/
    shell.go            # uses lib/subprocess.RunStep
    shell_test.go
    parse.go            # streaming patterns
    parse_test.go
    testdata/

  http/
    http.go             # net/http client; structured signal
    http_test.go
    expect.go           # HttpExpect evaluation
    expect_test.go
    body.go             # body_from resolution (fixture | template | inline)
    body_test.go
    testdata/

  assert/
    assert.go
    assert_test.go

  sensor/
    sensor.go           # calls orchestrator.RunOne with sub-context
    sensor_test.go

  match/
    match.go            # shared Matcher evaluator (http + assert)
    match_test.go

  outputs/
    outputs.go          # StepOutputs extraction (regex | jsonpath | trim)
    outputs_test.go

lib/fixture/
  load.go               # Discover(projectRoot) → map[name]absPath
  load_test.go
  resolve.go            # Resolve(name) → absPath (or error)
  resolve_test.go
  testdata/

lib/sensor/sensortest/
  builder.go            # test helper: construct decoded *Sensor for cross-package tests
  builder_test.go
```

Test helpers in `sensortest/` follow the `httptest`/`iotest` convention (Project Rule 11): one helper package per owning package, depending only on its owner and the standard library.

### Edits to existing packages

```
lib/template/
  actions.go           [NEW]   # RenderActions(input, ExecContext) — strict ${{ … }} parser
  actions_test.go      [NEW]
  render.go            [unchanged]
  render_test.go       [unchanged]

lib/sensor/
  load.go              [EDIT]  # normalize execution.command → execution.steps[0]; attach Fixtures map
  load_test.go         [EDIT]  # cases for both shapes
  validate.go          [EDIT]  # cross-field rules (1–9 in Schema section)
  validate_test.go     [EDIT]
  envelope.go          [unchanged]

lib/schema/
  validate.go          [EDIT minor]  # remove any version-detection scaffolding; single schema
  validate_test.go     [EDIT]
  schemas/             # embedded YAML; new sensor.yaml replaces old

lib/orchestrator/
  run.go               [EDIT]  # one call swap: subprocess.StreamSubprocess → exec.Run
  run_test.go          [EDIT]
  gate.go              [unchanged]
  gate_invariant_test.go [unchanged — see invariant note below]
  dag.go               [unchanged]
  cascade.go           [unchanged]

lib/signal/
  aggregate.go         [EDIT minor]  # MaxStreamVerdict folds mixed http/assert/parse signals
  aggregate_test.go    [EDIT]
  kind.go              [NEW]   # constants for HttpObservation, Assertion, SubRun
  kind_test.go         [NEW]
```

**Preflight invariant (Project Rule 12).** The required allowlist in `lib/orchestrator/gate_invariant_test.go` does not grow. Reasoning:

- `lib/exec/engine.go` does not spawn any command directly. It dispatches to `lib/step/*` implementations.
- `lib/step/shell` calls `lib/subprocess.RunStep` — already on the allowlist via `lib/subprocess/step.go`.
- `lib/step/http` makes HTTP calls; no process spawn.
- `lib/step/assert` is in-memory; no process spawn.
- `lib/step/sensor` calls `orchestrator.RunOne`, which itself runs `PreflightGate` before any spawn — the gate fires for the sub-run sensor, not bypassed.

The invariant test continues to pass without modification.

## Skill changes

### `skills/run-sensor/`

Both runners change in one place each: replace the body-of-command branch with `exec.Run`.

```
skills/run-sensor/scripts/
  run-computational.go     [EDIT]  # calls exec.Run; preflight gate stays
  run-computational_test.go [EDIT]
  run-inferential.go       [EDIT]  # idem; HARNESS_PROMPT + calibration unchanged
  run-inferential_test.go  [EDIT]
```

The inferential runner continues to spawn an LLM as its observable step. With `steps:`, the inferential workflow is one step of `type: shell` whose `run:` invokes the LLM subprocess. `HARNESS_AGGREGATE_CONFIDENCE` parsing is unchanged.

### `skills/detect-sensors/`

```
skills/detect-sensors/scripts/
  write-sensor.go        [EDIT minor]  # validator updated; output still command:-shape
  write-stack.go         [unchanged]
  replay-fixture.go      [REMOVE]      # `cat <fixture>` substitution no longer meaningful
  run-golden.go          [NEW]         # invokes the real sensor for each golden case, compares aggregate
  run-golden_test.go     [NEW]
```

The phase-7 verification loop in `SKILL.md` (currently "Smoke run each sensor, replay fixtures") shifts to "Smoke run each sensor for each golden case via `run-golden.go`, compare aggregate verdict/severity against `expected_*`." The skill body is updated to describe the new loop in present tense, without referring to the prior `replay-fixture` mechanism (Project Rule 10).

### `skills/heal-sensor/`

```
skills/heal-sensor/SKILL.md            [EDIT]   # per-step diagnostic instructions
skills/heal-sensor/scripts/            [EDIT]   # read metadata.steps[]; localize fix
```

Heal proposes edits scoped to the step that decided the aggregate (the failing step in a fail-fast chain). For sensors with `command:`-shortcut shape, heal continues to operate on the single line as before.

### Unchanged skills

`skills/create-sensor/` (only its validator picks up the new schema — output shape unchanged in this spec; Spec B teaches it the new vocabulary), `skills/{start,stop,list,tail}-sensor/`, `skills/detect-usecases/`.

## Hooks

`hooks/error-issue-autofiler.go` is unchanged. It fingerprints internal framework errors (panic, compile fail, signals with `verdict=error` and `metadata.kind` from a closed set of internal kinds). The new `metadata.kind` values introduced by Spec A — `http_observation`, `assertion`, `sub_run` — are **observation kinds**, not internal kinds, and are not in the autofiler's match set.

## Validation flow at load time

`lib/sensor.Load(path)` becomes:

```
1. Read YAML bytes from path.
2. lib/schema.Validate(bytes) — single schema, no version detection.
3. yaml → json → encoding/json.Unmarshal → *Sensor.
4. If execution.command is set:
     normalize to execution.steps = [{
       id:   "main",
       type: "shell",
       run:  <command>,
       exit_code_map: <existing>,
       parse: <derived from execution.output_parsing, if any>,
     }]
5. lib/fixture.Discover(projectRoot) — populate sensor.Fixtures.
6. lib/sensor/validate.go cross-field rules:
     - output ↔ parse coherence
     - blocking ↔ steps exclusion
     - duplicate step ids
     - with-fixture existence
     - interpolation references resolve in order
     - type: sensor cycle / depth / non-blocking target
7. Return *Sensor or error.
```

The normalization step is in-memory only. The YAML on disk keeps its declared shape. Persisters (in `skills/{create-sensor,detect-sensors,heal-sensor}/scripts/write-sensor.go`) write whatever shape was passed in; round-tripping a `command:`-shape sensor through Load + Persist produces the same `command:`-shape YAML.

## Testing strategy

Each new package ships table-driven tests in `*_test.go` (Project Rule 8). High-impact cases:

**`lib/exec/engine_test.go`**

- Happy path: 3-step chain (`shell` + `http` + `assert`) yields aggregate `pass`.
- Fail-fast: middle step fails, last step does not run, teardown executes.
- Stream: a shell step with `parse:` emits N individual signals before the aggregate.
- Outputs chained across steps; downstream consumer references resolve.
- Outputs reference to a non-declared key → validation error at load (not runtime).

**`lib/step/http/http_test.go`**

- Status matcher: `equals`, `gte`, `lte`.
- Body matchers combining `jsonpath` and `matches` via array form.
- Headers matcher `contains`.
- Timeout → verdict `error`.
- 5xx without `expect:` → verdict `fail` (default).
- 2xx without `expect:` → verdict `pass` (default).
- `body_from`: `fixture` reads file; `inline` JSON-encodes; `template` interpolates and sends as-is.

**`lib/step/sensor/sensor_test.go`**

- Sub-run completes; verdict propagates to step.
- Cycle A → B → A detected at load time (separate validator test).
- Depth > 5 → error.
- `outputs_passthrough: true` re-emits child signals.
- `outputs_passthrough: false` consumes child signals; only step result visible.
- Sub-run target with `blocking: true` → validation error.

**`lib/sensor/validate_test.go`**

- All nine cross-field rules covered (one or more cases each).

**`lib/template/actions_test.go`**

- Valid accessors resolve; identifiers with `-` accepted.
- Operators (`+`, `&&`, `||`), function calls, conditionals → error.
- Reference to a future step → error.
- Reference to an existing step's undeclared output → error.

**Cross-cutting**

- `lib/orchestrator/gate_invariant_test.go` still passes without modification (proves no new sensor-command spawn site bypassed `PreflightGate`).
- `go test ./lib/... && go test -tags=run_computational ./skills/... && go test -tags=run_inferential ./skills/...` is green.

## Implementation order

Independent PRs, ordered to minimize blast radius and keep `main` runnable at each step. Because there is no rollout — the prior schema is gone and existing sensors are removed up front — the order is purely about decomposition, not migration safety.

1. **`chore: remove legacy sensors and fixtures`** — delete the contents of `.harness/sensors/` and `.harness/sensors/fixtures/` in the plugin repo. After this PR, `.harness/sensors/` is empty until the acceptance sensors land in step 12.
2. **Schema + validator** — `schemas/sensor.yaml` reshape; `lib/schema/validate.go` simplification.
3. **`lib/fixture/`** — discovery, resolution, size cap.
4. **`lib/template/actions.go`** — strict `${{ … }}` renderer.
5. **`lib/step/shell` + `lib/step/match` + `lib/step/outputs`** — the simplest typed step, plus the shared matcher and outputs extractors.
6. **`lib/step/http` + `lib/step/assert`** — depend on `match` and `outputs`.
7. **`lib/step/sensor`** — depends on the engine in step 8.
8. **`lib/exec/`** — the engine wiring all step types together.
9. **`lib/sensor/validate.go`** — cross-field rules, cycle detection.
10. **`lib/orchestrator/run.go`** — single call swap (`subprocess.StreamSubprocess` → `exec.Run`).
11. **Runners (`skills/run-sensor/`)** — same call swap.
12. **`skills/detect-sensors/`** — remove `replay-fixture.go`; add `run-golden.go`; update SKILL.md.
13. **`skills/heal-sensor/`** — per-step diagnostic; update SKILL.md.
14. **Acceptance sensors** — author new sensors under `.harness/sensors/` that exercise all four step types, fixture references, and `type: sensor` composition. These also serve as living documentation.

## Risks

| Risk                                                                | Likelihood | Impact | Mitigation                                                                            |
|---------------------------------------------------------------------|------------|--------|---------------------------------------------------------------------------------------|
| `type: sensor` cycle escapes parse-time detection at runtime         | Low        | High   | DFS at load; max depth 5; explicit test                                                |
| HTTP step in a golden case generates traffic to production           | Medium     | High   | Documented; optional `HARNESS_HTTP_BLOCK_EXTERNAL=1` blocks non-localhost; Spec B brings mocks |
| Fixture > 1 MiB committed by accident                                 | Low        | Medium | `lib/fixture/load.go` rejects at discovery with a clear message                       |
| `${{ … }}` strict parser rejects a legitimate future case            | Medium     | Medium | Escape valve is `type: shell` — not a regression because shell is always available    |
| `lib/exec/` indirection adds latency on trivial sensors              | Low        | Low    | Normalization is in-memory; the additional dispatch is one map lookup per step         |
| `heal-sensor` proposes fixes against the wrong step                  | Medium     | Medium | `metadata.steps[]` on aggregate carries `{id, type, verdict}`; heal cites the id      |
| Schema change breaks an external tool parsing `sensor.yaml`          | Low        | Low    | Documented in CHANGELOG; the plugin does not advertise an external schema contract     |

## Acceptance

Spec A is considered delivered when:

1. After step 1 of the implementation order, `.harness/sensors/` is empty; after step 14, it contains at least one sensor demonstrating each of `shell`, `http`, `assert`, and `sensor` step types, plus at least one shared fixture under `.harness/fixtures/`.
2. `go test ./lib/...` is green.
3. `go test -tags=run_computational ./skills/... && go test -tags=run_inferential ./skills/...` is green.
4. `lib/orchestrator/gate_invariant_test.go` passes without new allowlist entries.
5. The acceptance sensors are referenced from a sample in `docs/` (separate doc, not this spec) demonstrating idiomatic authoring for each step type.

## Out of scope

Reaffirming what Spec A explicitly does **not** deliver:

- Declarative mocks/stubs for HTTP (Spec B).
- `/create-sensor` emitting multi-angle sensors per use case (Spec B).
- Policy treating `blind_spots` as a second-class authoring affordance (Spec B).
- `/detect-sensors` deep-scanning observability libraries (DataDog, OpenTelemetry, Prometheus client) and synthesizing telemetry sensors (Spec C).
- `stack.yaml` extensions for detected exporters and endpoints (Spec C).
- A full expression language (CEL, expr-lang, or equivalent) for matchers or conditions.
- Per-step `requires:` preflight gating.
- `blocking: true` sensors with `steps:` (explicitly rejected).
- Automatic migration of legacy sensors or legacy fixture paths (the schema does not know about them).
