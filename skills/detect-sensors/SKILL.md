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
2. **Components** — for each runtime-observable role (`logger`, `log-encoder`, `http-server`, `http-router`, `http-middleware`, `tracer`, `metrics`, `queue-consumer`, `queue-producer`, `db-client`, `rpc`, `test-runner`):
   - Identify the library actually used (Zap, Logrus, Pino, Winston, Logback, structlog, …).
   - **Open the initialization site** (`cmd/server/main.go`, `src/main.ts`, `app/__init__.py`, etc.) and read enough code to determine the CONCRETE config — not just "Zap is used" but "`zap.NewProductionConfig()` is called and `EncoderConfig.LevelKey` is overridden to `severity`".
   - Record an `evidence[]` entry pointing at the file (with line numbers when feasible) and a one-sentence `rationale`.
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

If after a thorough search you cannot identify any logger or HTTP middleware (project is exotic, no readable manifests, no clear initialization site), persist a **minimal stack** anyway:

```yaml
version: "0.1.0"
detected_at: "<now>"
detected_by: "<your-model-id-or-manual>"
languages:
  - name: "<best-guess>"
components: []
log_shapes: []
```

This is intentionally degenerate. Phase B (§4 below) will see an empty `log_shapes[]` and fall back to generic patterns (panic/error keyword matchers) annotated in the sensor's `blind_spots[]` as "stack discovery returned empty; refine patterns manually after observing real stdout".

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

**Mandate: never skip a sensor.** Emit one for every capability the project plausibly has, even when:

- The exact command isn't 100% determined — pick the closest production-shaped invocation (Dockerfile `CMD`, CI step, README "Run locally" recipe), put it in `execution.command`, and document the assumption in `blind_spots[]`.
- Required config files are gitignored (`.env*`, `config/*.local.*`, RSA keys) — they exist on the developer's machine; reference them in the command, declare the env names you need under `requires[kind=env]`, and let the runner abort with a clear remediation if the user runs it without them.
- The capability needs auth tokens, project ids, or other secrets — declare them under `requires[kind=env]`, NEVER hardcode. Sensor existence is independent of credential availability *right now*: the user (or a future agent turn) provides the value out of band when they invoke `/run-sensor`.
- The command runs continuously (watchers, dev servers, log tailers) — set `execution.blocking: true` and use the `/start-sensor` workflow and pair with `output: "stream"` patterns; do not downgrade to a proxy command just to keep things one-shot. See §1's lifecycle guidance.
- You aren't sure your patterns will catch every failure mode — that's what §7's iteration loop is for. Ship a v0.1.0 with your best guess and iterate via `/run-sensor` until the output is informative.

The only legitimate reason to omit a capability is "the project genuinely doesn't have it" (no observability stack at all → no `fetch-logs` sensor; no IaC → no `terraform-validate`). Never omit because credentials, watchers, or partial knowledge make the run-time path harder.

For every capability that survives step 3, draft a sensor object. The id MUST be kebab-case starting with a letter (`^[a-z][a-z0-9-]*$`) and unique across the directory — combine capability + tool + (optional) scope, e.g. `lint-eslint`, `build-go`, `fetch-logs-cloudrun`, `unit-test-pytest-domain`, `run-project-nest`.

Use these defaults unless the project tells you otherwise:

- `version: "0.1.0"` for the first cut of a sensor; bump (`0.2.0`, `0.3.0`, ...) on every iteration that changes `output`, `execution`, or `verification`. The version stamp is the audit trail readers compare against.
- `type: "computational"`, `determinism: "high"` for command-style sensors. Pick `output` per the rule in step 1.
- `cost.class` cheap for static analysis, medium for unit/contract tests, expensive for e2e/integration/observability fetches.
- `cost.latency` tuned to the capability's actual runtime (use the CI logs as a sanity check when available).
- `triggers[].on` from `{pull-request, file-change, cron, metric-anomaly, manual, agent-request}` only — do NOT confuse this with `phase`.
- `execution.exit_code_map` defaults to `[{exit_code: 0, verdict: pass, severity: info}, {exit_code: "*", verdict: fail, severity: <medium|high>}]`. Override per capability.
- **For `kind=observation` + `output=stream` sensors (Phase B):** do NOT hand-craft regexes. Instead:
  1. Load `<project>/.harness/stack.yaml` (produced by §0; if missing or empty, fall through to the degraded path below).
  2. Filter `log_shapes[]` to the shapes relevant to the sensor's command. For `run-*` / `watch-*` sensors observing a running service, that typically means shapes produced by components with role `logger`, `log-encoder`, or `http-middleware`. For `tail-*` / `fetch-*` sensors against external log stores, pick the shape whose encoder matches what the store emits.
  3. For each selected shape, write 2–6 regex patterns into `execution.output_parsing.patterns[]` that map the shape's `severity_values` onto Signal verdicts:
     - `severity ∈ {ERROR, FATAL, DPANIC, PANIC}` → `verdict: fail, severity: high`.
     - `severity == WARN` AND a `status_code` field with value in `4xx/5xx` → `verdict: fail, severity: medium`.
     - `severity == WARN` (other) → `verdict: warn, severity: low`.
     - `severity == INFO` AND `message` matches a boot/ready marker → `verdict: pass, severity: info`.
  4. Anchor every drafted regex on the shape's `sample`: the regex MUST match the sample. If it doesn't, the regex is wrong.
  5. In the sensor's `description`, cite the source: e.g. *"output_parsing derived from log_shape 'zap-prod-json' in .harness/stack.yaml"*. This is the audit trail when patterns later fail to match real stdout.
- **Degraded path:** if `.harness/stack.yaml` is missing OR `log_shapes[]` is empty (Phase A failed to identify a logger), emit generic patterns matching `panic\s*:`, `^\s*(ERROR|FATAL)`, and similar keyword markers, AND add a `blind_spots[]` entry: *"Patterns are generic keyword markers because stack discovery did not identify a structured logger; refine after observing real stdout."*
- `execution.output_parsing.patterns` (only when `output: "stream"`) — at least one regex per actionable verdict. For Go test, three patterns suffice: `^\s*--- PASS: (\S+)`, `^\s*--- FAIL: (\S+)`, `^\s*--- SKIP: (\S+)` with `captures.excerpt = 1`. For compilers/linters, one pattern: `^\s*(\S+\.go):(\d+):(\d+):\s+(.+)$` with `captures.{file:1,line_start:2,excerpt:4}`. RE2 syntax — when authoring in YAML, prefer single-quoted scalars or block scalars (`|`) so backslashes pass through literally; in double-quoted YAML, `\\s` is needed for a literal `\s`.
- `verification.golden_cases` MUST have at least one entry, and **every entry MUST point at a real fixture file** that exists at the path you write down. No `"TODO"` strings, no placeholder verdicts. See step 5 for how to author fixtures and step 6 for how to verify them.
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
verification:
  golden_cases:
    - fixture: .harness/sensors/fixtures/run-project-nest/clean-boot.txt
      expected_verdict: pass
      expected_severity: info
      notes: Captured stdout from a real local boot — Nest start banner + Listening line within ~3s.
    - fixture: .harness/sensors/fixtures/run-project-nest/port-collision.txt
      expected_verdict: fail
      expected_severity: high
      notes: EADDRINUSE on port 3000 when another instance is running.
blind_spots:
  - Boots the production binary (matches Dockerfile CMD), so a successful boot does not exercise the live-reload path that nest start --watch covers.
  - 30s window is heuristic — slow CI machines may need more; tighten or relax cost.latency.timeout_ms after first real runs.
```

Things to copy from this template into other continuous sensors: `output: "stream"` + `blocking: true`, `graceful_timeout_ms` sized to the process's expected shutdown time, success-marker patterns (boot lines, ready probes) AND failure-marker patterns (crashes, port conflicts, dependency errors), `requires[kind=env]` entries for any value that lives outside the repo, fixtures captured from a real boot.

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

### 5. Author fixtures BEFORE you persist

A sensor without real fixtures is half-built. For every `golden_cases[]` entry you wrote in step 4, create the file at `<project>/.harness/sensors/fixtures/<group>/<case>.txt` (or `.json` for parser-style sensors). The fixture must contain content shaped exactly like the production tool's stdout for that case.

Conventions that work:

- Group fixtures by tool family, not by sensor id, so `lint-go-vet-computational` and `lint-go-vet-inferential` share `.harness/sensors/fixtures/lint-go-vet/{clean,has-warning}.txt`. Same for `unit-test/{all-pass,has-failure,has-skip}.txt`, `build-runner/{clean,has-error}.txt`.
- Always include at least: one **happy path** fixture (clean stdout → expected `pass`) and one **fail path** fixture (representative finding → expected `fail` or `warn`). Add a third for `skip`/`warn` semantics when the tool produces them.
- Fixture content should be a faithful capture, not a hand-crafted approximation. For Go tests: `=== RUN ...\n--- PASS: ... (0.00s)\nPASS\nok ... 0.012s`. For Go vet/build: `# package\n./file.go:LINE:COL: message`. For schema parsers: a real malformed JSON.
- Keep them small (a handful of lines) — they exist for verification, not for stress.

`expected_verdict` and `expected_severity` for each case must match what the runner *actually* computes when patterns + exit_code_map see that fixture. The next step proves it.

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

Schema-valid is not the same as semantically useful. After persisting, **run each sensor through the runner once** and inspect the aggregate Signal. Iterate ONLY on shape-correctness symptoms (regex matches, exit_code_map right, fixtures replay correctly). For setup-shape symptoms (missing env, missing binary, absent .env), do NOT iterate here — invoke `/heal-sensor` (see step 7.5).

Shape-correctness symptoms to fix in this loop:

- `output: "stream"` sensor returning `evidence: []` and `metadata.counts: {error:0,fail:0,pass:0,warn:0}` *when the underlying command actually produced output*. That means your patterns matched nothing — fix the regex, add `-v`/`--verbose` to the command, or escalate the relevant lines (e.g. `go test` needs `-v` or no PASS lines appear).
- Aggregate `verdict: "pass"` when you know the codebase has unfixed findings — patterns are skipping them.
- `metadata.timed_out: true` — your `cost.latency.timeout_ms` is too low for a real run.
- `evidence` entries with empty `excerpt` and `rationale` falling back to the entire raw line — capture groups are wrong.

Run order:

```bash
# 1) Production happy-path: run the sensor against the real codebase.
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts @.harness/sensors/<id>.yaml | tail -n 1 \
  | jq -c '{verdict, severity, counts: .metadata.counts, individuals: (.evidence|length)}'

# 2) Replay each fail/warn fixture to prove the unhappy paths.
#    The Go script preserves sensor.id and routes the run through the
#    orchestrator with the project's real registry root, so each replay
#    lands at .harness/runtime/<sensor-id>/<run-id>/ alongside any other
#    valid run of that sensor.
go run -tags=replay_fixture ./skills/detect-sensors/scripts \
  --sensor=.harness/sensors/<id>.yaml --fixture=.harness/sensors/fixtures/<group>/<case>.txt \
  | tail -n 1 | jq -c '{verdict, severity, individuals: (.evidence|length)}'
```

For each sensor, both must hold:

- Happy path on the live repo: aggregate `verdict` matches reality (clean repo → `pass`; dirty repo → `fail`/`warn`). Empty `evidence` is acceptable iff the underlying tool is genuinely silent on success (vet, build, schema parsers); for tools that emit per-test output (Go test with `-v`, jest, pytest -v), `counts` MUST show non-zero in the relevant bucket.
- Each `golden_cases[]` entry: replay must produce the declared `expected_verdict` and `expected_severity`. If a replay disagrees, EITHER the patterns are wrong (most common) OR `expected_verdict` is wrong — fix one and re-replay until both agree.

If iteration changes `output`, `execution`, or `verification`, bump the sensor `version` (e.g. `0.1.0` → `0.2.0`) and re-persist via the validator. The version stamp is the audit trail of which shape was actually verified.

If a `kind=observation` + `output=stream` sensor's patterns match nothing during its first run, suspect Phase A first — not the regex. Inspect the persisted stack with `bat <project>/.harness/stack.yaml` (or `cat`). If the `log_shapes[].sample` no longer resembles the real stdout, rerun `/detect-sensors --refresh-stack` to regenerate. Only after the stack matches reality should you tweak the patterns themselves.

### 7.5. If smoke run fails with a setup-shape symptom, invoke /heal-sensor

When step 7's smoke run produces an aggregate Signal that is setup-shape (missing env, missing binary, absent `.env`, unavailable service), do NOT iterate inside this skill. Invoke `/heal-sensor` instead:

```
/heal-sensor --signal=<path-to-saved-aggregate-signal-json> --sensor=@.harness/sensors/<id>.yaml
```

`/heal-sensor` will read the project state, build a Setup Plan, apply allowlisted idempotent fixes (cp .env.example .env, mkdir, touch, set-env-in-file), persist any patched/new sensors via the same `lib/sensor.ValidateAndPersist` primitive this skill uses, and retry the original sensor. After it returns:

- If the retry passed: continue the draft loop — your sensor is healthy.
- If `/heal-sensor` couldn't recover: the failure is genuinely outside the harness's reach (needs `pnpm install`, `gcloud login`, etc.). Read the remediation it emitted, surface it to the user, and continue with the OTHER sensors. Don't block this skill on credentials the harness can't synthesize.

Setup-shape recovery used to be the responsibility of this skill's prose ("if credentials are missing, declare them and proceed"). It is now `/heal-sensor`'s job — exclusively. This skill stays focused on shape correctness.

### 8. Report back to the user

When every draft is persisted **and verified** (step 7 passed for happy and unhappy paths), surface the result as a bulleted list of paths plus the verdict observed for each, so the user can immediately fan out into `/run-sensor`:

```
Generated 7 sensors at /repo/.harness/sensors/ (all verified happy + replay paths):
- /repo/.harness/sensors/lint-eslint.yaml           — happy: pass · replay(has-finding): fail/medium ·  4 fixtures
- /repo/.harness/sensors/build-vite.yaml            — happy: pass · replay(compile-error): fail/high   · 2 fixtures
- /repo/.harness/sensors/unit-test-vitest.yaml      — happy: pass(83) · replay(has-failure): fail/high · 3 fixtures
- /repo/.harness/sensors/e2e-playwright.yaml        — happy: pass(12) · replay(timeout): fail/high     · 3 fixtures
- /repo/.harness/sensors/run-project-vite-dev.yaml  — single-mode  · replay(crash): fail/high          · 2 fixtures
- /repo/.harness/sensors/fetch-logs-cloudrun.yaml   — happy: pass · NEEDS-AUTH (gcloud login)          · 1 fixture
- /repo/.harness/sensors/fetch-metrics-cloud-monitoring.yaml — happy: pass · NEEDS-AUTH                · 1 fixture

Run any of them with `/run-sensor <id>`.
Fixtures live under /repo/.harness/sensors/fixtures/<group>/<case>.{txt,json}.
```

Be honest about anything still soft: sensors whose live command needs credentials you do not have (`NEEDS-AUTH`), `cost.latency` numbers that are estimates rather than measured, observability sensors whose query strings are best-guess. Call those out explicitly so the user knows where to focus the review.

## Safety notes

- The script never executes the detected commands. It only validates the draft and writes files.
- Existing files at `<out>/<sensor-id>.yaml` are overwritten atomically by `os.Create`. Commit `.harness/sensors/` before re-running so diffs are reviewable.
- Drafts you stage in `/tmp/` are yours to clean up; the script does not touch them.
- Schemas are resolved by walking up from cwd; invoke from inside the harness-framework checkout (or pass `--schemas-dir=<plugin>/schemas`) so the validator sees the right contract.
