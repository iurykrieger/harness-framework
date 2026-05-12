---
name: tail-sensor
description: Use when the user invokes /tail-sensor or wants to read new Signal lines from a running blocking sensor's signals.log. Takes `<sensor.id> <cursor>` (cursor is a 1-based line index — pass 0 to read all lines from the start). Returns each new Signal as a JSONL line on stdout, then a final envelope Signal carrying `metadata.next_cursor` (the line count after this read) for the agent to feed into the next /tail-sensor call.
---

# tail-sensor

Read new Signals from a running blocking sensor without disturbing it.

## Invocation

```
/tail-sensor <sensor.id> <cursor>
```

- `<cursor>` = 0 → return everything since the sensor started.
- `<cursor>` = N → return lines N+1..end.

## Procedure

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=tail_sensor \
  ./skills/tail-sensor/scripts <sensor.id> <cursor>
```

## Output contract

JSONL on stdout: zero or more individual Signals, then exactly one envelope Signal whose `metadata.kind=envelope` and `metadata.next_cursor=<line count after this read>`. The agent should parse the LAST line, extract `next_cursor`, and pass it as `<cursor>` on the next call. Cursor=0 is also useful for troubleshooting — it dumps the entire signals.log, so you can re-read history.

If no live entry exists for the given `<sensor.id>`, a single Signal with `metadata.kind=not_running` and `verdict=error` is emitted instead.

## Notes

- Cursor is line-based, not byte-based, because each Signal occupies one line. Re-reading from 0 always works regardless of buffer flushes.
- The runner does NOT persist the cursor for you — it is your responsibility (the agent's) to remember `next_cursor` between calls.
- Reading does NOT block: if there are no new lines, only the envelope is emitted (with `next_cursor` unchanged).
