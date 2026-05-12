---
name: run-sensor
description: Use when the user invokes /run-sensor or asks to run a harness sensor. Takes a sensor id (e.g. `my-sensor`). Reads `sensor.type` (computational|inferential) and `sensor.output` (single|stream). For both types, the runner spawns the sensor's `execution.command` as a subprocess. In `stream` mode it emits one Signal per matched output line plus a final aggregate Signal as the LAST JSONL line; in `single` mode it emits exactly one aggregate Signal.
---

# run-sensor

Execute a harness sensor and emit Signals back to the caller.

There are exactly two scripts, one per sensor `type`. Both follow the same pipeline: spawn `sh -c <execution.command>` as a subprocess and emit Signals on stdout. How many Signals appear is governed by `sensor.output`:

- **`single`** — exactly one aggregate Signal. The subprocess's stdout/stderr lines are not parsed; only the exit code informs the verdict (computational uses `exit_code_map`; inferential uses an exit-0=pass / non-zero=error fallback unless the sensor declares its own `exit_code_map`).
- **`stream`** — one individual Signal per matched output line plus a final aggregate Signal as the LAST JSONL line. The aggregate's verdict is the worst of `exit_code_map[exitCode]` and the highest-rank verdict observed in the stream.

`single` forbids `execution.output_parsing`. `stream` requires `execution.output_parsing.patterns` (≥1 pattern). The schema enforces both.

## Dependency resolution

When a sensor declares `requires[kind=sensor]` entries (e.g. `[{"kind":"sensor","id":"setup-x"},{"kind":"sensor","id":"setup-y"}]`), the runner resolves the transitive closure, sorts topologically (deps first), and runs each sensor's full lifecycle (requires[kind=step] → command → teardown) before the requested sensor starts. Cycles (including self-loops `A → A`) are detected and abort with exit 1. Missing deps (referenced id has no file under `.harness/sensors/`) also abort with exit 1.

The JSONL stream on stdout for `/run-sensor X` (where X has deps D1, D2) looks like:

```
{aggregate Signal of D1}
{aggregate Signal of D2}
{individual Signal 1 of X}    ← only when X has output: stream
{individual Signal 2 of X}
...
{aggregate Signal of X}        ← LAST line, contract preserved
```

Callers using `tail -n 1 | jq` continue to see exactly the requested sensor's aggregate. Callers persisting all lines see deps' Signals interleaved.

## Cascade behavior

When a dep produces verdict=fail or verdict=error, every transitively-dependent sensor is **skipped** and emits a "cascade" Signal (verdict=error, severity=high) pointing at the failed dep:

- `metadata.kind = "cascade"`
- `metadata.failed_dep_id`, `metadata.failed_dep_run_id`, `metadata.failed_dep_verdict`, `metadata.failed_dep_severity`
- `evidence[0].rationale` describes which dep failed and where to find its Signal.

The skipped sensor never runs its `command` or its prepare/teardown — only the cascade Signal is emitted.

## Invocation

```
/run-sensor <sensor.id>
```

The argument is a bare sensor id (e.g. `my-sensor`). The runner resolves it to `.harness/sensors/<id>.json` relative to the project root. If absent, ask the user. Do not invent a sensor.

### Refusing blocking sensors

If `execution.blocking: true`, the runner exits 2 with an error message pointing at `/start-sensor`. Blocking sensors do not have a hard timeout and cannot be invoked through `/run-sensor`. They can only be reached:

- Manually, through `/start-sensor` / `/stop-sensor` / `/tail-sensor` / `/list-sensors`.
- Implicitly, when another sensor lists them in `requires[kind=sensor]` — the orchestrator brings them up before the dependent runs.

## Procedure

### 1. Read `sensor.type` and `sensor.output`

Both fields are required by `schemas/sensor.json`. Use the Read tool. Branch on `sensor.type` to choose the runner; the runner reads `sensor.output` itself and surfaces it as `metadata.output_mode` on the aggregate Signal — you don't have to pass it.

### 2a. `computational`

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts <SENSOR_ID>
```

The script does everything: resolves the path (including `@` prefix), validates against `schemas/sensor.json`, spawns `sh -c <execution.command>` with the configured env capped by `cost.latency.timeout_ms`, scans stdout+stderr line-by-line, matches each line against `execution.output_parsing.patterns` (when declared), emits a Signal per match as JSONL, and ends with one aggregate Signal whose verdict is the worse of `exit_code_map[exitCode]` and the highest verdict observed in the stream. Pass its stdout through to step 3.

Exit codes: `0` Signals printed; `1` schema/pattern compile failure; `2` usage or I/O error (sensor unreadable, malformed JSON, wrong type).

### About the invocation contract

Every script the framework ships runs through the same three knobs:

- `-C "${CLAUDE_PLUGIN_ROOT}"` chdirs `go` itself to the plugin's checkout so the user's `go.mod` or `go.work` cannot interfere with module resolution.
- `HARNESS_REGISTRY_ROOT="$(pwd)"` captures the agent's cwd as the project root before `-C` moves `go`. The runner uses this to resolve `sensors/<id>.json` and to set the subprocess's working directory.
- `GOWORK=off` neutralizes any `go.work` in the user's tree.

`${CLAUDE_PLUGIN_ROOT}` is exposed by Claude Code to plugin-originated commands; if it is empty the runner emits `verdict=error metadata.cause=plugin_root_missing`.

### 2b. `inferential`

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_inferential \
  ./skills/run-sensor/scripts \
  [--slot key1=value1] [--slot key2=value2] ... \
  <SENSOR_ID>
```

Same pipeline, but the sensor's `execution.command` is an LLM CLI (e.g. `claude -p ...`). The runner renders `execution.user_prompt_template` against `--slot` bindings and exposes the result to the subprocess as the `HARNESS_PROMPT` env var. The subprocess prints judgment lines that are matched by `output_parsing.patterns` like any other sensor.

Calibration: if the subprocess emits a single `HARNESS_AGGREGATE_CONFIDENCE=<float>` line on its stdout, that value becomes the aggregate Signal's `confidence` and feeds the `fail → warn` downgrade rule (`confidence < calibration.confidence_threshold`). Otherwise `confidence` defaults to 1.0 and no downgrade triggers. The HARNESS_AGGREGATE_CONFIDENCE individual still appears in the JSONL stream but is filtered out of the aggregate's `evidence` and `metadata.counts`.

When the sensor declares its own `execution.exit_code_map`, the runner uses it (worst-of-two against the stream); otherwise it falls back to `exit 0 → pass/info`, anything else `→ error/high`.

Exit codes: `0` Signals printed; `1` schema/pattern failure or unbound `{{slot}}`; `2` usage or I/O error (sensor unreadable, wrong type, slot not in `key=value` form).

### 3. Emit

The runner prints JSONL on stdout. Surface it in your response with two fenced blocks (single-mode sensors will only have an aggregate, so the first block is empty and may be omitted):

- A ```jsonl``` block with the individual Signals (omit when there were none).
- A ```json``` block with the aggregate Signal as the **last** content of your response.

Calling agents parse bottom-up, so the aggregate stays unambiguously identifiable.

## Output contract

The final ```json``` block is the aggregate. Its `metadata.kind` is always `"aggregate"`; its `metadata.output_mode` echoes the sensor's declared `output` (`"single"` or `"stream"`). Per-line Signals (only emitted in `stream` mode) carry `metadata.kind: "individual"` and `metadata.line` with the raw matched text.

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
