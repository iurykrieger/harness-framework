---
name: start-sensor
description: Use when the user invokes /start-sensor or asks to bring a blocking sensor (one whose command does not terminate on its own) up for observation. Takes a `<sensor.id>` argument and resolves it to `sensors/<id>.json`. Validates the sensor against schemas/sensor.json, requires `execution.blocking: true`, resolves requires[kind=sensor] dep graph and brings deps up, runs requires[kind=step] fail-fast, spawns the command detached (Setsid, redirected stdout/stderr to .runtime/sensors/<id>/raw.log), spawns a watcher binary that tails raw.log and emits parsed Signals to .runtime/sensors/<id>/signals.log, and writes an entry into .runtime/sensors/running_sensors.json with `held_by: [{kind: "manual", attached_at: ...}]`. Emits a Signal `verdict=pass`, `metadata.kind=started`. Singleton: rejects with `rejected` if the sensor already has a live registry entry.
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

The script does everything: schema validation, dep graph resolution, prepare lifecycle, fork+exec the command detached, watcher spawn, registry write, started Signal emission. Pass its stdout through to the caller.

## Output contract

Stdout is JSONL. Multiple Signals can be emitted in order:

1. Aggregates of `kind=setup` or non-blocking deps that ran via `RunOne` (`metadata.kind=aggregate`).
2. Acks of blocking deps that the orchestrator brought up (`metadata.kind` ∈ {`dep_attached`, `dep_started`}).
3. Cascade signals for intermediate deps that were skipped because their own dep failed (`metadata.kind=cascade`).
4. **Exactly one** terminal Signal whose `metadata.kind` is one of:
   - `started` — subprocess and watcher are up; the sensor is now alive in the registry. `verdict=pass`. `metadata.next_cursor=0`. Carries `metadata.lifecycle.prepare`, `metadata.dep_chain`, `metadata.rebind_warnings` (omitted when empty).
   - `rejected` — already running (singleton check failed). `verdict=error`. `metadata.existing_pid` carries the live entry's pid.
   - `failed` — anything else preventing a `started` signal. `verdict=error`. `metadata.cause` discriminates: `dep_cascade`, `prepare_failed`, `spawn_failed`, `watcher_spawn_failed`, `registry_write_failed`, `schema_invalid`, `resolve_failed`, `preflight_failed`, `not_blocking`, `bootstrap_failed`. The `dep_cascade` cause carries `failed_dep_id`/`failed_dep_run_id`/`failed_dep_verdict`/`failed_dep_severity` from the failed dep's signal.

## Lifecycle integration

When the target sensor declares `requires[kind=sensor]` entries, `/start-sensor` resolves the dep graph and brings deps up before spawning the target:

- Setup or non-blocking deps run via `RunOne` (their command terminates; the result PASS or FAIL).
- Blocking deps come up via `AttachLiveDep` — if the dep is already alive in the registry, `/start-sensor` adds a holder; otherwise the dep is started fresh.
- The target's `requires[kind=step]` entries run fail-fast after deps are up but before the target subprocess spawns.
- After the target subprocess is spawned, dep holder pids are rebound from `/start-sensor`'s pid to the target subprocess pid, so `/list-sensors` and `/stop-sensor` see a holder that mirrors the target's lifetime.
- On any failure (cascade, prepare fail, spawn fail, watcher fail, registry write fail), every blocking dep we attached this run is detached in reverse order. If the detach drops a dep's last holder, the dep is stopped (SIGTERM/SIGKILL). State is left as before `/start-sensor` ran.

Use `/start-sensor` directly when:

- The blocking sensor is the observation target itself (e.g., the agent wants to watch logs while doing other work in parallel).
- The agent needs to interact with the live process (curl, edit, observe) without an immediately-dependent sensor driving the workflow.

## Notes & limits

- A sensor may have at most one live entry at a time. Use `/list-sensors` to see what's running.
- Logs are append-only; nothing is rotated. Long-running sessions should periodically `/stop-sensor`/`/start-sensor` to keep `.runtime/sensors/<id>/` from growing unboundedly.
- `cost.latency.timeout_ms` is forbidden by the schema for blocking sensors. Use `execution.graceful_timeout_ms` (min 100ms, default 5000) to control the SIGTERM→SIGKILL window in `/stop-sensor`.
