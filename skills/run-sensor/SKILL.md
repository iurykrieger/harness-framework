---
name: run-sensor
description: Use when the user invokes /run-sensor or asks to run a harness sensor. Takes a path to a sensor JSON file (e.g. `@sensors/<id>.json`). Reads `sensor.type` and dispatches to either `run-computational.go` or `run-inferential.go`. Both runners spawn a subprocess (the project's lint/build/test/log/etc. command, or an LLM CLI for inferential), stream individual Signals as JSONL while it runs, and emit one final aggregate Signal as the LAST JSONL line.
---

# run-sensor

Execute a harness sensor and emit Signals back to the caller.

There are exactly two scripts, one per sensor type. Both follow the same streaming model: a single `go run` produces JSONL on stdout — one Signal per matched output line during the run, then one aggregate Signal as the final line.

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

The script does everything: resolves the path (including `@` prefix), validates against `schemas/sensor.json`, spawns `sh -c <execution.command>` with the configured env capped by `cost.latency.timeout_ms`, scans stdout+stderr line-by-line, matches each line against `execution.output_parsing.patterns` (when declared), emits a Signal per match as JSONL, and ends with one aggregate Signal whose verdict is the worse of `exit_code_map[exitCode]` and the highest verdict observed in the stream. Pass its stdout through to step 3.

Exit codes: `0` Signals printed; `1` schema/pattern compile failure; `2` usage or I/O error (sensor unreadable, malformed JSON, wrong type).

### 2b. `inferential`

```bash
go run -tags=run_inferential ./skills/run-sensor/scripts \
  [--slot key1=value1] [--slot key2=value2] ... \
  <SENSOR_PATH>
```

Same streaming model. The sensor's `execution.command` is the LLM CLI (e.g. `claude -p ...`). The runner renders `execution.user_prompt_template` against `--slot` bindings and exposes the result to the subprocess as `HARNESS_PROMPT`. The subprocess prints judgment lines that get matched by `output_parsing.patterns` like any other sensor.

Calibration: if the subprocess emits a single `HARNESS_AGGREGATE_CONFIDENCE=<float>` line on its stdout, that value becomes the aggregate Signal's `confidence` and feeds the `fail → warn` downgrade rule (`confidence < calibration.confidence_threshold`). If absent, `confidence` defaults to 1.0 and no downgrade ever triggers. The HARNESS_AGGREGATE_CONFIDENCE individual still appears in the JSONL stream (the runner does not suppress it from output) but is filtered out of the aggregate's `evidence` and `metadata.counts`.

When the sensor declares its own `execution.exit_code_map`, the runner uses it (worst-of-two against the stream); otherwise it falls back to a default mapping (exit 0 → pass/info, anything else → error/high) suited to typical LLM CLI behaviour.

Exit codes: `0` Signals printed; `1` schema/pattern failure or unbound `{{slot}}`; `2` usage or I/O error (sensor unreadable, wrong type, slot not in `key=value` form).

### 3. Emit

The runner prints JSONL on stdout. Surface it in your response with two fenced blocks:

- A ```jsonl``` block with the individual Signals (omit when there were none).
- A ```json``` block with the aggregate Signal as the **last** content of your response.

Calling agents parse bottom-up, so the aggregate stays unambiguously identifiable.

## Output contract

The final ```json``` block is the aggregate. Its `metadata.kind` is always `"aggregate"`. Per-line Signals carry `metadata.kind: "individual"` and `metadata.line` with the raw matched text.

```json
{ ...aggregate Signal conforming to schemas/signal.json... }
```

## Error envelope

When the runner exits non-zero before any Signal can be produced, emit a Signal that still validates against `schemas/signal.json` as the trailing fenced ```json``` block. Capture the runner's stderr verbatim into `evidence[0].rationale`.

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
  "cost_actual": { "latency_ms": <elapsed> },
  "metadata":    { "kind": "aggregate" }
}
```

## Notes & limits

- Both runners use `sh -c`, so `execution.command` may use pipes, redirects, globs, and quoted args without escaping.
- The streaming buffer caps individual lines at 1 MB. Longer lines are silently truncated by the scanner.
- Inferential reproducibility: even with `temperature: 0`, sampling drift across model versions is real. Trust `confidence` (when emitted), not exact verdict equality.
- Computational sensors must be hermetic. If a sensor's command depends on uncommitted state, that's a sensor-design bug; don't paper over it in the runner.
- Slot bindings (`--slot key=value`) populate the inferential prompt template only. Computational sensors ignore `--slot`.
