# /detect-sensors Self-contained Components Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land Spec C — `stack.yaml.Component` becomes self-contained (3 optional fields + 8 new role values), and `/detect-sensors` SKILL.md is reframed to draft reusable building-block sensors per Component.

**Architecture:** Two atomic PRs. PR 1 ships the schema + struct + tests + framework stack.yaml enrichment as a single commit (CI must remain green at HEAD before Phase A re-runs surface new Components requiring the new roles). PR 2 ships the SKILL.md reframe + 2 new framework usecases + 2 new framework sensors + the CI skip-by-id entry.

**Tech Stack:** Go 1.25, JSON Schema Draft 2020-12, `sigs.k8s.io/yaml`, `santhosh-tekuri/jsonschema/v5`. No new dependencies.

**Spec under implementation:** `docs/superpowers/specs/2026-05-17-detect-sensors-self-contained-component-design.md`

---

## File Structure

### PR 1 — Atomic schema commit

| Action | Path | Responsibility |
|---|---|---|
| Modify | `schemas/stack.yaml:17-47` | Add 3 optional fields (`category`, `capabilities`, `observable_surface`) under `$defs/Component.properties`. |
| Modify | `schemas/stack.yaml:230-248` | Append 8 values to the end of `$defs/Role.enum`. Preserve `description:` and existing 12 values. |
| Modify | `lib/stack/shape.go:24-30` | Add 3 fields to the `Component` struct (matching JSON tag style). |
| Modify | `lib/stack/load_test.go` | Add test cases for: new fields round-trip, new role values accepted, unknown role rejected, backward-compat (existing fixture still parses). |
| Create | `lib/stack/testdata/golden-stack-with-new-fields.yaml` | Test fixture using all 3 new Component fields + one new role value. |
| Create | `lib/stack/testdata/invalid-unknown-role.yaml` | Test fixture using a role value not in the enum. |
| Modify | `.harness/stack.yaml:3-41` | Enrich the 3 existing Components with `category`, `capabilities`, `observable_surface`. |

### PR 2 — SKILL.md reframe + dogfooding

| Action | Path | Responsibility |
|---|---|---|
| Modify | `skills/detect-sensors/SKILL.md:33` | Replace verbatim Role enumeration with a single instruction pointing at the schema. |
| Modify | `skills/detect-sensors/SKILL.md:30-43` | Add broader evidence sources (Helm, Dockerfile, k8s, CI workflows, OTel/Sentry/DD configs, integration SDKs) + per-Component instructions to populate the 3 new fields. |
| Modify | `skills/detect-sensors/SKILL.md:63-77` | Extend degraded path so a project with only deploy/CI artifacts still produces useful Components. |
| Modify | `skills/detect-sensors/SKILL.md:166-225` | Phase B reframe: building-block authoring model + component-to-archetype heuristic table. |
| Create | `.harness/usecases/framework/framework-detect-sensors-component-self-contained.yaml` | Usecase: `/detect-sensors` fills the 3 new fields on every detected Component. |
| Create | `.harness/usecases/framework/framework-detect-sensors-deploy-artifacts-detected.yaml` | Usecase: Phase A surfaces Helm/Dockerfile/CI files as Components. |
| Create | `.harness/sensors/assert-stack-schema-valid.yaml` | New CI-running sensor: validates `.harness/stack.yaml` against `schemas/stack.yaml`. |
| Create | `.harness/sensors/assert-detect-sensors-self-contained.yaml` | New manual-acceptance sensor (skipped in CI): asserts each Component has all 3 new fields non-empty. |
| Modify | `.github/workflows/test.yml:215-218` | Add `assert-detect-sensors-self-contained` to the skip-by-id list. |

---

## PR 1: Atomic Schema + Struct + Tests + Framework stack.yaml

### Task 1: Add testdata fixture exercising new Component fields

**Files:**
- Create: `lib/stack/testdata/golden-stack-with-new-fields.yaml`

- [ ] **Step 1: Create the fixture file**

```yaml
# lib/stack/testdata/golden-stack-with-new-fields.yaml
components:
- category: observability/logger
  capabilities:
  - Emits structured JSON logs to stderr via Zap production preset.
  - Tags every line with a severity field consumed by downstream regexes.
  config_summary: zap.NewProductionConfig() with default encoder.
  evidence:
  - file: cmd/server/main.go
    line_end: 50
    line_start: 42
    rationale: zap.NewProductionConfig() call site
  name: go.uber.org/zap
  observable_surface:
  - Every stdout line of the running service.
  - Process exit code on panic propagated through Fatal.
  role: logger
  version: 1.27.0
- category: deployment/helm
  capabilities:
  - Packages the service into a Helm chart for staging and production.
  - Renders Kubernetes manifests via `helm template`.
  config_summary: Chart.yaml plus values.yaml at repo root under helm/.
  evidence:
  - file: helm/Chart.yaml
    rationale: Chart metadata
  name: helm
  observable_surface:
  - "`helm template` stdout (rendered manifests)."
  - "`helm install --dry-run` exit code and stderr."
  role: deployment-tool
  version: 3.14.0
detected_at: "2026-05-17T10:00:00Z"
detected_by: manual
languages:
- name: go
  version: "1.25"
log_shapes:
- fields:
  - key: severity
    meaning: severity
  - key: msg
    meaning: message
  format: json
  id: zap-prod-json
  produced_by:
  - go.uber.org/zap
  sample: '{"severity":"INFO","msg":"server listening"}'
  severity_values:
  - DEBUG
  - INFO
  - WARN
  - ERROR
version: 0.1.0
```

- [ ] **Step 2: No commit yet — fixture stands alone**

This fixture is consumed by the test added in Task 2.

### Task 2: Add testdata fixture for invalid unknown role

**Files:**
- Create: `lib/stack/testdata/invalid-unknown-role.yaml`

- [ ] **Step 1: Create the fixture file**

```yaml
# lib/stack/testdata/invalid-unknown-role.yaml
components:
- config_summary: pretends to be a known component.
  evidence:
  - file: src/main.go
    rationale: not really used anywhere
  name: my-fake-component
  role: garbage-role-that-does-not-exist
detected_at: "2026-05-17T10:00:00Z"
detected_by: manual
languages:
- name: go
  version: "1.25"
log_shapes:
- format: plain
  id: dummy
  produced_by:
  - my-fake-component
  sample: 'a line of plain output'
version: 0.1.0
```

- [ ] **Step 2: No commit yet — fixture stands alone**

### Task 3: Add failing test cases to lib/stack/load_test.go

**Files:**
- Modify: `lib/stack/load_test.go:9-33`

- [ ] **Step 1: Replace the test table with the extended list**

Open `lib/stack/load_test.go` and replace the `cases := []struct{...}{...}` literal (lines 10-20) with:

```go
	cases := []struct {
		name       string
		fixture    string
		wantCode   int
		wantSubstr string // expected fragment in stderr when wantCode != 0
	}{
		{name: "golden", fixture: "golden-stack.yaml", wantCode: 0},
		{name: "with journeys", fixture: "golden-stack-with-journeys.yaml", wantCode: 0},
		{name: "with new component fields", fixture: "golden-stack-with-new-fields.yaml", wantCode: 0},
		{name: "missing required", fixture: "invalid-missing-required.yaml", wantCode: 1, wantSubstr: "version"},
		{name: "bad enum", fixture: "invalid-enum.yaml", wantCode: 1, wantSubstr: "format"},
		{name: "unknown role", fixture: "invalid-unknown-role.yaml", wantCode: 1, wantSubstr: "role"},
	}
```

The two added rows are `with new component fields` (positive — uses `category`, `capabilities`, `observable_surface`, and the new `deployment-tool` role) and `unknown role` (negative — uses a role value not in the enum).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./lib/stack/... -run TestLoadStackFile -v`

Expected: both new rows FAIL. The `with new component fields` row fails because the schema currently rejects `category`/`capabilities`/`observable_surface` (Component has `additionalProperties: false`) AND rejects the `deployment-tool` role (not in enum). The `unknown role` row fails because today nothing distinguishes `garbage-role-that-does-not-exist` from a valid value — the test expects rejection on `role`, and currently any string ALREADY fails on `role` enum, so this row may actually pass at this point. Re-verify after Step 4 of Task 4 (schema extension); we want the unknown-role rejection to remain true after the enum is extended.

### Task 4: Extend `schemas/stack.yaml`

**Files:**
- Modify: `schemas/stack.yaml:17-47` (add Component fields)
- Modify: `schemas/stack.yaml:230-248` (append Role values)

- [ ] **Step 1: Add three optional fields under `$defs/Component.properties`**

Find the `Component:` block at `schemas/stack.yaml:17`. Inside its `properties:` map (currently containing `config_summary`, `evidence`, `name`, `role`, `version` in alphabetical order), add three more entries — keep alphabetical order:

```yaml
      capabilities:
        description: |
          What this component does for this project. Short complete
          sentences, each a discrete claim that downstream sensors can
          validate. Capabilities not exercised by the project MUST NOT
          be listed. Optional — manual stack.yaml entries may omit;
          /detect-sensors populates.
        items:
          type: string
        type: array
      category:
        description: |
          Canonical taxonomy slug, "<domain>/<role>" style. Examples:
          'observability/tracer', 'observability/errors',
          'http/framework', 'deployment/helm', 'deployment/k8s',
          'ci/github-actions', 'messaging/queue-consumer',
          'cache/client', 'integration/stripe'. Free-form by schema;
          convention by SKILL.md. Optional — /detect-sensors fills.
        type: string
```

And after `name:`, add:

```yaml
      observable_surface:
        description: |
          Where evidence of this component's behavior appears — log
          lines, config files, endpoints, commands, env vars. Each
          entry is a short prose pointer downstream sensor authoring
          can convert into a regex pattern, a probe command, or a
          file assertion. Free-form prose; concrete regex/command
          choices are sensor-level concerns.
        items:
          type: string
        type: array
```

Do NOT add the new keys to the `required:` array — they are optional.

- [ ] **Step 2: Append 8 values to `$defs/Role.enum`**

Find the `Role:` block at `schemas/stack.yaml:230`. Its `enum:` currently lists 12 values, ending with `- test-runner` at line 247. Append (do NOT replace) 8 new values after `- test-runner`, preserving the existing `description:` block above and the 12 existing values:

```yaml
    - error-tracker
    - cache-client
    - object-store
    - job-runner
    - container-runtime
    - deployment-tool
    - ci-cd
    - external-integration
```

The resulting `Role:` block has `description:` (unchanged), `enum:` (12 + 8 = 20 entries), and `type: string`.

- [ ] **Step 3: Run yq to confirm the YAML still parses**

Run: `yq -r '.title' schemas/stack.yaml`
Expected output: `Stack`

If yq prints an error, the YAML is malformed — re-check indentation. The existing CI step at `.github/workflows/test.yml:33` runs this exact command.

### Task 5: Extend the `Component` struct in `lib/stack/shape.go`

**Files:**
- Modify: `lib/stack/shape.go:24-30`

- [ ] **Step 1: Add three fields to the `Component` struct**

Open `lib/stack/shape.go` and find the `Component` struct at line 24. Replace it with:

```go
type Component struct {
	Role              Role       `json:"role"`
	Name              string     `json:"name"`
	Version           string     `json:"version,omitempty"`
	ConfigSummary     string     `json:"config_summary,omitempty"`
	Evidence          []Evidence `json:"evidence"`
	Category          string     `json:"category,omitempty"`
	Capabilities      []string   `json:"capabilities,omitempty"`
	ObservableSurface []string   `json:"observable_surface,omitempty"`
}
```

The three new fields are all `,omitempty` because the schema marks them optional — absence must round-trip as absence, not as the zero value.

- [ ] **Step 2: Add the 8 new role constants**

Find the `const (...)` block starting at line 65 listing `RoleHTTPServer ... RoleTestRunner`. Append 8 new constants before the closing `)`:

```go
	RoleErrorTracker        Role = "error-tracker"
	RoleCacheClient         Role = "cache-client"
	RoleObjectStore         Role = "object-store"
	RoleJobRunner           Role = "job-runner"
	RoleContainerRuntime    Role = "container-runtime"
	RoleDeploymentTool      Role = "deployment-tool"
	RoleCICD                Role = "ci-cd"
	RoleExternalIntegration Role = "external-integration"
```

- [ ] **Step 3: Run the test suite — expect green**

Run: `go test ./lib/stack/... -run TestLoadStackFile -v`

Expected: all 6 rows pass. The `with new component fields` fixture now decodes (schema accepts the 3 new keys; Go struct has matching fields). The `unknown role` fixture still rejects on `role` enum (closed enum unchanged in semantics — still closed, just with 8 more values).

If `with new component fields` still fails, re-check:
1. Indentation under `Component.properties:` (must be siblings of `name:` and `evidence:`).
2. The struct tag spelling (`json:"observable_surface,omitempty"`, not `observable-surface`).
3. Schema-side, the new keys are under `properties:` (not at the root).

### Task 6: Enrich `.harness/stack.yaml` with the new fields

**Files:**
- Modify: `.harness/stack.yaml:4-41`

- [ ] **Step 1: Add the 3 new fields to the `testing` Component**

Find the entry starting at `.harness/stack.yaml:4` (`config_summary: Go standard testing package...`, `name: testing`). Add three new keys alongside the existing keys (alphabetical order — `capabilities`, `category`, `config_summary` come before `evidence`, then `name`, `observable_surface`, `role`, `version`):

```yaml
- capabilities:
  - Runs the framework's Go unit tests via `go test`.
  - Reports per-test outcomes as actionable lines on stdout (`--- PASS:`, `--- FAIL:`, `--- SKIP:`).
  category: testing/runner
  config_summary: Go standard testing package. Tests are invoked via `go test -race
    -count=1 ./lib/...` and `go test -race -count=1 -tags=<tag> ./skills/...` in CI;
    sensors that observe these runs add `-v` so each test result becomes an individually-actionable
    `--- PASS:`, `--- FAIL:`, or `--- SKIP:` line on stdout. Table-driven tests are
    the project default.
  evidence:
  - file: .github/workflows/test.yml
    line_start: 40
    rationale: CI runs `go test -race -count=1 ./lib/...` and `-tags=run_computational/run_inferential
      ./skills/...`.
  - file: lib/sensor/envelope_test.go
    rationale: Representative table-driven test using the standard testing package.
  name: testing
  observable_surface:
  - "`go test` exit code (0 = pass, 1 = fail, 2 = build error)."
  - "`--- PASS:`, `--- FAIL:`, `--- SKIP:` lines on stdout under `-v`."
  - Build-time compilation errors emitted before any test runs.
  role: test-runner
  version: 1.25.3
```

- [ ] **Step 2: Add the 3 new fields to the `go-tool-compiler` Component**

Find the entry with `name: go-tool-compiler`. Add:

```yaml
- capabilities:
  - Compiles Go source into binaries used by every framework script.
  - Emits diagnostic lines during `go build` and `go vet` for type errors and lint findings.
  category: tooling/compiler
  config_summary: 'Go toolchain compiler and vet. Invoked via `go build -o /dev/null
    ./skills/...` and `go vet -tags=<tag> ./...`; emits diagnostic lines in the form
    `<file>:<line>:<col>: <message>` to stderr (and stdout when piped). Same diagnostic
    shape across vet and build.'
  evidence:
  - file: .github/workflows/test.yml
    line_start: 34
    rationale: CI runs `go vet -tags=run_computational ./...` and `go build -tags=...
      -o /dev/null` to surface diagnostics.
  name: go-tool-compiler
  observable_surface:
  - Diagnostic lines of the form `<file>:<line>:<col>: <message>` on stderr.
  - Exit code 0 on success, non-zero on any diagnostic.
  role: log-encoder
  version: 1.25.3
```

- [ ] **Step 3: Add the 3 new fields to the `harness-signal-encoder` Component**

Find the entry with `name: harness-signal-encoder`. Add:

```yaml
- capabilities:
  - Encodes one JSONL Signal per matched stream line during sensor execution.
  - Encodes a final aggregate Signal as the LAST JSONL line on stdout.
  - Conforms to schemas/signal.yaml for cross-sensor routing.
  category: observability/logger
  config_summary: The harness sensor runner emits one Signal JSON object per matched
    line (stream mode) plus one final aggregate Signal on stdout as JSONL; objects
    conform to schemas/signal.json. Carrier of verdict/severity for cross-sensor routing.
  evidence:
  - file: lib/signal/builder.go
    rationale: Where Signals are constructed and serialized as JSON to stdout.
  - file: schemas/signal.json
    rationale: Contract for the JSON shape.
  name: harness-signal-encoder
  observable_surface:
  - Every stdout line of every sensor invocation parses as a Signal JSON object.
  - The LAST JSONL line is always the aggregate Signal.
  - Aggregate Signal carries `metadata.counts: {pass, warn, fail, error}`.
  role: logger
  version: 1.1.0
```

- [ ] **Step 4: Run the validator end-to-end against the framework stack**

Run:
```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "$(pwd)" -tags=write_stack \
  ./skills/detect-sensors/scripts \
  --out=/tmp/stack-validation-pr1 \
  --schemas-dir="$(pwd)/schemas" \
  .harness/stack.yaml > /dev/null
```

Expected: exit code 0, no stderr output. The validator round-trips the file through the schema and writes a copy to `/tmp/stack-validation-pr1/stack.yaml`.

If validation fails, read the error tree — the most likely cause is YAML indentation drift in Step 1/2/3 (mixing tabs and spaces, or wrong alignment under the list entry).

### Task 7: Commit PR 1

- [ ] **Step 1: Confirm the working tree state**

Run: `git status --short`

Expected (file paths in any order):
```
 M .harness/stack.yaml
 M lib/stack/load_test.go
 M lib/stack/shape.go
 M schemas/stack.yaml
?? lib/stack/testdata/golden-stack-with-new-fields.yaml
?? lib/stack/testdata/invalid-unknown-role.yaml
```

- [ ] **Step 2: Run the full lib test suite once more**

Run: `go test -race -count=1 ./lib/stack/...`

Expected: all green. If anything fails, fix it before committing.

- [ ] **Step 3: Stage and commit**

```bash
git add schemas/stack.yaml lib/stack/shape.go lib/stack/load_test.go \
        lib/stack/testdata/golden-stack-with-new-fields.yaml \
        lib/stack/testdata/invalid-unknown-role.yaml \
        .harness/stack.yaml

git commit -m "$(cat <<'EOF'
feat(stack): self-contained Component schema (category, capabilities, observable_surface) + 8 new roles

Spec C — atomic schema commit. Adds three optional fields to
$defs/Component (category, capabilities, observable_surface) and
appends 8 values to $defs/Role.enum: error-tracker, cache-client,
object-store, job-runner, container-runtime, deployment-tool,
ci-cd, external-integration.

lib/stack/shape.go mirrors the additions with omitempty tags so
absence round-trips as absence. New testdata fixtures cover the
positive (golden-stack-with-new-fields) and negative
(invalid-unknown-role) paths.

The framework's own .harness/stack.yaml is enriched in the same
commit so the schema and the canonical example never disagree.
SKILL.md updates and the new framework sensors land in a follow-up
PR — Step 3 of the spec depends on Step 1 having merged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 4: Push and confirm CI green**

```bash
git push -u origin HEAD
gh pr create --title "feat(stack): self-contained Component schema (Spec C, PR 1/2)" --body "$(cat <<'EOF'
## Summary
- Adds optional `category`, `capabilities`, `observable_surface` to `$defs/Component` in `schemas/stack.yaml`.
- Appends 8 values to `$defs/Role.enum`: `error-tracker`, `cache-client`, `object-store`, `job-runner`, `container-runtime`, `deployment-tool`, `ci-cd`, `external-integration`.
- Mirrors the additions on `lib/stack.Component` and `lib/stack.Role` constants.
- Enriches the framework's own `.harness/stack.yaml` so the schema and the canonical example stay in lockstep.

PR 1 of 2 for Spec C. PR 2 will land the SKILL.md reframe and the new framework sensors.

## Test plan
- [ ] CI: `go test + vet` passes (covers `lib/stack/...` including new positive/negative fixtures).
- [ ] CI: `sensors — run plugin against itself` passes (the existing stack validation step covers the enriched `.harness/stack.yaml`).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Wait for CI. Both jobs (`go test + vet`, `sensors — run plugin against itself`) must be green before merging. If anything fails, fix it on a follow-up commit before opening PR 2.

---

## PR 2: SKILL.md Reframe + Dogfooding

**Precondition: PR 1 merged.** Step 3 of this PR re-runs `/detect-sensors` mentally and authors new framework sensors that may use the new role values (e.g., `deployment-tool` for Helm). Persisting them via `write-stack.go`/`write-sensor.go` against an un-extended schema would fail validation.

### Task 8: Refactor SKILL.md Phase A — broader evidence sources and stop enumerating Roles

**Files:**
- Modify: `skills/detect-sensors/SKILL.md:30-43`

- [ ] **Step 1: Replace the verbatim Role enumeration**

Find the line at `skills/detect-sensors/SKILL.md:33` that opens:

```markdown
2. **Components** — for each runtime-observable role (`logger`, `log-encoder`, `http-server`, `http-router`, `http-middleware`, `tracer`, `metrics`, `queue-consumer`, `queue-producer`, `db-client`, `rpc`, `test-runner`):
```

Replace it with:

```markdown
2. **Components** — for each runtime-observable role declared in `$defs/Role` of `schemas/stack.yaml` (the schema is the single source of truth — do not duplicate the value list in this prose):
```

This honors the project convention "Skill markdown stays stack-driven, never enumerated".

- [ ] **Step 2: Add broader evidence sources to Phase A**

Below the existing bullet "Identify the library actually used (Zap, Logrus, Pino, Winston, Logback, structlog, …)." and the bullet about `evidence[]`, insert a new bullet (still inside the same `2. **Components**` list item):

```markdown
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
```

- [ ] **Step 3: Add the "populate three new Component fields" instruction**

Immediately after the new bullet from Step 2 (still inside `2. **Components**`), append:

```markdown
   - **For every detected Component, populate the three self-containment fields** in addition to `role`, `name`, `version`, `config_summary`, `evidence`:
     - **`category`** — canonical taxonomy slug from the convention `<domain>/<role>` (e.g., `observability/tracer`, `http/framework`, `deployment/helm`). Free-form by the schema, conventional by this skill.
     - **`capabilities[]`** — what this Component does **for this project specifically**, not generic library capabilities. Derive from the Component's evidence + your knowledge of the library. If OTel is present but `main.go` only calls `otel.SetTracerProvider` and never `otel.SetMeterProvider`, list traces and NOT metrics. Capabilities the project doesn't exercise MUST NOT be listed.
     - **`observable_surface[]`** — where evidence of this Component's behavior appears: log lines, config files, endpoints, commands, env vars. Keep entries as short prose pointers — Phase B sensor authoring converts them into concrete regex patterns or probe commands. Do NOT pre-shape into regex/command syntax here.
```

### Task 9: Update Phase A degraded path to handle deploy-only projects

**Files:**
- Modify: `skills/detect-sensors/SKILL.md:63-77`

- [ ] **Step 1: Replace the degraded-path block**

Find the block starting at `skills/detect-sensors/SKILL.md:63` ("### 0.5 Stack discovery — degraded path") through line 77. Replace the body with:

```markdown
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
```

This change captures the spec's "Spec C extension: a project with just a Dockerfile + helm chart and no observable runtime still deserves `assert-image-builds` and `assert-helm-lint-pass` building blocks."

### Task 10: Reframe SKILL.md §4 — building blocks per Component

**Files:**
- Modify: `skills/detect-sensors/SKILL.md:166-225` (introduction to §4)

- [ ] **Step 1: Insert the "Authoring model" header at the start of §4**

Find the `### 4. Draft each sensor` heading at line ~166. Immediately after the heading and before the existing "Mandate: never skip a sensor." paragraph, insert:

```markdown
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

```

This goes IMMEDIATELY after the `### 4. Draft each sensor` heading and BEFORE the existing "**Mandate: never skip a sensor.**" paragraph. The rest of §4 (mandate, capability table, defaults, output_parsing rules, the run-project-nest template, §4.5, §4.6) is unchanged.

### Task 11: Validate the SKILL.md edits parse and commit

**Files:**
- The SKILL.md edits are now complete.

- [ ] **Step 1: Confirm the file is syntactically intact**

Run: `head -200 skills/detect-sensors/SKILL.md | wc -l`

Expected: 200 (no read errors).

Run: `grep -c '^### ' skills/detect-sensors/SKILL.md`

Expected: at least 12 (number of `### ` headings should be unchanged or grow by 0 — we did not add new section headers).

- [ ] **Step 2: Skip commit at this point**

Roll Tasks 8–10 (SKILL.md edits) into a single commit at the end of PR 2 alongside the usecases, sensors, and CI gate update. This keeps the PR atomic.

### Task 12: Author `framework-detect-sensors-component-self-contained` usecase

**Files:**
- Create: `.harness/usecases/framework/framework-detect-sensors-component-self-contained.yaml`

- [ ] **Step 1: Create the usecase file**

```yaml
# .harness/usecases/framework/framework-detect-sensors-component-self-contained.yaml
id: framework-detect-sensors-component-self-contained
version: 0.1.0
name: /detect-sensors produces self-contained Components in stack.yaml
journey_id: framework
description: |
  After `/detect-sensors` finishes Phase A, every Component it
  persists to stack.yaml carries the three self-containment fields
  (`category`, `capabilities`, `observable_surface`) populated, so
  Phase B can draft building-block sensors from the Component alone
  without re-deriving everything from raw evidence.
trigger:
  shape: harness skill invocation
  summary: /detect-sensors completes Phase A on a project with at least one detectable Component.
  fixture:
    project_state:
      go.mod: present
      .harness/stack.yaml: absent
behavior:
  summary: |
    Phase A scans manifests + deploy/observability artifacts,
    populates `category`/`capabilities`/`observable_surface` for
    each detected Component, and persists via write-stack.go.
  business_rules:
    - Every Component in the persisted stack.yaml has a non-empty `category` string.
    - Every Component has at least one entry in `capabilities[]`.
    - Every Component has at least one entry in `observable_surface[]`.
    - `category` follows the `<domain>/<role>` slug convention.
expected_outcome:
  shape: One persisted .harness/stack.yaml + schema validation pass
  summary: stack.yaml exists, parses against schemas/stack.yaml, and every Component has all 3 self-containment fields non-empty.
  fixture:
    persisted_file: .harness/stack.yaml
    every_component:
      category: non-empty string
      capabilities: array with >= 1 entry
      observable_surface: array with >= 1 entry
  invariants:
    - .harness/stack.yaml parses against schemas/stack.yaml.
    - For every entry in components[], `.category` is a non-empty string.
    - For every entry in components[], `(.capabilities | length)` is >= 1.
    - For every entry in components[], `(.observable_surface | length)` is >= 1.
  side_effects:
    - Creates .harness/stack.yaml.
evidence:
  - file: schemas/stack.yaml
    rationale: Schema defines the 3 optional self-containment fields on $defs/Component.
  - file: skills/detect-sensors/SKILL.md
    rationale: Phase A prose instructs the agent to populate all 3 fields per Component.
  - file: lib/stack/shape.go
    rationale: Go struct mirrors the schema; Component.Category/Capabilities/ObservableSurface decode from YAML.
regression_priority: high
tags:
  - framework-detect-sensors
  - self-contained-component
```

### Task 13: Author `framework-detect-sensors-deploy-artifacts-detected` usecase

**Files:**
- Create: `.harness/usecases/framework/framework-detect-sensors-deploy-artifacts-detected.yaml`

- [ ] **Step 1: Create the usecase file**

```yaml
# .harness/usecases/framework/framework-detect-sensors-deploy-artifacts-detected.yaml
id: framework-detect-sensors-deploy-artifacts-detected
version: 0.1.0
name: /detect-sensors surfaces deploy and CI artifacts as Components
journey_id: framework
description: |
  When the project contains deploy artifacts (Dockerfile, Helm
  chart, k8s manifests, terraform, CI workflows), Phase A persists
  one Component per artifact using the new Role values introduced
  in Spec C — so Phase B can draft building blocks like
  `assert-image-builds`, `assert-helm-lint-pass`,
  `assert-workflow-lint` on top of them.
trigger:
  shape: harness skill invocation
  summary: /detect-sensors runs on a project that has at least one of (Dockerfile, helm/Chart.yaml, .github/workflows/, *.tf).
  fixture:
    project_state:
      Dockerfile: present
      .github/workflows/test.yml: present
behavior:
  summary: |
    Phase A inspects the listed deploy/CI files, persists one
    Component per kind using the new role values, and pairs each
    with the self-containment fields (`category`, `capabilities`,
    `observable_surface`).
  business_rules:
    - A Dockerfile becomes `role: container-runtime` Component named `docker`.
    - A Helm Chart.yaml becomes `role: deployment-tool` Component named `helm`.
    - A GitHub workflow becomes `role: ci-cd` Component named `github-actions`.
    - Each surfaced Component carries category/capabilities/observable_surface.
expected_outcome:
  shape: stack.yaml contains one Component per detected deploy/CI artifact
  summary: For each present artifact, exactly one Component with the right role and self-containment fields.
  fixture:
    expected_components:
      - role: container-runtime
        category: runtime/container
      - role: ci-cd
        category_prefix: ci/
  invariants:
    - .harness/stack.yaml parses against schemas/stack.yaml.
    - For every detected deploy/CI artifact, components[] contains an entry with a matching role.
    - No deploy/CI Component lacks the 3 self-containment fields.
  side_effects:
    - Creates .harness/stack.yaml.
evidence:
  - file: skills/detect-sensors/SKILL.md
    rationale: Phase A "Beyond code manifests" bullet enumerates the deploy/CI artifact → role mapping.
  - file: schemas/stack.yaml
    rationale: $defs/Role enum includes container-runtime, deployment-tool, ci-cd values.
regression_priority: high
tags:
  - framework-detect-sensors
  - deploy-artifacts
```

### Task 14: Author `assert-stack-schema-valid` sensor + persist

**Files:**
- Create: `.harness/sensors/assert-stack-schema-valid.yaml`

- [ ] **Step 1: Stage the draft**

Write to `/tmp/assert-stack-schema-valid-draft.yaml`:

```yaml
id: assert-stack-schema-valid
version: 0.1.0
name: assert .harness/stack.yaml parses against schemas/stack.yaml
description: |
  Schema validation of the framework's own stack.yaml. Runs in CI on
  every PR — a regression in the Component shape (missing required
  fields, unknown role values, malformed capabilities[]) trips this
  sensor before the rest of the CI gate runs sensors that consume
  stack.yaml.
kind: assertion
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute:
    cpu: low
    memory_mb: 128
  latency:
    p50_ms: 500
    p95_ms: 5000
    timeout_ms: 30000
triggers:
  - "on": manual
use_cases:
  - framework-detect-sensors-component-self-contained
execution:
  command: |
    HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
      go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_stack \
      ./skills/detect-sensors/scripts \
      --out=/tmp/assert-stack-schema-valid \
      --schemas-dir="${CLAUDE_PLUGIN_ROOT}/schemas" \
      .harness/stack.yaml >/dev/null
  exit_code_map:
    - exit_code: 0
      verdict: pass
      severity: info
    - exit_code: "*"
      verdict: fail
      severity: high
```

- [ ] **Step 2: Persist via write-sensor.go**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "$(pwd)" -tags=write_sensor \
  ./skills/detect-sensors/scripts \
  --out="$(pwd)/.harness/sensors" \
  --schemas-dir="$(pwd)/schemas" \
  /tmp/assert-stack-schema-valid-draft.yaml
```

Expected: exit code 0, stdout prints the absolute path `<repo>/.harness/sensors/assert-stack-schema-valid.yaml`.

If exit is non-zero, the validator emitted a schema error tree to stderr. Read it, fix the draft, re-run. Common issues:
- `use_cases[0]` not on disk yet → run Task 12 first.
- Missing `triggers`, `cost.latency.timeout_ms`, `cost.compute` fields the schema requires.

- [ ] **Step 3: Run the sensor once locally**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "$(pwd)" -tags=run_computational \
  ./skills/run-sensor/scripts assert-stack-schema-valid | tail -n 1 | jq .
```

Expected: aggregate Signal with `verdict: pass`, `metadata.kind: aggregate`.

If `verdict: fail`, the sensor command itself is wrong (path resolution, missing `--schemas-dir`, etc.). Fix and re-persist.

### Task 15: Author `assert-detect-sensors-self-contained` sensor + persist

**Files:**
- Create: `.harness/sensors/assert-detect-sensors-self-contained.yaml`

- [ ] **Step 1: Stage the draft**

Write to `/tmp/assert-detect-sensors-self-contained-draft.yaml`:

```yaml
id: assert-detect-sensors-self-contained
version: 0.1.0
name: assert every Component in stack.yaml has the 3 self-containment fields populated
description: |
  Manual-acceptance sensor verifying Spec C's central invariant:
  every Component in .harness/stack.yaml has non-empty `category`,
  `capabilities`, and `observable_surface`. Listed in CI's
  skip-by-id because /detect-sensors authoring is a human/LLM
  workflow that doesn't run in CI; this sensor runs on demand
  after re-running /detect-sensors locally.
kind: assertion
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute:
    cpu: low
    memory_mb: 64
  latency:
    p50_ms: 200
    p95_ms: 2000
    timeout_ms: 10000
blind_spots:
  - Only verifies the 3 self-containment fields are NON-EMPTY; does not verify factual correctness of the strings.
  - Manual: skipped in CI because the upstream /detect-sensors invocation is interactive.
triggers:
  - "on": manual
use_cases:
  - framework-detect-sensors-component-self-contained
  - framework-detect-sensors-deploy-artifacts-detected
execution:
  steps:
    - id: check-category
      type: shell
      run: |
        set -e
        # Every component must have a non-empty category.
        missing=$(yq -o=json '.components[] | select((.category // "") == "") | .name' .harness/stack.yaml)
        if [ -n "$missing" ]; then
          echo "FAIL: components missing category:"
          echo "$missing"
          exit 1
        fi
      exit_code_map:
        "0": pass
        "*": fail
    - id: check-capabilities
      type: shell
      run: |
        set -e
        missing=$(yq -o=json '.components[] | select(((.capabilities // []) | length) < 1) | .name' .harness/stack.yaml)
        if [ -n "$missing" ]; then
          echo "FAIL: components with empty capabilities:"
          echo "$missing"
          exit 1
        fi
      exit_code_map:
        "0": pass
        "*": fail
    - id: check-observable-surface
      type: shell
      run: |
        set -e
        missing=$(yq -o=json '.components[] | select(((.observable_surface // []) | length) < 1) | .name' .harness/stack.yaml)
        if [ -n "$missing" ]; then
          echo "FAIL: components with empty observable_surface:"
          echo "$missing"
          exit 1
        fi
      exit_code_map:
        "0": pass
        "*": fail
```

- [ ] **Step 2: Persist via write-sensor.go**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "$(pwd)" -tags=write_sensor \
  ./skills/detect-sensors/scripts \
  --out="$(pwd)/.harness/sensors" \
  --schemas-dir="$(pwd)/schemas" \
  /tmp/assert-detect-sensors-self-contained-draft.yaml
```

Expected: exit code 0, stdout prints the absolute path. The 1:N use_cases[] (2 entries) is allowed by the schema.

- [ ] **Step 3: Run the sensor once locally — expect pass**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "$(pwd)" -tags=run_computational \
  ./skills/run-sensor/scripts assert-detect-sensors-self-contained | tail -n 1 | jq .
```

Expected: `verdict: pass`. PR 1 already enriched all 3 framework Components, so every step's `yq` query returns empty → exit 0 → pass.

If `verdict: fail` and the stderr shows a specific Component missing a field, return to Task 6 and ensure all 3 of `testing`, `go-tool-compiler`, `harness-signal-encoder` got all 3 new fields.

### Task 16: Update CI gate — skip `assert-detect-sensors-self-contained`

**Files:**
- Modify: `.github/workflows/test.yml:215-218`

- [ ] **Step 1: Add the skip-by-id branch**

Open `.github/workflows/test.yml` and find the block (around line 211–218):

```yaml
            # assert-create-sensor-multi-angle is a manual self-test: it
            # asserts /create-sensor's output, and there is no produced
            # sensor on disk in CI to assert against. The sensor itself
            # documents this precondition in its blind_spots[].
            if [ "$id" = "assert-create-sensor-multi-angle" ]; then
              echo "skip $id (manual acceptance — needs /create-sensor invocation first)"
              continue
            fi
```

Immediately after this block, add a second skip branch:

```yaml
            # assert-detect-sensors-self-contained is the Spec C manual
            # acceptance sensor: it asserts /detect-sensors filled the
            # 3 self-containment fields on every Component, which is a
            # workflow run by humans/LLMs interactively, not by CI.
            if [ "$id" = "assert-detect-sensors-self-contained" ]; then
              echo "skip $id (manual acceptance — needs /detect-sensors invocation first)"
              continue
            fi
```

Do NOT skip `assert-stack-schema-valid` — that sensor IS automatable and should run in CI.

- [ ] **Step 2: Run the sensors gate locally**

```bash
# Simulate the CI's sensors gate against the new local files.
for f in .harness/sensors/*.yaml; do
  id=$(basename "$f" .yaml)
  kind=$(yq -r '.kind' "$f")
  if [ "$kind" = "setup" ]; then continue; fi
  if [ "$id" = "assert-create-sensor-multi-angle" ]; then continue; fi
  if [ "$id" = "assert-detect-sensors-self-contained" ]; then continue; fi
  echo "running $id"
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
    go run -C "$(pwd)" -tags=run_computational \
    ./skills/run-sensor/scripts "$id" 2>&1 | tail -n 1 | jq -c '{id: "'"$id"'", verdict, severity}'
done
```

Expected: every sensor printed prints `verdict: pass`. The 3 existing sensors (`smoke-typed-pipeline`, `smoke-with-setup`, `assert-create-sensor-multi-angle` — last is skipped) plus the new `assert-stack-schema-valid` all return `pass`. `assert-detect-sensors-self-contained` is skipped by the loop.

If anything else returns non-pass, fix the sensor file before the next task.

### Task 17: Final sanity sweep before committing PR 2

- [ ] **Step 1: Verify the working tree state**

Run: `git status --short`

Expected:
```
 M .github/workflows/test.yml
 M skills/detect-sensors/SKILL.md
?? .harness/sensors/assert-detect-sensors-self-contained.yaml
?? .harness/sensors/assert-stack-schema-valid.yaml
?? .harness/usecases/framework/framework-detect-sensors-component-self-contained.yaml
?? .harness/usecases/framework/framework-detect-sensors-deploy-artifacts-detected.yaml
```

(plus any other untracked items not related to PR 2 — leave those alone.)

- [ ] **Step 2: Validate every sensor and the stack.yaml end-to-end**

```bash
# Stack
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "$(pwd)" -tags=write_stack \
  ./skills/detect-sensors/scripts \
  --out=/tmp/pr2-stack-validation \
  --schemas-dir="$(pwd)/schemas" \
  .harness/stack.yaml >/dev/null && echo "STACK OK"

# Sensors
for f in .harness/sensors/*.yaml; do
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
    go run -C "$(pwd)" -tags=write_sensor \
    ./skills/detect-sensors/scripts \
    --out=/tmp/pr2-sensors-validation \
    --schemas-dir="$(pwd)/schemas" \
    "$f" >/dev/null && echo "OK $(basename "$f")"
done
```

Expected:
```
STACK OK
OK assert-create-sensor-multi-angle.yaml
OK assert-detect-sensors-self-contained.yaml
OK assert-stack-schema-valid.yaml
OK smoke-typed-pipeline.yaml
OK smoke-with-setup.yaml
```

Any line that doesn't print `OK` means schema validation failed for that file — read stderr and fix before committing.

- [ ] **Step 3: Confirm SKILL.md grep checks**

```bash
# Confirm the verbatim Role enumeration was removed.
if grep -qE 'for each runtime-observable role \(`logger`,' skills/detect-sensors/SKILL.md; then
  echo "FAIL: Role enumeration still present in SKILL.md"
  exit 1
fi
echo "OK: Role list removed"

# Confirm the new "Authoring model" header was added.
if ! grep -q 'Authoring model — sensors as building blocks per Component' skills/detect-sensors/SKILL.md; then
  echo "FAIL: Building-block model section missing"
  exit 1
fi
echo "OK: Building-block section present"

# Confirm Rule #10 — no temporal narration.
if grep -qE '\b(used to be|after Spec [ABC]|PR #[0-9]+|after PR)' skills/detect-sensors/SKILL.md; then
  echo "FAIL: SKILL.md mentions temporal/version-history content"
  exit 1
fi
echo "OK: SKILL.md is temporal-narration-free"
```

Expected: 3× `OK` lines. If any FAIL, return to the relevant SKILL.md task and fix.

### Task 18: Commit PR 2

- [ ] **Step 1: Stage everything**

```bash
git add skills/detect-sensors/SKILL.md \
        .harness/usecases/framework/framework-detect-sensors-component-self-contained.yaml \
        .harness/usecases/framework/framework-detect-sensors-deploy-artifacts-detected.yaml \
        .harness/sensors/assert-stack-schema-valid.yaml \
        .harness/sensors/assert-detect-sensors-self-contained.yaml \
        .github/workflows/test.yml
```

- [ ] **Step 2: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(detect-sensors): building blocks per Component + broader Phase A evidence

Spec C, PR 2 of 2 — SKILL.md reframe and framework dogfooding.

Phase A:
- Stop enumerating Role values verbatim (stack-driven, never enumerated).
- Add broader evidence sources (Dockerfile, Helm, k8s, terraform, CI
  workflows, OTel/Sentry/DD configs, integration SDK clients).
- Per Component, populate category/capabilities/observable_surface.
- Degraded path extended for deploy-artifact-only projects.

Phase B:
- Reframe sensor authoring: building blocks per Component, not
  one-shot per usecase. use_cases[] lists every usecase a building
  block helps validate (1:N is intentional reuse).
- Component-to-archetype starter heuristic table inserted under §4.

Framework dogfooding:
- 2 new framework usecases under .harness/usecases/framework/:
  framework-detect-sensors-component-self-contained,
  framework-detect-sensors-deploy-artifacts-detected.
- 2 new framework sensors under .harness/sensors/:
  assert-stack-schema-valid (runs in CI),
  assert-detect-sensors-self-contained (manual acceptance, skipped
  in CI alongside assert-create-sensor-multi-angle).
- CI workflow extended with the new skip-by-id branch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 3: Push and open PR**

```bash
git push -u origin HEAD
gh pr create --title "feat(detect-sensors): building blocks per Component (Spec C, PR 2/2)" --body "$(cat <<'EOF'
## Summary
- SKILL.md Phase A: broader evidence sources, populate `category`/`capabilities`/`observable_surface` per Component, schema-driven Role list (no more verbatim enumeration).
- SKILL.md Phase B: reframe sensor authoring as **building blocks per Component**; `use_cases[]` lists every usecase a building block helps validate (1:N intentional reuse).
- Two new framework usecases under `.harness/usecases/framework/` covering self-containment and deploy-artifact detection.
- Two new framework sensors under `.harness/sensors/`: `assert-stack-schema-valid` (runs in CI) and `assert-detect-sensors-self-contained` (manual acceptance, skipped in CI).
- CI workflow extended with the new skip-by-id branch for the manual sensor.

PR 2 of 2 for Spec C. Builds on the schema additions in PR 1.

## Test plan
- [ ] CI: `go test + vet` passes.
- [ ] CI: `sensors — run plugin against itself` passes — `assert-stack-schema-valid` runs and emits `verdict: pass`; `assert-detect-sensors-self-contained` is skipped.
- [ ] Local: `assert-detect-sensors-self-contained` returns `verdict: pass` when invoked manually against `.harness/stack.yaml`.
- [ ] Local: `grep -qE 'for each runtime-observable role \\(\`logger\`,' skills/detect-sensors/SKILL.md` returns nothing (no verbatim Role list).
- [ ] Local: `grep -q 'Authoring model — sensors as building blocks per Component' skills/detect-sensors/SKILL.md` matches (building-block section present).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Wait for CI green**

Both `go test + vet` and `sensors — run plugin against itself` must pass before merge. If anything fails, fix on a follow-up commit before merging.

---

## Self-Review Notes (controller — read after writing the plan)

### Spec coverage

Walking the spec's section list:

- **§Problem.** Covered by the overall split into Phase A enrichment (Tasks 8–9) and Phase B reframe (Task 10).
- **§Conceptual model.** Documented in the SKILL.md `Authoring model` block inserted in Task 10.
- **§Schema changes → stack.yaml Component additions.** Tasks 1–7 (PR 1).
- **§Schema changes → Role enum extension.** Task 4 (append-only); Task 5 (Go constants).
- **§Schema changes → sensor.yaml unchanged.** No tasks required; verified during spec review.
- **§SKILL.md changes → Phase A broader evidence + populate 3 fields.** Task 8.
- **§SKILL.md changes → degraded path.** Task 9.
- **§SKILL.md changes → Phase B building-block model + table.** Task 10.
- **§Implementation order → Step 1 (atomic schema commit).** Tasks 1–7 (PR 1).
- **§Implementation order → Step 2 (SKILL.md).** Tasks 8–10.
- **§Implementation order → Step 3 (framework self-application).** Tasks 12–15 produce the new usecases and sensors; Task 17 validates the framework's stack and sensors end-to-end.
- **§Implementation order → Step 4 (usecases).** Tasks 12–13.
- **§Implementation order → Step 5 (CI gate).** Task 16.
- **§Acceptance criteria 1 (schema check).** Tasks 3, 4, 5 (tests + struct + schema).
- **§Acceptance criteria 2 (framework self-application).** Task 6 (existing 3 components enriched).
- **§Acceptance criteria 3 (skill body coherence).** Task 17 step 3 (grep checks for temporal narration).
- **§Acceptance criteria 4 (sensor reusability — ≥2 use_cases on new sensor).** Task 15 (`assert-detect-sensors-self-contained` lists both new usecases).
- **§Acceptance criteria 5 (CI green).** Tasks 7 and 18 (push + wait for CI).

No gaps.

### Placeholder scan

No "TBD", "TODO", "implement later", or "Add error handling" placeholders in the plan. Every step shows the exact YAML/Go/shell content.

### Type / identifier consistency

- `Component.Category` (Task 5) ↔ `category` YAML key (Task 4) ↔ `--category` (none — no CLI flag) ✔
- `Component.Capabilities` ↔ `capabilities` ↔ schema `items: type: string` ✔
- `Component.ObservableSurface` ↔ `observable_surface` (snake_case in YAML/JSON tag) ↔ `Component.observable_surface` schema key ✔
- `RoleErrorTracker = "error-tracker"` (Task 5 step 2) ↔ schema enum `- error-tracker` (Task 4 step 2) ✔
- Sensor `id: assert-stack-schema-valid` (Task 14) ↔ same id in CI skip check (Task 16 confirms it is NOT skipped) ✔
- Sensor `id: assert-detect-sensors-self-contained` (Task 15) ↔ same id in CI skip block (Task 16 step 1) ✔
- Usecase id `framework-detect-sensors-component-self-contained` (Task 12) ↔ `use_cases[]` in Task 14 and Task 15 ✔
- Usecase id `framework-detect-sensors-deploy-artifacts-detected` (Task 13) ↔ `use_cases[]` in Task 15 ✔

All identifiers cross-checked consistent.

---
