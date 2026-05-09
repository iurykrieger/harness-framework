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
3. **New shared library `lib/heal/`.** Hosts the deterministic core: extensible setup-shape classifier (`classify.go` + `rules.go` + per-rule files `rule_*.go`), curated regex set (`patterns.go`), Setup Plan model (`plan.go`), allowlist applier (`apply.go`), `.env` writer with `chmod 600` (`envwriter.go`), and version transformer (`version.go`). Imported by both the hook binary and the heal skill's scripts. Sensor persistence is NOT in `lib/heal/` — it lives in `lib/sensor/` (see point 5).
4. **Classifier as a Rule registry, not a hardcoded chain.** `lib/heal/classify.go` defines a `Rule` interface; each rule lives in its own file under `lib/heal/rule_*.go` (one rule per file, with a sibling `_test.go`). A registrar in `lib/heal/rules.go` holds the canonical ordered list. Adding a new rule = adding a new `rule_*.go` file and one line in `rules.go`; existing rule files and `classify.go` are not edited. This is the only way to add classification rules — no else-if chain anywhere.
5. **Generic sensor validation+persistence in `lib/sensor/`.** A single function `lib/sensor.ValidateAndPersist(sensorJSON []byte, outDir string, schemasDir string) (path string, err error)` becomes THE primitive for "validate against `schemas/sensor.json` and write atomically to `<outDir>/<id>.json`". Both `skills/detect-sensors/scripts/write-sensor.go` (existing CLI wrapper, refactored to call the lib) and `/heal-sensor`'s plan-application path (which iterates the Plan's `sensor_patches[]` and `new_setup_sensors[]`, applies version bumps via `lib/heal/version.go`, then calls the same lib function) funnel through it. **No `lib/heal/persist.go`. No `scripts/persist-sensor.go`.** Heal is just another caller of the shared primitive.
6. **`/detect-sensors` simplified.** Today's iteration loop conflates two concerns: shape correctness (regex matches, exit_code_map right, fixtures replay) and setup readiness (env present, deps installed, services up). The new flow keeps the shape-correctness iteration and removes the setup-readiness iteration — `/detect-sensors` writes the draft, smoke-runs once, and lets the hook + `/heal-sensor` close any setup-shape gaps. Detect-sensors never again blocks on the user "configuring credentials".
7. **No schema changes.** `schemas/sensor.json` and `schemas/signal.json` stay at current shape. Heal works on the existing contract. New per-Signal hints land in `metadata.heal_hint` (free-form per `signal.json`) so the classifier can make sharper decisions without a schema bump.

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
     ├─ lib/heal/classify.go walks the Rule registry (lib/heal/rules.go)
     │  Each rule is a Match(signal) → (matched, shape, hint, ruleName)
     │  First matching rule wins; otherwise no-op.
     └─ if matched: print additionalContext
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
     │    runs allowlisted file mutations from auto_apply[];
     │    items that fail apply move into propose_only.
     ├─ scripts/apply-sensors.go
     │    iterates plan.sensor_patches[] and new_setup_sensors[]:
     │      lib/heal/version.go bumps version on patches;
     │      lib/sensor.ValidateAndPersist (the SHARED primitive)
     │      validates against schemas/sensor.json and writes atomically.
     │    Same lib call detect-sensors uses; no duplication.
     ├─ scripts/retry-original.go
     │    re-invokes the runner for sensor X exactly once
     └─ surfaces final Signal:
          ├─ if retry passed: emit retry's aggregate
          └─ if retry failed: emit aggregate with remediation
             listing propose_only items + AskUserQuestion cancellations
```

### Setup Plan contract (`lib/heal/plan.go`)

Internal contract — not exposed via `schemas/`. The plan is the structured handoff between `diagnose.go` (LLM-flavored, runs an inferential sensor or directly prompts the calling agent) and the deterministic appliers (`apply.go` for file-system mutations, `apply-sensors.go` + `lib/sensor.ValidateAndPersist` for sensor mutations).

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

- **Event**: `Stop`. Fires when the LLM turn ends, by which point any `/run-sensor` invocation in the turn has produced its final aggregate Signal. `PostToolUse` was considered but rejected: `/run-sensor` is a skill that may issue multiple tool calls (validator, runner, retry), and `PostToolUse` would fire after each — adding noise without benefit. `Stop` fires once per turn.
- **Command**: invokes the compiled `setup-failure-detector` binary with the conversation transcript path (Claude Code passes this via env or arg per hook protocol).
- **Output**: stdout JSON conforming to Claude Code's `additionalContext` schema. Empty stdout when the classifier decides "not setup-shape" — hook is a no-op.
- **Idempotence guard**: if the transcript already shows `/heal-sensor` was invoked since the last `/run-sensor` aggregate, the hook returns no-op. Prevents a heal-then-retry cycle from re-triggering itself when the retry's aggregate is itself read at end-of-turn.

### `metadata.heal_hint` contract

When the orchestrator emits a `heal_hint`, the value follows a stable grammar consumed by the classifier:

```
heal_hint := <shape> ":" <detail>
shape     := "missing-env" | "binary-not-found" | "env-file-absent" | "service-unavailable"
detail    := opaque string (env var name, binary name, file path, service name)
```

The grammar is documented in a comment block at the top of `lib/heal/patterns.go` and treated as a stable contract: adding a new `shape` is a versioned plugin change, deleting one is a breaking change. The classifier's hint-fast-path accepts only the listed shapes; unknown prefixes fall back to evidence/regex paths.

`warn` verdicts never trigger heal — only `error` and `fail`. This means the inferential calibration `fail → warn` downgrade (when `confidence < calibration.confidence_threshold`) suppresses heal as a side effect: a low-confidence inferential failure is treated as advisory, not as a setup-shape problem to fix.

### Classifier as a Rule registry (`lib/heal/classify.go` + `rules.go` + `rule_*.go`)

The classifier is intentionally extensible. A new classification rule must be addable as a single new file with no modifications to existing rule files or the dispatcher. The pattern:

#### The `Rule` interface (`lib/heal/classify.go`)

```go
package heal

// Rule classifies a Signal as setup-shape (or not).
// Implementations are registered in rules.go and walked in
// registration order; the first matching rule wins.
type Rule interface {
    // Name is a stable identifier used in telemetry/logs/tests
    // (e.g. "missing-env", "exit-code-127"). Lowercase kebab-case.
    Name() string
    // Match inspects the Signal and (when relevant) the failed
    // sensor's declared requires.* set. Returns matched=true with
    // shape and a free-form detail when the rule fires; otherwise
    // matched=false and the other return values are ignored.
    Match(signal Signal, failed FailedSensor) (matched bool, shape Shape, detail string)
}

// Classify walks the registered rules in order, returning the
// first match. Empty result means "not setup-shape".
func Classify(signal Signal, failed FailedSensor) (Result, bool) { ... }
```

`Shape` and `FailedSensor` are types defined in `lib/heal/classify.go` alongside the `Rule` interface (`Shape` is a closed Go enum: `ShapeMissingEnv`, `ShapeBinaryNotFound`, `ShapeEnvFileAbsent`, `ShapeServiceUnavailable`, ...; `FailedSensor` is a thin struct exposing the failed sensor's `id`, `requires.env`, `requires.tools`, and `requires.context` — the only fields any rule needs). Adding a shape is a typed constant addition + accompanying rule(s); both are localised changes.

#### The registrar (`lib/heal/rules.go`)

```go
package heal

// rules is the canonical ordered list. Adding a new rule means
// importing nothing extra (rules live in this package) and
// inserting one line into this slice. Order is deterministic
// and matters: more-specific rules go before more-generic ones.
var rules = []Rule{
    ruleMissingEnv{},
    ruleHealHint{},
    ruleExitCode127{},
    rulePrepareTemplateCopy{},
    ruleStderrPattern{},
}

func registeredRules() []Rule { return rules }
```

That is the *only* file that knows about the full rule set. `classify.go` calls `registeredRules()` and walks. `rules.go` is the one-line edit point when a new rule lands.

#### Per-rule files (`lib/heal/rule_*.go`)

Each rule is a struct in its own file with its own test:

| File | Rule | Shape produced |
|---|---|---|
| `rule_missing_env.go` | `verdict=error` AND evidence matches `(?i)required env(ironment)? variable .* not set` AND var name appears in the failed sensor's `requires.env[].name` | `missing-env` |
| `rule_heal_hint.go` | `metadata.heal_hint` is set with a known `<shape>:<detail>` prefix | the prefix's shape |
| `rule_exit_code_127.go` | `metadata.exit_code == 127` AND `requires.tools[]` is non-empty | `binary-not-found` |
| `rule_prepare_template_copy.go` | `metadata.lifecycle.prepare[*].verdict == fail` for a step whose `command` matches `cp\s+.*\.example\b` | `env-file-absent` |
| `rule_stderr_pattern.go` | Any curated regex from `patterns.go` matches an `evidence[].rationale` | shape from the regex's mapping |

Each rule file ships `rule_*_test.go` with positive and negative table-driven cases. **No `if/else` chain in `classify.go`** — the chain is the slice. Test coverage is independent per rule.

#### Adding a new rule

1. Create `lib/heal/rule_<name>.go` with a struct implementing `Rule`.
2. Create `lib/heal/rule_<name>_test.go` with positive + negative cases.
3. Insert one line in `rules.go` at the appropriate priority position.
4. (Optional) If the rule introduces a new `Shape`, add a constant in `classify.go` and a corresponding `propose_only` / `auto_apply` mapping in `apply.go`.

No existing files in `lib/heal/rule_*.go` change. No conditional logic in `classify.go` changes. The diff is fully additive.

`patterns.go` continues to host the curated stderr regex set (independent of any one rule) — `rule_stderr_pattern.go` consumes it. Initial set:

- `ENOENT.*\.env` → `env-file-absent`
- `permission denied:.*\.env` → `env-file-absent`
- `connection refused.*(postgres|mysql|redis|kafka)` → `service-unavailable`
- `command not found` → `binary-not-found`

### `/heal-sensor` skill internals

```
skills/heal-sensor/
  SKILL.md                    # orchestration prose
  scripts/
    diagnose.go               # produces Setup Plan; consumes Signal + project root
    diagnose_test.go
    apply-safe.go             # runs allowlist file mutations from auto_apply[]
    apply-safe_test.go
    apply-sensors.go          # iterates plan.sensor_patches[] / new_setup_sensors[],
                              # bumps versions, and persists each via
                              # lib/sensor.ValidateAndPersist (the SHARED primitive)
    apply-sensors_test.go
    retry-original.go         # re-invokes the runner once
    retry-original_test.go
```

`diagnose.go` is the only script that involves LLM reasoning. It either (a) inlines an inferential sub-call to a project-local heal-oracle sensor (deferred — see Out of scope), or (b) emits a structured prompt the calling agent fills in based on its own context. The MVP picks (b): the script reads the relevant inputs, prints them in a compact form, and SKILL.md instructs the calling agent to fill in the Plan slots. Determinism stays in apply/persist; reasoning stays at the agent layer.

`apply-safe.go` walks `auto_apply[]`. Each item runs through the allowlist gate; non-matching items are silently moved to `propose_only[]` with a rationale. For `set-env-in-file` items with `value_source: "ask-user"`, the script does NOT prompt — it returns control, SKILL.md prose calls `AskUserQuestion`, and apply is re-invoked with the supplied value.

`apply-sensors.go` is a thin CLI wrapper. It reads a Plan, iterates `sensor_patches[]` (applying `lib/heal/version.BumpPatch` to each before persisting) and `new_setup_sensors[]` (no version bump — they're new at v0.1.0), and calls `lib/sensor.ValidateAndPersist` per sensor. Same lib function `skills/detect-sensors/scripts/write-sensor.go` calls. There is **no separate persistence pipeline for heal**; if validation fails, the script reports the error and the offending sensor is NOT written; sensors that already validated and were written stay (they were valid, atomic, idempotent).

`retry-original.go` shells out to `go run -tags=run_computational ./skills/run-sensor/scripts <sensor>` (or `run_inferential` per type), captures stdout, returns. Single retry — no loop. The retry's aggregate is what surfaces.

### Shared sensor persistence (`lib/sensor/persist.go`)

The single primitive used everywhere a sensor is validated + written:

```go
package sensor

// ValidateAndPersist validates sensorJSON against schemas/sensor.json
// (loaded from schemasDir; if empty, the lib walks up from cwd) and,
// on success, writes a canonicalised copy (2-space indent) to
// <outDir>/<id>.json atomically. Returns the absolute path on success.
//
// The function is idempotent: writing the same sensor twice produces
// the same byte-identical file. It does NOT mutate sensorJSON.
//
// Error semantics:
//   - schema validation failure: error of type *schema.ValidationError
//     (caller can render the validator's tree); nothing written.
//   - I/O failure (mkdir, atomic rename): error wraps the underlying
//     os error; partial state guaranteed cleaned (atomic rename).
//   - input that fails to parse as JSON: error of type *json.SyntaxError.
func ValidateAndPersist(sensorJSON []byte, outDir string, schemasDir string) (path string, err error)
```

This replaces the inline implementation currently inside `skills/detect-sensors/scripts/write-sensor.go`. The CLI script becomes a flag-parser + I/O glue around this call. Heal's `apply-sensors.go` calls the same function for every sensor in a Plan. Tests for the lib live in `lib/sensor/persist_test.go` and cover: valid sensor → file appears at the right path; invalid sensor → no file written, error has expected shape; idempotent write; atomic rename behavior under simulated mid-write failure.

There is no `lib/heal/persist.go`. There is no `scripts/persist-sensor.go`. Heal-specific transformations (version bump) are pure value transformations in `lib/heal/version.go`, applied by callers BEFORE invoking `lib/sensor.ValidateAndPersist`.

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
- **Removed**: instructions about "if credentials are missing, declare them and proceed". Detect-sensors declares `requires.env` from project inspection (best effort — README, CLAUDE.md, code grep for env access patterns), writes the sensor, runs it once. If the smoke run produces a setup-shape failure, the SKILL.md prose explicitly tells the LLM to invoke `/heal-sensor` (compositional skill-from-skill — purely a prose-level step in detect-sensors' SKILL.md, no Go-level skill-call primitive needed). The hook path is reserved for `/run-sensor` invocations the user issues directly.
- **Result**: detect-sensors SKILL.md gets ~30% shorter; the iteration responsibility for setup gaps moves to a single place.

### `/run-sensor` invariance

No changes to the runner, the SKILL.md, or the orchestrator's emission contract. The existing aggregate Signal shape is preserved. The hook reads the existing fields. The hook's installation is the only delta visible to a `/run-sensor` user, and it's invisible until a setup-shape failure happens.

### `lib/orchestrator` minor addition

Optional: when `lib/orchestrator/run.go` produces an aggregate Signal whose error path is unambiguously setup (the runner already aborted because a `requires.env` name was unset in `os.Environ`), it MAY set `metadata.heal_hint = "missing-env:<NAME>"` on the aggregate. Consumed by the classifier as a fast path. Not required for correctness — the classifier also reads `evidence[].rationale` — but cheaper than regex.

## Implementation phases

1. **Phase 1 — `lib/sensor/persist.go` extraction (foundation).** Extract the validate + canonical-write core from `skills/detect-sensors/scripts/write-sensor.go` into `lib/sensor/ValidateAndPersist`. Refactor `write-sensor.go` to be a thin CLI wrapper. Test: the existing `write-sensor_test.go` golden cases continue to pass; add `lib/sensor/persist_test.go` covering validation failure modes and atomicity. **Pure refactor, no behavior change.** Lands first because both `lib/heal/` and the future `apply-sensors.go` depend on this primitive.
2. **Phase 2 — `lib/heal/` foundations with the Rule registry.** `plan.go`, `patterns.go`, `classify.go` (the `Rule` interface + `Classify` walker), `rules.go` (the registrar), `rule_*.go` (one file per initial rule, each with its own `_test.go`), `apply.go`, `envwriter.go`, `version.go`. Full test coverage. No skill or hook yet. Verifiable by tests alone.
3. **Phase 3 — `setup-failure-detector` hook binary.** `hooks/setup-failure-detector.go` calling into `lib/heal/classify.go`. Wired into `.claude-plugin/plugin.json` for auto-install. Test: dispatch hand-crafted Signal JSONs, assert injection text matches expected per rule.
4. **Phase 4 — `skills/heal-sensor/`.** SKILL.md prose + four scripts (`diagnose`, `apply-safe`, `apply-sensors`, `retry-original`). End-to-end test: a fixture project with `.env.example` + a sensor that fails on missing env → trigger heal → assert `.env` written, sensor patched/persisted via `lib/sensor.ValidateAndPersist`, retry passes.
5. **Phase 5 — `/detect-sensors` simplification.** Edit SKILL.md to drop setup-iteration prose. Add the compositional `/heal-sensor` invocation at end of draft flow. No code changes (`write-sensor.go` already calls the shared lib after Phase 1).

Each phase is independently shippable. Phases 2–5 only proceed once Phase 1 is merged, since they all depend on the shared persistence primitive.

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
| Plan mutates a sensor that was hand-edited | `lib/sensor.ValidateAndPersist` validates against `schemas/sensor.json` and writes atomically; user can `git diff sensors/` and revert. The sensor's `version` is bumped on patch by `lib/heal/version.go` (heal increments patch) so the audit trail records the heal's intervention. |
| `.env` written with secret + committed by accident | Heal relies on the consuming project's existing `.gitignore` (repos that ship `.env.example` almost universally already gitignore `.env`). Heal does NOT modify `.gitignore` and does NOT install one. Before writing, `envwriter.go` checks whether `.env`'s parent dir is gitignored; if not, the write is downgraded to `propose_only` with a remediation telling the user to add the entry first. |

## Tests

| Layer | Coverage |
|---|---|
| `lib/sensor/persist.go` | Valid sensor JSON → file at expected path, idempotent rewrite, atomicity on simulated failure; invalid sensor → error of expected type, no file written |
| `lib/heal/classify.go` | The `Classify` walker hits the registered rules in order, returns first match, returns no-op on empty match. Mocks rules to verify dispatch (not rule semantics) |
| `lib/heal/rules.go` | Asserts the canonical registration order is stable (rules slice is the source of truth) |
| `lib/heal/rule_*.go` | Each rule file owns its `_test.go` with table-driven positive + negative cases. Rules are isolated — no rule's tests reach into another |
| `lib/heal/patterns.go` | Each curated regex has at least one positive and one negative fixture |
| `lib/heal/apply.go` | Each allowlist `kind` × (precondition met / precondition violated / target already in desired state) |
| `lib/heal/envwriter.go` | New file vs. existing file; var absent vs. present (idempotency); chmod success / failure; gitignore-coverage check |
| `lib/heal/version.go` | `BumpPatch` increments correctly; rejects malformed semver |
| `hooks/setup-failure-detector.go` | E2E: feed a transcript fixture → assert hook stdout is the expected `additionalContext` JSON |
| `skills/heal-sensor/scripts/*` | Per-script unit tests covering happy path + error paths. `apply-sensors_test.go` verifies it calls the shared lib (not a duplicate persistence path) |
| `skills/detect-sensors/scripts/write-sensor_test.go` | Existing tests stay green after Phase 1 refactor (pure refactor: behavior preserved) |
| End-to-end (skill smoke) | A fixture project with `.env.example` and a sensor whose `command` reads `RSA_PRIVATE_KEY` → invoke `/run-sensor` → assert hook fires → assert `/heal-sensor` invoked → assert `.env` written → assert retry passes |
| `/detect-sensors` regression | Existing detect tests stay green after SKILL.md simplification (the simplification is prose, not code) |
| Rule extensibility regression | Add a fake `rule_test_only.go` in a test file using build tags; assert it integrates by adding ONE line to a test-mode registrar without touching `classify.go` or any existing rule. Locks the "single edit point" property as a test invariant |

## Migration

No sensor JSON migration needed — the schema is unchanged. Existing sensors continue to run identically; the heal mechanism is purely additive and only fires on failure paths that previously produced unrecoverable errors.

The plugin manifest gets the new hook entry. Users who already have the plugin installed will pick up the hook on next plugin sync. Removing the hook (manual edit or future CLI flag) disables the mechanism wholesale.
