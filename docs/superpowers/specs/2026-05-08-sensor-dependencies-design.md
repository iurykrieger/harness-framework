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
2. **Top-level `kind: "observation" | "assertion" | "setup"`.** Distinguishes sensors that observe behavior, sensors that assert against expectations, and sensors that prepare the environment. Default `"observation"`. Metadata only — runner treats all three identically; tooling uses it for classification and naming.
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
    "default": "observation",
    "description": "Categoria do sensor. observation = observa comportamento sem expectativa fixa (run-project, fetch-logs, fetch-metrics, trace-request, watch-build) — verdict descreve a saúde da observação. assertion = checa contra expectativa (unit-test, integration-test, e2e-test, lint, type-check, build, schema-validate) — verdict pass/fail é semântico. setup = sensor auxiliar idempotente que torna pré-condição verdadeira (start-postgres, setup-env-from-example, install-deps-pnpm, login-gcloud) — tipicamente referenciado via depends_on. Metadado: o runner trata os três iguais; ferramentas (detect-sensors, listagem) usam para classificar e nomear."
  },

  "depends_on": {
    "type": "array",
    "default": [],
    "description": "IDs de sensores que devem rodar (e PASSAR) antes deste. O runner resolve em ordem topológica e propaga falhas. Use para encadear setup sensors (start-postgres) ou assertion sensors (unit-tests antes de e2e-tests). Ciclos são detectados e abortam com exit 1.",
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
        "description": "Comandos shell silenciosos rodados antes de execution.command. Usados para preparar pré-condições do sensor (gerar arquivos, popular .env local a partir de .env.example, builds intermediários). Não são observados via patterns; só importam para verdict de pré-condição. Falha em qualquer item aborta o sensor antes do command rodar; teardown ainda roda.",
        "items": { "$ref": "#/$defs/LifecycleStep" }
      },

      "teardown": {
        "type": "array",
        "default": [],
        "description": "Comandos shell silenciosos rodados APÓS execution.command, com semântica finally — rodam mesmo se prepare ou command falharem, e mesmo se command for morto por timeout. Use para limpar recursos criados em prepare (drop banco local após E2E, parar containers, remover arquivos temporários). Falhas em teardown viram Signals individuais com severity=warn; não derrubam o verdict do sensor (o que importa é o command).",
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
        "command":       { "type": "string", "description": "Shell invocation (sh -c). Deve ser idempotente." },
        "timeout_ms":    { "type": "integer", "minimum": 1, "description": "Hard cap; default herda cost.latency.timeout_ms se omitido." },
        "exit_code_map": {
          "type": "array",
          "minItems": 1,
          "description": "Default: [{exit_code: 0, verdict: pass, severity: info}, {exit_code: '*', verdict: fail, severity: high}]. Override quando o tooling usa códigos customizados (ex: 2 = 'já existe', tratar como pass).",
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
2. Resolves transitive `depends_on` (`orchestrator.Resolve`).
3. For each sensor in topo order, calls `orchestrator.RunOne` which:
   - Runs prepare[] (each step via `lib/subprocess.RunStep`, accumulating Signals if any step fails).
   - Runs the main command (delegates to existing `lib.StreamSubprocess`).
   - Runs teardown[] (each step via `lib/subprocess.RunStep`, collecting warn Signals on failure).
   - Aggregates into a single Signal per sensor.
   - Writes Signal as JSONL line on stdout.
4. If any sensor's verdict is `fail` or `error`, marks dependents as skipped and emits cascade Signals for them.

The contract for `lib.StreamSubprocess` is unchanged. Building tags (`run_computational`, `run_inferential`) remain on the script files. The orchestrator package is build-tag-free; both runners import it.

### Backwards compatibility

A sensor with no `depends_on`, no `execution.prepare`, no `execution.teardown` (all defaulting to `[]`) flows through the orchestrator with one path: `RunOne(sensor) → existing pipeline → emit aggregate`. Behavior is bit-identical to today's runners.

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

### Phase 1 — schema

1. Update `schemas/sensor.json`:
   - Add `kind`, `depends_on` top-level.
   - Add `execution.prepare`, `execution.teardown`.
   - Add `$defs/ExitCodeMapEntry` and `$defs/LifecycleStep`.
   - Refactor `execution.exit_code_map.items` to `$ref: #/$defs/ExitCodeMapEntry`.
   - Remove `requires.upstream_sensors`.
2. Validate all existing sensors in `sensors/*.json` still pass (defaults make them backwards-compatible at the schema level — `kind` defaults to `observation` even though most are actually `assertion`).

### Phase 2 — orchestrator

3. Add `lib/orchestrator/{dag,lifecycle,cascade}.go` plus tests.
4. Update `skills/run-sensor/scripts/run-computational.go` and `run-inferential.go` to delegate to the orchestrator.
5. Run full test suite (`go test ./lib/... && go test -tags=run_computational ./skills/... && go test -tags=run_inferential ./skills/...`).

### Phase 3 — sensor migration

6. Migrate all existing sensors in `sensors/*.json` adding explicit `kind` (most → `assertion`; `validate-plugin-manifest` and `lint-skill-frontmatter` → `assertion`).

### Phase 4 — skills

7. Update `skills/detect-sensors/SKILL.md`:
   - Teach the LLM to classify sensors as `observation`/`assertion`/`setup` and use `depends_on` for cross-sensor wiring.
   - Add guidance for emitting setup sensors (start-postgres, setup-env-from-example, install-deps-*) with idempotent commands.
   - Add guidance for `execution.prepare[]`/`teardown[]` authoring (when to use which).
8. Update `skills/run-sensor/SKILL.md`:
   - Document the JSONL output now includes deps' Signals before the dependent's aggregate.
   - Document cascade behavior on dep failure.
9. Update `CLAUDE.md`:
   - Add vocabulary: `kind`, lifecycle phases (prepare/command/teardown), setup sensor.
   - Update build/test commands if any new tags are needed (none expected).

### Phase 5 — verification

10. Author golden fixtures for the orchestrator behavior (cycle, missing dep, prepare fail, teardown best-effort, cascade).
11. Run end-to-end smoke: a hand-written `e2e-with-deps` sensor exercises the full pipeline against a real docker-compose postgres.

## Non-goals

- **Data flow between sensors** (Approach 2 from brainstorming). Setup sensors do not export structured outputs that dependents consume via slot substitution. Postgres exposes itself via the conventional `localhost:5432`; `.env` is a file the dependent's command reads via `dotenv`. No template engine between Signals.
- **Session-level dependency caching** (Approach 3). Each `/run-sensor X` invocation re-runs all of X's deps. Idempotent setups make this cheap. Sharing dep instances across multiple sensors in one CLI invocation is a future feature.
- **Auto-trigger on missing host tools.** A sensor with `requires.tools: ["psql"]` does NOT automatically depend on an `install-psql` setup sensor when `psql` is missing on the host. The connection is editorial (the LLM detect-sensors skill suggests both), not runtime-automatic.
- **Concurrent dep execution.** v1 is serial. Parallelism is a future optimization once profiles show meaningful gains.
- **Soft / optional deps.** All `depends_on` entries are required. `optional: true` flags are deferred to v2 if a real use case appears.
- **Per-edge metadata** (must_pass, lifecycle, export_env). `depends_on` is array of strings only. Object form is reserved for future extensions via `oneOf [string, object]`.

## Risks and trade-offs

- **`kind` default `observation` is wrong for existing assertion sensors.** Without explicit migration, sensors created before this spec keep `kind: observation` even though they're assertions. Mitigated by Phase 3 migration. Long-term, requiring `kind` would be cleaner — defer until next breaking schema bump.
- **Cascade Signal noise.** Running `/run-sensor e2e-tests` when `start-postgres` fails emits 4+ Signals (one per dep + one cascade per dependent). Callers using `tail -n 1` are fine; callers persisting all lines see more output. Acceptable.
- **Teardown best-effort can mask real bugs.** A teardown that always fails (typo in `docker compose stop`) emits warns forever without affecting the verdict. Mitigated by surfacing the warns prominently in the aggregate's `evidence[]`.
- **`execution.prepare[]` overlaps semantically with depends_on setup sensors.** Authors will face the choice "does this go in prepare or in a setup sensor?" Guidance in the SKILL.md: reusable across sensors → setup sensor; specific to this sensor → prepare. Trade-off explicit.

## References

- Boeckeler, B. et al. *Harness Engineering*. martinfowler.com.
- Lopopolo, R. *Harness engineering: leveraging Codex in an agent-first world*. openai.com, 2026-02-11.
- `docs/superpowers/specs/2026-05-06-streaming-sensors-design.md` — prior spec (streaming and signal contract).
