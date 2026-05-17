# /detect-sensors — Self-contained Components and Sensors-as-Building-Blocks

**Status:** design
**Date:** 2026-05-17
**Scope:** Spec C — third of three improvements decomposed from `/create-sensor` framework fragility brainstorming. Specs A (complex commands, PR #58) and B (multi-angle authoring, PR #66) shipped.

## Problem

`/detect-sensors` today produces shallow sensor coverage on anything beyond the most obvious stack. Two compounding causes:

1. **Components in `stack.yaml` carry too little signal.** Each `components[]` entry has `role`, `name`, `version`, `config_summary`, `evidence` — enough to identify the library but not enough to reason about what it does for *this specific project* or where its behavior surfaces at runtime. A `tracer: OpenTelemetry` entry tells you OTel is present, not whether the project exports metrics, traces, or both; not which exporter endpoint to probe; not which log lines indicate exporter failure.

2. **Sensor authoring (Phase B) treats sensors as one-shot artifacts per usecase.** The current SKILL.md prose drives the agent toward "for each capability, draft one sensor" — implicitly per-usecase. But sensors are meant to be reusable building blocks: `assert-build-passes` validates every usecase that depends on a green build; `observe-http-request-roundtrip` composes with `assert-log-emitted` to validate a usecase like "checkout triggers an audit log entry". The current SKILL.md does not encode this reuse-first framing.

The combination produces a long tail of missed sensors: deploy-time validations (Helm chart renders, k8s manifests apply, dry-run), observability surface validations (OTel exporter health, Sentry capture rate, DataDog forwarder status), infra side-effects (queue consumer lag, cache miss rate, scheduled job execution), and integration-specific checks (Stripe webhook signature, AWS SDK credential refresh).

## Conceptual model

Spec C codifies a model the framework has converged on implicitly:

```
Stack    = { Component, ... }
Journey  = { Usecase, ... }
Usecase  = acceptance criteria + input/output fixtures
Sensor   = reusable capability that builds steps/commands over
           Components to deterministically validate one or more Usecases
```

**Component** describes one important part of the application's implementation: a technology, framework, observability primitive (metrics/logs/traces), infrastructure dependency (queue/cache/job), deployment artifact (Helm chart, Dockerfile), or external integration. Each Component is self-contained: looking at it alone, the LLM can reason about what it does for this project and where its behavior surfaces.

**Sensor** is a building block. A sensor:

- Targets one Component (or composes other sensors that target Components).
- Lists every usecase it helps validate in `use_cases[]` — many usecases, not one. The 1:N traceability shipped in Spec B is the reuse contract.
- Composes with other sensors via `steps[type=sensor]` (Spec A) when the validation requires a chain (e.g., trigger an HTTP request → observe the log line → assert the metric counter incremented).

Validation by composition replaces validation by one-shot sensor. `/detect-sensors` Phase B drafts the building blocks; `/create-sensor` (or manual authoring) composes them per usecase.

## Goal

Make `/detect-sensors` produce **self-contained Components** in `stack.yaml` and **reusable building-block sensors** anchored on those Components, so that the dense observability surface of real applications (deploy/observability/infra/integrations) is covered uniformly rather than via ad-hoc LLM judgment per project.

## Schema changes

### `schemas/stack.yaml` — `Component`

Three new optional fields plus a closed-enum extension on `Role`.

**Optional additions to `$defs/Component`** (place under `properties:` — `Component` is `additionalProperties: false` at `schemas/stack.yaml:18`, so siblings to `properties:` would reject):

```yaml
Component:
  properties:
    # ... existing role, name, version, config_summary, evidence
    category:
      description: |
        Canonical taxonomy slug, "<domain>/<role>" style. Examples:
        'observability/tracer', 'observability/errors', 'http/framework',
        'deployment/helm', 'deployment/k8s', 'ci/github-actions',
        'messaging/queue-consumer', 'cache/client', 'integration/stripe'.
        Free-form string by schema; convention by SKILL.md. Optional —
        manual stack.yaml entries may omit; /detect-sensors fills it.
      type: string
    capabilities:
      description: |
        What this component does FOR THIS PROJECT (not generic library
        capabilities). Short complete sentences, each a discrete claim
        that subsequent sensors can validate. Example for an OpenTelemetry
        SDK observed exporting via OTLP:
          - "Exports distributed traces via OTLP to $OTEL_EXPORTER_OTLP_ENDPOINT"
          - "Instruments incoming HTTP requests with span context"
          - "Samples spans according to TraceIDRatioBased(0.1) per config"
        Capabilities the project doesn't exercise MUST NOT be listed.
      items:
        type: string
      type: array
    observable_surface:
      description: |
        Where evidence of this component's behavior appears — log lines,
        config files, endpoints, commands, files, env vars. Each entry is
        a short prose pointer that downstream sensor authoring can convert
        into a regex pattern, a probe command, or a file assertion.
        Example for OpenTelemetry SDK with OTLP exporter:
          - "OTLP exporter retry/failure log lines on stderr"
          - "Sampler drop counter via OTel metrics endpoint"
          - "Reachability of $OTEL_EXPORTER_OTLP_ENDPOINT (HTTP 200 on /v1/traces)"
        Free-form prose, intentionally — concrete regex and command
        choices are sensor-level concerns made at Phase B.
      items:
        type: string
      type: array
```

**Role enum extension:**

The closed enum at `$defs/Role` (`schemas/stack.yaml:230-248`) currently lists 12 values plus a multi-line `description:` block. Spec C **appends** 8 values to the end of the `enum:` list and leaves `description:` untouched. The edit is additive — no rewrite of the existing `Role:` block.

Values to append (in order; comments are illustrative and not part of the YAML):

```yaml
# append these to $defs/Role.enum at the tail; preserve the existing
# `description:` block and the 12 existing enum entries above.
- error-tracker          # Sentry, Bugsnag, Rollbar
- cache-client           # Redis, Memcached
- object-store           # S3, GCS, Azure Blob
- job-runner             # Sidekiq, Celery, k8s Job, cron
- container-runtime      # Dockerfile, podman, OCI-image-based runtime
- deployment-tool        # Helm, k8s manifests, terraform
- ci-cd                  # GitHub Actions, GitLab CI, CircleCI
- external-integration   # Stripe, SendGrid, Twilio, AWS-SDK service clients
```

**Backwards compatibility.** All three new Component fields are optional. The Role enum is *extended* (new values added; existing values untouched), so any stack.yaml in the wild remains valid. No existing sensors or stack.yaml files require regeneration; opting into the new fields is incremental.

### `schemas/sensor.yaml`

**No schema changes.** Sensors-as-building-blocks is a SKILL.md framing change; the sensor schema already supports it via:
- `use_cases: minItems: 1` (1:N traceability, shipped in Spec B).
- `execution.steps[type=sensor]` (composition, shipped in Spec A).

## SKILL.md changes

### Phase A — broader evidence + self-contained Components

Phase A's "What to discover" section (currently lines 30–43 of `skills/detect-sensors/SKILL.md`) gains:

1. **Broader evidence sources.** Beyond `go.mod`/`package.json` and source initialization sites, Phase A inspects:
   - `Dockerfile`, `docker-compose.yml` → `role: container-runtime`
   - `Chart.yaml`, `helm/`, `charts/` directories → `role: deployment-tool`, category `deployment/helm`
   - `kubernetes/`, `k8s/` directories or any YAML with `apiVersion:` + `kind: Deployment|Service|StatefulSet|...` → `role: deployment-tool`, category `deployment/k8s`
   - `*.tf` / `terraform/` → `role: deployment-tool`, category `iac/terraform`
   - `.github/workflows/*.yml`, `.gitlab-ci.yml`, `.circleci/config.yml`, `Jenkinsfile`, `azure-pipelines.yml` → `role: ci-cd`
   - `otel-collector*.yaml`, `otel-config.yaml` → `role: tracer` (collector config) with category `observability/tracer-collector`
   - `sentry.properties`, `.sentryclirc`, `sentry.client.config.*` → `role: error-tracker`, category `observability/errors`
   - `datadog.yaml`, `datadog-agent.yaml`, `dd-trace-*` package presence → `role: tracer` or `role: metrics` (category `observability/datadog`)
   - Stripe/SendGrid/Twilio/AWS-SDK client initialization (any language) → `role: external-integration`, category `integration/<vendor>`

   Phase A reads enough evidence to populate `evidence[]` honestly — never invent files. If a Helm chart isn't there, Helm isn't a component.

2. **Per Component, populate the three new fields.** For each detected Component, write:
   - `category` (canonical taxonomy)
   - `capabilities[]` — what this Component does *for this project*, derived from the LLM's knowledge of the library combined with what the project's evidence actually shows. Example: OTel is present, but `main.go` only calls `otel.SetTracerProvider` and never `otel.SetMeterProvider` → `capabilities` lists traces only, not metrics.
   - `observable_surface[]` — where the Component's behavior surfaces. Treat as raw material for Phase B's sensor authoring; do not pre-shape into regex or command syntax.

3. **Stack.yaml continues to be persisted** via existing `write-stack.go`. The new fields ride through unchanged — `schemas/stack.yaml` already validates them per the schema change above.

4. **Stop duplicating Role values in Phase A prose.** `skills/detect-sensors/SKILL.md:33` currently enumerates the 12 Role enum values verbatim ("`logger`, `log-encoder`, `http-server`, ..."). With the enum extended to 20, this duplication becomes a maintenance trap (schema and prose drift apart silently). Per the existing project convention "Skill markdown stays stack-driven, never enumerated" (memory), replace the verbatim list with a single instruction pointing at the schema: "for each runtime-observable role declared in `$defs/Role` of `schemas/stack.yaml`, ..." The schema is the single source of truth for Role values; the skill body merely references it.

### Phase A.5 — usecase ledger check

Unchanged. Continues to refuse sensor drafting when no usecases exist.

### Phase B — sensors as building blocks per Component

The §4 sensor-drafting prose (currently lines 166–225) is reframed:

**Authoring model:** For each Component in `stack.yaml`, draft 1+ building-block sensors that exercise its `capabilities` and probe its `observable_surface`. A building-block sensor:

- Targets a single Component or composes sensors that target Components (via `execution.steps[type=sensor]`).
- Lists in `use_cases[]` **every** usecase it helps validate, not just one. The 1:N is intentional — a sensor reused across 5 usecases is healthier than 5 near-identical sensors.
- Is named to communicate its role as a building block: `assert-*` for deterministic checks, `observe-*` for runtime observation, `setup-*` for idempotent preconditions. The naming conventions already in §1.5 carry over.

**Composition guidance:** When a usecase requires a chain (trigger → side-effect → assertion), do NOT draft a monolithic sensor that conflates the trigger with the assertion. Draft separate building blocks and compose:

- `observe-http-request-roundtrip` — trigger primitive; takes URL + method, captures status + latency + body excerpt.
- `assert-log-line-matches` — assertion primitive; takes regex, tail target.
- The usecase-validating sensor uses `execution.steps[type=sensor]` to chain them.

Component-to-archetype heuristic (LLM judgment, prose only — Rule #6 carve-out for genuinely non-deterministic reasoning):

| Component category | Typical building blocks |
|---|---|
| `observability/tracer` | `observe-trace-export-health`, `assert-trace-context-propagated`, `assert-sampler-config-valid` |
| `observability/errors` (Sentry) | `observe-error-capture-rate`, `assert-dsn-reachable`, `assert-before-send-no-pii` |
| `observability/datadog` | `observe-datadog-agent-forward`, `assert-dd-trace-injected` |
| `http/framework` (Gin/Echo/FastAPI/Express) | `observe-http-request-roundtrip`, `assert-route-404-rate`, `assert-handler-panic-recovered`, `assert-validation-rejection`, `assert-middleware-error-propagation` |
| `messaging/queue-consumer` | `observe-consume-error-rate`, `observe-dlq-rate`, `assert-rebalance-completes`, `setup-start-local-queue` |
| `messaging/queue-producer` | `observe-publish-success-rate`, `assert-nack-handled` |
| `cache/client` | `observe-cache-miss-rate`, `observe-connection-error`, `assert-eviction-policy-set` |
| `deployment/helm` | `assert-helm-lint-pass`, `assert-helm-template-renders`, `assert-helm-install-dryrun-pass`, `assert-values-schema-valid` |
| `deployment/k8s` | `assert-manifests-kubectl-apply-dryrun`, `assert-namespace-isolation` |
| `iac/terraform` | `assert-terraform-validate`, `assert-tfsec-clean`, `observe-terraform-plan-diff` |
| `ci/github-actions` | `assert-workflow-lint`, `assert-workflow-no-secret-in-logs` |
| `container-runtime` | `assert-image-builds`, `assert-image-no-root`, `assert-healthcheck-defined` |
| `integration/<vendor>` | `observe-<vendor>-api-error-rate`, `assert-<vendor>-credentials-refresh`, `assert-<vendor>-webhook-signature` (when applicable) |

The table is a *starter heuristic*, not a closed list. Real Components yield real building blocks shaped by their `observable_surface`.

**The §4.5 (lifecycle phases), §4.6 (wrap-vs-unroll), §5 (use_cases wiring), §6 (persist), §7 (run + iterate), §7.5 (heal-sensor handoff) sections remain unchanged.** They describe sensor *authoring* mechanics, not the *authoring model*; the building-block reframe sits on top.

### Phase A degraded path

The current degraded path (lines 63–77 of SKILL.md) persists a minimal stack when no logger is found. Spec C adds: if no Components at all are detected, but Phase A finds at least one deploy/CI artifact (`Dockerfile`, Helm chart, GitHub workflow), persist those as Components anyway with empty `log_shapes[]`. A project with just a Dockerfile + helm chart and no observable runtime still deserves `assert-image-builds` and `assert-helm-lint-pass` building blocks.

## Implementation order

A single atomic schema commit gates everything else. CI fires on push to main and on every PR, so each PR must be schema-coherent at HEAD.

### Step 1 — schema + lib/stack updates (atomic)

One commit, one PR:

1. `schemas/stack.yaml`:
   - Add `category`, `capabilities`, `observable_surface` to `$defs/Component` (optional fields).
   - Add 8 new values to `$defs/Role` enum.
2. `lib/stack/shape.go`: add corresponding Go struct fields (`Category string`, `Capabilities []string`, `ObservableSurface []string`). Tag with `yaml:"category,omitempty"` etc.
3. `lib/stack/load_test.go`: add cases verifying:
   - A stack.yaml with the new fields loads cleanly and round-trips.
   - A stack.yaml without them still loads (backwards compat).
   - A Component with an extended Role value (`error-tracker`, `deployment-tool`, etc.) loads cleanly.
   - A Component with an unknown role value still rejects (enum closed).
4. `lib/stack/validate.go`: no cross-checks needed for the new fields (they are descriptive, not relational).
5. `.harness/stack.yaml`: regenerate the framework's own stack with the new fields populated for its existing 3 Components. This dogfoods the change and prevents schema drift between the spec and the canonical example.

CI must pass green on this commit alone.

### Step 2 — SKILL.md update

Single commit. Edit `skills/detect-sensors/SKILL.md`:

- Phase A "What to discover" prose: add the broader evidence sources list (Dockerfile, Helm, k8s, terraform, CI workflows, OTel/Sentry/DD configs, integration SDK clients).
- Phase A: add the "per Component, populate the three new fields" instruction.
- Phase A degraded path: extend to handle deploy-artifact-only projects.
- Phase B §4: replace the "for each capability, draft a sensor" framing with the "for each Component, draft building-block sensors" framing. Insert the component-to-archetype table.
- Update the running examples (the `run-project-nest` template in §4 already cites `log_shapes`; add a note that its `use_cases: [...]` should list every journey variation it helps validate, including the boot variation AND the runtime-observation variation).

The skill body grows ~80 lines net. Rule #10 (no temporal/version-history content) is honored — the prose describes the *current* model in present tense.

### Step 3 — framework self-application

**Depends on Step 1 having merged first.** Step 3 may surface new Components (e.g., GitHub Actions from `.github/workflows/test.yml`) that require the new Role values; persisting them via `write-stack.go` against an un-extended schema would fail validation. Steps 1 and 2 may share a PR; Step 3 must wait for Step 1 to land.

Re-run `/detect-sensors` against the framework itself:

1. The 3 existing `.harness/sensors/*.yaml` (`assert-create-sensor-multi-angle`, `smoke-typed-pipeline`, `smoke-with-setup`) remain — they target framework usecases that survived Spec B's restructure.
2. Author 2–3 *new* framework-scope building-block sensors to demonstrate the model:
   - `assert-stack-schema-valid` — building block validating that `.harness/stack.yaml` parses against `schemas/stack.yaml` (deterministic Go scan; targets the framework's own self-application discipline).
   - `assert-detect-sensors-self-contained` — manual acceptance sensor asserting that `.harness/stack.yaml` has at least one Component with all three new fields populated (`category`, `capabilities`, `observable_surface` non-empty). Skipped in CI like `assert-create-sensor-multi-angle` is.

These prove the model on the framework's own stack before users encounter it.

### Step 4 — usecases for the new framework sensors

Author usecases under `.harness/usecases/framework/`:

- `framework-detect-sensors-component-self-contained.yaml` — describes the journey variation "/detect-sensors fills all three new fields on every detected Component".
- `framework-detect-sensors-deploy-artifacts-detected.yaml` — describes "Phase A surfaces Helm/Dockerfile/CI as Components".

Each usecase carries `trigger`, `behavior`, `expected_outcome` with concrete fixtures (snippets of the expected stack.yaml shape). These wire into the new framework sensors' `use_cases[]`.

### Step 5 — CI gate update

`.github/workflows/test.yml`:

- The existing `validate every sensor YAML against schemas/sensor.yaml` step continues to cover the new framework sensors.
- The existing `validate .harness/stack.yaml against schemas/stack.yaml` step continues to cover the regenerated stack.yaml.
- Add `assert-detect-sensors-self-contained` to the skip-by-id list in the sensors gate (manual acceptance, like `assert-create-sensor-multi-angle`).

No new build tags, no new go test invocations — the schema work in Step 1 is exercised by `go test ./lib/...` which is already in CI.

## Acceptance criteria

A reviewer can confirm Spec C by:

1. **Schema check.** `schemas/stack.yaml` defines `category`, `capabilities`, `observable_surface` as optional on `Component`, and `Role` lists the 8 new values. `go test ./lib/stack/...` covers both the new shapes and the backward-compat case.
2. **Framework self-application.** `.harness/stack.yaml` shows the 3 existing Components plus any new ones (e.g., GitHub Actions detected from `.github/workflows/test.yml`) with `category`, `capabilities`, `observable_surface` populated. Every Component has all three fields non-empty.
3. **Skill body coherence.** Reading `skills/detect-sensors/SKILL.md` from §0 through §8 explains the building-block model in present tense — no "used to be", no "after PR #X", no migration notes.
4. **Sensor reusability evidence.** At least one of the *new* framework sensors authored in Step 3 (not a pre-existing sensor) lists ≥2 usecases in its `use_cases[]`, proving Spec C's 1:N building-block intent in the artifacts the spec itself produces.
5. **CI green.** Both jobs (`go test + vet` and `sensors — run plugin against itself`) pass on the PR.

## Out of scope

- **Catalog of known libraries with pre-curated capabilities/observable_surface entries.** Spec C does not introduce `lib/components/` or any Go catalog. The LLM fills the three new fields from its own knowledge of the library combined with what the project's evidence shows. Future specs may revisit if drift between LLM-filled and curated entries becomes noticeable.
- **Auto-composition of sensors.** Spec C drafts building blocks; chaining them with `steps[type=sensor]` per usecase is `/create-sensor`'s job (Spec B). `/detect-sensors` does not generate the composed sensors automatically.
- **Schema enforcement of category taxonomy.** `category` is free-form string. Conventions (`domain/role` slugs) live in SKILL.md, not the schema. A future spec may close the taxonomy if enough convention drift accumulates.
- **Sensors for languages/runtimes themselves.** `languages[]` is a separate top-level stack.yaml field; Components describe libraries/frameworks/artifacts. Spec C does not redefine that boundary.
- **Backfilling existing user stack.yaml files.** Users who regenerate via `/detect-sensors --refresh-stack` get the new fields; existing files keep working without them. No migration script.

## References

- `schemas/stack.yaml` — current schema definition.
- `schemas/sensor.yaml` — sensor schema, unchanged by this spec.
- `skills/detect-sensors/SKILL.md` — current skill body to be updated.
- `docs/superpowers/specs/2026-05-17-create-sensor-multi-angle-design.md` — Spec B, source of the `use_cases[]` 1:N traceability.
- Spec A (complex commands) shipped as PR #58 — introduced `execution.steps[type=sensor]` for composition.
- CLAUDE.md rules #6 (deterministic logic in Go), #10 (no temporal content in skill bodies), #11 (testdata vs testhelpers vs `.harness/fixtures`).
