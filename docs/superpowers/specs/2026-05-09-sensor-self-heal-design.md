# Sensor self-heal design

Status: proposed
Date: 2026-05-09
Related: `schemas/sensor.json`, `schemas/signal.json`, `skills/run-sensor/`, `skills/detect-sensors/`, `lib/orchestrator/`

## Why

The dependency + lifecycle work that landed in #3 made it easy for a sensor to *declare* its environmental needs (`requires.env`, `depends_on` to setup sensors, `execution.prepare[]`). It did not, however, make those declarations resilient. Two failure modes recur in real use:

1. **`/detect-sensors` underspecifies setup at draft time.** Setup instructions for a project usually live in prose (`README.md`, `CLAUDE.md`, `AGENTS.md`) or implicit conventions (a `.env.example` sitting next to a missing `.env`). The current draft loop occasionally surfaces these and occasionally misses them, depending on what the LLM happened to read. Sensors land in `sensors/` with incomplete `requires.env` and missing `depends_on` to setup-* siblings that were never generated.
2. **`/run-sensor` aborts on missing setup with no recovery path.** When a non-optional `requires.env` value is unset at run time, the runner aborts with `verdict=error` and a remediation pointing at the missing var. The sensor stays unrun. The user has to read the remediation, hand-fix the environment, and reinvoke. Every other class of "I need a precondition that this project documents but my harness doesn't apply" produces the same dead end.

Both cases share root cause: there is no mechanism that turns a setup-shaped failure into an automatic, bounded, idempotent attempt at fixing the environment and retrying. This spec adds that mechanism.

## What changes

1. **New skill `/heal-sensor`.** Skill-grade primitive that consumes a failed Signal (or, when called by `/detect-sensors`, a freshly-drafted sensor whose smoke run failed setup-shape), emits a structured Setup Plan, applies the plan's allowlisted idempotent actions, persists patched/new sensors, and re-runs the original sensor exactly once.
2. **New Claude Code hook `setup-failure-detector`.** Go binary shipped by the plugin and auto-installed via `.claude-plugin/plugin.json`. Triggered after `/run-sensor` completes; classifies the last aggregate Signal; on setup-shape, injects `additionalContext` instructing the LLM to invoke `/heal-sensor`. Decouples `/run-sensor` from healing — the runner stays oblivious; removing the hook from settings disables self-heal entirely.
3. **New shared library `lib/heal/`.** Hosts the deterministic core: setup-shape classifier (`classify.go`, `patterns.go`), Setup Plan model (`plan.go`), allowlist applier (`apply.go`), `.env` writer with `chmod 600` (`envwriter.go`), and validator-backed sensor persister (`persist.go`). Imported by both the hook binary and the heal skill's scripts.
4. **`/detect-sensors` simplified.** Today's iteration loop conflates two concerns: shape correctness (regex matches, exit_code_map right, fixtures replay) and setup readiness (env present, deps installed, services up). The new flow keeps the shape-correctness iteration and removes the setup-readiness iteration — `/detect-sensors` writes the draft, smoke-runs once, and lets the hook + `/heal-sensor` close any setup-shape gaps. Detect-sensors never again blocks on the user "configuring credentials".
5. **No schema changes.** `schemas/sensor.json` and `schemas/signal.json` stay at current shape. Heal works on the existing contract. New per-Signal hints land in `metadata.heal_hint` (free-form per `signal.json`) so the classifier can make sharper decisions without a schema bump.

## Architecture

### The heal loop, end-to-end

```
        /run-sensor X
              │
              ▼
   lib/orchestrator runs X (and its deps)
   emits JSONL Signals; LAST line = X's aggregate
              │
              ▼
   exit, stdout returned to caller LLM
              │
              ▼
[Claude Code: PostToolUse / Stop hook fires]
              │
              ▼
   hooks/setup-failure-detector.go
     ├─ reads last aggregate Signal
     ├─ lib/heal/classify.go decides setup-shape?
     │    ├─ verdict=error AND evidence cites missing requires.env name?
     │    ├─ exit_code=127?
     │    ├─ prepare[] step "cp.*\.example" failed?
     │    └─ stderr matches lib/heal/patterns.go allowlist?
     └─ if YES: print additionalContext
        "Invoke /heal-sensor --signal=<path> --sensor=X"
              │
              ▼
   LLM sees injection, invokes /heal-sensor
              │
              ▼
   /heal-sensor (skill prose orchestration)
     ├─ scripts/diagnose.go
     │    ├─ reads project root (README, CLAUDE.md, AGENTS.md, .env.example)
     │    ├─ reads existing sensors/ (so heal can wire depends_on)
     │    ├─ reads the failed Signal
     │    └─ emits Setup Plan JSON to stdout
     ├─ skill prose interprets the Plan:
     │    for each `auto_apply[]` item with value_source: "ask-user"
     │    invoke AskUserQuestion synchronously
     ├─ scripts/apply-safe.go
     │    runs the allowlisted ops; rejects anything outside it
     │    items that fail apply move into propose_only
     ├─ scripts/persist-sensor.go
     │    validates each sensor_patches[] and new_setup_sensors[]
     │    against schemas/sensor.json; writes to sensors/ atomically
     ├─ scripts/retry-original.go
     │    re-invokes the runner for sensor X exactly once
     └─ surfaces final Signal:
          ├─ if retry passed: emit retry's aggregate
          └─ if retry failed: emit aggregate with remediation
             listing propose_only items + AskUserQuestion cancellations
```

### Setup Plan contract (`lib/heal/plan.go`)

Internal contract — not exposed via `schemas/`. The plan is the structured handoff between `diagnose.go` (LLM-flavored, runs an inferential sensor or directly prompts the calling agent) and `apply.go` / `persist.go` (deterministic).

```jsonc
{
  "diagnosis": {
    "failed_sensor_id": "run-project-nest",
    "shape": "missing-env" | "binary-not-found" | "env-file-absent" | "service-unavailable",
    "evidence_excerpt": "...",
    "root_cause_hint": "RSA_PRIVATE_KEY not set; project has config/.env.example with placeholder"
  },
  "auto_apply": [
    { "kind": "copy-template", "src": "config/.env.example", "dst": "config/.env" },
    { "kind": "set-env-in-file", "file": "config/.env", "name": "RSA_PRIVATE_KEY", "value_source": "ask-user" }
  ],
  "propose_only": [
    { "kind": "shell", "command": "pnpm install", "rationale": "package.json declares deps not installed" }
  ],
  "sensor_patches": [
    { "id": "run-project-nest", "patch": { "requires.env": [...] } }
  ],
  "new_setup_sensors": [
    { "id": "setup-env-from-example-nest", "json": { "id": "setup-env-from-example-nest", "kind": "setup", "..." } }
  ]
}
```

`shape` enumerates the classifier's recognized failure shapes; one-of, drives downstream branching. `auto_apply[]` items are constrained to the allowlist (see below); anything outside lands in `propose_only[]`. `sensor_patches[]` and `new_setup_sensors[]` are mutations to the harness configuration itself, persisted via the validator.

### Auto-apply allowlist (`lib/heal/apply.go`)

Hardcoded. The allowlist is the boundary of the blast radius — every entry must be idempotent, side-effect-bounded, and trivially reversible.

| `kind` | Pre-condition | Action | Post-condition |
|---|---|---|---|
| `copy-template` | `src` exists AND `dst` does not | `cp <src> <dst>` | `dst` exists |
| `mkdir` | `dir` ∈ paths declared in failed sensor's `requires.context` | `mkdir -p <dir>` | `dir` exists |
| `touch` | `file` ∈ paths declared in `requires.context` AND not exists | `touch <file>` | `file` exists, empty |
| `set-env-in-file` | Var declared in `requires.env` AND target file exists AND var not present in file | append `<NAME>=<VALUE>` (value via ask-user or `value` literal); apply `chmod 600` | Var present in file |

Anything else extracted from project docs — `pnpm install`, `docker compose up`, `gcloud auth login`, custom Makefile targets — lands in `propose_only[]` and surfaces in the final Signal's `remediation`. Adding new kinds is a versioned plugin change, code-reviewable.

### Hook plumbing

The plugin auto-installs the hook via `.claude-plugin/plugin.json`. Hook configuration:

- **Event**: `Stop` (fires when the LLM turn ends after `/run-sensor` completes). `PostToolUse` is an alternative if `/run-sensor` is implemented as a single tool call; the prototype will measure and pick the one that triggers reliably without firing on unrelated turns.
- **Command**: invokes the compiled `setup-failure-detector` binary with the conversation transcript path (Claude Code passes this via env or arg per hook protocol).
- **Output**: stdout JSON conforming to Claude Code's `additionalContext` schema. Empty stdout when the classifier decides "not setup-shape" — hook is a no-op.

### Classifier rules (`lib/heal/classify.go` + `patterns.go`)

Pure Go, table-driven, unit-tested. A signal is classified setup-shape when **any** of:

1. `verdict=error` AND `evidence[].rationale` matches `(?i)required env(ironment)? variable .* not set` AND the var name appears in the failed sensor's `requires.env[].name`.
2. The runner's emitted `metadata.heal_hint` (when present) starts with `"missing-env:"`, `"binary-not-found:"`, `"env-file-absent:"`, or `"service-unavailable:"`.
3. `metadata.exit_code == 127` AND the failed sensor has `requires.tools[]` non-empty.
4. `metadata.lifecycle.prepare[*].verdict == fail` for a step whose `command` matches `cp\s+.*\.example\b`.
5. Any of the curated stderr regexes in `lib/heal/patterns.go` matches a Signal's `evidence[].rationale`. Initial set:
   - `ENOENT.*\.env` → `env-file-absent`
   - `permission denied:.*\.env` → `env-file-absent`
   - `connection refused.*(postgres|mysql|redis|kafka)` → `service-unavailable`
   - `command not found` → `binary-not-found`

The list is small, curated, versioned with the plugin. New patterns require a code change with a test.

### `/heal-sensor` skill internals

```
skills/heal-sensor/
  SKILL.md                    # orchestration prose
  scripts/
    diagnose.go               # produces Setup Plan; consumes Signal + project root
    diagnose_test.go
    apply-safe.go             # runs allowlist items; rejects others
    apply-safe_test.go
    persist-sensor.go         # validate + write to sensors/
    persist-sensor_test.go
    retry-original.go         # re-invokes the runner once
    retry-original_test.go
```

`diagnose.go` is the only script that involves LLM reasoning. It either (a) inlines an inferential sub-call to a project-local heal-oracle sensor (deferred — see Out of scope), or (b) emits a structured prompt the calling agent fills in based on its own context. The MVP picks (b): the script reads the relevant inputs, prints them in a compact form, and SKILL.md instructs the calling agent to fill in the Plan slots. Determinism stays in apply/persist; reasoning stays at the agent layer.

`apply-safe.go` walks `auto_apply[]`. Each item runs through the allowlist gate; non-matching items are silently moved to `propose_only[]` with a rationale. For `set-env-in-file` items with `value_source: "ask-user"`, the script does NOT prompt — it returns control, SKILL.md prose calls `AskUserQuestion`, and apply is re-invoked with the supplied value.

`persist-sensor.go` reuses the validator extracted from `skills/detect-sensors/scripts/write-sensor.go`. The plan to extract the core into `lib/sensor/persist.go` (or wherever cohesive) lands in this same change; `write-sensor.go` becomes a thin CLI wrapper. Both heal-sensor and detect-sensors call into the lib.

`retry-original.go` shells out to `go run -tags=run_computational ./skills/run-sensor/scripts <sensor>` (or `run_inferential` per type), captures stdout, returns. Single retry — no loop. The retry's aggregate is what surfaces.

### Credential flow (`lib/heal/envwriter.go`)

When the Setup Plan calls for a `set-env-in-file` with `value_source: "ask-user"`:

1. SKILL.md prose invokes `AskUserQuestion` with the var's `description` from `requires.env[]` as the question header. Single-line free text ("Other" only) so the user can paste a token.
2. The answer flows to `envwriter.go`. If the project has a `.env.example`, the corresponding `.env` (or sibling without `.example` suffix) is the target. Append `<NAME>=<VALUE>\n` if the line is absent; do nothing if already present (idempotent).
3. After write, `chmod 600 <target>`. If the chmod fails (e.g., Windows filesystem), log a warn into the plan's `propose_only[]` but proceed — the value is set; permission tightening is best-effort.
4. If the project has no `.env.example`: skip the file write; instead pass the value as `execution.env` on the retry call (one-shot, in-memory only). The Plan's surface remediation explains this so the user knows the value didn't persist.

If the user cancels (chooses "Other" with no input or aborts), heal aborts: emit final Signal with `evidence[].rationale` describing the cancellation and `remediation.instructions` listing the var that was needed.

### Retry policy

**Exactly one retry per `/run-sensor` invocation.** Spec lock — no flag, no escalation. If the retry fails, the heal surfaces remediation and stops. Subsequent invocations of `/run-sensor` are independent: each gets its own single retry budget.

Rationale: open-ended retry loops mask real bugs under setup churn. Single retry forces the heal to be conservative — it only auto-applies things it has high confidence about; everything else surfaces and the user decides. The cost of "user re-runs `/run-sensor` and another setup-shape gap appears" is low (heal kicks in again, applies the next thing). The cost of "heal looped 7 times applying half-fixes that masked the real problem" is high.

### `/detect-sensors` simplification

Today's `skills/detect-sensors/SKILL.md` step 7 (`Run each sensor and iterate until the output is informative`) covers shape correctness AND setup readiness. After this change:

- **Kept**: smoke run, verify aggregate matches reality, replay golden cases, fix patterns / exit_code_map / fixtures, bump versions on shape change.
- **Removed**: instructions about "if credentials are missing, declare them and proceed". Detect-sensors declares `requires.env` from project inspection (best effort — README, CLAUDE.md, code grep for env access patterns), writes the sensor, runs it once. If the smoke run produces a setup-shape failure, the hook fires and `/heal-sensor` takes over compositional ly (skill-calls-skill, since detect-sensors is the entrypoint and knows it just drafted; no point waiting for a hook to react to its own draft).
- **Result**: detect-sensors SKILL.md gets ~30% shorter; the iteration responsibility for setup gaps moves to a single place.

### `/run-sensor` invariance

No changes to the runner, the SKILL.md, or the orchestrator's emission contract. The existing aggregate Signal shape is preserved. The hook reads the existing fields. The hook's installation is the only delta visible to a `/run-sensor` user, and it's invisible until a setup-shape failure happens.

### `lib/orchestrator` minor addition

Optional: when `lib/orchestrator/run.go` produces an aggregate Signal whose error path is unambiguously setup (the runner already aborted because a `requires.env` name was unset in `os.Environ`), it MAY set `metadata.heal_hint = "missing-env:<NAME>"` on the aggregate. Consumed by the classifier as a fast path. Not required for correctness — the classifier also reads `evidence[].rationale` — but cheaper than regex.

## Implementation phases

1. **Phase 1 — `lib/heal/` foundations.** `plan.go`, `patterns.go`, `classify.go`, `apply.go`, `envwriter.go`, `persist.go` with full test coverage. No skill or hook yet. Verifiable by tests alone.
2. **Phase 2 — `setup-failure-detector` hook binary.** `hooks/setup-failure-detector.go` calling into `lib/heal/classify.go`. Wired into `.claude-plugin/plugin.json` for auto-install. Test: dispatch hand-crafted Signal JSONs, assert injection text is correct.
3. **Phase 3 — `skills/heal-sensor/`.** SKILL.md prose + four scripts. End-to-end test: a fixture project with `.env.example` + a sensor that fails on missing env → trigger heal → assert `.env` written + retry passed.
4. **Phase 4 — `/detect-sensors` simplification.** Edit SKILL.md to drop setup-iteration prose. Add the compositional `/heal-sensor` invocation at end of draft flow. No code changes.
5. **Phase 5 — extract `write-sensor.go` core into `lib/sensor/persist.go`.** Reused by both detect and heal. CLI wrappers stay where they are.

Each phase is independently shippable.

## Out of scope

- Inferential heal-oracle sensor. The MVP keeps reasoning at the calling-agent layer (SKILL.md prose). A future follow-up may extract diagnosis into a project-local inferential sensor for sharper isolation, but it's not on the path.
- Long-running heal loops. Single retry is the spec.
- Concurrent `/run-sensor` invocations. Out of scope; harness is single-user.
- Sub-agent dispatch. The original draft considered isolating heal in a sub-agent. Removed — Go scripts compress the heavy work; remaining LLM reasoning fits in the calling agent's context.
- Healing of non-setup failures (a real test failure, a real lint finding). Heal explicitly does not touch these — sensor-design failures are a different problem.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Allowlist too narrow → most heals end up in `propose_only` | Start narrow, expand based on real usage telemetry. Each new entry is a code review with tests. |
| Allowlist too broad → heal mutates project state in surprising ways | Boundaries are documented per-entry; user can disable the entire mechanism by removing the hook from settings. |
| Hook fires on non-`/run-sensor` turns | Classifier returns "no" when the last Signal isn't a sensor aggregate; net effect is wasted Go invocation, no false heal. |
| `AskUserQuestion` cancellation leaves project in half-set state | `apply-safe.go` is idempotent and the Setup Plan is committed atomically; on cancel, partial state is what was already auto-appliable. Surfaced explicitly in the final Signal. |
| Plan mutates a sensor that was hand-edited | `persist.go` validates against `schemas/sensor.json` and writes atomically; user can `git diff sensors/` and revert. The sensor's `version` is bumped on patch (heal increments minor) so the audit trail records the heal's intervention. |
| `.env` written with secret + committed by accident | The plugin's `.gitignore` template (and project `.env.example`-bearing repos almost universally already gitignore `.env`) covers this. Heal does not modify `.gitignore`. |

## Tests

| Layer | Coverage |
|---|---|
| `lib/heal/classify.go` | Table-driven over crafted Signal JSONs covering each rule + negative cases |
| `lib/heal/patterns.go` | Each curated regex gets at least one positive and one negative fixture |
| `lib/heal/apply.go` | Each allowlist `kind` × (precondition met / precondition violated / target already in desired state) |
| `lib/heal/envwriter.go` | New file vs. existing file; var absent vs. present (idempotency); chmod success / failure |
| `lib/heal/persist.go` | Valid sensor JSON write succeeds; invalid fails without mutation |
| `hooks/setup-failure-detector.go` | E2E: feed a transcript fixture → assert hook stdout is the expected `additionalContext` JSON |
| `skills/heal-sensor/scripts/*` | Per-script unit tests covering happy path + error paths |
| End-to-end (skill smoke) | A fixture project with `.env.example` and a sensor whose `command` reads `RSA_PRIVATE_KEY` → invoke `/run-sensor` → assert hook fires → assert `/heal-sensor` invoked → assert `.env` written → assert retry passes |
| `/detect-sensors` regression | Existing detect tests stay green after SKILL.md simplification (the simplification is prose, not code) |

## Migration

No sensor JSON migration needed — the schema is unchanged. Existing sensors continue to run identically; the heal mechanism is purely additive and only fires on failure paths that previously produced unrecoverable errors.

The plugin manifest gets the new hook entry. Users who already have the plugin installed will pick up the hook on next plugin sync. Removing the hook (manual edit or future CLI flag) disables the mechanism wholesale.
