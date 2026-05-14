# UseCase entity and /detect-usecases skill design

Status: proposed
Date: 2026-05-14
Related: `schemas/sensor.json`, `schemas/signal.json`, `schemas/stack.json`, `skills/detect-sensors/`, `lib/stack/`

## Why

The harness today scaffolds **sensors** — feedback controls that observe the system after the agent acts. Sensors are excellent at "did the build pass, did the linter find issues, did the boot succeed", but they are **capability-shaped**, not **functionality-shaped**. They do not, by themselves, encode the contract "POST /users with a valid email returns 201 with the user, and emits a `user.created` event" — the *use case* that the application actually exists to fulfill.

Applications that adopt this plugin already have established use cases that have been hardened in production. The risk surface that matters most to them is regression: *will the next change preserve the behavior I shipped last quarter?* A sensor catalog built around `lint-eslint` / `unit-test-vitest` / `build-vite` cannot answer that question on its own — it answers "does the code still compile?", not "does the journey still work?".

Bridging the gap requires a first-class **entity** for the application's use cases:

- A use case must be **discoverable** — the plugin scans the project and produces them mechanically, the way `/detect-sensors` produces sensors.
- A use case must be **descriptive, not executable** — it captures input, behavior, expected outcome, and code evidence in a form the LLM can read; the *executable* regression check is a sensor synthesized separately by `/create-sensor` (out of scope for this design).
- A use case must be **composable** — `/create-sensor` will read a UseCase and decide which existing sensors to compose via `requires[]` (e.g. `requires: [{ kind: "sensor", id: "run-project-local" }, { kind: "sensor", id: "setup-local-database" }]`).
- A use case must be **organized by journey** — multiple variations of the same logical flow (`create-user-with-email`, `create-user-duplicate-email-conflict`, `create-user-invalid-format`) share a `journey_id` so the audit surface tells the user "I covered registration but not login" rather than just a flat list.

The triggering scenario is the same as `/detect-sensors`'s: an LLM agent walks into a repository, runs one slash command, and walks out with a structured artifact that the rest of the framework consumes. The artifact for sensors is `.harness/sensors/<id>.json`. The artifact for use cases is `.harness/usecases/<id>.json`. The shape of `stack.json` already established the precedent of a project-level artifact that captures LLM-derived judgment in a schema-validated form; UseCase follows that pattern.

## What changes

1. **New entity: UseCase** with its own schema `schemas/usecase.json` (fourth entity-schema alongside `sensor.json`, `signal.json`, and `stack.json`). A UseCase describes one observable journey variation: trigger (input), behavior (what the app does between input and output), expected outcome (output + invariants + side-effects), and evidence (file:line pointers to the implementing code).
2. **Extension of `schemas/stack.json`** with three new optional top-level fields: `purpose` (string), `archetypes[]` (enum array), `journeys[]` (structured array). The fields are additive — existing `stack.json` artifacts on disk remain valid.
3. **New skill `/detect-usecases`** (`skills/detect-usecases/SKILL.md`) — scans the project, infers `purpose`/`archetypes`/`journeys` (augmenting `stack.json` when those fields are absent), enumerates variations per journey, and drafts one UseCase per variation, persisting through a deterministic Go writer.
4. **New Go package `lib/usecase/`** owns UseCase load, validate, persist, evidence checking, and cross-checking journey references against `stack.json`. Mirrors `lib/sensor/` and `lib/stack/` conventions.
5. **New script `skills/detect-usecases/scripts/write-usecase.go`** is the deterministic write-path: validate against `schemas/usecase.json`, cross-check that `journey_id` references a known `stack.journeys[].id`, verify each `evidence[].file` exists in the project, persist atomically.
6. **Extension of `lib/stack/shape.go`** with `Purpose`, `Archetypes`, `Journeys` fields and `Journey`/`EntryPoint` struct types. Existing tests retain coverage of legacy stack files (no new fields) for retrocompatibility.
7. **`CLAUDE.md` update** — adds `usecase.json` to the schema enumeration, documents the `.harness/usecases/` directory, references the new skill.

This design **does not** include:
- `/create-sensor` — generating a sensor from a UseCase is a separate skill, designed and shipped later.
- Diff/comparison of UseCases across runs — `/detect-usecases` reruns overwrite atomically; comparing versions is future work.
- Executable replay of a UseCase by the harness itself — the UseCase carries fixtures and invariants in prose; turning those into executable code is `/create-sensor`'s job.

## Architecture

```
┌────────────────────────────────────────────────────────────────────┐
│  /detect-sensors  (already exists)                                  │
│    Phase A → .harness/stack.json (components, log_shapes)           │
└────────────────────────────────────────────────────────────────────┘
                                ▼
┌────────────────────────────────────────────────────────────────────┐
│  /detect-usecases  [project-path]  [--refresh-stack] [--journey=X] │
└───────────────────────────────┬────────────────────────────────────┘
                                ▼
                  ┌───────────────────────────┐
                  │  Phase 0  — Stack precheck│
                  └─────────────┬─────────────┘
                                ▼
     .harness/stack.json absent ─▶ abort with stack_missing remediation
     present, no purpose/archetypes/journeys ─▶ Phase 0.5 (augment)
     present and complete ─▶ continue
                                ▼
                  ┌───────────────────────────┐
                  │  Phase 0.5 — Stack augment │  (LLM judgment, when needed)
                  └─────────────┬─────────────┘
                                ▼
     Infer purpose from components + README + CLAUDE.md
     Derive archetypes from component roles (http-server → http-api, etc.)
     Per archetype, enumerate journeys with entry_points + evidence
     Call write-stack.go → re-persists augmented stack.json
                                ▼
                  ┌───────────────────────────┐
                  │  Phase 1  — Per journey,   │  (LLM judgment)
                  │   enumerate variations     │
                  └─────────────┬─────────────┘
                                ▼
     Sources of variation:
       1. Input validation schemas (Zod, Joi, class-validator, Pydantic, struct tags)
       2. Branches in handler / service / use-case layer
       3. Pre-condition states (existing/missing records, feature flags, authz)
       4. Conditional side-effects (events emitted only under certain conditions)
       5. Existing tests + OpenAPI/Swagger as oracle (not cited in UseCase)
                                ▼
                  ┌───────────────────────────┐
                  │  Phase 2  — Draft + persist│  (Go validation)
                  └─────────────┬─────────────┘
                                ▼
     Per variation:
       Draft UseCase JSON (trigger.summary + shape + fixture,
                           behavior.summary + business_rules,
                           expected_outcome.summary + shape + fixture
                                            + invariants + side_effects,
                           evidence[] file:line)
       Call write-usecase.go → validates against schemas/usecase.json
                             → cross-checks journey_id ∈ stack.journeys[].id
                             → verifies evidence[].file exists in projectRoot
                             → persists .harness/usecases/<id>.json atomically
                                ▼
                  ┌───────────────────────────┐
                  │  Phase 3  — Report         │
                  └─────────────┬─────────────┘
                                ▼
     Grouped list by journey: id · regression_priority · tags

   .harness/                                  ┌──────────────────────┐
   ├── sensors/<id>.json                      │ usecase.json         │
   ├── usecases/<id>.json   ← NEW             │   id                 │
   ├── stack.json (extended)                  │   journey_id         │
   └── runtime/...                            │   trigger            │
                                              │   behavior           │
                                              │   expected_outcome   │
                                              │   evidence[]         │
                                              └──────────────────────┘
```

## The UseCase entity

### Schema (`schemas/usecase.json`)

JSON Schema Draft 2020-12, mirrors the conventions of `sensor.json` and `stack.json` (kebab-case ids, SemVer versions, file:line evidence shape).

**Top-level required fields:** `id`, `version`, `name`, `description`, `journey_id`, `trigger`, `behavior`, `expected_outcome`, `evidence`.

- `id` enforces `"pattern": "^[a-z][a-z0-9-]*$"` — kebab-case starting with a letter, the same constraint `sensor.json` uses. The further `<verb>-<entity>-<discriminator>` shape (`create-user-with-email`, `login-with-wrong-password`) is a *naming convention* enforced by the skill prose in Phase 1, not by the regex.
- `version` enforces SemVer via `"pattern": "^\\d+\\.\\d+\\.\\d+(-[A-Za-z0-9.-]+)?(\\+[A-Za-z0-9.-]+)?$"`, same as `sensor.json`.
- `journey_id` enforces the same kebab-case pattern as `id`.

**Top-level optional fields:** `regression_priority` (enum `critical|high|medium|low`), `blind_spots[]` (array of string), `tags[]` (array of string), `references[]` (array of string — URLs or paths to specs, RFCs, ADRs justifying this UseCase; same shape as `sensor.json`'s `references[]`).

#### `trigger` sub-schema

```jsonc
{
  "type": "object",
  "required": ["summary", "shape", "fixture"],
  "properties": {
    "summary":       { "type": "string", "description": "One sentence describing the input." },
    "shape":         { "type": "string", "description": "Free-form label of the input protocol: 'HTTP request', 'Kafka message', 'CLI invocation', 'scheduled tick'." },
    "fixture":       { "description": "Concrete example input. Free-shape — depends on the trigger kind. /create-sensor reads this to materialize a real call." },
    "preconditions": { "type": "array", "items": { "type": "string" } }
  }
}
```

#### `behavior` sub-schema

```jsonc
{
  "type": "object",
  "required": ["summary"],
  "properties": {
    "summary":        { "type": "string" },
    "business_rules": { "type": "array", "items": { "type": "string" } }
  }
}
```

#### `expected_outcome` sub-schema

```jsonc
{
  "type": "object",
  "required": ["summary", "shape", "fixture"],
  "properties": {
    "summary":      { "type": "string" },
    "shape":        { "type": "string" },
    "fixture":      { "description": "Concrete example output." },
    "invariants":   { "type": "array", "items": { "type": "string" } },
    "side_effects": { "type": "array", "items": { "type": "string" } }
  }
}
```

#### `evidence[]` sub-schema

Same shape as `stack.json $defs/Evidence`:

```jsonc
{
  "type": "object",
  "required": ["file", "rationale"],
  "properties": {
    "file":       { "type": "string" },
    "line_start": { "type": ["integer", "null"], "minimum": 1 },
    "line_end":   { "type": ["integer", "null"], "minimum": 1 },
    "rationale":  { "type": "string" }
  }
}
```

At least one entry. Reusing the stack-defined Evidence ensures consistency across all artifacts.

### Why narrative + fixture instead of typed discriminators

A naive design would discriminate `trigger` by `kind: "http-request"|"queue-message"|"cli-invocation"|...` and type-validate each branch (method/path/body for HTTP, broker/topic/payload for queues, etc.). That approach has a closed enumeration problem: any archetype the schema didn't predict (gRPC streaming, WebSocket subscription, GraphQL mutation, file watcher, …) requires a schema bump. It also pushes determinism into the schema where it doesn't belong — the deterministic consumer of the trigger is `/create-sensor`, not the validator.

The chosen design (narrative `summary` + freeform `shape` label + freeform `fixture`) keeps the schema minimal: it validates structure (required fields present, types correct) but does not validate content. The narrative is what the LLM reads to reason; the fixture is the concrete example for replay. This casts the determinism boundary at the right line: `/create-sensor` is the deterministic translator (prose → executable sensor), `/detect-usecases` is the inferential producer (code → prose).

### Example UseCase

```jsonc
{
  "id": "create-user-with-email",
  "version": "0.1.0",
  "name": "Create user with valid email",
  "description": "Happy path: POST /users with a valid, unique email creates an account and returns 201 with the user representation.",
  "journey_id": "user-registration",
  "trigger": {
    "summary": "POST request to /users carrying a JSON body with a valid email.",
    "shape": "HTTP request",
    "fixture": {
      "method": "POST",
      "path": "/users",
      "headers": { "Content-Type": "application/json" },
      "body": { "email": "alice@example.com" }
    },
    "preconditions": [
      "No user with email 'alice@example.com' exists in the users table."
    ]
  },
  "behavior": {
    "summary": "The controller validates the email format, checks uniqueness against the users table, hashes any provided password, persists a new row, and emits a user.created domain event.",
    "business_rules": [
      "Email must match RFC 5322 simplified form.",
      "Email uniqueness is enforced before insertion.",
      "Created users start with role='member'."
    ]
  },
  "expected_outcome": {
    "summary": "HTTP 201 Created with the new user object (id, email, role); no password echoed back.",
    "shape": "HTTP response",
    "fixture": {
      "status": 201,
      "headers": { "Content-Type": "application/json" },
      "body": { "id": "550e8400-e29b-41d4-a716-446655440000", "email": "alice@example.com", "role": "member" }
    },
    "invariants": [
      "Response.body.id matches UUID v4 format.",
      "Response.body.email equals the email sent in the request.",
      "Response.body does NOT contain a password field.",
      "Response.status is exactly 201, not 200 or 204."
    ],
    "side_effects": [
      "Row inserted in users table with email='alice@example.com'.",
      "Event 'user.created' published to the internal event bus."
    ]
  },
  "evidence": [
    { "file": "src/users/users.controller.ts", "line_start": 42, "line_end": 68, "rationale": "POST /users handler implementing the registration flow." },
    { "file": "src/users/users.service.ts",    "line_start": 15, "line_end": 47, "rationale": "Service method enforcing uniqueness and persisting." }
  ],
  "regression_priority": "critical",
  "tags": ["happy-path", "registration", "idempotent-input"]
}
```

## Extension of `stack.json`

Three new optional top-level fields, all retrocompatible (absent in legacy stack files = treated as empty):

### `purpose` (string, optional)

A one-sentence declarative description of what the application does in the world. Examples:

- `"HTTP API for managing user accounts and JWT-based authentication"`
- `"Kafka consumer that processes payment events and updates the ledger"`
- `"CLI tool for migrating Postgres schemas with rollback support"`

### `archetypes[]` (array of string, optional)

Closed enum of archetype labels the application embodies. Multiple values are valid for hybrid apps (e.g. HTTP API that also consumes events).

```
Archetype: ["http-api", "http-spa", "http-ssr", "queue-consumer",
            "queue-producer", "cli-tool", "library", "iac",
            "data-pipeline", "scheduler", "event-driven-service",
            "db-bound-service"]
```

### `journeys[]` (array, optional)

The agreggation layer that UseCases reference via `journey_id`.

```jsonc
{
  "id": "user-registration",
  "name": "User registration",
  "summary": "Flow that creates a new user account, validates the email, and issues credentials.",
  "archetype": "http-api",
  "entry_points": [
    {
      "kind": "http-route",
      "method": "POST",
      "path": "/users",
      "evidence": { "file": "src/users/users.controller.ts", "line_start": 42, "rationale": "controller endpoint declaration" }
    }
  ]
}
```

`EntryPointKind` enum: `["http-route", "queue-subscription", "cli-command", "scheduled-job", "event-handler", "grpc-method"]`. Each kind activates a subset of fields (`method`+`path` for `http-route`, `topic` for `queue-subscription`, `command` for `cli-command`, `schedule` for `scheduled-job`). Validation of those subsets is done by the JSON Schema's `allOf`/`if`/`then` discriminator pattern (same idiom as `sensor.json`).

### Semantic cross-checks (in Go, not in JSON Schema)

- `journeys[].archetype` must be a value present in the top-level `archetypes[]` array. Validated by `lib/stack` at persist time.
- `journeys[].entry_points[].evidence.file` must exist on disk relative to the project root. Validated by `lib/stack` at persist time.

These rules cannot be expressed in JSON Schema alone (cross-field referential integrity and filesystem existence are outside its remit) and are enforced by Go alongside the schema validation, the same way `lib/stack.ValidateAndPersist` already cross-checks `log_shapes[].produced_by[] ⊆ components[].name`.

## The `/detect-usecases` skill

`skills/detect-usecases/SKILL.md` — frontmatter `name: detect-usecases`, `description:` triggers on `/detect-usecases`, prose body describing the 4-phase procedure.

### Phase 0 — Stack precheck

Read `<project>/.harness/stack.json`.

| Condition | Behavior |
|---|---|
| File absent | Abort with Signal `verdict=error`, `metadata.kind=stack_missing`. Remediation: *"Run /detect-sensors first to produce .harness/stack.json"*. Do not attempt to synthesize a stack from scratch — that is `/detect-sensors`'s job. |
| File present, no `purpose`/`archetypes`/`journeys` | Continue to Phase 0.5 (augmentation). |
| File present and all three fields populated | Continue to Phase 1. |

`--refresh-stack` forces Phase 0.5 to run even when fields are already populated, regenerating them.

### Phase 0.5 — Stack augmentation (when needed)

LLM judgment, informed by what the stack already contains:

1. **Purpose**: triangulate `languages` + `components` + top-level docs (`README.md`, `CLAUDE.md`, `AGENTS.md`). Produce one declarative sentence.
2. **Archetypes**: derive from component roles:
   - Component `http-server` or `http-router` → `http-api` (or `http-spa`/`http-ssr` if a frontend framework is among the components).
   - Component `queue-consumer` → `queue-consumer`.
   - Component `queue-producer` → `queue-producer`.
   - `bin/` or `cmd/` directories with no server → `cli-tool`.
   - Library manifest (`pyproject.toml` with `[project]` table, `package.json` with no server entrypoint, etc.) → `library`.
   - `*.tf`, `Pulumi.yaml`, `Chart.yaml` → `iac`.
   - Cron declarations (`@Cron`, `cron.yaml`, `.scheduler.yml`) → `scheduler`.
   - Mix as needed.
3. **Journeys**: per archetype, scan entry-point declarations:
   - `http-api` → controllers, route files (`@Controller`, `app.post()`, `router.HandleFunc`, Flask decorators, FastAPI routes, etc.). Group routes serving one domain concept under a single journey. Heuristic: CRUD on the same resource forms one journey (`user-lifecycle`); auxiliary flows like login are separate.
   - `queue-consumer` → consumer registrations (`@KafkaListener`, `sqs.consume`, `EventBridge.handler`). One topic/queue = one journey.
   - `cli-tool` → top-level commands (`@Command`, Cobra `cmd.AddCommand`).
   - `scheduler` → each scheduled job.

Persist via the existing `skills/detect-sensors/scripts/write-stack.go` writer. This skill calls the same Go primitive (`stack.ValidateAndPersist`) without adding a CLI flag — augmenting `purpose`/`archetypes`/`journeys` is a normal overwrite of `<project>/.harness/stack.json` with the updated `Stack` struct. No new flag, no new subcommand, no change to the existing `write_stack` script's CLI surface.

### Phase 1 — Per journey, enumerate variations

For each `journey` in `stack.journeys[]`:

1. **Read the source** pointed to by `entry_points[].evidence` — the handler, the service it delegates to, the use-case/domain layer below it.
2. **Identify sources of variation** (each is a candidate UseCase):
   - **Input validation** — schemas declared in Zod/Joi/class-validator/Pydantic/struct tags. Each rule that can fail is a variation (`missing-required-field`, `invalid-format`, `out-of-range`, `wrong-type`).
   - **Branches in handler/service** — `if (existing)`, `if (!user)`, `try/catch`, domain-error returns. Each branch is a distinct observable path.
   - **Pre-condition states** — existing vs absent records, feature flags, authorization (authenticated vs anonymous, role-gated).
   - **Conditionally-emitted events** — `if (orderTotal > 100) emit('high-value-order')`. A side-effect that only fires under specific conditions deserves its own UseCase.
   - **Existing tests** (`*.spec.ts`, `*_test.go`, `test_*.py`) and OpenAPI/Swagger files in the entry-point's neighborhood — *used as oracle for what variations the team considers important*. The UseCase does **not** reference the test or the spec file in its `evidence[]` — evidence points at the implementation, not the spec.
3. **Draft a UseCase** per variation:
   - `id`: kebab-case, `<verb>-<entity>-<discriminator>` pattern (`create-user-with-email`, `create-user-duplicate-email-conflict`, `login-with-wrong-password`).
   - `journey_id`: the `journey.id` from `stack.journeys[]`.
   - `trigger`: prose summary + free-form `shape` label + concrete fixture.
   - `behavior`: prose summary + extracted business rules.
   - `expected_outcome`: prose summary + free-form `shape` + concrete fixture + invariants[] (verifiable rules in prose) + side_effects[] (DB writes, event publications, external calls).
   - `evidence[]`: pointers to handler and service code that implements the variation. Minimum one entry.
   - `regression_priority`: `critical` for happy-path nuclear journeys; `high` for error variations with side-effects (charging, persistence); `medium` for common validation; `low` for obscure edges.
   - `tags`: stable convention — `happy-path`, `error-handling`, `validation`, `authz`, `idempotent`, `side-effects`.

### Phase 2 — Persist

Per draft:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_usecase \
  ./skills/detect-usecases/scripts \
  --out=<project>/.harness/usecases \
  --project-root=<project> \
  --schemas-dir=<plugin>/schemas \
  /tmp/<draft-name>.json
```

`write-usecase.go`:
- Reads draft JSON.
- Reads `<project-root>/.harness/stack.json` (error if absent).
- Validates draft against `schemas/usecase.json`.
- Cross-checks `journey_id` ∈ `stack.journeys[].id`.
- Verifies each `evidence[].file` exists.
- Writes canonical `<out>/<usecase.id>.json` atomically (2-space indent, alphabetized keys).

Exit codes: `0` (written), `1` (validation failed; nothing written), `2` (usage/IO/setup error).

### Phase 3 — Report

Prose summary grouped by journey:

```
Generated 14 use cases at /repo/.harness/usecases/:

journey: user-registration (5 use cases)
  - create-user-with-email.json                 — critical · happy-path
  - create-user-duplicate-email-conflict.json   — high · error-handling
  - create-user-invalid-email-format.json       — medium · validation
  - create-user-missing-password.json           — medium · validation
  - create-user-with-disposable-email.json      — low · edge-case

journey: user-login (4 use cases)
  - ...

Next: /create-sensor <use-case-id> to generate a deterministic regression sensor for each.
```

### Idempotency and re-execution

- **Default**: re-running overwrites `<id>.json` atomically (same idiom as `/detect-sensors`). Commit `.harness/usecases/` between runs to make diffs reviewable.
- **`--refresh-stack`**: forces Phase 0.5 to regenerate `purpose`/`archetypes`/`journeys`.
- **`--journey=<id>`**: limits Phase 1 to one journey (iteration).

### Behavior in projects with weak oracles

The skill **never refuses** to produce UseCases for lack of tests or docs. When the oracle is weak:

- Reduce to variations inferred from code (branches, validations, conditional returns).
- Mark `regression_priority: low` for inferred-without-fixture-confirmation variations.
- Annotate `blind_spots[]`: *"Fixture inferred from types; no test or payload example found in the repo."*

## Go package: `lib/usecase/`

Follows project rule 9 — context organization, action-named files.

```
lib/usecase/
  shape.go            UseCase, Trigger, Behavior, ExpectedOutcome structs
  shape_test.go
  load.go             LoadFromFile(path) (*UseCase, error)
  load_test.go
  persist.go          ValidateAndPersist(draft, outDir, projectRoot, stack, schemasDir) (writtenPath, error)
  persist_test.go
  evidence.go         CheckEvidenceFiles(uc, projectRoot) error
  evidence_test.go
  cross_check.go      CheckJourneyReference(uc, stack) error
  cross_check_test.go
  usecasetest/        Test helpers consumed cross-package
    fixtures.go
  testdata/
    valid/
      create-user-with-email.json
      minimal.json
      with-side-effects.json
    invalid/
      missing-journey-id.json
      empty-evidence.json
      bad-id-pattern.json
      bad-version-format.json
      missing-trigger-fixture.json
```

`UseCase.Evidence` is `[]stack.Evidence` — same struct as `lib/stack/shape.go`. No duplication.

`persist.go` mirrors `lib/sensor/persist.go`:

```go
func ValidateAndPersist(
    draftPath   string,
    outDir      string,
    projectRoot string,
    stk         *stack.Stack,
    schemasDir  string,
) (writtenPath string, err error)
```

Validation order:
1. JSON Schema (`schemas/usecase.json`) via `lib/schema.Validate`.
2. `CheckJourneyReference(uc, stk)`.
3. `CheckEvidenceFiles(uc, projectRoot)`.
4. Canonical write to `<outDir>/<uc.ID>.json` (atomic, 2-space indent, alphabetized keys).

## Extension of `lib/stack/`

`lib/stack/shape.go` adds three optional fields and two new struct types:

```go
type Stack struct {
    // existing fields...
    Version    string      `json:"version"`
    DetectedAt time.Time   `json:"detected_at"`
    DetectedBy string      `json:"detected_by"`
    Languages  []Language  `json:"languages"`
    Components []Component `json:"components"`
    LogShapes  []LogShape  `json:"log_shapes"`

    Purpose    string      `json:"purpose,omitempty"`
    Archetypes []string    `json:"archetypes,omitempty"`
    Journeys   []Journey   `json:"journeys,omitempty"`
}

type Journey struct {
    ID          string       `json:"id"`
    Name        string       `json:"name"`
    Summary     string       `json:"summary"`
    Archetype   string       `json:"archetype"`
    EntryPoints []EntryPoint `json:"entry_points"`
}

type EntryPoint struct {
    Kind     string   `json:"kind"`
    Method   string   `json:"method,omitempty"`
    Path     string   `json:"path,omitempty"`
    Topic    string   `json:"topic,omitempty"`
    Command  string   `json:"command,omitempty"`
    Schedule string   `json:"schedule,omitempty"`
    Evidence Evidence `json:"evidence"`
}
```

`lib/stack/load.go` and `lib/stack/persist.go` inherit the new fields via Go's struct encoding without API changes. New tests cover (a) legacy stack (no new fields) decodes correctly and round-trips, (b) full stack with new fields round-trips, (c) `Purpose==""` / nil slices omit those keys on serialization, (d) `journeys[].archetype` cross-check against `archetypes[]`.

## Scripts: `skills/detect-usecases/scripts/`

Follows project rules 5 and 7 — one file per script, no subcommands.

```
skills/detect-usecases/scripts/
  write-usecase.go         //go:build write_usecase
  write-usecase_test.go
```

CLI:

```
write-usecase --out=<dir> --project-root=<dir> --schemas-dir=<dir> <draft.json>
```

Flow:
1. Parse flags.
2. Read draft JSON.
3. Read `<project-root>/.harness/stack.json` (error `verdict=error metadata.kind=stack_missing` if absent).
4. Call `usecase.ValidateAndPersist(draft, out, projectRoot, stack, schemasDir)`.
5. Print written absolute path on stdout.

Exit codes: `0` (ok), `1` (validation failed; no write), `2` (IO/setup).

No `read-usecase.go`, no `list-usecases.go`, no subcommand dispatch — `Read` tool and `ls` cover those needs without a Go script.

## Tests

### `lib/usecase/`

| Test | Asserts |
|---|---|
| `shape_test.go` | JSON encode/decode round-trip; `omitempty` removes empty optionals; field tags match schema property names |
| `load_test.go` | Loads valid JSON; rejects malformed JSON with typed error; rejects schema-violating JSON with field-pointing error |
| `persist_test.go` | Happy path validates + writes; invalid draft does not touch disk; existing file overwritten atomically; file name equals `<id>.json` |
| `evidence_test.go` | OK when all evidence files exist; error listing missing files when any absent; resolves paths relative to projectRoot |
| `cross_check_test.go` | OK when `journey_id` matches a `stack.journeys[].id`; error naming the missing id otherwise |

### `lib/stack/` (additions)

| Test | Asserts |
|---|---|
| `decode_legacy_stack` | Stack without purpose/archetypes/journeys decodes |
| `decode_full_stack` | Stack with all new fields decodes |
| `journey_roundtrip` | Journey + EntryPoint round-trip JSON |
| `persist_omits_empty` | Empty `Purpose`/`Archetypes`/`Journeys` are absent from output |
| `journey_archetype_cross_check` | `journeys[].archetype` not in `archetypes[]` is rejected |

### `skills/detect-usecases/scripts/write-usecase_test.go`

Invokes `main` with `os.Args` patterns; captures stdout/stderr and inspects on-disk state.

| Case | Exit |
|---|---|
| Draft valid, journey exists, evidence files exist | 0 |
| Draft violates JSON Schema | 1 |
| Draft `journey_id` absent in stack | 1 |
| Draft `evidence[].file` missing | 1 |
| `--out` or draft path absent | 2 |
| `stack.json` absent | 2 |

## Implementation milestones

Ordered — each milestone is a potential PR boundary. Schema before struct before validator before script before skill.

1. **`schemas/usecase.json`** — write schema; load-test it via `lib/schema.LoadSchema`.
2. **`schemas/stack.json`** — add `purpose`/`archetypes`/`journeys` optional fields + their `$defs/{Journey,EntryPoint,Archetype,EntryPointKind}`. Bump artifact `version` examples to `0.2.0`. No CLI changes to `write-stack.go` — augmentation by `/detect-usecases` reuses the existing `stack.ValidateAndPersist` primitive with the updated struct.
3. **`lib/stack/`** — extend `shape.go` with `Purpose`/`Archetypes`/`Journeys` fields + `Journey`/`EntryPoint` types; add cross-check for `journeys[].archetype` membership; expand tests (legacy + full).
4. **`lib/usecase/`** — implement in order `shape.go` → `load.go` → `evidence.go` → `cross_check.go` → `persist.go`, each TDD with its `_test.go`. Build `testdata/` fixtures alongside.
5. **`skills/detect-usecases/scripts/write-usecase.go`** — CLI wrapper over `usecase.ValidateAndPersist`; build tag `write_usecase`; CLI tests.
6. **`skills/detect-usecases/SKILL.md`** — write skill prose mirroring this design's Phase 0 → Phase 3.
7. **`CLAUDE.md` update** — schema enumeration becomes "four schemas"; mention `.harness/usecases/` directory; reference the new skill in the architecture section.

Milestones 1–5 can land without the skill (the skill is the only LLM-facing piece; the rest is mechanical). Milestone 6 ships the user-visible entrypoint; milestone 7 is the documentation cleanup.

## Open questions

- Should `journey_id` validation be lenient when `stack.journeys[]` is empty (allow any `journey_id`, defer to the user) or strict (require the journey to exist in stack)? **Current design: strict.** Rationale: cross-check catches typos and stale UseCases pointing at deleted journeys. Lenient mode would defer those errors to `/create-sensor` later, which is worse UX.
- Should the `fixture` field have a maximum size? Large request bodies (file uploads, base64 blobs) could bloat the JSON. **Current design: no limit.** Rationale: skill prose advises keeping fixtures small; a hard limit punishes legitimate cases (image uploads, multipart). Revisit if it becomes a problem.
- Should `regression_priority` be required? **Current design: optional.** Rationale: skill always sets it (heuristic per source-of-variation type); making it required would force a hand-edit when the heuristic is uncertain.

## Future work

- **`/create-sensor <usecase-id>`** — the next skill. Reads a UseCase, composes a sensor (`kind=assertion`, typically `output=stream`) that materializes the trigger fixture into a real call, asserts the expected outcome and invariants, and declares appropriate `requires[kind=sensor]` to bring up the runtime (`run-project-local`, `setup-local-database`, etc.). Whether the sensor is one-shot or blocking depends on the trigger kind.
- **`/diff-usecases`** — when a UseCase JSON changes between runs, surface what variations were added/removed/renamed. Useful in PR review.
- **`/audit-usecases`** — read all UseCases, run their fixtures against the live app (executable mode), and report drift between declared invariants and observed behavior.
- **UseCase coverage report** — given a journey, report which sources of variation (validation rules, branches, conditional side-effects) are covered by a UseCase and which are not. Helps identify gaps.
