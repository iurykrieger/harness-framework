---
name: detect-sensors
description: Use when the user invokes /detect-sensors or asks to scaffold a harness for the project they are working in. Inspects the project to infer its archetype(s) (frontend, backend API, event-consumer, event-producer, IaC, CLI, library, data-pipeline, ...), reasons about which capabilities each archetype typically exposes (lint, build, unit-test, e2e-test, integration-test, run-project, fetch-logs, fetch-metrics, trace-request, ...), drafts one sensor per capability conforming to schemas/sensor.yaml, and persists each through the validator script at `<project>/.harness/sensors/<sensor-id>.yaml` so the user can immediately invoke `/run-sensor <sensor-id>`.
---

# detect-sensors

Bootstrap a project's sensor harness end-to-end. The detection step is **your judgment**: look at the project, decide what kind of system it is, decide which capabilities deserve a sensor, and draft the YAML. The persistence step is the helper script `scripts/write-sensor.go`, which is the only deterministic part of the loop — it validates each draft against `schemas/sensor.yaml` and writes it to the target directory.

There is no fixed taxonomy of project types or capabilities baked into the script. If the project is a Pulumi stack with custom drift checks, sensors emerge from *that* shape; if it is a Kafka consumer with a Confluent schema registry, sensors emerge from *that* shape. The fact that this is an LLM skill is the whole point — exploit that.

## Invocation

```
/detect-sensors [project-path]
```

If the user supplies no argument, scan the cwd. The output directory is always `<project>/.harness/sensors/`. Do not ask the user to choose; create it if it does not exist.

## Procedure

### 0. Stack discovery (Phase A)

Before drafting any observation sensor, synthesize a structured description of the project's stack and persist it to `<project>/.harness/stack.yaml` via `schemas/stack.yaml`. This artifact is reused across `/detect-sensors` invocations and consumed in §4 below when drafting `kind=observation` + `output=stream` sensors.

**When to run Phase A:**
- Default: only if `<project>/.harness/stack.yaml` does NOT already exist. Reuse on every subsequent invocation.
- Always when the user passes `--refresh-stack`.

**What to discover:**

1. **Languages** — read `go.mod`, `package.json` engines, `pyproject.toml` requires-python, `Cargo.toml`, `pom.xml`, `build.gradle`, `Gemfile`. Capture name + version per language present.
2. **Components** — for each runtime-observable role declared in `$defs/Role` of `schemas/stack.yaml` (the schema is the single source of truth — do not duplicate the value list in this prose):
   - Identify the library actually used (Zap, Logrus, Pino, Winston, Logback, structlog, …).
   - **Open the initialization site** (`cmd/server/main.go`, `src/main.ts`, `app/__init__.py`, etc.) and read enough code to determine the CONCRETE config — not just "Zap is used" but "`zap.NewProductionConfig()` is called and `EncoderConfig.LevelKey` is overridden to `severity`".
   - Record an `evidence[]` entry pointing at the file (with line numbers when feasible) and a one-sentence `rationale`.
   - **Beyond code manifests, inspect deploy and observability artifacts:**
     - `Dockerfile`, `docker-compose.yml` → `role: container-runtime`, category `runtime/container`.
     - `Chart.yaml`, `helm/`, `charts/` directories → `role: deployment-tool`, category `deployment/helm`.
     - `kubernetes/`, `k8s/` directories or any YAML with `apiVersion:` + `kind: Deployment|Service|StatefulSet|…` → `role: deployment-tool`, category `deployment/k8s`.
     - `*.tf` / `terraform/` → `role: deployment-tool`, category `iac/terraform`.
     - `.github/workflows/*.yml`, `.gitlab-ci.yml`, `.circleci/config.yml`, `Jenkinsfile`, `azure-pipelines.yml` → `role: ci-cd`, category `ci/<vendor>`.
     - `otel-collector*.yaml`, `otel-config.yaml`, OpenTelemetry SDK initialization → `role: tracer` or `role: metrics`, category `observability/<tracer-collector|metrics>`.
     - `sentry.properties`, `.sentryclirc`, `sentry.client.config.*`, Sentry SDK initialization → `role: error-tracker`, category `observability/errors`.
     - `datadog.yaml`, `datadog-agent.yaml`, `dd-trace-*` package presence → `role: tracer` or `role: metrics`, category `observability/datadog`.
     - Stripe/SendGrid/Twilio/AWS-SDK service-client initialization (any language) → `role: external-integration`, category `integration/<vendor>`.

     Evidence rules apply unchanged: every Component MUST have an `evidence[]` entry pointing at a real file. If a Helm chart isn't there, Helm isn't a Component.
   - **For every detected Component, populate the three self-containment fields** in addition to `role`, `name`, `version`, `config_summary`, `evidence`:
     - **`category`** — canonical taxonomy slug from the convention `<domain>/<role>` (e.g., `observability/tracer`, `http/framework`, `deployment/helm`). Free-form by the schema, conventional by this skill.
     - **`capabilities[]`** — what this Component does **for this project specifically**, not generic library capabilities. Derive from the Component's evidence + your knowledge of the library. If OTel is present but `main.go` only calls `otel.SetTracerProvider` and never `otel.SetMeterProvider`, list traces and NOT metrics. Capabilities the project doesn't exercise MUST NOT be listed.
     - **`observable_surface[]`** — where evidence of this Component's behavior appears: log lines, config files, endpoints, commands, env vars. Keep entries as short prose pointers — Phase B sensor authoring converts them into concrete regex patterns or probe commands. Do NOT pre-shape into regex/command syntax here.
3. **Log shapes** — for each distinct stdout shape the components produce:
   - Pick a kebab-case `id` (e.g. `zap-prod-json`, `chi-access-log`, `panic-stack-trace`).
   - List the `produced_by[]` component names (verbatim from `components[].name`).
   - Pick the `format` enum: `json` for structured JSON loggers, `logfmt` for key=value, `combined-log-format` for Apache/Nginx-style access logs, `stack-trace` for panic dumps, `plain` for human-text fallbacks.
   - When `format` is `json` or `logfmt`, populate `fields[]` mapping literal keys to semantic `meaning` (severity, message, timestamp, trace_id, span_id, status_code, latency_ms, method, path, user_id, request_id, service, version, other). Use `meaning: "other"` as escape hatch for project-specific keys.
   - When the shape has a `severity` field, populate `severity_values[]` with the values the project actually emits — for Zap: `["DEBUG","INFO","WARN","ERROR","DPANIC","PANIC","FATAL"]`; for Pino numeric: `["10","20","30","40","50","60"]`.
   - Provide a `sample` string: one real line in this shape (capture from a CI log, test fixture, or synthesize one from the library docs + the config you observed).

**Concrete examples for the four most common stacks:**

- **Go + Zap (production config)** → one `LogShape` with `format: "json"`; `fields[]` includes `severity` (key=`level` by default, `severity` if overridden), `ts` (timestamp), `msg` (message), `caller` (other). `severity_values: ["DEBUG","INFO","WARN","ERROR","DPANIC","PANIC","FATAL"]`.
- **Node + Pino (default config)** → `format: "json"`; `fields[]` includes `level` (severity, NUMERIC), `time` (timestamp), `msg` (message), `req` and `res` (other) when `pino-http` is wired. `severity_values: ["10","20","30","40","50","60"]` (trace/debug/info/warn/error/fatal).
- **Python + structlog (JSONRenderer)** → `format: "json"`; `fields[]` keys depend on the processor chain — common defaults are `level` (severity), `timestamp`, `event` (message). `severity_values: ["debug","info","warning","error","critical"]` (Python logging level names lowercased).
- **Java + Logback (default pattern)** → `format: "plain"`; no structured fields. If the project uses `logstash-logback-encoder` for JSON output, it becomes `format: "json"` with `fields[]` for `@timestamp`, `level`, `message`, `logger_name`.

**Then call `write-stack.go`:**

```bash
go run -tags=write_stack ./skills/detect-sensors/scripts \
  --out=<project-root> \
  --schemas-dir=<plugin-root>/schemas \
  <draft-stack.yaml>
```

It validates against `schemas/stack.yaml`, cross-checks that every `log_shapes[].produced_by[]` references a known `components[].name`, and writes `<project-root>/.harness/stack.yaml` atomically.

### 0.5 Stack discovery — degraded path

If after a thorough search you cannot identify any logger, HTTP middleware, OR runtime-observable component, but Phase A's evidence sweep surfaced at least one deploy/CI/runtime artifact (`Dockerfile`, `Chart.yaml`/`helm/`, `k8s/`, `*.tf`, `.github/workflows/`), persist a **deploy-only stack**:

```yaml
version: "0.1.0"
detected_at: "<now>"
detected_by: "<your-model-id-or-manual>"
languages:
  - name: "<best-guess>"
components:
  # Whatever artifacts you found — populated with the three
  # self-containment fields per the rules above. Even a single
  # Dockerfile is enough to author `assert-image-builds`.
  - role: container-runtime
    name: docker
    category: runtime/container
    capabilities:
      - Builds a container image from `Dockerfile`.
    observable_surface:
      - "`docker build` exit code and stderr."
    evidence:
      - file: Dockerfile
        rationale: Present at repo root.
log_shapes: []
```

If after the thorough search NO components and NO deploy artifacts are found at all, persist the truly minimal degenerate stack (empty `components`, empty `log_shapes`) — Phase B will note this in `blind_spots[]` of every drafted sensor and the user iterates manually after observing real output.

### Phase A.5: Usecase ledger check

Sensors produced by /detect-sensors MUST reference real usecase ids. Before drafting any sensor, list the existing usecases:

```bash
find .harness/usecases -type f -name '*.yaml' 2>/dev/null
```

If no usecases exist yet, emit a Signal with `verdict=error metadata.kind=usecases_missing`, surface the remediation ("run `/detect-usecases` first"), and DO NOT proceed with sensor drafting. /detect-sensors cannot emit a schema-valid sensor with empty `use_cases[]` (the schema requires `minItems: 1`).

If usecases DO exist, build a quick mental index of which usecases belong to which journey — sensors will be wired to the most relevant ones during drafting (Phase B).

### 1. Read the schema first

Always start by reading `schemas/sensor.yaml`, `schemas/signal.yaml`, and `schemas/stack.yaml` from this plugin so your drafts match the current shape (required fields, discriminators, enum values). The schema is the contract — never guess it from memory.

Pay attention to the `allOf` discriminators:

- `type: "computational"` → requires `cost.compute` + `execution.{command, exit_code_map}`; forbids `cost.tokens` and the inferential `execution` fields.
- `type: "inferential"` → requires `cost.tokens`, `execution.{command, model, system_prompt, user_prompt_template, decoding}`, and a top-level `calibration` block.
- `output: "single"` → forbids `execution.output_parsing`.
- `output: "stream"` → requires `execution.output_parsing.patterns` (≥1).

Pick `output` deliberately:

- **`stream`** when the underlying tool emits one observation per line that is independently meaningful — test runners (`--- PASS:` / `--- FAIL:` per test), linters/compilers (`file:line:col: msg` per finding), log fetchers (one matched event per line). On a clean run the stream is empty and the aggregate verdict comes from the exit code; on a dirty run each finding becomes its own actionable Signal.
- **`single`** only when the tool's output is binary or unstructured and the exit code is the entire story — schema parsers, dry-run deploys, smoke pings. If you would have to write a regex to fake a "success line" just to get a non-zero count, you picked the wrong mode — switch to `stream` and let the empty-stream + exit-0 case represent success honestly.

`inferential` is reserved for verdicts that genuinely require LLM judgment (AI code review, semantic-duplicate detection). Most CI-mirroring sensors are computational.

The `stack.yaml` schema is the contract for Phase A (§0). When drafting observation sensors (§4), you'll consult the persisted `<project>/.harness/stack.yaml` — not the schema directly.

### 1.5 Classify each sensor: kind = observation | assertion | setup

Every sensor MUST declare `kind` (top-level, required). Pick by purpose:

- **observation** — observes behavior with no fixed expectation. Verdict describes the *health of the observation*, not pass/fail of an assertion. Examples: `run-project-nest`, `fetch-logs-cloudrun`, `fetch-metrics-datadog`, `trace-request`, `watch-build`, `tail-logs-local`. Naming convention: `run-*`, `watch-*`, `fetch-*`, `trace-*`, `tail-*`.
- **assertion** — checks against an expectation. Verdict pass/fail is semantic. Examples: `lint-eslint`, `unit-test-vitest`, `e2e-playwright`, `type-check-tsc`, `build-vite`, `schema-validate-json`, `validate-plugin-manifest`. Naming convention: `lint-*`, `build-*`, `unit-test-*`, `integration-test-*`, `e2e-*`, `validate-*`, `schema-*`.
- **setup** — idempotent auxiliary sensor that makes a precondition true. Typically referenced by other sensors via `requires[kind=sensor]`. Examples: `start-postgres`, `setup-env-from-example`, `install-deps-pnpm`, `login-gcloud`, `seed-db`, `provision-tunnel`. Naming convention: `start-*`, `setup-*`, `install-*`, `seed-*`, `login-*`, `provision-*`. Setup sensors MUST be idempotent (re-running with the same input is a no-op when the world is already in the desired state); document the strategy in `description` (`"test -f .env || cp .env.example .env"`, `"docker compose up -d postgres"` is idempotent by default, etc.).

Inferential setup sensors are technically allowed but **discouraged**: setup operations should be deterministic and idempotent; LLM-driven setup is neither. Do not emit `kind: "setup"` paired with `type: "inferential"`.

Pick the **lifecycle** deliberately too:

- **One-shot** (default, `execution.blocking` omitted or `false`) — the command runs to natural completion. `cost.latency.timeout_ms` is a hard cap; exceeding it forces `verdict=error`. Suits tests, builds, linters, parsers, dry-runs.
- **Blocking** (`execution.blocking: true`) — the command does not terminate on its own and must be invoked via `/start-sensor` / `/stop-sensor` (not `/run-sensor`). The runner spawns the process, the watcher streams pattern-matched Signals while it runs, and `/stop-sensor` produces the aggregate. `cost.latency.timeout_ms` is forbidden for blocking sensors; instead, `execution.graceful_timeout_ms` controls the SIGTERM→SIGKILL window. Use this for sensors whose value is observation while the process runs (e.g., `npm run dev`, `make watch`, log tailers, replay loops). Pair with `output: stream` (the schema enforces this) and patterns that capture both failure modes (errors, port collisions) and success markers (boot lines, ready probes). Other sensors may declare a blocking sensor via `requires[kind=sensor]`; the orchestrator will start (or attach to) it before the dependent runs and stop it at teardown when no other dependent holds it.

### 2. Inspect the project

Use Read, Glob, Bash to build a picture of the project. At minimum:

- **Project documentation — read this FIRST**: `README.md`, `CLAUDE.md`, `AGENTS.md`, `GEMINI.md`, `CONTRIBUTING.md`, plus any top-level guides under `docs/`. Maintainers document the commands they actually run — usually under headings like `## Build`, `## Test`, `## Run`, `## Development`, `## Quickstart`. Treat both fenced code blocks (```` ```bash ... ``` ````) AND inline prose mentions (e.g. "run `make build` to compile") as authoritative. These commands are the **canonical invocations** the project's humans rely on; everything else is secondary evidence.
- Top-level layout (`ls -la`), `git log` for recency.
- Manifest files: `package.json`, `go.mod`, `Cargo.toml`, `pyproject.toml`, `requirements.txt`, `Gemfile`, `composer.json`, `pom.xml`, `build.gradle`, `Chart.yaml`, `helmfile.yaml`, `Pulumi.yaml`, `*.tf`, `serverless.yml`, `wrangler.toml`, `Dockerfile`, `docker-compose.yml`.
- CI definitions: `.github/workflows/`, `.gitlab-ci.yml`, `.circleci/`, `Jenkinsfile`, `azure-pipelines.yml`. These often reveal the *real* commands used in production.
- Source layout signals: `pages/` or `app/` (Next.js), `cmd/` + `internal/` (Go service), `src/handlers/` (Lambda), `consumer.go`/`*Consumer.cs` (event consumer), `*Publisher.*` / `*Producer.*` (event producer), `infrastructure/` or `iac/` (IaC), `migrations/` (DB-bound service).
- Observability hints: `Dockerfile`, `*.deployment.yaml`, `terraform/.../monitoring/`, `datadog.yaml`, `otel-collector-config.yaml`, `prometheus.yml`, `grafana/dashboards/`, `serverless.yml` events, log driver configs.

You are looking for *both* the archetype and the concrete commands. A `package.json` script is a literal command; a CI workflow step is a literal command; a Dockerfile `CMD` is a literal command — but a command written down in `CLAUDE.md` / `README.md` / `AGENTS.md` is the **most authoritative** literal command, because the maintainers wrote it down on purpose. When the literal command is unclear, propose a reasonable one and flag it in the sensor's `description`/`blind_spots`.

**Command-source precedence when sources disagree:**

1. `CLAUDE.md` / `AGENTS.md` / `GEMINI.md` — explicit agent guidance, highest signal.
2. `README.md` / `CONTRIBUTING.md` / `docs/*` — human-facing canonical recipes.
3. CI workflow steps (`.github/workflows/*`, `.gitlab-ci.yml`, etc.) — real production invocations, but tend to wrap things in matrices or composite actions that don't translate cleanly to a single shell command.
4. `Dockerfile` `CMD`/`ENTRYPOINT` — production runtime for run-style sensors.
5. `Makefile` targets / `Taskfile` aliases / `package.json` scripts — developer-facing wrappers; useful when nothing more authoritative exists. When the canonical command IS such a wrapper (e.g. `make dev`, `pnpm dev`, `task run`), apply the wrap-vs-unroll rule in §4.6 — do NOT silently inline the wrapper's body into `execution.command`.

When a docs-sourced command and an inferred one disagree (e.g. README says `pnpm vitest run --coverage` but `package.json` has `"test": "vitest"`), the docs win — capture the documented form verbatim in `execution.command`. Note the source in `description` (e.g. *"Auto-detected via /detect-sensors from CLAUDE.md '## Build, validate, test' section"*) so the audit trail points at the exact heading the command came from.

### 3. Classify the project archetype(s)

A project may be more than one — a frontend + IaC + backend monorepo is common. Pick the archetype labels that fit, then look up the typical capability set for each. Use this as a *starting heuristic*, not a closed list:

| Archetype          | Typical capabilities                                                                    |
|--------------------|-----------------------------------------------------------------------------------------|
| Frontend SPA / SSR | lint, type-check, build, unit-test, component-test, e2e-test, visual-regression, run-project |
| Backend API        | lint, build, unit-test, contract-test, integration-test, run-project, fetch-logs, fetch-metrics, trace-request |
| Event consumer     | lint, build, unit-test, contract-test, integration-test (with broker), run-project, fetch-consumer-lag, fetch-error-rate, replay-message |
| Event producer     | lint, build, unit-test, schema-validation, run-project, fetch-publish-rate, fetch-error-rate |
| IaC (Terraform/Pulumi/CDK) | lint (tflint/cfn-lint), validate, plan, security-scan (tfsec/checkov), drift-detect |
| CLI tool           | lint, build, unit-test, install-test, run-project                                       |
| Library / SDK      | lint, build, unit-test, doc-build, package-publish-dry-run                              |
| Data pipeline      | lint, build, unit-test, sample-data-test, run-project, fetch-pipeline-metrics           |

Add or drop capabilities to match what you see. If the project sets up Sentry, draft a `fetch-error-rate` sensor that hits Sentry's CLI; if it uses GCP Cloud Logging, `fetch-logs` becomes `gcloud logging read ...`; if there is no observability stack at all, omit the observability sensors instead of generating dead placeholders.

### 4. Draft each sensor

**Authoring model — sensors as building blocks per Component.** For each Component in `<project>/.harness/stack.yaml`, draft 1+ reusable building-block sensors that exercise its `capabilities` and probe its `observable_surface`. A building-block sensor:

- Targets a single Component, or composes other sensors that target Components (via `execution.steps[type=sensor]`).
- Lists **every** usecase it helps validate in `use_cases[]` — not just one. The 1:N is intentional: a sensor reused across 5 usecases is healthier than 5 near-identical sensors.
- Is named to communicate its role: `assert-*` for deterministic checks, `observe-*` for runtime observation, `setup-*` for idempotent preconditions (the naming conventions in §1.5 carry over).

When a usecase requires a chain (trigger → side-effect → assertion), do NOT draft a monolithic sensor that conflates the trigger with the assertion. Draft separate building blocks and compose:

- `observe-http-request-roundtrip` — trigger primitive; takes URL + method, captures status + latency.
- `assert-log-line-matches` — assertion primitive; takes a regex and a tail target.
- A usecase-validating sensor uses `execution.steps[type=sensor]` to chain them.

Component-to-archetype starter heuristic (LLM judgment, prose — Rule #6 carve-out for genuinely non-deterministic reasoning):

| Component category | Typical building blocks |
|---|---|
| `observability/tracer` (OTel) | `observe-trace-export-health`, `assert-trace-context-propagated`, `assert-sampler-config-valid` |
| `observability/errors` (Sentry, Bugsnag) | `observe-error-capture-rate`, `assert-dsn-reachable`, `assert-before-send-no-pii` |
| `observability/datadog` | `observe-datadog-agent-forward`, `assert-dd-trace-injected` |
| `http/framework` (Gin/Echo/FastAPI/Express) | `observe-http-request-roundtrip`, `assert-route-404-rate`, `assert-handler-panic-recovered`, `assert-validation-rejection`, `assert-middleware-error-propagation` |
| `messaging/queue-consumer` | `observe-consume-error-rate`, `observe-dlq-rate`, `assert-rebalance-completes`, `setup-start-local-queue` |
| `messaging/queue-producer` | `observe-publish-success-rate`, `assert-nack-handled` |
| `cache/client` (Redis, Memcached) | `observe-cache-miss-rate`, `observe-connection-error`, `assert-eviction-policy-set` |
| `deployment/helm` | `assert-helm-lint-pass`, `assert-helm-template-renders`, `assert-helm-install-dryrun-pass`, `assert-values-schema-valid` |
| `deployment/k8s` | `assert-manifests-kubectl-apply-dryrun`, `assert-namespace-isolation` |
| `iac/terraform` | `assert-terraform-validate`, `assert-tfsec-clean`, `observe-terraform-plan-diff` |
| `ci/<vendor>` | `assert-workflow-lint`, `assert-workflow-no-secret-in-logs` |
| `runtime/container` (Dockerfile) | `assert-image-builds`, `assert-image-no-root`, `assert-healthcheck-defined` |
| `integration/<vendor>` | `observe-<vendor>-api-error-rate`, `assert-<vendor>-credentials-refresh`, `assert-<vendor>-webhook-signature` (when applicable) |

The table is a starter heuristic, not a closed list. Real Components yield real building blocks shaped by their `observable_surface`.

**Mandate: never skip a sensor.** Emit one for every capability the project plausibly has, even when:

- The exact command isn't 100% determined — pick the closest production-shaped invocation (Dockerfile `CMD`, CI step, README "Run locally" recipe), put it in `execution.command`, and document the assumption in `blind_spots[]`.
- Required config files are gitignored (`.env*`, `config/*.local.*`, RSA keys) — they exist on the developer's machine; reference them in the command, declare the env names you need under `requires[kind=env]`, and let the runner abort with a clear remediation if the user runs it without them.
- The capability needs auth tokens, project ids, or other secrets — declare them under `requires[kind=env]`, NEVER hardcode. Sensor existence is independent of credential availability *right now*: the user (or a future agent turn) provides the value out of band when they invoke `/run-sensor`.
- The command runs continuously (watchers, dev servers, log tailers) — set `execution.blocking: true` and use the `/start-sensor` workflow and pair with `output: "stream"` patterns; do not downgrade to a proxy command just to keep things one-shot. See §1's lifecycle guidance.
- You aren't sure your patterns will catch every failure mode — that's what §7's iteration loop is for. Ship a v0.1.0 with your best guess and iterate via `/run-sensor` until the output is informative.

The only legitimate reason to omit a capability is "the project genuinely doesn't have it" (no observability stack at all → no `fetch-logs` sensor; no IaC → no `terraform-validate`). Never omit because credentials, watchers, or partial knowledge make the run-time path harder.

For every capability that survives step 3, draft a sensor object. The id MUST be kebab-case starting with a letter (`^[a-z][a-z0-9-]*$`) and unique across the directory — combine capability + tool + (optional) scope, e.g. `lint-eslint`, `build-go`, `fetch-logs-cloudrun`, `unit-test-pytest-domain`, `run-project-nest`.

Use these defaults unless the project tells you otherwise:

- `version: "0.1.0"` for the first cut of a sensor; bump (`0.2.0`, `0.3.0`, ...) on every iteration that changes `output`, `execution`, or `use_cases`. The version stamp is the audit trail readers compare against.
- `type: "computational"`, `determinism: "high"` for command-style sensors. Pick `output` per the rule in step 1.
- `cost.class` cheap for static analysis, medium for unit/contract tests, expensive for e2e/integration/observability fetches.
- `cost.latency` tuned to the capability's actual runtime (use the CI logs as a sanity check when available).
- `triggers[].on` from `{pull-request, file-change, cron, metric-anomaly, manual, agent-request}` only — do NOT confuse this with `phase`.
- `execution.exit_code_map` defaults to `[{exit_code: 0, verdict: pass, severity: info}, {exit_code: "*", verdict: fail, severity: <medium|high>}]`. Override per capability.
- **For `kind=observation` + `output=stream` sensors (Phase B):** do NOT hand-craft regexes. Instead:
  1. Load `<project>/.harness/stack.yaml` (produced by §0; if missing or empty, fall through to the degraded path below).
  2. Filter `log_shapes[]` to the shapes relevant to the sensor's command. For `run-*` / `watch-*` sensors observing a running service, that typically means shapes produced by any runtime-observable component role — i.e. roles describing services that emit stdout/stderr (or otherwise observable lines) at runtime, as enumerated by `$defs/Role` in `schemas/stack.yaml`. For `tail-*` / `fetch-*` sensors against external log stores, pick the shape whose encoder matches what the store emits.
  3. For each selected shape, derive patterns from TWO orthogonal axes (severity classes AND per-event observation). A shape may satisfy one, the other, or both — emit the union, severity-first so a logger-labelled crash beats the per-event catch-all when both match the same line. Aim for 2–6 patterns per shape total (don't fan out beyond that).
     - **Severity-based patterns** (only when the shape declares `severity_values[]`). For each tier present in `severity_values`, anchor the regex on the literal severity key (`fields[].key` where `meaning == "severity"`) plus the value. Map values to verdicts:
       - `severity ∈ {ERROR, FATAL, DPANIC, PANIC}` (or library equivalent — `critical`, `60` for Pino numeric, etc.) → `verdict: fail, severity: high`.
       - `severity == WARN` AND a `status_code` field with value in `4xx/5xx` → `verdict: fail, severity: medium`.
       - `severity == WARN` (other) → `verdict: warn, severity: low`.
       - `severity == INFO` AND `message` matches a boot/ready marker (e.g. `"Listening on"`, `"server started"`, `"ready"`, framework-specific banners) → `verdict: pass, severity: info`.
     - **Per-event observation patterns.** The fundamental promise of a `kind=observation` sensor is one Signal per discrete unit of work the system processes: one per HTTP request for http-api archetypes, one per consumed message for queue-consumer, one per produced message for queue-producer, one per call for rpc, one per query for db-client, one per execution for scheduler. A sensor that observes a running service WITHOUT emitting per-unit Signals blinds the harness to the dense observability surface that justifies the sensor's existence — this is the difference between "is the service alive?" (a boot-only Signal) and "what is the service actually doing right now?" (one Signal per request/message/call).

       Trigger this branch when any of the following is true of the shape: `format == "combined-log-format"`; any `fields[].meaning ∈ {status_code, method, path, latency_ms}`; any `produced_by[]` component has role in `{http-server, http-router, http-middleware, queue-consumer, queue-producer, rpc, db-client}`. Severity does NOT gate this branch — the per-event outcome class IS the authority on the verdict, regardless of which severity the framework chose to label the line with.

       Identify the shape's **outcome** semantic — the field, marker, or substring that distinguishes success from failure in this archetype's encoding — and emit 3–4 patterns mapping outcome classes onto verdicts:
       - **Success outcome** → `verdict: pass, severity: info`. The dense observation channel; expect this to match the majority of matched lines on a healthy system.
       - **Recoverable / client-side failure** → `verdict: warn, severity: low`. Per-event trouble that is NOT a service-health regression (HTTP 4xx, queue retry, RPC cancellation, DB constraint violation).
       - **Unrecoverable / service-side failure** → `verdict: fail, severity: high`. Per-event trouble that IS a service-health regression (HTTP 5xx, consumer exception, RPC INTERNAL, DB deadlock).
       - **Silent drop / dead-letter** (queue archetypes only; optional fourth tier) → `verdict: warn, severity: medium`. The consumer accepted the event but marked it unprocessable.

       Concrete outcome semantics per archetype (use the shape's literal `fields[].key`, not the `meaning`, when building each regex — the LLM consults the shape, not memory):
       - **HTTP** (`http-server`/`http-router`/`http-middleware`) — HTTP status code class. `2\d{2}`/`3\d{2}` → success; `4\d{2}` → recoverable; `5\d{2}` → unrecoverable.
       - **Queue consumer** (`queue-consumer`) — processing result. `committed`/`acked`/`processed`/`commit-offset` → success; `retry`/`retrying`/`will-retry` → recoverable; `failed`/`exception`/`error`/`uncaught` → unrecoverable; `dlq`/`dead-letter`/`dropped`/`poison-message` → silent-drop tier.
       - **Queue producer** (`queue-producer`) — publish result. `published`/`acked`/`ack`/`delivered` → success; `retry`/`retrying` → recoverable; `nack`/`failed`/`send-error` → unrecoverable.
       - **RPC** (`rpc`) — gRPC/SDK status. `OK`/`grpc.code: OK`/`status: 0` → success; `CANCELLED`/`DEADLINE_EXCEEDED`/`UNAUTHENTICATED`/`PERMISSION_DENIED`/`NOT_FOUND`/`ALREADY_EXISTS` → recoverable; `UNAVAILABLE`/`INTERNAL`/`DATA_LOSS`/`UNKNOWN`/`UNIMPLEMENTED` → unrecoverable.
       - **DB client** (`db-client`) — query result. `commit`/`success`/SQLSTATE `00000` → success; constraint violation / unique-key / FK violation / SQLSTATE `23xxx` → recoverable; deadlock / connection-loss / serialization-failure / SQLSTATE `40xxx`/`08xxx` → unrecoverable.

       Anchor every regex on the shape's actual encoding so it cannot drift from reality. Common templates:
       - For `format: combined-log-format` (Apache/Nginx access log): `'^\S+ \S+ \S+ \[[^\]]+\] "(\S+) (\S+)[^"]*" (2\d{2}) '` and class variants for `(3\d{2})`/`(4\d{2})`/`(5\d{2})`. Use `captures.{excerpt: 2, rationale: 3}` so the Signal carries path and status.
       - For `format: json` / `logfmt` with a structured outcome field, use the **literal key** from `fields[].key`: e.g. `'"status_code"\s*:\s*(2\d{2})\b'` (HTTP), `'"grpc.code"\s*:\s*"OK"'` (RPC), `'"outcome"\s*:\s*"committed"'` (Kafka consumer with structured outcome), `'"event"\s*:\s*"message_published"'` (queue producer marker style), `'"sqlstate"\s*:\s*"00000"'` (DB client). Read the literal key off the shape; do not guess. When a topic/partition/offset/path field is present in the shape, fold its `key` into the regex so the captured `excerpt`/`rationale` carries the per-event identifier.
       - For shapes where the outcome is embedded in a free-form `msg` string (e.g. a Zap+chi middleware msg literally containing `"GET /foo HTTP/1.1\" from 127.0.0.1 - 200 12B in 3ms"`, or a Sarama consumer msg literally containing `"consumed topic=orders partition=2 offset=12345 outcome=committed"`), anchor on that substring shape — typically `'"msg":"[^"]*?\\"(\S+) (\S+) HTTP[^\\\"]*\\" [^"]*? (2\d{2}) '` (HTTP) or `'"msg":"[^"]*?topic=(\S+) [^"]*?outcome=(committed|acked)"'` (queue). When this is the only viable anchor, note it in `blind_spots[]` because the regex is sensitive to the framework's logging format string.
  4. Anchor every drafted regex on the shape's `sample`: the regex MUST match the sample. If it doesn't, the regex is wrong. For per-event patterns, either the shape's `sample` already contains a successful event line (in which case the "success" variant must match it), or the discovery step persisted only a degenerate sample (boot banner, error line, etc.) — in that case capture or synthesize a representative success line and either widen the shape's `sample` (re-running `/detect-sensors --refresh-stack`) or accept that only one of the outcome-class regexes can be sample-anchored. Never ship a regex whose sample-anchor was waived silently — call it out in `blind_spots[]`.
  5. In the sensor's `description`, cite the source: e.g. *"output_parsing derived from log_shape 'zap-prod-json' in .harness/stack.yaml"* or *"derived from log_shapes 'sarama-consumer-jsonl' (per-event observation) and 'logback-stderr' (severity tiers)"*. This is the audit trail when patterns later fail to match real stdout.
- **Degraded path:** if `.harness/stack.yaml` is missing OR `log_shapes[]` is empty (Phase A failed to identify a logger), emit generic patterns matching `panic\s*:`, `^\s*(ERROR|FATAL)`, and similar keyword markers, AND add a `blind_spots[]` entry: *"Patterns are generic keyword markers because stack discovery did not identify a structured logger; refine after observing real stdout."*
- `execution.output_parsing.patterns` (only when `output: "stream"`) — at least one regex per actionable verdict. For Go test, three patterns suffice: `^\s*--- PASS: (\S+)`, `^\s*--- FAIL: (\S+)`, `^\s*--- SKIP: (\S+)` with `captures.excerpt = 1`. For compilers/linters, one pattern: `^\s*(\S+\.go):(\d+):(\d+):\s+(.+)$` with `captures.{file:1,line_start:2,excerpt:4}`. RE2 syntax — when authoring in YAML, prefer single-quoted scalars or block scalars (`|`) so backslashes pass through literally; in double-quoted YAML, `\\s` is needed for a literal `\s`.
- `use_cases` MUST have at least one entry. Every entry is the `id` of a usecase YAML that already exists under `<project>/.harness/usecases/<journey>/<id>.yaml`. The schema enforces `minItems: 1`, and the persister cross-checks that each referenced file is on disk before writing the sensor. No placeholder ids — wire the sensor to the usecases it actually validates. See Phase A.5 for how to enumerate them and step 5 for how to choose the right ids per sensor.
- `description` should be one sentence: trigger condition + what is observed + regulation dimension. Mention how you detected the capability (`Auto-detected via /detect-sensors from <evidence>`) and why you chose `output: <single|stream>`. When the command came from project docs, name the file *and* the heading — e.g. *"Auto-detected from CLAUDE.md '## Build, validate, test'"* or *"Auto-detected from README.md '## Run locally'"* — so the source is one click away.
- For inferential sensors, add `calibration` (`confidence_threshold`, `calibration_set`, `calibration_size`, `calibration_date: 2026-05-08`) and `blind_spots`.

When the literal command is uncertain (common for `fetch-logs`, `fetch-metrics`, `trace-request`), still emit the sensor: put your best-guess command in `execution.command` and add a `blind_spots[]` entry stating what you assumed (auth profile, project id, region, log filter, etc.). The user reviews and tightens.

**Continuous-sensor template** (`run-project`-style — same shape works for `tail-logs-local`, `watch-build`, `replay-events`):

```yaml
id: run-project-nest
version: "0.1.0"
name: Run project (nest start --watch)
description: On demand, boots the NestJS app locally with the production-shaped command and listens for the first 30s. Regulates behaviour by surfacing crash, port, and dependency errors during startup.
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: stream
cost:
  class: expensive
  latency:
    p50_ms: 30000
    p95_ms: 30000
  compute:
    cpu: medium
    memory_mb: 1024
triggers:
  - on: manual
requires:
  - kind: env
    name: DATABASE_URL
    description: Postgres connection string used by the app at boot
  - kind: env
    name: RSA_PRIVATE_KEY
    description: PEM contents for JWT signing — gitignored, lives in config/.env.development on dev machines
execution:
  command: node ./dist/main.js
  blocking: true
  graceful_timeout_ms: 5000
  exit_code_map:
    - exit_code: 0
      verdict: pass
      severity: info
    - exit_code: "*"
      verdict: fail
      severity: high
  output_parsing:
    patterns:
      - regex: 'Nest application successfully started'
        verdict: pass
        severity: info
      - regex: 'Listening on .* port (\d+)'
        verdict: pass
        severity: info
        captures:
          excerpt: 1
      - regex: 'EADDRINUSE'
        verdict: fail
        severity: high
      - regex: '(?:ECONNREFUSED|ETIMEDOUT)'
        verdict: fail
        severity: high
      - regex: '(?i)\bunhandled (?:exception|rejection)\b'
        verdict: fail
        severity: high
# example — replace with real ids resolved under .harness/usecases/run-project-nest/
use_cases:
  - run-project-nest-clean-boot
  - run-project-nest-port-collision
blind_spots:
  - Boots the production binary (matches Dockerfile CMD), so a successful boot does not exercise the live-reload path that nest start --watch covers.
  - 30s window is heuristic — slow CI machines may need more; tighten or relax cost.latency.timeout_ms after first real runs.
```

Things to copy from this template into other continuous sensors: `output: "stream"` + `blocking: true`, `graceful_timeout_ms` sized to the process's expected shutdown time, success-marker patterns (boot lines, ready probes) AND failure-marker patterns (crashes, port conflicts, dependency errors), `requires[kind=env]` entries for any value that lives outside the repo, `use_cases[]` listing the usecase ids the sensor regulates. The template above shows only the boot-phase patterns — for any sensor whose target service has components with role in `{http-server, http-router, http-middleware, queue-consumer, queue-producer, rpc, db-client}`, the per-event observation patterns derived from the relevant `log_shape` (per the rules in §4 above) MUST also be emitted, so the sensor produces one Signal per discrete unit of work the service processes (request, consumed message, produced message, call, query) once it is past boot. A sensor that ships boot patterns only blinds the harness to the dense per-event observability surface — requests for http-api, messages for event-driven services, calls for RPC, queries for db-bound — which is the entire reason a running-service project deserves a `kind=observation` sensor in the first place.

**Never skip a sensor because credentials are missing.** If a capability needs auth tokens, RSA keys, project ids, region selectors, or any other host-supplied secret, declare them as `requires[kind=env]` entries and emit the sensor anyway. The runner forwards every listed env var from its own environment into the subprocess; if a non-optional name is unset at run time, the runner aborts with `verdict=error` and a remediation that names the missing var. The sensor's *existence* is independent of whether you, right now, can run it. Examples:

```yaml
requires:
  - kind: env
    name: GITHUB_TOKEN
    description: PAT with repo:read scope
  - kind: env
    name: GCP_PROJECT_ID
    description: Project id used by gcloud logging read filters
  - kind: env
    name: DATADOG_API_KEY
    description: Datadog API key for metric queries
  - kind: env
    name: AWS_PROFILE
    optional: true
    description: Optional named profile; default credentials chain is used when unset
```

Use `requires[kind=env]` for anything secret or per-developer; use `execution.env` only for static, non-secret literals (`LANG`, feature flags, deterministic seeds). Never put a token, key, or per-environment id directly into the sensor file — that file is committed to the repo.

### 4.5 Authoring lifecycle phases (requires[kind=step] / teardown)

A sensor's `requires[]` and `execution` together drive three phases: `requires[kind=step]` entries (setup steps, run before the command), `execution.command` (the observed one), and `execution.teardown[]`. Use them to keep the sensor self-contained when its setup is specific to it (vs reusable across many sensors → use a setup sensor referenced via `requires[kind=sensor]`).

- **`requires[kind=step]`** — silent, fail-fast. Each item: `{ kind: "step", command, timeout_ms?, exit_code_map? }`. The first non-pass step aborts and skips the main command (but teardown still runs). Use for: generating intermediate artifacts (`pnpm prisma generate`, `make protos`), populating local config (`test -f .env || cp .env.example .env`), running pre-build steps that aren't worth a separate sensor.
- **teardown[]** — silent, best-effort. Same item shape (without `kind`). Every step runs regardless of requires[kind=step]/command outcome (finally semantics). Use for: dropping local DB after E2E (`pnpm prisma migrate reset --force --skip-seed`), stopping containers, removing temp files. Teardown failures contribute warn evidence but do NOT downgrade the sensor's aggregate verdict — the command is the source of truth.

**When to use requires[kind=step] vs a setup sensor with requires[kind=sensor]:**
- Reusable across multiple sensors → setup sensor (e.g. `{ "kind": "sensor", "id": "start-postgres" }` in requires[])
- Specific to this sensor only → `requires[kind=step]`

Example (E2E sensor with full lifecycle):

```yaml
id: e2e-tests
kind: assertion
requires:
  - kind: sensor
    id: start-postgres
  - kind: sensor
    id: setup-env-from-example
  - kind: step
    command: pnpm prisma migrate deploy
    timeout_ms: 30000
execution:
  command: pnpm playwright test
  exit_code_map: [...]
  output_parsing:
    patterns: [...]
  teardown:
    - command: pnpm prisma migrate reset --force --skip-seed
      timeout_ms: 15000
    - command: docker compose stop postgres
      timeout_ms: 10000
```

### 4.6 Wrap-vs-unroll: when the canonical command is a wrapper

When the detected canonical command is itself a wrapper — `make dev` reading several steps out of a Makefile, `pnpm dev` aliasing a longer `docker compose` invocation, `task run` chaining a Taskfile recipe — choose between TWO strategies. **Never silently inline the wrapper's body into `execution.command`.** Silent inlining drops steps, hides drift (the Makefile can change but the sensor cannot follow), and produces commands that the project's humans do not recognize.

**Strategy A — Wrap (preferred when shell-compatible).** Set `execution.command` to the wrapper invocation verbatim and stop there. This works when the wrapper:

- Runs the underlying tool in the **foreground** (no `-d`, no `&`, no `nohup`, no `tmux/screen` detach).
- Targets a **single observable process** (one container, one server, one watcher) — wrappers that bring up N services at once do not fit.
- Already produces output on stdout/stderr that the sensor's patterns can consume — if patterns need to see stderr, append `2>&1` to the wrapper invocation (not inside the target body).

Cite the source in the sensor `description` using the exact form *"Auto-detected from `<relative-path>#<target>`"* — e.g. *"Auto-detected from charge-api/Makefile#dev"*, *"Auto-detected from package.json scripts.dev"*. Always include the path and the wrapper-internal address (target name / script key / task name) so the audit trail points at the exact body of the wrapper that backs this sensor. Pin the wrapper to a `requires[kind=tool]` entry — `{ kind: tool, name: make }` / `{ kind: tool, name: pnpm }` / `{ kind: tool, name: task }` — so the preflight gate aborts cleanly when the wrapper is not on PATH.

**Strategy B — Unroll (only when Strategy A's three preconditions fail).** The wrapper is incompatible because it daemonizes, brings up multiple services, or has prerequisite steps the harness needs as separate fail-fast checkpoints. In that case, decompose deliberately:

1. **Pre-steps go to `requires[kind=step]`.** Every wrapper command BEFORE the observed one becomes a `requires[]` entry of `kind: step`, in the order the wrapper executes them, with the literal shell command verbatim. The §4.5 fail-fast semantics apply: any pre-step failing aborts before the main command runs.
2. **The foreground equivalent goes to `execution.command`.** Translate the daemonized/multi-service final step into its foreground, mono-service form: e.g. `docker compose --profile dev up -d` → `docker compose --profile dev up --build api 2>&1` (drop `-d`, scope to one service, redirect stderr).
3. **Annotate the divergence in `description` and `blind_spots[]`.** This is the load-bearing part of unrolling — without it, the next reader cannot tell why the sensor command differs from `make dev`.
   - `description` MUST cite the wrapper *and* state it was decomposed. Example: *"Auto-detected from charge-api/Makefile#dev. Decomposed because the target daemonizes (-d) and builds multiple services — pre-steps preserved as requires[kind=step], final step rewritten as foreground mono-service."*
   - `blind_spots[]` MUST include an entry: *"Decomposed from `<path>#<target>`. Changes to the wrapper body do not propagate — re-run /detect-sensors after editing the wrapper to recompute pre-steps and the foreground command."*

**Example — unrolling `make dev` for an API service:**

```yaml
description: |
  On demand, boots charge-api with its production-shaped docker compose stack and listens
  for the first 30s. Auto-detected from charge-api/Makefile#dev. Decomposed because the
  target daemonizes (-d) and builds multiple services — pre-steps preserved as
  requires[kind=step], final step rewritten as foreground mono-service.
requires:
  - kind: tool
    name: docker
  - kind: tool
    name: make
  - kind: step
    command: cd charge-api && docker compose stop
    timeout_ms: 30000
  - kind: step
    command: cd charge-api && docker compose build api
    timeout_ms: 300000
execution:
  command: cd charge-api && docker compose --profile dev up --build api 2>&1
  blocking: true
  ...
blind_spots:
  - "Decomposed from charge-api/Makefile#dev. Changes to the wrapper body do not propagate — re-run /detect-sensors after editing the Makefile target to recompute pre-steps and the foreground command."
```

**The same rule applies to non-Makefile wrappers:**

- `package.json` scripts that chain commands (`"dev": "npm run build && npm run start:watch"`) — wrap when the chain stays foreground/mono-service; otherwise unroll the chain into `requires[kind=step]` for everything before the final foreground step.
- `Taskfile.yml` targets with `deps:` — the declared deps become `requires[kind=step]` entries; the recipe body either wraps (Strategy A) or unrolls (Strategy B).
- Nested wrappers (`make deploy` → invokes `scripts/deploy.sh` → invokes `terraform apply`) — unroll recursively until you reach a single shell command per step. The deepest foreground command is `execution.command`; every command above it on the path is either a `requires[kind=step]` (when its side effects are needed) or dropped (when it is purely a dispatcher with no side effects of its own).

**Why this matters.** A sensor whose `execution.command` no longer resembles the project's documented workflow is a bug magnet: it skips setup the maintainers consider mandatory, it diverges from what `dev local roda make dev` produces, and when the Makefile changes the sensor lies silently. Strategy A keeps the sensor and the wrapper in lockstep. Strategy B makes the unrolling explicit and citable — when the wrapper changes, the `blind_spots[]` entry tells the reader the sensor must be regenerated.

### 5. Wire `use_cases[]` to the right usecase ids

A sensor without real `use_cases[]` is half-built — and the schema's `minItems: 1` plus the persister's on-disk cross-check will refuse to write it. For each sensor you drafted in step 4, choose the usecase ids it actually regulates from the ledger built in Phase A.5.

Conventions that work:

- **Match the sensor's scope, not its name.** A `lint-eslint` sensor regulates every usecase whose expected_outcome includes "no lint errors" or whose invariants forbid console warnings — not just one usecase with `lint` in its id. A `run-project-nest` sensor regulates every usecase whose trigger is "boot the service" or whose expected_outcome describes a healthy startup.
- **Aim for 1–4 ids per sensor.** A single id is fine when the sensor narrowly proves one journey variation; more than four usually means the sensor is too broad (split it) or you are stretching the link (drop the weaker matches). The persister rejects empty arrays — there is no "TODO" placeholder.
- **Cross-cutting capabilities reference framework usecases.** When a sensor regulates a system-wide property (the harness's own contract, e.g. "every persisted sensor parses against schemas/sensor.yaml"), wire it to ids under `<project>/.harness/usecases/framework/`. Capability-specific sensors wire to journey-scoped ids (`run-sensor/run-sensor-single-output-pass`, `tail-sensor/tail-sensor-no-registry`, etc.).
- **Never invent ids.** The persister checks each entry against `<project>/.harness/usecases/**/*.yaml`; an unknown id fails the write with a `missing_usecase` Signal. If a journey variation deserves regulation but no usecase yet exists, **stop and run `/detect-usecases`** to author it first.

The runner does not consult `use_cases[]` at execution time — the list is the audit trail wiring sensors back to the journeys they validate. When the journey set evolves, re-run `/detect-usecases` and revisit the wiring; bump the sensor's `version` if you add or remove ids.

### 6. Persist each draft

Write the draft YAML to a temp file, then run the validator-and-writer:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_sensor \
  ./skills/detect-sensors/scripts \
  --out=<project>/.harness/sensors \
  /tmp/<draft-name>.yaml
```

The script:

- Reads the draft file.
- Validates against `schemas/sensor.yaml` (cross-file `$ref` to `signal.yaml` resolved).
- If invalid: prints the validator's error tree to stderr and exits **1** without writing. Read the tree, fix the draft, re-run.
- If valid: writes the canonical sensor to `<out>/<sensor.id>.yaml` (block style, deterministic key order) and prints the absolute path on stdout.

Exit codes:

- `0` — sensor written.
- `1` — schema validation failed; nothing was written.
- `2` — usage / I/O error (missing `--out`, draft unreadable, mkdir failed, schemas not found).

Run the script once per draft. Iterate per capability — it is faster than batching because validator output is per-sensor.

Do not skip the validator step. The schema's `allOf` discriminators catch a class of mistakes you cannot reliably catch by inspection (computational sensors leaking inferential fields, single sensors declaring `output_parsing`, etc.).

### 7. Run each sensor and iterate on shape correctness only

Schema-valid is not the same as semantically useful. After persisting, **run each sensor through the runner once** and inspect the aggregate Signal. Iterate ONLY on shape-correctness symptoms (regex matches, exit_code_map right, capture groups correct). For setup-shape symptoms (missing env, missing binary, absent .env), do NOT iterate here — invoke `/heal-sensor` (see step 7.5).

Shape-correctness symptoms to fix in this loop:

- `output: "stream"` sensor returning `evidence: []` and `metadata.counts: {error:0,fail:0,pass:0,warn:0}` *when the underlying command actually produced output*. That means your patterns matched nothing — fix the regex, add `-v`/`--verbose` to the command, or escalate the relevant lines (e.g. `go test` needs `-v` or no PASS lines appear).
- Aggregate `verdict: "pass"` when you know the codebase has unfixed findings — patterns are skipping them.
- `metadata.timed_out: true` — your `cost.latency.timeout_ms` is too low for a real run.
- `evidence` entries with empty `excerpt` and `rationale` falling back to the entire raw line — capture groups are wrong.

Run order:

```bash
# Production happy-path: run the sensor against the real codebase.
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts @.harness/sensors/<id>.yaml | tail -n 1 \
  | jq -c '{verdict, severity, counts: .metadata.counts, individuals: (.evidence|length)}'
```

The aggregate on the live repo must match reality (clean repo → `pass`; dirty repo → `fail`/`warn`). Empty `evidence` is acceptable iff the underlying tool is genuinely silent on success (vet, build, schema parsers); for tools that emit per-test output (Go test with `-v`, jest, pytest -v), `counts` MUST show non-zero in the relevant bucket. On any mismatch, fix the sensor's `execution.command`, `exit_code_map`, or `output_parsing.patterns` — never adjust expectations to paper over a regex that does not actually match the tool's stdout.

If iteration changes `output`, `execution`, or `use_cases`, bump the sensor `version` (e.g. `0.1.0` → `0.2.0`) and re-persist via the validator. The version stamp is the audit trail of which shape was actually verified.

If a `kind=observation` + `output=stream` sensor's patterns match nothing during its first run, suspect Phase A first — not the regex. Inspect the persisted stack with `bat <project>/.harness/stack.yaml` (or `cat`). If the `log_shapes[].sample` no longer resembles the real stdout, rerun `/detect-sensors --refresh-stack` to regenerate. Only after the stack matches reality should you tweak the patterns themselves.

### 7.5. If smoke run fails with a setup-shape symptom, invoke /heal-sensor

When step 7's smoke run produces an aggregate Signal that is setup-shape (missing env, missing binary, absent `.env`, unavailable service), do NOT iterate inside this skill. Invoke `/heal-sensor` instead:

```
/heal-sensor --signal=<path-to-saved-aggregate-signal-json> --sensor=@.harness/sensors/<id>.yaml
```

`/heal-sensor` will read the project state, build a Setup Plan, apply allowlisted idempotent fixes (cp .env.example .env, mkdir, touch, set-env-in-file), persist any patched/new sensors via the same `lib/sensor.ValidateAndPersist` primitive this skill uses, and retry the original sensor. After it returns:

- If the retry passed: continue the draft loop — your sensor is healthy.
- If `/heal-sensor` couldn't recover: the failure is genuinely outside the harness's reach (needs `pnpm install`, `gcloud login`, etc.). Read the remediation it emitted, surface it to the user, and continue with the OTHER sensors. Don't block this skill on credentials the harness can't synthesize.

Setup-shape recovery is exclusively `/heal-sensor`'s job. This skill stays focused on shape correctness.

### 8. Report back to the user

When every draft is persisted **and verified** (step 7 passed on the live repo), surface the result as a bulleted list of paths plus the verdict observed for each, so the user can immediately fan out into `/run-sensor`:

```
Generated 7 sensors at /repo/.harness/sensors/ (all verified on the live repo):
- /repo/.harness/sensors/lint-eslint.yaml           — happy: pass · use_cases: 2
- /repo/.harness/sensors/build-vite.yaml            — happy: pass · use_cases: 1
- /repo/.harness/sensors/unit-test-vitest.yaml      — happy: pass(83) · use_cases: 3
- /repo/.harness/sensors/e2e-playwright.yaml        — happy: pass(12) · use_cases: 3
- /repo/.harness/sensors/run-project-vite-dev.yaml  — single-mode · use_cases: 2
- /repo/.harness/sensors/fetch-logs-cloudrun.yaml   — happy: pass · NEEDS-AUTH (gcloud login) · use_cases: 1
- /repo/.harness/sensors/fetch-metrics-cloud-monitoring.yaml — happy: pass · NEEDS-AUTH · use_cases: 1

Run any of them with `/run-sensor <id>`.
Each sensor's use_cases[] entry resolves to .harness/usecases/<journey>/<id>.yaml.
```

Be honest about anything still soft: sensors whose live command needs credentials you do not have (`NEEDS-AUTH`), `cost.latency` numbers that are estimates rather than measured, observability sensors whose query strings are best-guess. Call those out explicitly so the user knows where to focus the review.

## Safety notes

- The script never executes the detected commands. It only validates the draft and writes files.
- Existing files at `<out>/<sensor-id>.yaml` are overwritten atomically by `os.Create`. Commit `.harness/sensors/` before re-running so diffs are reviewable.
- Drafts you stage in `/tmp/` are yours to clean up; the script does not touch them.
- Schemas are resolved by walking up from cwd; invoke from inside the harness-framework checkout (or pass `--schemas-dir=<plugin>/schemas`) so the validator sees the right contract.
