---
name: start-sensor
description: Use when the user invokes /start-sensor or asks to bring a blocking sensor (one whose command does not terminate on its own) up for observation. Takes a `<sensor.id>` argument and resolves it to `sensors/<id>.json`. Validates the sensor against schemas/sensor.json, requires `execution.blocking: true`, runs `execution.prepare[]` fail-fast, spawns the command detached (Setsid, redirected stdout/stderr to .runtime/sensors/<id>/raw.log), spawns a watcher binary that tails raw.log and emits parsed Signals to .runtime/sensors/<id>/signals.log, and writes an entry into .runtime/sensors/running_sensors.json with `held_by: [{kind: "manual", attached_at: ...}]`. Emits a Signal `verdict=pass`, `metadata.kind=started`. Singleton: rejects with `start_rejected` if the sensor already has a live registry entry.
---

# start-sensor

Bring a blocking sensor up. Only `execution.blocking: true` sensors can be started this way; the schema and the runner both reject non-blocking sensors.

## Invocation

```
/start-sensor <sensor.id>
```

The argument must be the sensor's id (lowercase letters/digits/dashes, starting with a letter). The runner resolves it to `sensors/<id>.json` relative to the project root.

## Procedure

```bash
go run -tags=start_sensor ./skills/start-sensor/scripts <sensor.id>
```

The script does everything: schema validation, prepare lifecycle, fork+exec the command detached, watcher spawn, registry write, started Signal emission. Pass its stdout through to the caller.

## Output contract

A single Signal on stdout. `metadata.kind` is one of:

- `started` — the subprocess and watcher are up; the sensor is now alive in the registry. Signal verdict is `pass`. `metadata.next_cursor` is `0` so the agent can begin tailing immediately.
- `start_rejected` — already running; existing run is referenced in evidence. `verdict=error`.
- `start_failed` — schema invalid, prepare step failed, fork failed, or registry write failed. `verdict=error`.

## Lifecycle integration

Other sensors may declare a blocking sensor in `depends_on`. When `/run-sensor` is invoked for such a dependent, the orchestrator will start (or attach to) the blocking dep automatically using the same primitives this skill exposes — you do not need to invoke `/start-sensor` manually for that case. Use `/start-sensor` directly when:

- The blocking sensor is the observation target itself (e.g., the agent wants to watch logs while doing other work in parallel).
- The agent needs to interact with the live process (curl, edit, observe) without an immediately-dependent sensor driving the workflow.

## Notes & limits

- A sensor may have at most one live entry at a time. Use `/list-sensors` to see what's running.
- Logs are append-only; nothing is rotated. Long-running sessions should periodically `/stop-sensor`/`/start-sensor` to keep `.runtime/sensors/<id>/` from growing unboundedly.
- `cost.latency.timeout_ms` is forbidden by the schema for blocking sensors. Use `execution.graceful_timeout_ms` (min 100ms, default 5000) to control the SIGTERM→SIGKILL window in `/stop-sensor`.
