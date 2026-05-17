---
name: detect-usecases
description: Use when the user invokes /detect-usecases or asks to scan a project for its use cases and persist them under .harness/usecases/. Reads .harness/stack.yaml (errors out if absent — /detect-sensors must run first), augments it with purpose/archetypes/journeys when those fields are missing, then enumerates variations per journey from validation schemas, code branches, pre-condition states, conditionally-emitted events, and the project's tests/OpenAPI as oracle. Drafts one UseCase YAML per variation and persists each via skills/detect-usecases/scripts/write-usecase.go. Enforces full journey coverage via the coverage-report.go gate before reporting back — every declared journey must end the run with ≥1 persisted UseCase or be listed in a documented skip section.
---

# detect-usecases

Scan a project, identify the journeys that compose its purpose, enumerate the variations within each journey, and persist one descriptive `UseCase` per variation as YAML under `<project>/.harness/usecases/`. Each `UseCase` carries trigger/behavior/expected_outcome as narrative prose plus a concrete fixture; a future `/create-sensor` skill reads the persisted UseCases and synthesizes deterministic regression sensors.

## Invocation

```
/detect-usecases [project-path]
```

If the user supplies no argument, scan cwd. The output directory is always `<project>/.harness/usecases/`.

Optional flags:
- `--refresh-stack` — force Phase 0.5 to regenerate `purpose`/`archetypes`/`journeys` even when already populated.
- `--journey=<id>` — limit Phase 1 to a single journey by id (for iteration).

## Procedure

### Phase 0 — Stack precheck

Read `<project>/.harness/stack.yaml`.

- File absent → abort with `verdict=error`, `metadata.kind=stack_missing`. Remediation: *"Run /detect-sensors first to produce .harness/stack.yaml"*.
- File present, no `purpose`/`archetypes`/`journeys` → continue to Phase 0.5.
- File present and all three fields populated → continue to Phase 1.

### Phase 0.5 — Stack augmentation (when needed)

Infer the three top-level fields and persist the augmented `stack.yaml` via the existing `stack.ValidateAndPersist` primitive.

1. **Purpose** — triangulate `languages` + `components` + top-level docs (`README.md`, `CLAUDE.md`, `AGENTS.md`). One declarative sentence.
2. **Archetypes** — derive from component roles:
   - Component `http-server` or `http-router` → `http-api` (or `http-spa`/`http-ssr` when a frontend framework is among the components).
   - Component `queue-consumer` → `queue-consumer`.
   - Component `queue-producer` → `queue-producer`.
   - `bin/` or `cmd/` and no server → `cli-tool`.
   - Library manifest with no server entrypoint → `library`.
   - `*.tf`, `Pulumi.yaml`, `Chart.yaml` → `iac`.
   - Cron declarations (`@Cron`, `cron.yaml`) → `scheduler`.
   - Hybrid apps get multiple values.
3. **Journeys** — per archetype, scan entry-point declarations:
   - `http-api` → controllers, route files (`@Controller`, `app.post()`, `router.HandleFunc`, Flask/FastAPI decorators). Group routes serving one domain concept under one journey.
   - `queue-consumer` → consumer registrations (`@KafkaListener`, `sqs.consume`, `EventBridge.handler`). One topic/queue = one journey.
   - `cli-tool` → top-level commands (`@Command`, Cobra `cmd.AddCommand`).
   - `scheduler` → each scheduled job.
   - Record each journey's `entry_points[]` with file:line evidence pointing at the registration site.

### Phase 1 — Per journey, enumerate variations

**Hard precondition.** Every id in `stack.journeys[]` MUST end Phase 2 with at least one persisted UseCase. The skill is not free to drop a journey because the prompt is long, the codebase is large, or the variations are tedious — the Phase 3 coverage gate will refuse to report success otherwise.

Before doing any drafting, write the **journey ledger** to scratch: enumerate `stack.journeys[].id` in order, one per line, with a `[ ]` checkbox each. Tick each id off as you persist at least one UseCase for it. Do not exit Phase 2 with unticked boxes unless you intend to record the journey in the Phase 4 skip list with a one-sentence reason.

For each `journey` in `stack.journeys[]`:

1. **Read the source** pointed to by `entry_points[].evidence` — the handler, the service it delegates to, the use-case/domain layer below it.
2. **Extract the typed contract declarations** that determine `trigger.fixture` and `expected_outcome.fixture`. The handler signature names a request body type, a response type, and (often) path/query/header param schemas — each declared somewhere in source, never inferred from the controller body alone.

   Resolve those declarations through `<project>/.harness/stack.yaml`, not through a baked-in list of frameworks:
   - Read `components[]` and `languages[]`. Component names are the literal libraries this project pulls in (look at their `name` and `version`, and at the `evidence[]` rows that show where each is wired up).
   - For each component that participates in request decoding, validation, response serialization, or message routing for the journey's archetype, recall its idiomatic shape for typed declarations. When the library is unfamiliar, you are unsure of the current convention, or the project pins an older major version, fetch the library's official documentation with `WebFetch`/web search before drafting — do not infer the convention from the controller body.
   - Use the imports/decorators visible on the handler as the bridge from "component named in stack.yaml" to "declaration file in source": follow the import path, then open the cited type, schema object, struct, message-broker entry, or CLI flag binding.
   - The fixture YAML MUST use the field names declared there — same casing, same nesting depth, same optionality. Listed primitive enums (status, type, kind) MUST use the literal values declared by the type, not a translation.

   Each declaration file becomes an `evidence[]` row on the drafted UseCase with `kind: contract` and a rationale naming both the declared type and the library that authors it (e.g. *"ChargeCreateRequest declared via <library-from-stack.yaml>; defines payment_method/amount/local_datetime/pix_transaction"*). Evidence rows that point at handler/service/domain code remain `kind: implementation` (the default). When `trigger.fixture` or `expected_outcome.fixture` carries any non-primitive value (map/list), the `write-usecase` validator rejects the draft if no `kind: contract` row is present — there is no escape hatch, cite the inline type declaration on the handler itself if the project genuinely declares the shape there.
3. **Identify variation sources**:
   - **Input validation** — schemas declared in Zod/Joi/class-validator/Pydantic/struct tags. Each rule that can fail is a variation (`missing-required-field`, `invalid-format`, `out-of-range`, `wrong-type`).
   - **Branches in handler/service** — `if (existing)`, `if (!user)`, `try/catch`, domain-error returns. Each branch is a distinct observable path.
   - **Pre-condition states** — existing vs absent records, feature flags, authorization (authenticated vs anonymous, role-gated).
   - **Conditionally-emitted events** — `if (orderTotal > 100) emit('high-value-order')`. A side-effect that only fires under specific conditions deserves its own UseCase.
   - **Existing tests** (`*.spec.ts`, `*_test.go`, `test_*.py`) and OpenAPI/Swagger files in the entry-point's neighborhood — *used as oracle for what variations the team considers important*. The UseCase does **not** reference the test or the spec file in its `evidence[]` — evidence points at the implementation, not the spec.
4. **Minimum-variations checklist.** A journey is *covered* when **all** of the following hold for that journey:
   - One `happy-path` UseCase is persisted (always required, no exceptions).
   - One UseCase per failure path of any `try/catch` block or domain-error return found in the handler/service (`error-handling`).
   - One UseCase per declared validator rule (Zod/Joi/class-validator/Pydantic/struct tag) reachable from the entry point (`validation`).

   If a rule on the checklist has no observable variation in the source (no validator, no catch), it is satisfied vacuously — but the absence must be confirmed by reading the code, not assumed.
5. **Draft a UseCase per variation**:
   - `id`: kebab-case, `<verb>-<entity>-<discriminator>` pattern (`create-user-with-email`, `create-user-duplicate-email-conflict`, `login-with-wrong-password`).
   - `journey_id`: the `journey.id` from `stack.journeys[]`.
   - `trigger`: prose summary + free-form `shape` label (`HTTP request`, `Kafka message`, `CLI invocation`, `scheduled tick`) + concrete fixture.
   - `behavior`: prose summary + extracted business rules.
   - `expected_outcome`: prose summary + free-form `shape` + concrete fixture + `invariants[]` (verifiable rules in prose) + `side_effects[]`.
   - `evidence[]`: at least one row pointing at the handler/service/domain code that implements the variation (default `kind: implementation`), PLUS at least one row pointing at the typed contract declaration drafted in step 2 (`kind: contract`, rationale naming the declared type). The contract row is mandatory whenever either fixture is non-primitive; the same contract file may be cited twice with distinct rationales when the request and response types live in the same module.
   - `regression_priority`: heuristic — `critical` for happy-path nuclear journeys; `high` for error variations with side-effects; `medium` for common validation; `low` for obscure edges.
   - `tags`: stable convention — `happy-path`, `error-handling`, `validation`, `authz`, `idempotent`, `side-effects`.

### Phase 2 — Persist each draft

Write each draft to a temp file, then run the validator-and-writer:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_usecase \
  ./skills/detect-usecases/scripts \
  --out=<project>/.harness/usecases \
  --project-root=<project> \
  --schemas-dir=<plugin>/schemas \
  /tmp/<draft-name>.yaml
```

The script reads `<project>/.harness/stack.yaml`, validates the draft against `schemas/usecase.yaml`, cross-checks `journey_id` against `stack.journeys[].id`, verifies every `evidence[].file` exists, enforces that at least one `evidence[]` row carries `kind: contract` whenever `trigger.fixture` or `expected_outcome.fixture` is non-primitive (rejecting with `contract_evidence_missing`), then writes canonical YAML to `<out>/<journey_id>/<id>.yaml` atomically (the per-journey subdirectory is created on first write for that journey).

Exit codes:
- `0` — written.
- `1` — schema validation or cross-check failed; nothing written.
- `2` — usage / I/O / setup error (missing flag, draft unreadable, stack_missing, schemas not found).

### Phase 3 — Coverage verification (gate)

Before drafting the user-facing report, run the deterministic coverage gate:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=coverage_report \
  ./skills/detect-usecases/scripts \
  --project-root=<project> \
  [--journey=<id>]                # only when /detect-usecases was invoked with --journey
```

The script reads `<project>/.harness/stack.yaml`, scans `<project>/.harness/usecases/<journey_id>/*.yaml`, and prints a coverage matrix to stdout. Exit codes:

- `0` — every declared journey (or the single `--journey` selected) has ≥1 persisted UseCase. Skill proceeds to Phase 4.
- `1` — at least one journey has zero UseCases. **The skill MUST NOT report success.** Branch to one of:
  1. **Loop back to Phase 1** for each `❌` row in the matrix and draft the missing UseCases (minimum: one `happy-path` per uncovered journey). Re-run the coverage gate afterwards. This is the default branch — do not exit until the gate is green or the row is in the documented skip list below.
  2. **Document a deliberate skip** *only* when the source is genuinely unreachable from the entry point (third-party route stub, dead code) and you can name the reason in one short sentence. Record each skipped journey in the Phase 4 report under a `Skipped journeys` section with the reason. Skips are an audit trail, not an escape hatch.
- `2` — setup error (missing flag, stack absent, unknown `--journey` id). Abort and remediate.

The matrix is the source of truth for what to report in Phase 4; do not paraphrase it.

### Phase 4 — Report back

Paste the matrix produced by Phase 3 verbatim into the report, then group the persisted files per journey:

```
Coverage matrix (3 of 3 journeys covered, 14 use cases):
  ✅ user-registration  5 use cases
  ✅ user-login         4 use cases
  ✅ user-logout        5 use cases

Full coverage achieved.

Generated 14 use cases at /repo/.harness/usecases/:

journey: user-registration (5 use cases) → /repo/.harness/usecases/user-registration/
  - create-user-with-email.yaml                 — critical · happy-path
  - create-user-duplicate-email-conflict.yaml   — high · error-handling
  - create-user-invalid-email-format.yaml       — medium · validation
  - create-user-missing-password.yaml           — medium · validation
  - create-user-with-disposable-email.yaml      — low · edge-case

journey: user-login (4 use cases) → /repo/.harness/usecases/user-login/
  - ...

Next: /create-sensor <use-case-id> to generate a deterministic regression sensor for each.
```

When the gate exits `1` and you have deliberately skipped journeys, paste the matrix as-is and append a `Skipped journeys` section. Do not call this a successful run — the headline must read *"Incomplete coverage: N journeys skipped (see reasons)"*:

```
Coverage matrix (1 of 3 journeys covered, 1 use case):
  ✅ user-registration  1 use case
  ❌ user-login         0 use cases  ← BLOCKER
  ❌ user-logout        0 use cases  ← BLOCKER

Incomplete coverage: 2 of 3 journeys uncovered.

Skipped journeys:
  - user-login: entry point delegates to vendor SDK; no observable variation in this repo.
  - user-logout: handler returns 204 unconditionally; no branch, no validator, no event.

Re-run /detect-usecases --journey=<id> once an observable variation lands for each.
```

## Behavior in projects with weak oracles

The skill never refuses to produce UseCases for lack of tests or docs. When the oracle is weak:

- Reduce to variations inferred from code (branches, validations, conditional returns).
- Mark `regression_priority: low` for inferred-without-fixture-confirmation variations.
- Annotate `blind_spots[]`: *"Fixture inferred from types; no test or payload example found in the repo."*

## Safety notes

- The script never executes the implementation code. It only validates the draft and writes files.
- Existing files at `<out>/<journey_id>/<id>.yaml` are overwritten atomically by `os.Create` + `os.Rename` within the per-journey subdirectory. Commit `.harness/usecases/` before re-running so diffs are reviewable.
- Drafts staged in `/tmp/` are the user's to clean up; the script does not touch them.
- Schemas are resolved by walking up from cwd; invoke from inside the harness-framework checkout (or pass `--schemas-dir=<plugin>/schemas`) so the validator sees the right contract.
