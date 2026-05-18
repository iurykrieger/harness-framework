---
name: create-sensor
description: Use when the user invokes /create-sensor or asks to author sensors that validate one or more usecases. Reads .harness/usecases/** (by id, journey, path, or free-text match), applies deterministic grouping + kind/type/output inference (Go script plan-sensors.go), then synthesizes typed execution.steps[] (shell/http/assert/sensor) — one step per business_rule of every covered usecase. Emits 1..N sensors per invocation; every sensor declares use_cases[] referencing the source usecase ids. Distinct from /detect-sensors, which sweeps the whole project for observation/setup sensors.
---

# create-sensor

Take one or more usecase ids (or a journey id, or a usecase file path, or a free-text requirement) as input and produce one or more sensors that validate the implied behavior from multiple angles. Use deterministic Go scripts for grouping/inference (`read-usecases.go` → `plan-sensors.go`); synthesize each step from `behavior.business_rules[]` via LLM judgment.

This skill produces **one or more sensors per invocation**. Every sensor it emits declares `use_cases: [<id>, ...]` referencing real usecase files under `<project>/.harness/usecases/**`. For project-wide bootstrapping or for `observation` / `setup` sensors that don't trace to a usecase, use `/detect-sensors`.

## Invocation

```
/create-sensor [usecase-id | journey-id | path/to/usecase.yaml | "<free text>"]
```

If no argument is supplied, block:

> What is the requirement to cover? Pass a usecase id (`tail-sensor-no-registry`), a journey id (`tail-sensor`), a file path, or a free-text requirement.

## Procedure

### Phase 1 — Parse invocation

Classify the input:

| Input | Resolution |
|---|---|
| `<usecase-id>` | Resolve to a file under `<project>/.harness/usecases/**/<usecase-id>.yaml`. If multiple matches, fail asking the user to qualify by journey. |
| `<journey-id>` | Read all `<project>/.harness/usecases/<journey-id>/*.yaml`. |
| `path/to/file.yaml` | Read the file. Validation deferred to Phase 2. |
| `"<free text>"` | No usecase resolved; jump to Phase 1.5. |

### Phase 1.5 — Free-text inference (only if input is text)

Invoke the thin index loader to enumerate every usecase id + name + tags.

1. List every journey, then enumerate each one's usecases:

```bash
for j in $(ls "${HARNESS_REGISTRY_ROOT:-$(pwd)}/.harness/usecases" 2>/dev/null); do
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
    go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=read_usecases \
    ./skills/create-sensor/scripts \
    --journey "$j" \
    --list-only
done
```

Concatenate the thin-index outputs into one in-memory catalog of `{id, name, tags}` triples.

For each candidate, judge whether the free-text requirement reasonably corresponds. Collect a matched set.

If the matched set is empty, ask the user:

> No existing usecase matches this requirement. Two options:
> 1. Run `/detect-usecases` first to populate the ledger, then re-invoke `/create-sensor`.
> 2. Proceed by drafting an inline synthetic usecase that will be persisted to `.harness/usecases/inline/` as part of this PR.
>
> Which do you prefer?

Block until the user answers. No default.

### Phase 2 — Load ledger

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=read_usecases \
  ./skills/create-sensor/scripts \
  --usecases "<id-1>,<id-2>,..." \
  --include-stack \
  --include-catalog
```

If warn signals appeared on stdout BEFORE the ledger JSON, surface them to the user inline.

Save the ledger JSON to a tmp file for Phase 3. Re-run the same command with stdout redirected:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=read_usecases \
  ./skills/create-sensor/scripts \
  --usecases "<id-1>,<id-2>,..." \
  --include-stack \
  --include-catalog \
  > /tmp/ledger-$(date +%s).json
```

Remember the exact path for Phase 3.

### Phase 3 — Plan sensors

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=plan_sensors \
  ./skills/create-sensor/scripts \
  < /tmp/ledger-<saved-epoch>.json
```

Substitute `<saved-epoch>` with the epoch suffix produced in Phase 2 (the `$(date +%s)` value).

The script emits JSONL: one Plan line per proposed sensor, ending with one Aggregate Signal. Parse each line.

### Phase 3.5 — Report plan + confirm

Echo a one-line summary per sensor to the user:

```
Planned 3 sensors:

  ▸ assert-tail-sensor-error-handling  (assertion / computational / stream)
    use_cases: 3, steps: 9
    rationale: <plan.rationale>

  ▸ assert-tail-sensor-happy-path  (assertion / computational / single)
    use_cases: 2, steps: 6
    rationale: <plan.rationale>

  ▸ setup-tmp-registry  (setup / computational / single)
    use_cases: inherited from assert-tail-sensor-error-handling
    notes: auxiliary, created to satisfy mock_strategy=setup-mock-infra
```

Ask: *"Proceed? (yes/no)"*. Yes/no only — no editing. If the user wants different grouping, they can re-invoke with a narrower input.

If the user says no, abort cleanly without persisting anything.

### Phase 4 — Synthesize sensor drafts

For each planned sensor IN ORDER (not parallel — fixture writes need path coordination):

1. **Read** the `step_outline[]` plus the corresponding usecase YAML(s) (use `<project>/.harness/usecases/**` paths recorded in `source_usecase`).
2. **Construct an in-memory YAML draft** with these top-level fields populated:
   - `id`, `version: "0.1.0"`, `name`, `description` — from the plan + the usecase summaries.
   - `kind`, `type`, `output` — from the plan.
   - `regulation: "behaviour"` (default).
   - `phase: "on-demand"` (default).
   - `determinism`: `"high"` for computational, `"medium"` for inferential.
   - `cost` — use these canonical defaults:
     - **Computational**: `cost.compute = {cpu: low, memory_mb: 64}`; `cost.latency = {p50_ms: 10, p95_ms: 100, timeout_ms: 5000}` for shell-only sensors; `{p50_ms: 200, p95_ms: 2000, timeout_ms: 30000}` if any HTTP step is present.
     - **Inferential**: `cost.tokens = {model: "", input_avg: 4000, output_avg: 1000, max_output: 4096}` (see step 4 below — `model` must come from the user); `cost.latency = {p50_ms: 3000, p95_ms: 20000, timeout_ms: 60000}`.
     - **cost.class**: `cheap` if all steps are local shell/file; `medium` if any HTTP step or `setup-mock-infra`; `expensive` otherwise (or always `expensive` for inferential).
   - `triggers: [{ "on": "manual" }]`.
   - `use_cases` — from the plan.
   - `execution.steps[]` — one per `step_outline[]` entry, expanded according to the matrix below.
3. **Expand each `step_outline[i]`** per:

   | `suggested_step_type` | `mock_strategy` | Expansion |
   |---|---|---|
   | `shell` | `stub-deterministic` | `type: shell`, `run: <command exercising evidence>`, `exit_code_map: {0: pass, "*": fail}` |
   | `http` | `fixture-http-step` | `type: http`, `with: { fixture: <persisted name> }`, `expect.status: <from invariants>` |
   | `assert` | any | `type: assert`, `expect: { value: "${{ steps.<prior>.outputs.<name> }}", contains: "<from business_rule>" }` |
   | any | `setup-mock-infra` | Generate ALSO a sibling `kind=setup` sensor (id `setup-<journey>-mock`), declare it as `requires[{kind: sensor, id: <setup-id>}]` on the main sensor, plan it as the FIRST sensor to persist. The setup sensor MUST copy the dependent sensor's `use_cases[]` (every persisted sensor needs a non-empty `use_cases[]` per schema `minItems: 1`). |

4. **Inferential calibration gate.** If the sensor's `type` is `inferential`, do NOT proceed to step 8 (Serialize) until the user has provided:

   - `cost.tokens.model` (concrete provider/model id; the empty default is intentionally invalid)
   - A `calibration` block with `confidence_threshold`, `calibration_set` path, `calibration_size ≥ 1`, `calibration_date`

   If any of these are missing, block:

   > The planned sensor `<sensor-id>` is inferential. I need before persisting: (a) the model id (e.g. `anthropic/claude-sonnet-4-6`), (b) the calibration set path under `.harness/calibration/`, (c) the calibration size, (d) the calibration date.

   Wait for the user. Do not fabricate placeholders. Computational sensors skip this gate.

5. **If `evidence` does not yield a concrete shell command** (e.g., evidence is `lib/foo.Bar` with no shell surface), do NOT fabricate a command. Stop and ask the user:

   > I cannot infer a concrete shell command for step `<step_id>` from evidence `<file>:<line>`. Options: (a) wrap as `go test -run <Test>`; (b) you supply the exact command. Which do you prefer?

   Block. Do not persist the sensor with a placeholder command.

6. **Fixtures.** Reuse from the shared pool first:

   - When the step exercises a usecase whose `trigger.fixture.ref` (or
     `expected_outcome.fixture.ref`) is already populated, the step's
     `with: { fixture: <ref> }` reuses that exact path. Do not write a
     duplicate file under `_sensors/<sensor-id>/`.

   - For ad-hoc fixtures (the step does not trace to any usecase
     fixture — e.g. a bootstrap `/health` GET before the scenario), source
     the payload using the three-tier rule (`find-on-disk` →
     `derive-from-contract` → block on the user), then persist via
     `write-fixture` at `_sensors/<sensor-id>/<step-id>.<ext>`:

     ```bash
     printf '%s' "<payload>" | \
       HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
       go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_fixture \
       ./skills/create-sensor/scripts \
       "_sensors/<sensor-id>/<step-id>.<ext>"
     ```

     The step references it as `with: { fixture: "_sensors/<sensor-id>/<step-id>.<ext>" }`.

7. **Self-coherence check** before serializing the YAML: confirm (a) every `${{ steps.X.outputs.Y }}` reference points to a prior step that declared that output; (b) every `with: { fixture: ... }` references a path that was written in step 6.

8. **Serialize** the YAML to `/tmp/create-sensor-draft-<sensor-id>.yaml`.

### Phase 5 — Persist + report

For each draft, in order:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_sensor \
  ./skills/create-sensor/scripts \
  --out "${HARNESS_REGISTRY_ROOT}/.harness/sensors" \
  --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas" \
  /tmp/create-sensor-draft-<sensor-id>.yaml
```

Outcomes:

- **`verdict=pass`** — persist succeeded. Move on to the next sensor.
- **`verdict=error, metadata.kind=schema_invalid`** — patch the draft per the rationale and retry. Max 2 retries; then hand the draft to the user.
- **`verdict=error, metadata.kind=usecase_not_found`** — the sensor referenced a usecase that doesn't exist on disk. Abort the WHOLE invocation; surface which ids are missing.
- **`verdict=error, metadata.kind=sensor_already_exists`** — ask the user whether to pick a new id or abort.

When all sensors are persisted (or a fatal error halts), report:

```
Created N sensors covering M usecases:

  ✓ <sensor-id-1>
    use_cases: [...]
    steps: K
    deps: <required dep ids or —>

  ✓ <sensor-id-2>
    ...

Next: run `/run-sensor <sensor-id>` to exercise the sensor.
```

## What this skill does NOT do

- Does not exercise sensors after creation — `/run-sensor` stays manual.
- Does not modify existing sensors — id collisions are surfaced; user resolves.
- Does not interpret free text as "create a usecase" — Phase 1.5 proposes and blocks.
- Does not auto-replay usecases at runtime — `use_cases[]` is declarative traceability only.
