---
name: stop-sensor
description: Use when the user invokes /stop-sensor or asks to bring down a previously-started blocking sensor. Takes `<sensor.id>` and an optional `--reap-dead-holders` flag. Idempotent: stopping a sensor that is not running emits a warn Signal and exits 0. Otherwise removes the user's `kind=manual` hold, refuses with `held` if any sensor still holds the run, or proceeds with SIGTERM → wait `execution.graceful_timeout_ms` → SIGKILL on the subprocess group, then signals the watcher to drain, reads signals.log, and emits the aggregate Signal. Removes the entry from `.runtime/sensors/running_sensors.json` on success.
---

# stop-sensor

Bring a blocking sensor down and produce its aggregate.

## Invocation

```
/stop-sensor <sensor.id> [--reap-dead-holders]
```

## Procedure

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=stop_sensor \
  ./skills/stop-sensor/scripts <sensor.id> [--reap-dead-holders]
```

## When to use --reap-dead-holders

If `/list-sensors` shows the sensor with `held_by` entries whose `pid_alive=false` — or the `held` signal carries a non-empty `metadata.dead_holders` — the holder process (typically a crashed orchestrator running a dependent sensor, or a `/start-sensor` that was SIGKILL'd between attach and rebind) leaked the hold. Pass `--reap-dead-holders` to drop those entries before evaluating whether the sensor is still held. The aggregate Signal carries `metadata.reaped_holders` listing what was removed.

## Output contract

A single aggregate Signal on stdout. `metadata.kind` is one of:

- `aggregate` — the subprocess and watcher were brought down cleanly. `verdict` is the worst-of-stream and exit-side per `signal.Aggregate`.
- `not_running` — no live entry; `verdict=warn`.
- `held` — other holders remain. `verdict=warn`. `metadata.holders` lists remaining holders; `metadata.dead_holders` is the subset whose pid is no longer alive (empty when none). Process not stopped.
- `failed` — registry I/O failed. `verdict=error`.

## Notes

- A blocking sensor's `cost.latency.timeout_ms` is forbidden; `execution.graceful_timeout_ms` (min 100ms, default 5000) controls the SIGTERM→SIGKILL window here.
- Per-sensor `.runtime/sensors/<id>/{raw.log, signals.log}` are NOT deleted by stop — auditable. `.runtime/sensors/<id>/` cleanup is manual.
- When a subprocess dies on its own before /stop-sensor, the watcher's reaper records the exit; the aggregate then uses `Blocking: false` so `exit_code_map` interprets the verdict (a crashed dev server aggregates as fail/error rather than pass).
