# Sensor dependencies and lifecycle design

Status: proposed
Date: 2026-05-08
Related: `schemas/sensor.json`, `schemas/signal.json`, `skills/run-sensor/`, `skills/detect-sensors/`

## Why

Real project capabilities are not isolated commands. A `run-project` sensor that boots a NestJS server on port 3000 only works after postgres is up, after `.env` exists with valid credentials, and after `node_modules` is installed. An `e2e-tests` sensor needs the same setup *plus* migrations and seed data, and afterward needs to drop the local database so the next run starts clean.

Today the schema offers no way to express any of this:

- `requires.upstream_sensors` is purely declarative — the runner ignores it. Authors who set it get false confidence; authors who don't set it have no recourse.
- `execution.command` is a single shell string. Multi-step setup means concatenating with `&&` and losing per-step verdict, per-step timeout, and the ability for setup errors to surface distinctly from observation errors.
- There is no taxonomy for sensors that *prepare* the world (start postgres, write `.env`, install deps) versus sensors that *observe* it. Detection skills can't classify them; tooling can't filter; humans reading the directory can't tell at a glance.

This spec adds the smallest schema surface that lets sensors declare their dependencies, run prepare/teardown phases around the main observed command, and be classified by purpose. The runner gains a thin DAG orchestrator; everything else stays put.

## What changes

1. **Top-level `depends_on: string[]`.** A sensor declares the IDs of sensors that must run and pass before it. The runner resolves the transitive closure, topologically sorts, and runs each dep's full lifecycle before the dependent. Failures cascade.
2. **Top-level `kind: "observation" | "assertion" | "setup"`.** Distinguishes sensors that observe behavior, sensors that assert against expectations, and sensors that prepare the environment. **Required** — no default. Metadata only — runner treats all three identically; tooling uses it for classification and naming. Phase 1 (schema bump) and Phase 3 (existing-sensor migration) ship in the same PR so no sensor lands in an unlabeled state.
3. **`execution.prepare[]` (silent, fail-fast).** Lifecycle steps that run before `execution.command`. Each is a shell command with optional per-step `timeout_ms` and `exit_code_map`. The first failure aborts prepare, skips the main command, but still runs teardown.
4. **`execution.teardown[]` (silent, best-effort).** Lifecycle steps that run *after* `execution.command` with `finally` semantics — they run on prepare failure, command failure, and command timeout. Individual teardown failures emit `severity=warn` evidence but do not downgrade the sensor's aggregate verdict.
5. **`requires.upstream_sensors` removed.** Replaced by top-level `depends_on`. No sensors in the repo depend on it (runner ignores it today), so removal is safe.
6. **New subpackage `lib/orchestrator/`.** DAG resolution, cycle detection, lifecycle execution, cascade propagation. Both runners (`run-computational`, `run-inferential`) delegate to it.

## Architecture

```
        /run-sensor X
              │
              ▼
        Load X (sensor.json)
              │
              ▼
        Resolve depends_on transitive closure
        (lib/sensor/load.go traverses by id)
              │
              ▼
        Topological sort (Kahn's algorithm)
        Cycle detection → exit 1
              │
              ▼
   ┌──────────────────────────────────┐
   │  for each sensor S in topo order │
   │  ┌────────────────────────────┐  │
   │  │ prepare[] (fail-fast)      │  │
   │  │   timeout_ms per step      │  │
   │  │   exit_code_map per step   │  │
   │  │   first failure → break    │  │
   │  └─────────────┬──────────────┘  │
   │                ▼                  │
   │  ┌────────────────────────────┐  │
   │  │ command (existing pipeline)│  │
   │  │   spawn sh -c              │  │
   │  │   stream patterns          │  │
   │  │   exit_code_map            │  │
   │  └─────────────┬──────────────┘  │
   │                ▼                  │
   │  ┌────────────────────────────┐  │
   │  │ teardown[] (best-effort)   │  │
   │  │   ALL items run            │  │
   │  │   failures → severity=warn │  │
   │  └─────────────┬──────────────┘  │
   │                ▼                  │
   │  Emit aggregate Signal for S      │
   │  (JSONL on stdout)                │
   │                ▼                  │
   │  if S.verdict ∈ {fail, error}:    │
   │    skip transitively-dependent    │
   │    each emits Signal:             │
   │      verdict=error                │
   │      rationale="dep <S.id> failed"│
   └──────────────────────────────────┘
              │
              ▼
   stdout: JSONL stream of all Signals
   (deps in topo order, then dependent)
   LAST line = dependent's aggregate
```

**The "last line is the aggregate" contract is preserved.** Today: last line is the single sensor's aggregate. Tomorrow: last line is the *requested* sensor's aggregate; deps' Signals appear earlier in the stream as separate JSONL lines. Callers that only care about the final verdict (`tail -n 1 | jq`) keep working.

## Schema changes

### Top-level additions

```jsonc
{
  "kind": {
    "type": "string",
    "enum": ["observation", "assertion", "setup"],
    "description": "Sensor category. observation = observes behavior with no fixed expectation (run-project, fetch-logs, fetch-metrics, trace-request, watch-build); verdict describes the health of the observation. assertion = checks against an expectation (unit-test, integration-test, e2e-test, lint, type-check, build, schema-validate); verdict pass/fail is semantic. setup = idempotent auxiliary sensor that makes a precondition true (start-postgres, setup-env-from-example, install-deps-pnpm, login-gcloud); typically referenced via depends_on. Metadata only — the runner treats all three identically; tooling (detect-sensors, listings) uses it to classify and name."
  },

  "depends_on": {
    "type": "array",
    "default": [],
    "description": "IDs of sensors that must run (and PASS) before this one. The runner resolves the transitive closure in topological order and propagates failures. Use to chain setup sensors (start-postgres) or assertion sensors (unit-tests before e2e-tests). Cycles (including self-loops A → A) are detected and abort with exit 1.",
    "items": {
      "type": "string",
      "pattern": "^[a-z][a-z0-9-]*$"
    },
    "uniqueItems": true
  }
}
```

### `execution` additions

```jsonc
{
  "execution": {
    "properties": {
      "prepare": {
        "type": "array",
        "default": [],
        "description": "Silent shell commands run before execution.command. Used to satisfy the sensor's preconditions (generate files, populate local .env from .env.example, intermediate builds). NOT observed via patterns; per-step verdict is folded into the sensor's aggregate evidence (no individual JSONL emission). Fail-fast: first non-pass step aborts prepare and skips the main command; teardown still runs.",
        "items": { "$ref": "#/$defs/LifecycleStep" }
      },

      "teardown": {
        "type": "array",
        "default": [],
        "description": "Silent shell commands run AFTER execution.command with finally semantics — they run on prepare failure, command failure, and command timeout. Use to clean up resources created in prepare (drop local DB after E2E, stop containers, remove temp files). Best-effort: every step runs regardless of earlier teardown failures. Per-step results are folded into the aggregate's evidence; teardown failures contribute severity=warn evidence entries but do NOT downgrade the sensor's aggregate verdict (the command is the source of truth).",
        "items": { "$ref": "#/$defs/LifecycleStep" }
      }
    }
  }
}
```

### New `$defs`

```jsonc
{
  "$defs": {
    "Signal": { "$ref": "signal.json" },

    "ExitCodeMapEntry": {
      "type": "object",
      "additionalProperties": false,
      "required": ["exit_code", "verdict", "severity"],
      "properties": {
        "exit_code": {
          "oneOf": [
            { "type": "integer", "minimum": 0 },
            { "type": "string", "const": "*" }
          ]
        },
        "verdict":  { "$ref": "signal.json#/$defs/Verdict" },
        "severity": { "$ref": "signal.json#/$defs/Severity" }
      }
    },

    "LifecycleStep": {
      "type": "object",
      "additionalProperties": false,
      "required": ["command"],
      "properties": {
        "command":       { "type": "string", "description": "Shell invocation (sh -c). MUST be idempotent." },
        "timeout_ms":    { "type": "integer", "minimum": 1, "description": "Hard cap; falls back to cost.latency.timeout_ms when omitted." },
        "exit_code_map": {
          "type": "array",
          "minItems": 1,
          "description": "Override only when tooling uses non-standard codes (e.g. 2 = 'already exists', treat as pass). When omitted, the runner applies the default [{exit_code: 0, verdict: pass, severity: info}, {exit_code: '*', verdict: fail, severity: high}] — defaults are runner-applied (JSON Schema cannot encode array defaults), so implementers must set them in lib/orchestrator code, not rely on schema.",
          "items": { "$ref": "#/$defs/ExitCodeMapEntry" }
        }
      }
    }
  }
}
```

The existing `execution.exit_code_map` items are also refactored to reference `#/$defs/ExitCodeMapEntry` (DRY, no semantic change).

### Removals

- `requires.upstream_sensors` — removed entirely. No backwards compat shim.

### `allOf` discriminators

The existing `allOf` blocks (`type: computational/inferential`, `output: single/stream`) stay unchanged. None of the new fields interact with the discriminators:

- `depends_on` and `kind` are top-level metadata; orthogonal to type and output.
- `execution.prepare`/`teardown` are silent (no patterns), so they don't conflict with `output: single`'s prohibition on `output_parsing`.

**Important schema mechanics:** `execution` declares `additionalProperties: false` (schemas/sensor.json:189). The new `prepare` and `teardown` fields MUST therefore be added to `execution.properties` directly (as shown in the `execution additions` snippet above) — adding them only inside an `allOf` branch would not satisfy `additionalProperties: false` outside that branch. The discriminator branches (computational/inferential) do NOT need to know about these fields; their existing `not.anyOf` lists (forbidding the wrong-branch fields) remain unchanged because `prepare`/`teardown` apply to both types.

## Runner changes

### New `lib/orchestrator/` subpackage

Following CLAUDE.md rule 9 (`lib/` organizes by context, action-named files):

```
lib/orchestrator/
├── dag.go             # Resolve(rootID, sensorRoot) → []sensor.Sensor topo-sorted
├── dag_test.go        # cycles, missing deps, diamond dependencies, depth 3+
├── lifecycle.go       # Run(sensor, schemasDir, stdout, stderr) → Signal
├── lifecycle_test.go  # prepare fail-fast, teardown finally, timeout per step
├── cascade.go         # Propagate(failedDep, dependents) → []Signal
└── cascade_test.go    # dep fail → dependent verdict=error with rationale
```

### Runner script changes

`skills/run-sensor/scripts/run-computational.go` and `run-inferential.go` lose their direct `lib.StreamSubprocess` invocation and gain a single call:

```go
signal, code := orchestrator.RunWithDeps(sensorID, sensorRoot, schemasDir, stdout, stderr)
```

`orchestrator.RunWithDeps` internally:

1. Loads the sensor by ID (`lib/sensor/load.go`).
2. Resolves transitive `depends_on` (`orchestrator.Resolve`). Self-loops (A → A) and longer cycles are caught by Kahn's algorithm and abort with exit 1.
3. For each sensor in topo order, calls `orchestrator.RunOne` which:
   - Runs prepare[] (each step via `lib/subprocess.RunStep`, folding per-step results into the sensor's aggregate evidence — NO individual JSONL emission for prepare steps).
   - Runs the main command (delegates to existing `lib.StreamSubprocess`; this DOES emit individual JSONL Signals for matched output lines, exactly like today).
   - Runs teardown[] (each step via `lib/subprocess.RunStep`, folding per-step results — including warn entries on step failure — into the sensor's aggregate evidence; NO individual JSONL emission).
   - Aggregates into a single Signal per sensor.
   - Writes the aggregate Signal as a JSONL line on stdout.
4. If any sensor's verdict is `fail` or `error`, marks dependents as skipped and emits cascade Signals for them (envelope defined below).

**v1 is serial.** Sensors run one at a time in topo order; siblings at the same level are NOT parallelized. Concurrent dep execution is deferred until profiles show meaningful gains (see Non-goals).

The contract for `lib.StreamSubprocess` is unchanged. Build tags (`run_computational`, `run_inferential`) remain on the script files. The orchestrator package is build-tag-free; both runners import it.

### JSONL emission contract (full picture)

The stdout stream for `/run-sensor X` (when X has deps D1, D2 and X.execution emits N individuals) looks like:

```
{aggregate Signal of D1}
{aggregate Signal of D2}
{individual Signal 1 of X}
{individual Signal 2 of X}
...
{individual Signal N of X}
{aggregate Signal of X}    ← LAST line, contract preserved
```

Prepare/teardown steps NEVER appear as individual JSONL lines. Per-step results are folded into the sensor's aggregate Signal under top-level `metadata.lifecycle` (signal.json's free-form `metadata` field). Schema sketch (informal — `metadata` is intentionally not constrained by signal.json):

```jsonc
"metadata": {
  "kind":      "aggregate",
  "command":   "...",
  "exit_code": 0,
  "timed_out": false,
  "counts":    { "pass": 5, "warn": 0, "fail": 0, "error": 0 },
  "lifecycle": {
    "prepare":  [
      { "command": "pnpm prisma generate", "exit_code": 0, "latency_ms": 1200, "verdict": "pass", "severity": "info" }
    ],
    "teardown": [
      { "command": "pnpm prisma migrate reset --force --skip-seed", "exit_code": 0, "latency_ms": 800,  "verdict": "pass", "severity": "info" },
      { "command": "docker compose stop postgres",                  "exit_code": 1, "latency_ms": 200,  "verdict": "warn", "severity": "low",  "stderr_excerpt": "..." }
    ]
  }
}
```

Failed prepare/teardown steps additionally append an `evidence[]` entry (using the existing `rationale` + optional `excerpt` fields, since signal.json restricts evidence items to a closed property set) so the entry is consumable by callers that only walk `evidence[]`.

### Cascade Signal envelope

When a dep fails, every transitively-dependent sensor emits a "cascade" Signal so the caller sees one Signal per requested-or-implied sensor (predictable count, debuggable). All required signal.json fields are populated; cascade-specific structure lives under top-level `metadata` (free-form per signal.json:124–127). `evidence[]` items use only the closed property set defined in signal.json (`file, line_start, line_end, excerpt, rationale`):

```jsonc
{
  "sensor_id":   "<dependent's id>",
  "version":     "<dependent's version, copied from its definition>",
  "run_id":      "<ULID generated at orchestrator entry>",
  "started_at":  "<ISO-8601 instant when cascade was emitted>",
  "finished_at": "<same as started_at — sensor never executed>",
  "verdict":     "error",
  "severity":    "high",
  "confidence":  1.0,
  "evidence": [
    {
      "rationale": "Skipped: dependency '<failed-dep-id>' produced verdict=<verdict>/<severity> in run_id=<rid>. See its Signal earlier in this JSONL stream.",
      "file":      "sensors/<failed-dep-id>.json"
    }
  ],
  "cost_actual": { "latency_ms": 0 },
  "metadata": {
    "kind":             "cascade",
    "command":          "<dependent's execution.command, for debugging>",
    "exit_code":        null,
    "timed_out":        false,
    "counts":           { "pass": 0, "warn": 0, "fail": 0, "error": 1 },
    "failed_dep_id":      "<failed-dep-id>",
    "failed_dep_run_id":  "<that Signal's run_id>",
    "failed_dep_verdict": "<verdict>",
    "failed_dep_severity":"<severity>"
  }
}
```

`confidence: 1.0` matches signal.json's "MUST be 1.0 for computational sensors" — the cascade is a deterministic outcome, not a probabilistic judgment. `cost_actual.latency_ms: 0` because the sensor literally did not run. Top-level `metadata` is free-form per signal.json so `metadata.exit_code: null` and the `failed_dep_*` keys are valid without schema changes. Tests for this envelope live in `lib/orchestrator/cascade_test.go`.

### Backwards compatibility

**Runner-side:** a sensor with no `depends_on`, no `execution.prepare`, no `execution.teardown` (all defaulting to `[]`) flows through the orchestrator with one path: `RunOne(sensor) → existing pipeline → emit aggregate`. Behavior is bit-identical to today's runners.

**Schema-side: NOT bit-identical.** Because `kind` is required (no default), every existing sensor in `sensors/*.json` becomes invalid against the new schema until Phase 3 migration adds the field. This is why Phases 1 and 3 ship as one atomic PR (see Migration). The other new fields (`depends_on`, `execution.prepare`, `execution.teardown`) ARE defaulted, so existing sensors do not need to declare them.

## Examples

### Setup sensor (reusable)

```jsonc
{
  "id": "start-postgres",
  "version": "0.1.0",
  "name": "Start local postgres",
  "description": "Brings the docker-compose postgres service up and waits until pg_isready returns 0. Idempotent: re-runs are no-ops when the container is already healthy. Auto-detected from docker-compose.yml.",
  "kind": "setup",
  "type": "computational",
  "regulation": "behaviour",
  "phase": "on-demand",
  "determinism": "high",
  "output": "single",
  "cost": {
    "class": "medium",
    "latency": { "p50_ms": 5000, "p95_ms": 30000, "timeout_ms": 60000 },
    "compute": { "cpu": "low", "memory_mb": 256 }
  },
  "triggers": [{ "on": "agent-request" }],
  "execution": {
    "command": "docker compose up -d postgres && pg_isready -h localhost -p 5432 -t 30",
    "exit_code_map": [
      { "exit_code": 0,   "verdict": "pass", "severity": "info" },
      { "exit_code": "*", "verdict": "fail", "severity": "high" }
    ]
  },
  "verification": {
    "golden_cases": [
      { "fixture": "sensors/fixtures/start-postgres/healthy.txt",   "expected_verdict": "pass", "expected_severity": "info" },
      { "fixture": "sensors/fixtures/start-postgres/no-docker.txt", "expected_verdict": "fail", "expected_severity": "high" }
    ]
  }
}
```

### Observation sensor with deps + lifecycle

```jsonc
{
  "id": "run-project-nest",
  "version": "0.2.0",
  "name": "Run project (nest start)",
  "description": "On demand, boots the NestJS app locally and listens for the first 30s. Auto-detected from CLAUDE.md '## Run locally'.",
  "kind": "observation",
  "type": "computational",
  "regulation": "behaviour",
  "phase": "on-demand",
  "determinism": "high",
  "output": "stream",
  "depends_on": ["start-postgres", "setup-env-from-example", "install-deps-pnpm"],
  "cost": {
    "class": "expensive",
    "latency": { "p50_ms": 30000, "p95_ms": 30000, "timeout_ms": 30000 },
    "compute": { "cpu": "medium", "memory_mb": 1024 }
  },
  "triggers": [{ "on": "manual" }],
  "execution": {
    "prepare": [
      { "command": "pnpm prisma generate", "timeout_ms": 30000 }
    ],
    "command": "node ./dist/main.js",
    "long_running": true,
    "exit_code_map": [
      { "exit_code": 0,   "verdict": "pass", "severity": "info" },
      { "exit_code": "*", "verdict": "fail", "severity": "high" }
    ],
    "output_parsing": {
      "patterns": [
        { "regex": "Nest application successfully started",       "verdict": "pass", "severity": "info" },
        { "regex": "Listening on .* port (\\d+)",                  "verdict": "pass", "severity": "info", "captures": { "excerpt": 1 } },
        { "regex": "EADDRINUSE",                                   "verdict": "fail", "severity": "high" },
        { "regex": "(?:ECONNREFUSED|ETIMEDOUT)",                   "verdict": "fail", "severity": "high" }
      ]
    }
  },
  "verification": { "golden_cases": [...] }
}
```

### Assertion sensor with full lifecycle (E2E)

```jsonc
{
  "id": "e2e-tests",
  "version": "0.1.0",
  "name": "E2E tests (Playwright)",
  "description": "On every pull request, runs the Playwright E2E suite against a local app stack. Migrates and seeds the DB before, drops it after.",
  "kind": "assertion",
  "type": "computational",
  "regulation": "behaviour",
  "phase": "pre-merge",
  "determinism": "medium",
  "output": "stream",
  "depends_on": ["start-postgres", "setup-env-from-example", "install-deps-pnpm"],
  "cost": {
    "class": "expensive",
    "latency": { "p50_ms": 120000, "p95_ms": 300000, "timeout_ms": 600000 },
    "compute": { "cpu": "high", "memory_mb": 4096 }
  },
  "triggers": [{ "on": "pull-request" }],
  "execution": {
    "prepare": [
      { "command": "pnpm prisma migrate deploy", "timeout_ms": 30000 },
      { "command": "pnpm prisma db seed",        "timeout_ms": 15000 }
    ],
    "command": "pnpm playwright test",
    "exit_code_map": [
      { "exit_code": 0,   "verdict": "pass", "severity": "info" },
      { "exit_code": "*", "verdict": "fail", "severity": "high" }
    ],
    "output_parsing": {
      "patterns": [
        { "regex": "^\\s+✓ (.+) \\(\\d+ms\\)$",  "verdict": "pass", "severity": "info", "captures": { "excerpt": 1 } },
        { "regex": "^\\s+✘ (.+) \\(\\d+ms\\)$",  "verdict": "fail", "severity": "high", "captures": { "excerpt": 1 } }
      ]
    },
    "teardown": [
      { "command": "pnpm prisma migrate reset --force --skip-seed", "timeout_ms": 15000 },
      { "command": "docker compose stop postgres",                  "timeout_ms": 10000 }
    ]
  },
  "verification": { "golden_cases": [...] }
}
```

## Migration

Phases 1 and 3 ship in the **same PR** so no sensor lands in an unlabeled state. `kind` is required (no default), so the schema bump and the existing-sensor labeling are atomic.

### Phase 1 — schema (atomic with Phase 3)

1. Update `schemas/sensor.json`:
   - Add `kind` (required, no default), `depends_on` (default `[]`) top-level.
   - Add `execution.prepare`, `execution.teardown` (default `[]`).
   - Add `$defs/ExitCodeMapEntry` and `$defs/LifecycleStep`.
   - Refactor `execution.exit_code_map.items` to `$ref: #/$defs/ExitCodeMapEntry`.
   - Remove `requires.upstream_sensors`.

### Phase 2 — orchestrator

2. Add `lib/orchestrator/{dag,lifecycle,cascade}.go` plus tests. Each file is action/aspect-named per CLAUDE.md rule 9: `dag.go` (DAG/topology aspect), `lifecycle.go` (lifecycle aspect), `cascade.go` (cascade aspect, also a verb).
3. Update `skills/run-sensor/scripts/run-computational.go` and `run-inferential.go` to delegate to the orchestrator. Existing tests in `run-computational_test.go` and `run-inferential_test.go` that assert "stdout has N JSONL lines" or "last line is the aggregate" will need updates: the *count* may grow when deps are present, but the "last line = aggregate of the requested sensor" invariant is preserved. Tests for sensors with NO deps and NO prepare/teardown should continue to pass unchanged (regression guard).
4. Run full test suite (`go test ./lib/... && go test -tags=run_computational ./skills/... && go test -tags=run_inferential ./skills/...`).

### Phase 3 — sensor migration (atomic with Phase 1)

5. Migrate all existing sensors in `sensors/*.json` adding explicit `kind`:
   - `lint-go-vet-computational`, `lint-go-vet-inferential`, `lint-gofmt`, `lint-skill-frontmatter` → `assertion`
   - `unit-test-lib`, `unit-test-skills-computational`, `unit-test-skills-inferential` → `assertion`
   - `build-runner-computational`, `build-runner-inferential` → `assertion`
   - `schema-validate-json`, `validate-plugin-manifest` → `assertion`

### Phase 4 — skills

6. Update `skills/detect-sensors/SKILL.md`:
   - Teach the LLM to classify sensors as `observation`/`assertion`/`setup` and use `depends_on` for cross-sensor wiring.
   - Add guidance for emitting setup sensors (start-postgres, setup-env-from-example, install-deps-*) with idempotent commands.
   - Add guidance for `execution.prepare[]`/`teardown[]` authoring (when to use which).
7. Update `skills/run-sensor/SKILL.md`:
   - Document the JSONL output now includes deps' Signals before the dependent's aggregate.
   - Document cascade behavior on dep failure.
8. Update the **Architecture** section of `CLAUDE.md` (not the Project rules section, which holds the durable conventions):
   - Add vocabulary: `kind`, lifecycle phases (prepare/command/teardown), setup sensor.
   - Update build/test commands if any new tags are needed (none expected).

### Phase 5 — verification

9. Author golden fixtures for the orchestrator behavior:
   - Cycle (A → B → A and self-loop A → A).
   - Missing dep (referenced ID not in `sensors/`).
   - Prepare fail-fast (second prepare step fails; command does NOT run; teardown DOES run).
   - Teardown best-effort (command passes; first teardown fails; second teardown still runs; aggregate verdict is `pass` with warn evidence).
   - Cascade (3-deep chain A → B → C; C fails; A and B both emit cascade Signals).
10. Run end-to-end smoke: a hand-written `e2e-with-deps` sensor exercises the full pipeline against a real docker-compose postgres.

## Non-goals

- **Data flow between sensors** (Approach 2 from brainstorming). Setup sensors do not export structured outputs that dependents consume via slot substitution. Postgres exposes itself via the conventional `localhost:5432`; `.env` is a file the dependent's command reads via `dotenv`. No template engine between Signals.
- **Session-level dependency caching** (Approach 3). Each `/run-sensor X` invocation re-runs all of X's deps. Idempotent setups make this cheap. Sharing dep instances across multiple sensors in one CLI invocation is a future feature.
- **Auto-trigger on missing host tools.** A sensor with `requires.tools: ["psql"]` does NOT automatically depend on an `install-psql` setup sensor when `psql` is missing on the host. The connection is editorial (the LLM detect-sensors skill suggests both), not runtime-automatic.
- **Concurrent dep execution.** v1 is serial (also stated in Architecture). Parallelism is a future optimization once profiles show meaningful gains.
- **Soft / optional deps.** All `depends_on` entries are required. `optional: true` flags are deferred to v2 if a real use case appears.
- **Per-edge metadata** (must_pass, lifecycle, export_env). `depends_on` is array of strings only. Object form is reserved for future extensions via `oneOf [string, object]`.
- **Inferential setup sensors.** A sensor with `kind: "setup"` and `type: "inferential"` (an LLM-as-judge that "prepares" something) is technically allowed by the schema but discouraged. Setup operations should be deterministic and idempotent; LLM-driven setup is neither. The schema does not enforce this — discouragement is editorial in `skills/detect-sensors/SKILL.md`.

## Risks and trade-offs

- **Atomic Phase 1 + Phase 3 PR is mandatory.** `kind` is required (no default), so the schema bump alone would invalidate every existing sensor. Phases ship together: schema change + sensor relabeling in one commit/PR. Reviewers must check both.
- **Cascade Signal noise.** Running `/run-sensor e2e-tests` when `start-postgres` fails emits 4+ Signals (one per dep + one cascade per dependent). Callers using `tail -n 1` are fine; callers persisting all lines see more output. Acceptable.
- **Teardown best-effort can mask real bugs.** A teardown that always fails (typo in `docker compose stop`) emits warns forever without affecting the verdict. Mitigated by surfacing the warns prominently in the aggregate's `evidence[]`.
- **`execution.prepare[]` overlaps semantically with depends_on setup sensors.** Authors will face the choice "does this go in prepare or in a setup sensor?" Guidance in the SKILL.md: reusable across sensors → setup sensor; specific to this sensor → prepare. Trade-off explicit.

## References

- Boeckeler, B. et al. *Harness Engineering*. martinfowler.com.
- Lopopolo, R. *Harness engineering: leveraging Codex in an agent-first world*. openai.com, 2026-02-11.
- `docs/superpowers/specs/2026-05-06-streaming-sensors-design.md` — prior spec (streaming and signal contract).
