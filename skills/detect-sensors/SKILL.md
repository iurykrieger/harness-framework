---
name: detect-sensors
description: Use when the user invokes /detect-sensors or asks to scaffold a harness for the project they are working in. Inspects the project to infer its archetype(s) (frontend, backend API, event-consumer, event-producer, IaC, CLI, library, data-pipeline, ...), reasons about which capabilities each archetype typically exposes (lint, build, unit-test, e2e-test, integration-test, run-project, fetch-logs, fetch-metrics, trace-request, ...), drafts one sensor JSON per capability conforming to schemas/sensor.json, and persists each through the validator script at `<project>/sensors/<sensor-id>.json` so the user can immediately invoke `/run-sensor @sensors/<sensor-id>.json`.
---

# detect-sensors

Bootstrap a project's sensor harness end-to-end. The detection step is **your judgment**: look at the project, decide what kind of system it is, decide which capabilities deserve a sensor, and draft the JSON. The persistence step is the helper script `scripts/write-sensor.go`, which is the only deterministic part of the loop — it validates each draft against `schemas/sensor.json` and writes it to the target directory.

There is no fixed taxonomy of project types or capabilities baked into the script. If the project is a Pulumi stack with custom drift checks, sensors emerge from *that* shape; if it is a Kafka consumer with a Confluent schema registry, sensors emerge from *that* shape. The fact that this is an LLM skill is the whole point — exploit that.

## Invocation

```
/detect-sensors [project-path]
```

If the user supplies no argument, scan the cwd. The output directory is always `<project>/sensors/`. Do not ask the user to choose; create it if it does not exist.

## Procedure

### 1. Read the schema first

Always start by reading `schemas/sensor.json` and `schemas/signal.json` from this plugin so your drafts match the current shape (required fields, discriminators, enum values). The schema is the contract — never guess it from memory.

Pay attention to the `allOf` discriminators:

- `type: "computational"` → requires `cost.compute` + `execution.{command, exit_code_map}`; forbids `cost.tokens` and the inferential `execution` fields.
- `type: "inferential"` → requires `cost.tokens`, `execution.{command, model, system_prompt, user_prompt_template, decoding}`, and a top-level `calibration` block.
- `output: "single"` → forbids `execution.output_parsing`.
- `output: "stream"` → requires `execution.output_parsing.patterns` (≥1).

Pick `output` deliberately:

- **`stream`** when the underlying tool emits one observation per line that is independently meaningful — test runners (`--- PASS:` / `--- FAIL:` per test), linters/compilers (`file:line:col: msg` per finding), log fetchers (one matched event per line). On a clean run the stream is empty and the aggregate verdict comes from the exit code; on a dirty run each finding becomes its own actionable Signal.
- **`single`** only when the tool's output is binary or unstructured and the exit code is the entire story — schema parsers, dry-run deploys, smoke pings. If you would have to write a regex to fake a "success line" just to get a non-zero count, you picked the wrong mode — switch to `stream` and let the empty-stream + exit-0 case represent success honestly.

`inferential` is reserved for verdicts that genuinely require LLM judgment (AI code review, semantic-duplicate detection). Most CI-mirroring sensors are computational.

### 2. Inspect the project

Use Read, Glob, Bash to build a picture of the project. At minimum:

- README, top-level layout (`ls -la`), `git log` for recency.
- Manifest files: `package.json`, `go.mod`, `Cargo.toml`, `pyproject.toml`, `requirements.txt`, `Gemfile`, `composer.json`, `pom.xml`, `build.gradle`, `Chart.yaml`, `helmfile.yaml`, `Pulumi.yaml`, `*.tf`, `serverless.yml`, `wrangler.toml`, `Dockerfile`, `docker-compose.yml`.
- CI definitions: `.github/workflows/`, `.gitlab-ci.yml`, `.circleci/`, `Jenkinsfile`, `azure-pipelines.yml`. These often reveal the *real* commands used in production.
- Source layout signals: `pages/` or `app/` (Next.js), `cmd/` + `internal/` (Go service), `src/handlers/` (Lambda), `consumer.go`/`*Consumer.cs` (event consumer), `*Publisher.*` / `*Producer.*` (event producer), `infrastructure/` or `iac/` (IaC), `migrations/` (DB-bound service).
- Observability hints: `Dockerfile`, `*.deployment.yaml`, `terraform/.../monitoring/`, `datadog.yaml`, `otel-collector-config.yaml`, `prometheus.yml`, `grafana/dashboards/`, `serverless.yml` events, log driver configs.

You are looking for *both* the archetype and the concrete commands. A `package.json` script is the literal command; a CI workflow step is the literal command; a Dockerfile `CMD` is the literal command. When the literal command is unclear, propose a reasonable one and flag it in the sensor's `description`/`blind_spots`.

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

For every capability that survives step 3, draft a sensor JSON object. The id MUST be kebab-case starting with a letter (`^[a-z][a-z0-9-]*$`) and unique across the directory — combine capability + tool + (optional) scope, e.g. `lint-eslint`, `build-go`, `fetch-logs-cloudrun`, `unit-test-pytest-domain`.

Use these defaults unless the project tells you otherwise:

- `version: "0.1.0"` for the first cut of a sensor; bump (`0.2.0`, `0.3.0`, ...) on every iteration that changes `output`, `execution`, or `verification`. The version stamp is the audit trail readers compare against.
- `type: "computational"`, `determinism: "high"` for command-style sensors. Pick `output` per the rule in step 1.
- `cost.class` cheap for static analysis, medium for unit/contract tests, expensive for e2e/integration/observability fetches.
- `cost.latency` tuned to the capability's actual runtime (use the CI logs as a sanity check when available).
- `triggers[].on` from `{pull-request, file-change, cron, metric-anomaly, manual, agent-request}` only — do NOT confuse this with `phase`.
- `execution.exit_code_map` defaults to `[{exit_code: 0, verdict: pass, severity: info}, {exit_code: "*", verdict: fail, severity: <medium|high>}]`. Override per capability.
- `execution.output_parsing.patterns` (only when `output: "stream"`) — at least one regex per actionable verdict. For Go test, three patterns suffice: `^\s*--- PASS: (\S+)`, `^\s*--- FAIL: (\S+)`, `^\s*--- SKIP: (\S+)` with `captures.excerpt = 1`. For compilers/linters, one pattern: `^\s*(\S+\.go):(\d+):(\d+):\s+(.+)$` with `captures.{file:1,line_start:2,excerpt:4}`. RE2 syntax — escape backslashes once for JSON, once for regex (`\\\\s` → `\s` in the compiled regex).
- `verification.golden_cases` MUST have at least one entry, and **every entry MUST point at a real fixture file** that exists at the path you write down. No `"TODO"` strings, no placeholder verdicts. See step 5 for how to author fixtures and step 6 for how to verify them.
- `description` should be one sentence: trigger condition + what is observed + regulation dimension. Mention how you detected the capability (`Auto-detected via /detect-sensors from <evidence>`) and why you chose `output: <single|stream>`.
- For inferential sensors, add `calibration` (`confidence_threshold`, `calibration_set`, `calibration_size`, `calibration_date: 2026-05-08`) and `blind_spots`.

When the literal command is uncertain (common for `fetch-logs`, `fetch-metrics`, `trace-request`), still emit the sensor: put your best-guess command in `execution.command` and add a `blind_spots[]` entry stating what you assumed (auth profile, project id, region, log filter, etc.). The user reviews and tightens.

**Never skip a sensor because credentials are missing.** If a capability needs auth tokens, RSA keys, project ids, region selectors, or any other host-supplied secret, declare them in `requires.env` and emit the sensor anyway. The runner forwards every listed env var from its own environment into the subprocess; if a non-optional name is unset at run time, the runner aborts with `verdict=error` and a remediation that names the missing var. The sensor's *existence* is independent of whether you, right now, can run it. Examples:

```jsonc
"requires": {
  "env": [
    { "name": "GITHUB_TOKEN",                     "description": "PAT with repo:read scope" },
    { "name": "GCP_PROJECT_ID",                   "description": "Project id used by gcloud logging read filters" },
    { "name": "DATADOG_API_KEY",                  "description": "Datadog API key for metric queries" },
    { "name": "AWS_PROFILE",  "optional": true,   "description": "Optional named profile; default credentials chain is used when unset" }
  ]
}
```

Use `requires.env` for anything secret or per-developer; use `execution.env` only for static, non-secret literals (`LANG`, feature flags, deterministic seeds). Never put a token, key, or per-environment id directly into the sensor JSON — that file is committed to the repo.

### 5. Author fixtures BEFORE you persist

A sensor without real fixtures is half-built. For every `golden_cases[]` entry you wrote in step 4, create the file at `<project>/sensors/fixtures/<group>/<case>.txt` (or `.json` for parser-style sensors). The fixture must contain content shaped exactly like the production tool's stdout for that case.

Conventions that work:

- Group fixtures by tool family, not by sensor id, so `lint-go-vet-computational` and `lint-go-vet-inferential` share `sensors/fixtures/lint-go-vet/{clean,has-warning}.txt`. Same for `unit-test/{all-pass,has-failure,has-skip}.txt`, `build-runner/{clean,has-error}.txt`.
- Always include at least: one **happy path** fixture (clean stdout → expected `pass`) and one **fail path** fixture (representative finding → expected `fail` or `warn`). Add a third for `skip`/`warn` semantics when the tool produces them.
- Fixture content should be a faithful capture, not a hand-crafted approximation. For Go tests: `=== RUN ...\n--- PASS: ... (0.00s)\nPASS\nok ... 0.012s`. For Go vet/build: `# package\n./file.go:LINE:COL: message`. For schema parsers: a real malformed JSON.
- Keep them small (a handful of lines) — they exist for verification, not for stress.

`expected_verdict` and `expected_severity` for each case must match what the runner *actually* computes when patterns + exit_code_map see that fixture. The next step proves it.

### 6. Persist each draft

Write the draft JSON to a temp file, then run the validator-and-writer:

```bash
go run ./skills/detect-sensors/scripts \
  --out=<project>/sensors \
  /tmp/<draft-name>.json
```

The script:

- Reads the draft file.
- Validates against `schemas/sensor.json` (cross-file `$ref` to `signal.json` resolved).
- If invalid: prints the validator's error tree to stderr and exits **1** without writing. Read the tree, fix the draft, re-run.
- If valid: writes the canonical sensor to `<out>/<sensor.id>.json` (2-space indent, alphabetised keys) and prints the absolute path on stdout.

Exit codes:

- `0` — sensor written.
- `1` — schema validation failed; nothing was written.
- `2` — usage / I/O error (missing `--out`, draft unreadable, mkdir failed, schemas not found).

Run the script once per draft. Iterate per capability — it is faster than batching because validator output is per-sensor.

Do not skip the validator step. The schema's `allOf` discriminators catch a class of mistakes you cannot reliably catch by inspection (computational sensors leaking inferential fields, single sensors declaring `output_parsing`, etc.).

### 7. Run each sensor and iterate until the output is informative

Schema-valid is not the same as semantically useful. After persisting, **run each sensor through `/run-sensor`** and inspect the aggregate Signal. Reject any of these symptoms:

- `output: "stream"` sensor returning `evidence: []` and `metadata.counts: {error:0,fail:0,pass:0,warn:0}` *when the underlying command actually produced output*. That means your patterns matched nothing — fix the regex, add `-v`/`--verbose` to the command, or escalate the relevant lines (e.g. `go test` needs `-v` or no PASS lines appear).
- Aggregate `verdict: "pass"` when you know the codebase has unfixed findings — patterns are skipping them.
- `metadata.timed_out: true` — your `cost.latency.timeout_ms` is too low for a real run.
- `evidence` entries with empty `excerpt` and `rationale` falling back to the entire raw line — capture groups are wrong.

Run order:

```bash
# 1) Production happy-path: run the sensor against the real codebase.
go run -tags=run_computational ./skills/run-sensor/scripts @sensors/<id>.json | tail -n 1 \
  | jq -c '{verdict, severity, counts: .metadata.counts, individuals: (.evidence|length)}'

# 2) Replay each fail/warn fixture to prove the unhappy paths.
#    Build a temp sensor whose execution.command is `cat <fixture>` so the
#    runner exercises the production patterns + exit_code_map against captured
#    content, then assert the aggregate matches expected_verdict.
TMP=$(mktemp /tmp/replay-XXXX.json)
jq --arg cmd "cat sensors/fixtures/<group>/<case>.txt" \
   '.execution.command = $cmd | .id = "replay-" + .id' \
   sensors/<id>.json > "$TMP"
go run -tags=run_computational ./skills/run-sensor/scripts "$TMP" | tail -n 1 \
  | jq -c '{verdict, severity, individuals: (.evidence|length)}'
rm "$TMP"
```

For each sensor, both must hold:

- Happy path on the live repo: aggregate `verdict` matches reality (clean repo → `pass`; dirty repo → `fail`/`warn`). Empty `evidence` is acceptable iff the underlying tool is genuinely silent on success (vet, build, schema parsers); for tools that emit per-test output (Go test with `-v`, jest, pytest -v), `counts` MUST show non-zero in the relevant bucket.
- Each `golden_cases[]` entry: replay must produce the declared `expected_verdict` and `expected_severity`. If a replay disagrees, EITHER the patterns are wrong (most common) OR `expected_verdict` is wrong — fix one and re-replay until both agree.

If iteration changes `output`, `execution`, or `verification`, bump the sensor `version` (e.g. `0.1.0` → `0.2.0`) and re-persist via the validator. The version stamp is the audit trail of which shape was actually verified.

### 8. Report back to the user

When every draft is persisted **and verified** (step 7 passed for happy and unhappy paths), surface the result as a bulleted list of paths plus the verdict observed for each, so the user can immediately fan out into `/run-sensor`:

```
Generated 7 sensors at /repo/sensors/ (all verified happy + replay paths):
- /repo/sensors/lint-eslint.json           — happy: pass · replay(has-finding): fail/medium ·  4 fixtures
- /repo/sensors/build-vite.json            — happy: pass · replay(compile-error): fail/high   · 2 fixtures
- /repo/sensors/unit-test-vitest.json      — happy: pass(83) · replay(has-failure): fail/high · 3 fixtures
- /repo/sensors/e2e-playwright.json        — happy: pass(12) · replay(timeout): fail/high     · 3 fixtures
- /repo/sensors/run-project-vite-dev.json  — single-mode  · replay(crash): fail/high          · 2 fixtures
- /repo/sensors/fetch-logs-cloudrun.json   — happy: pass · NEEDS-AUTH (gcloud login)          · 1 fixture
- /repo/sensors/fetch-metrics-cloud-monitoring.json — happy: pass · NEEDS-AUTH                · 1 fixture

Run any of them with `/run-sensor @sensors/<id>.json`.
Fixtures live under /repo/sensors/fixtures/<group>/<case>.{txt,json}.
```

Be honest about anything still soft: sensors whose live command needs credentials you do not have (`NEEDS-AUTH`), `cost.latency` numbers that are estimates rather than measured, observability sensors whose query strings are best-guess. Call those out explicitly so the user knows where to focus the review.

## Safety notes

- The script never executes the detected commands. It only validates JSON and writes files.
- Existing files at `<out>/<sensor-id>.json` are overwritten atomically by `os.Create`. Commit `sensors/` before re-running so diffs are reviewable.
- Drafts you stage in `/tmp/` are yours to clean up; the script does not touch them.
- Schemas are resolved by walking up from cwd; invoke from inside the harness-framework checkout (or pass `--schemas-dir=<plugin>/schemas`) so the validator sees the right contract.
