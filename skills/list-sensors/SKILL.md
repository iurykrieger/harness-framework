---
name: list-sensors
description: Use when the user invokes /list-sensors or asks "what's running". No arguments. Reads `.runtime/sensors/running_sensors.json`, validates each entry's PID with `kill(pid, 0)`, and emits a single Signal `verdict=pass`, `metadata.kind=list`, `metadata.entries=[…]`. Each entry carries sensor_id, pid (with pid_alive flag), watcher_pid (with watcher_alive), started_at, command, held_by (each holder annotated with its own pid_alive when kind=sensor), signals_log_path, and state ("running" or "orphan" when the subprocess pid is dead).
---

# list-sensors

Show all live blocking-sensor runs.

## Invocation

```
/list-sensors
```

## Procedure

```bash
go run -tags=list_sensors ./skills/list-sensors/scripts
```

## Output contract

A single Signal `verdict=pass`, `metadata.kind=list`. `metadata.entries` is the list — empty when nothing is running.

## When to use

- Sanity check before `/start-sensor` (avoid `start_rejected`).
- Find leaked holders: any held_by entry with `pid_alive: false` is a candidate for `/stop-sensor <id> --reap-dead-holders`.
- Spot orphans: an entry with `state: orphan` means the subprocess is dead but the registry entry was not cleaned up. `/stop-sensor <id>` will fold the existing signals.log into an aggregate and remove the entry.
