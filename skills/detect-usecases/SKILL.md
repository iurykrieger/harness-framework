---
name: detect-usecases
description: Use when the user invokes /detect-usecases or asks to scan a project for its use cases and persist them under .harness/usecases/. Reads .harness/stack.json (errors out if absent — /detect-sensors must run first), augments it with purpose/archetypes/journeys when those fields are missing, then enumerates variations per journey from validation schemas, code branches, pre-condition states, conditionally-emitted events, and the project's tests/OpenAPI as oracle. Drafts one UseCase JSON per variation and persists each via skills/detect-usecases/scripts/write-usecase.go.
---

# detect-usecases

Scan a project, identify the journeys that compose its purpose, enumerate the variations within each journey, and persist one descriptive `UseCase` per variation as JSON under `<project>/.harness/usecases/`. Each `UseCase` carries trigger/behavior/expected_outcome as narrative prose plus a concrete fixture; a future `/create-sensor` skill reads the persisted UseCases and synthesizes deterministic regression sensors.

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

Read `<project>/.harness/stack.json`.

- File absent → abort with `verdict=error`, `metadata.kind=stack_missing`. Remediation: *"Run /detect-sensors first to produce .harness/stack.json"*.
- File present, no `purpose`/`archetypes`/`journeys` → continue to Phase 0.5.
- File present and all three fields populated → continue to Phase 1.

### Phase 0.5 — Stack augmentation (when needed)

Infer the three top-level fields and persist the augmented `stack.json` via the existing `stack.ValidateAndPersist` primitive.

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

For each `journey` in `stack.journeys[]`:

1. **Read the source** pointed to by `entry_points[].evidence` — the handler, the service it delegates to, the use-case/domain layer below it.
2. **Identify variation sources**:
   - **Input validation** — schemas declared in Zod/Joi/class-validator/Pydantic/struct tags. Each rule that can fail is a variation (`missing-required-field`, `invalid-format`, `out-of-range`, `wrong-type`).
   - **Branches in handler/service** — `if (existing)`, `if (!user)`, `try/catch`, domain-error returns. Each branch is a distinct observable path.
   - **Pre-condition states** — existing vs absent records, feature flags, authorization (authenticated vs anonymous, role-gated).
   - **Conditionally-emitted events** — `if (orderTotal > 100) emit('high-value-order')`. A side-effect that only fires under specific conditions deserves its own UseCase.
   - **Existing tests** (`*.spec.ts`, `*_test.go`, `test_*.py`) and OpenAPI/Swagger files in the entry-point's neighborhood — *used as oracle for what variations the team considers important*. The UseCase does **not** reference the test or the spec file in its `evidence[]` — evidence points at the implementation, not the spec.
3. **Draft a UseCase per variation**:
   - `id`: kebab-case, `<verb>-<entity>-<discriminator>` pattern (`create-user-with-email`, `create-user-duplicate-email-conflict`, `login-with-wrong-password`).
   - `journey_id`: the `journey.id` from `stack.journeys[]`.
   - `trigger`: prose summary + free-form `shape` label (`HTTP request`, `Kafka message`, `CLI invocation`, `scheduled tick`) + concrete fixture.
   - `behavior`: prose summary + extracted business rules.
   - `expected_outcome`: prose summary + free-form `shape` + concrete fixture + `invariants[]` (verifiable rules in prose) + `side_effects[]`.
   - `evidence[]`: pointers to handler and service code that implements the variation. Minimum one entry.
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
  /tmp/<draft-name>.json
```

The script reads `<project>/.harness/stack.json`, validates the draft against `schemas/usecase.json`, cross-checks `journey_id` against `stack.journeys[].id`, verifies every `evidence[].file` exists, then writes canonical JSON to `<out>/<id>.json` atomically.

Exit codes:
- `0` — written.
- `1` — schema validation or cross-check failed; nothing written.
- `2` — usage / I/O / setup error (missing flag, draft unreadable, stack_missing, schemas not found).

### Phase 3 — Report back

Surface a grouped list per journey:

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

## Behavior in projects with weak oracles

The skill never refuses to produce UseCases for lack of tests or docs. When the oracle is weak:

- Reduce to variations inferred from code (branches, validations, conditional returns).
- Mark `regression_priority: low` for inferred-without-fixture-confirmation variations.
- Annotate `blind_spots[]`: *"Fixture inferred from types; no test or payload example found in the repo."*

## Safety notes

- The script never executes the implementation code. It only validates JSON and writes files.
- Existing files at `<out>/<id>.json` are overwritten atomically by `os.Create` + `os.Rename`. Commit `.harness/usecases/` before re-running so diffs are reviewable.
- Drafts staged in `/tmp/` are the user's to clean up; the script does not touch them.
- Schemas are resolved by walking up from cwd; invoke from inside the harness-framework checkout (or pass `--schemas-dir=<plugin>/schemas`) so the validator sees the right contract.
