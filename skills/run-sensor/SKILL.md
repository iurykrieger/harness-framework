---
name: run-sensor
description: Use when the user invokes /run-sensor or asks to run a harness sensor. Takes a path to a sensor JSON file (e.g. `@sensors/<id>.json`). Reads `sensor.type` from the file, dispatches to either `run-computational.go` or `run-inferential.go`, and emits the resulting Signal as the LAST content of the response. Both runners do the full deterministic pipeline (path resolution, schema validation, execution, Signal emission) in one Go invocation; the LLM only orchestrates and, for inferential, arranges slot bindings.
---

# run-sensor

Execute a harness sensor and emit a Signal back to the caller.

There are exactly two scripts, one per sensor type. Both are end-to-end: a single `go run` produces a validated Signal on stdout. You orchestrate by reading `sensor.type` from the sensor JSON and dispatching.

## Invocation

```
/run-sensor <path-to-sensor.json>
```

The argument may use `@`-prefix file syntax, a repo-relative path, or an absolute path. If absent, ask the user. Do not invent a sensor.

## Procedure

### 1. Read `sensor.type`

`sensor.type` is required by `schemas/sensor.json`, so it is present in every well-formed sensor file. Use the Read tool against the resolved path. Branch on the value.

### 2a. `computational`

```bash
go run -tags=run_computational ./skills/run-sensor/scripts <SENSOR_PATH>
```

The script does everything: resolves the path (including `@` prefix), validates against `schemas/sensor.json`, runs `execution.command` with the configured env capped by `cost.latency.timeout_ms`, maps the exit code via `execution.exit_code_map` (with `*` wildcard), assembles a Signal, validates against `schemas/signal.json`, and prints it. Pass its stdout through to step 3.

Exit codes: `0` Signal printed to stdout; `1` schema validation failed (stderr names the path); `2` usage or I/O error (sensor unreadable, malformed JSON, wrong type).

### 2b. `inferential`

Requires `ANTHROPIC_API_KEY` in the environment. If absent, ask the user to set it before retrying. The script makes a single Anthropic Messages API call.

```bash
ANTHROPIC_API_KEY=... go run -tags=run_inferential ./skills/run-sensor/scripts \
  [--slot key1=value1] [--slot key2=value2] ... \
  <SENSOR_PATH>
```

The script renders `execution.user_prompt_template` against the supplied `--slot` bindings (each `{{slot}}` must be bound), POSTs to the Anthropic API with `system_prompt` + rendered user prompt + `decoding`, parses the model's JSON output as the variable parts of a Signal (`verdict`, `severity`, `score`, `confidence`, `evidence`, `remediation`, `cost_actual`), applies the calibration rule (downgrades `verdict: fail` → `warn` when `confidence < sensor.calibration.confidence_threshold`, stamping `metadata.calibration_downgrade: true`), validates, and prints the final Signal.

Exit codes: `0` Signal printed; `1` schema validation failed, an `{{slot}}` is unbound, or the model's output isn't valid JSON; `2` usage or I/O error (missing API key, sensor unreadable, non-Anthropic provider in `execution.model`).

If the script returns `1` because the model output didn't parse, the recovery is the LLM's responsibility (this skill, your turn): tighten the system prompt by editing the sensor or, in extreme cases, emit an error envelope (see below) and stop.

### 3. Emit

Print exactly one fenced ```json``` block as the **last** content in your response, with the runner's stdout as its body. No prose, no follow-up questions after that block — calling agents parse from the bottom up.

## Output contract

The final ```json``` block is the only thing the harness reads. Everything before it is human-facing transcript.

```json
{ ... Signal conforming to schemas/signal.json ... }
```

## Error envelope

When the runner exits non-zero before a real Signal can be produced, emit a Signal that still validates against `schemas/signal.json`. Capture the runner's stderr verbatim into `evidence[0].rationale`. The runners validate Signals they emit themselves; when *you* construct an error envelope, follow `schemas/signal.json` exactly — the required fields are:

```json
{
  "sensor_id":   "<sensor.id, or 'run-sensor' if the sensor was unreadable>",
  "version":     "<sensor.version, or '0.0.0'>",
  "run_id":      "<a UUID>",
  "started_at":  "<when you started, ISO-8601 UTC>",
  "finished_at": "<now, ISO-8601 UTC>",
  "verdict":     "error",
  "severity":    "high",
  "confidence":  1.0,
  "evidence":    [{ "rationale": "<captured stderr from the failing runner>" }],
  "remediation": { "instructions": "<what the caller should change to recover>" },
  "cost_actual": { "latency_ms": <elapsed> }
}
```

## Notes & limits

- `run-inferential` only supports `anthropic/*` models. Sensors declaring other providers exit `2` with a clear message.
- Inferential reproducibility: even with `temperature: 0`, sampling drift across model versions is real. Trust `confidence`, not exact verdict equality.
- Computational sensors must be hermetic. If a sensor's command depends on uncommitted state, that's a sensor-design bug; don't paper over it in the runner.
- Slot bindings are passed via `--slot key=value` on the inferential invocation. There is no automatic binding from `sensor.requires.context`; if the user invokes `/run-sensor` without slot values for an inferential sensor that needs them, ask them.
