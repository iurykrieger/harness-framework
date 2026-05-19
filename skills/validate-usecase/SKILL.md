---
name: validate-usecase
description: Use when the user invokes /validate-usecase or asks to exercise the per-usecase sensor bundle and report a confidence score. Reads the bundle at .harness/sensors/<usecase-id>/, identifies layer entrypoints (one per unique sensor.layer), invokes the runtime in topological order, and emits a Signal carrying ceiling/coverage/realized counts plus the worst-result aggregate verdict.
---

# validate-usecase

Orchestrate the per-usecase sensor bundle, run every layer entrypoint, and emit a confidence-report Signal showing how well the bundle covers and passes the usecase. Deterministic Go scripts handle bundle discovery, entrypoint selection, runtime invocation, and report assembly; the agent interprets results and advises on next steps.

## Invocation

```
/validate-usecase <usecase-id>
```

If no argument is supplied, block:

> What is the usecase to validate? Pass a usecase id (e.g., `create-user`).

## Phase 1 — Resolve and verify the bundle

Verify that `.harness/sensors/<usecase-id>/` exists and contains at least one `.yaml` file. If the directory is absent or empty, surface:

> No sensor bundle found for usecase `<usecase-id>`. Run `/create-sensors <usecase-id>` first to author the layer bundle.

Then stop — do not proceed to the runner.

## Phase 2 — Run the validate-usecase script

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=validate_usecase \
  ./skills/validate-usecase/scripts \
  <usecase-id>
```

The script emits exactly one JSONL Signal on stdout. Parse it.

### What the script does internally

1. Resolves the project root via `lib/registry.Lookup`.
2. Walks `.harness/sensors/<usecase-id>/`. Reads each `.yaml`. Skips files that fail `schemas/sensor.yaml` validation and files whose `sensor.layer` is empty.
3. Groups sensors by `sensor.layer`. Picks one entrypoint per layer: a sensor whose `execution.steps[]` contains at least one `type: sensor` step (composite) is preferred; otherwise the sole sensor for that layer.
4. Computes `ceiling` = count of applicable layers by calling each registered `LayerRecipe.Applicable(stack, usecase, nil)`. Reads `stack.yaml` and the usecase yaml from disk.
5. Runs each layer entrypoint via `lib/orchestrator.RunWithDeps` in alphabetical layer-slug order. Captures the LAST JSONL line from stdout as the aggregate Signal for each entrypoint.
6. Tallies:
   - `coverage` = unique sensor.layer values found in the bundle.
   - `realized` = count of entrypoints whose aggregate verdict is `pass`.
   - `aggregate_verdict` = worst of all verdicts (`error > fail > warn > pass`); `error` with `metadata.kind=no_coverage` when coverage is zero.
7. Emits one Signal: `verdict = aggregate_verdict`, `metadata.kind = "confidence_report"`, `metadata.confidence_report = <report>`.

### Report shape

```json
{
  "usecase_id": "create-user",
  "computed_at": "2026-05-18T12:00:00Z",
  "ceiling": {
    "value": 5,
    "applicable": ["contract-test", "db-state", "integration-test", "log-trace", "unit-test"],
    "not_applicable": ["accessibility: no role=browser component on stack", "..."]
  },
  "coverage": {
    "value": 3,
    "generated": ["contract-test", "integration-test", "unit-test"]
  },
  "realized": {
    "value": 2,
    "layer_verdicts": [
      {"layer": "contract-test", "verdict": "pass", "sensor_id": "contract-test-create-user", "finished_at": "..."},
      {"layer": "integration-test", "verdict": "fail", "sensor_id": "integration-test-create-user", "finished_at": "..."},
      {"layer": "unit-test", "verdict": "pass", "sensor_id": "unit-test-create-user", "finished_at": "..."}
    ]
  },
  "ratios": {
    "completeness": 0.6,
    "pass_rate": 0.667,
    "executed_pass_rate": 0.667,
    "confidence": 0.4
  },
  "aggregate_verdict": "fail"
}
```

## Phase 3 — Interpret and report to the user

Parse `metadata.confidence_report` from the emitted Signal and present a human-readable summary:

```
usecase: create-user
aggregate verdict: fail

Confidence: 40% (2 / 5 applicable layers passed)
Coverage:   60% (3 / 5 applicable layers have sensors)

Layer verdicts:
  contract-test      pass   (contract-test-create-user)
  integration-test   fail   (integration-test-create-user)
  unit-test          pass   (unit-test-create-user)

Layers with no sensor (coverage gap):
  db-state
  log-trace

Layers not applicable to this stack:
  accessibility: no role=browser component on stack
  ...
```

Then give a concise recommendation:

- **aggregate_verdict = pass**: the bundle is fully passing; suggest scheduling or CI integration.
- **aggregate_verdict = warn**: some layers have non-fatal warnings; surface the affected sensor ids and suggest `/heal-sensor <id>` for each.
- **aggregate_verdict = fail**: identify the failing layers; suggest the user investigate those sensors (`/run-sensor <id>`) or fix the code under test.
- **aggregate_verdict = error**: a sensor failed to run (preflight gate, schema error, subprocess crash). Surface `metadata.confidence_report.realized.layer_verdicts` for the `error` rows and suggest re-running after fixing the root cause.

## Phase 4 — Coverage-gap advice

When `coverage < ceiling` (some applicable layers have no sensor), surface:

> Layers `<missing>` are applicable to this stack but have no sensor in the bundle. Run `/create-sensors <usecase-id> --force-layer <name>` for each to fill the gap.

## What this skill does NOT do

- Does not modify sensors or usecases — read-only except for the runtime's own `signals.log` writes.
- Does not re-run failed sensors automatically — surface failures and let the user decide.
- Does not compute confidence across multiple usecases — invoke once per usecase id.
- Does not synthesize sensors — `/create-sensors` is the authoring step.
