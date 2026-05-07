# Streaming sensors design

Status: proposed
Date: 2026-05-06
Related: `schemas/sensor.json`, `schemas/signal.json`, `skills/run-sensor/`

## Why

Sensors today are one-shot: the runner executes a command, maps the final exit code, and emits a single Signal. This misrepresents what real project capabilities (lint, build, run-project, tests, log tails) actually look like — a long-running process that reports many discrete observations along the way and then exits with a status. The current design also embeds the Anthropic HTTP client inside `run-inferential`, blurring the line between "sensor as project capability" and "agent calling an LLM."

Sensors must be reframed as **processes that stream signals over their lifetime** and end with a single aggregate verdict. Both `computational` and `inferential` sensors fit this model — the only difference between them is what kind of process the runner spawns.

## What changes

1. **Sensor = process that streams signals.** Each line of subprocess output that matches a declared pattern produces an individual Signal. When the process exits, the runner emits one final aggregate Signal. All signals (individuals and aggregate) conform to `schemas/signal.json` unchanged.
2. **`output_parsing` becomes structural.** A new `execution.output_parsing.patterns[]` array defines how lines map to verdicts/severities, with optional regex captures that populate `evidence[].file`, `line_start`, `excerpt`, `rationale`.
3. **Output is JSONL on stdout.** One Signal per line, terminated by the aggregate. Persisting it is the caller's choice.
4. **Aggregate verdict = worst of two.** `max_severity( exit_code_map[exitCode], max_individual_verdict )`. Closes the gap where exit code and stream disagree (log sensors, lint fatals).
5. **Inferential drops the HTTP client.** It still spawns a process, just one that calls an LLM (e.g. a CLI like `claude -p ...`). Streaming and aggregation work identically; the only `inferential`-specific piece left is the calibration downgrade (fail→warn under threshold).
6. **`command` runs through `sh -c`.** Eliminates the `strings.Fields` parsing trap so real commands (`go test ./... 2>&1 | tee log`, globs, quoted args) work.
7. **Aggregate Signal carries useful `metadata`.** Keys: `command`, `exit_code`, `timed_out`, `counts.{pass,warn,fail,error}`, `kind: "aggregate"`. Individuals carry `kind: "individual"` and `line` (raw matched text).

## Architecture

```
                ┌────────────────────────────────┐
                │  sensor.json (validated)       │
                └──────────────┬─────────────────┘
                               │
                ┌──────────────▼─────────────────┐
                │  Runner (Go, build-tagged)     │
                │  - spawns sh -c <command>      │
                │  - tails stdout+stderr         │
                │  - matches each line against   │
                │    output_parsing.patterns     │
                │  - emits individual Signal     │
                │    (JSONL line) per match      │
                │  - on exit: builds aggregate   │
                │    Signal as last JSONL line   │
                └──────────────┬─────────────────┘
                               │ stdout (JSONL)
                ┌──────────────▼─────────────────┐
                │  Caller (claude-code agent)    │
                │  - parses last JSONL line for  │
                │    the aggregate verdict       │
                │  - may walk preceding lines    │
                │    for per-observation detail  │
                │  - persistence is its choice   │
                └────────────────────────────────┘
```

Two runners coexist via build tags (unchanged from today):

- `run-computational` — process is whatever the project provides (`go test ./...`, `eslint .`, `tail -f /var/log/app.log`).
- `run-inferential` — process is an LLM CLI (e.g. `claude -p "<prompt>"`). The runner reads the same JSONL contract from its stdout. Calibration logic only kicks in at aggregation time.

Shared pipeline lives in `skills/run-sensor/scripts/lib/`.

## Schema changes

### `schemas/sensor.json`

Add `execution.output_parsing` (replacing the current free-form string with a structured object). Applies to both types.

```json
"output_parsing": {
  "type": "object",
  "additionalProperties": false,
  "required": ["patterns"],
  "properties": {
    "patterns": {
      "type": "array",
      "minItems": 1,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["regex", "verdict", "severity"],
        "properties": {
          "regex":    { "type": "string", "description": "Go regexp (RE2). Anchored or not, sensor's choice." },
          "verdict":  { "$ref": "signal.json#/$defs/Verdict" },
          "severity": { "$ref": "signal.json#/$defs/Severity" },
          "captures": {
            "type": "object",
            "additionalProperties": false,
            "description": "Map capture-group index (1-based) to a Signal evidence field.",
            "properties": {
              "file":       { "type": "integer", "minimum": 1 },
              "line_start": { "type": "integer", "minimum": 1 },
              "line_end":   { "type": "integer", "minimum": 1 },
              "excerpt":    { "type": "integer", "minimum": 1 },
              "rationale":  { "type": "integer", "minimum": 1 }
            }
          }
        }
      }
    }
  }
}
```

`output_parsing` is optional. Sensors that only care about exit code (e.g. `make build`, `go vet ./...` when treated as binary pass/fail) omit it entirely and the runner emits only the aggregate. When present, `patterns` MUST contain at least one entry — declaring `output_parsing` with empty patterns is a configuration error caught at schema validation.

The `inferential` branch keeps `model`/`system_prompt`/`user_prompt_template`/`decoding`, but the runner no longer interprets them as HTTP request bodies — they become a stable record of what the spawned LLM CLI was instructed to do (still validated, still surfaced to operators reading the sensor file).

**`execution.command` becomes mandatory for both types.** The top-level `allOf` is updated:

- `computational`: requires `command` and `exit_code_map`. Forbids `model`/`system_prompt`/`user_prompt_template`/`decoding` (those belong to inferential).
- `inferential`: requires `command`, `model`, `system_prompt`, `user_prompt_template`, `decoding`. `exit_code_map` becomes optional (the LLM CLI typically exits 0 on a successful judgment regardless of verdict, so most inferential sensors will rely on `output_parsing` + exit-code-as-error-only). `command` for an inferential sensor is the LLM CLI invocation; the prompt template fields are rendered into args by the runner before `sh -c`.

The inferential runner still requires `calibration` and applies the fail→warn downgrade to the **aggregate** Signal.

### `schemas/signal.json`

No structural change. Convention added in description for `metadata`:

- `metadata.kind`: `"individual"` or `"aggregate"`. Always set by the runner — callers can rely on it being present.
- `metadata.line` (individuals only): raw matched line.
- `metadata.command`, `metadata.exit_code`, `metadata.timed_out`, `metadata.counts` (aggregate only).

Schema stays permissive on `metadata` (it already is `additionalProperties: true` — confirmed in current shape via `"description": "Sensor-specific free-form data. Not interpreted by the harness."`).

## Aggregation rule

```
verdictFromExit = exit_code_map[exitCode]   // or "error"/"high" if no entry + no wildcard
verdictFromStream = max(individual.verdict for individual in individuals)  // "pass" if empty
aggregate.verdict = max(verdictFromExit, verdictFromStream)
aggregate.severity = severity associated with whichever side won (ties → exit side)
```

Ordering: `pass < warn < fail < error`.

Cases:
- Lint fatal config error: exit=2 → `error`; stream empty → `pass`; aggregate = `error`.
- Log tail with errors: exit=0 → `pass`; stream has 1 `fail` → aggregate = `fail`.
- Tests with 1 of 28 failing: exit=1 → `fail`; stream has 1 `fail` + 27 `pass` → aggregate = `fail`.
- All tests pass: exit=0 → `pass`; stream all `pass` → aggregate = `pass`.

## Data flow detail

```
1. Runner reads sensor.json, validates against schemas/sensor.json.
2. Runner builds an Envelope (sensor_id, version, run_id, started_at).
3. Runner compiles output_parsing.patterns (one *regexp.Regexp per pattern).
4. Runner spawns sh -c <command> via exec.CommandContext with timeout = cost.latency.timeout_ms.
5. Runner attaches a line scanner to a merged stdout+stderr pipe.
6. For each scanned line:
   a. Walk patterns in declared order.
   b. First match wins. Build an individual Signal:
      - Reuses run_id / sensor_id / version / started_at from envelope.
      - finished_at = now.
      - confidence = 1.0. Individuals come from a regex match, which is deterministic; the LLM's uncertainty is captured at the aggregate level via calibration, not per-line.
      - evidence[0]: capture-group-driven file/line/excerpt/rationale, defaulting rationale to the matched line if no capture is mapped.
      - metadata.kind = "individual", metadata.line = full line.
   c. Validate against schemas/signal.json. If invalid (shouldn't happen — the runner builds it), log to stderr and continue. Do NOT abort the stream over one bad individual.
   d. Encode as one JSON line on stdout, flushed.
7. On subprocess exit (or timeout):
   a. Compute aggregate verdict via the rule above.
   b. Build aggregate Signal:
      - finished_at = now.
      - evidence[]: include up to N most-severe individuals (configurable via lib constant; default top 20 fails/errors, then warns).
      - metadata.kind = "aggregate".
      - metadata.command, metadata.exit_code, metadata.timed_out, metadata.counts = {pass: x, warn: y, fail: z, error: w}.
   c. Validate, encode as final JSONL line, exit 0.
8. Inferential extra step (between 7a and 7b): apply calibration downgrade to aggregate.verdict.
```

## Error handling

| Failure | Exit | stdout | stderr |
| --- | --- | --- | --- |
| Sensor path unreadable / not JSON | 2 | empty | `error: ...` |
| Schema validation failure | 1 | empty | indented validation tree |
| Pattern regex compile failure | 1 | empty | `error: pattern[i] regex: ...` |
| Subprocess fails to spawn (binary missing) | 0 | aggregate Signal `verdict=error severity=high` | empty |
| Subprocess exit code unmapped, no wildcard | 0 | aggregate `verdict=error severity=high` | empty |
| Timeout | 0 | aggregate `verdict=error severity=high metadata.timed_out=true` | empty |
| Individual Signal fails self-validation | 0 | bad signal skipped, run continues | one-line warning per skip |

The runner never aborts a stream because of a malformed individual — partial data beats no data for the agent.

## Module layout

The shared library moves from `skills/run-sensor/scripts/lib/` to a top-level `lib/` so future skills (audit, calibrate, replay, etc.) can import the same primitives without duplicating them. Scripts remain skill-local — only the reusable library promotes.

```
lib/                              ← top-level, importable as
                                    github.com/iurykrieger/harness-framework/lib
  schema.go                        ← Validator, FindSchemasDir, PrintValidationError
  schema_test.go
  envelope.go                      ← BuildEnvelope, NewUUIDv4, NowFn / NewRunIDFn hooks
  envelope_test.go
  path.go                          ← ResolveSensorPath, MultiFlag
  path_test.go
  exitcode.go                      ← MapExitCode (and "*" wildcard)
  exitcode_test.go
  template.go                      ← RenderTemplate (slot substitution)
  template_test.go
  stream.go                        ← StreamSubprocess: per-line loop, JSONL emitter
  stream_test.go
  patterns.go                      ← pattern compile + match + capture-group extraction
  patterns_test.go
  aggregate.go                     ← worst-of-two, counts, evidence selection
  aggregate_test.go

skills/run-sensor/
  SKILL.md                         ← updated: JSONL contract, aggregate-as-last-line
  scripts/
    run-computational.go            ← thin wrapper, build tag run_computational
    run-computational_test.go
    run-inferential.go              ← thin wrapper, build tag run_inferential, no HTTP
    run-inferential_test.go
```

The current `skills/run-sensor/scripts/lib/` package is split by responsibility during the move (one file per cohesive concern instead of one `lib.go` blob). Each runner imports `github.com/iurykrieger/harness-framework/lib` and stays under ~50 lines — pure CLI plumbing.

**`CLAUDE.md` rule 4 needs a small amendment** to reflect this: scripts stay skill-local, but a top-level `lib/` is permitted for cross-skill primitives (schema validation, envelope construction, subprocess streaming). The "duplicate before couple" guidance still applies for skill-specific logic; only stable, schema-tied infrastructure belongs in `lib/`.

## Test plan

Layered, all using the standard `testing` package, table-driven where applicable.

### Unit (`lib/`)

- `patterns_test.go`:
  - capture-group extraction across `file`/`line_start`/`excerpt`/`rationale`
  - first-match-wins ordering when multiple patterns match the same line
  - lines that match no pattern produce no individual
  - regex compile failure surfaced upstream
- `aggregate_test.go`:
  - worst-of-two across the full 4×4 (`pass`,`warn`,`fail`,`error`) matrix
  - empty individuals → exit code dictates
  - exit code unmapped → falls back to `error` severity high
  - timeout flag forces `verdict=error`
  - inferential calibration downgrade applied after worst-of-two
- `stream_test.go`:
  - feed a synthetic process (`sh -c 'echo a; echo b; exit 1'`) and assert JSONL lines on stdout
  - mixed stdout+stderr lines all surfaced in declaration order
  - subprocess that times out → SIGTERM, aggregate has `timed_out=true`
  - shell features (`sh -c '... | grep ...'`) work because runner uses `sh -c`

### End-to-end (per runner)

- `run-computational_test.go`:
  - **all-pass**: command `printf 'PASS a\nPASS b\n'`, patterns map both lines, exit 0 → 2 individuals + aggregate pass.
  - **mixed**: `printf 'PASS a\nFAIL b\n'; exit 1`, exit_code_map maps 1→fail → 2 individuals + aggregate fail.
  - **log-style**: exit 0, stream contains one `ERROR` line → aggregate fail (worst-of-two trips on stream).
  - **fatal-no-stream**: command `false` with empty patterns → aggregate fail from exit code only.
  - **timeout**: `sleep 10` with `timeout_ms=200` → aggregate error + `timed_out=true`.
- `run-inferential_test.go`:
  - LLM CLI mocked by a small shell script that prints a known JSONL stream and exits 0; assert calibration downgrade on the aggregate when confidence < threshold.
  - Non-matching lines from the LLM CLI are ignored (no individuals).
  - HTTP client is gone — no `httptest.Server` needed; the test fixture is a temp shell script, kept simple.

### Schema

- `lib_test.go` already exercises cross-file `$ref`. Add cases for the new `output_parsing` subtree (valid + a few invalid mutations).

## Caller contract

The runner's stdout is JSONL. The aggregate is always the last line. `SKILL.md` instructs the agent to place the aggregate inside a fenced ```json``` block at the bottom of its response, optionally preceded by a fenced ```jsonl``` block with the individuals when surfacing them is useful. The current "last fenced JSON block is the Signal" parsing convention continues to identify the aggregate.

The HTTP client and `httptest.Server` fixtures used by the old inferential runner are removed.

## Out of scope

- Concurrency: the runner is single-process, single-stream. No parallel sensors yet.
- Persistence: the caller decides whether to tee to a file. The runner does not write to disk.
- Live streaming to remote consumers: stdout is the only egress.
- A generic "sensor library" (curated regex patterns for popular tools) — that's a separate effort once the runtime is in place.
