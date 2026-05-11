# /stop-sensor log cleanup design

Status: proposed
Date: 2026-05-11
Related: `.vibeflow/prds/stop-sensor-log-cleanup.md`, `skills/stop-sensor/`, `skills/start-sensor/`, `lib/registry/`

## Why

`/stop-sensor` today documents an explicit "preserve for audit" policy
(`skills/stop-sensor/SKILL.md:38`): on success, the registry entry is
removed from `running_sensors.json`, but the per-sensor directory
`.runtime/sensors/<id>/{raw.log, signals.log, watcher.log}` is left in
place. In daily use this policy fails to pay for itself:

1. **`.runtime/sensors/` accumulates clutter.** After a session of
   starts and stops, dozens of `<id>/` directories remain on disk. They
   carry no live meaning — the sensor that produced them is no longer
   running and the next `/start-sensor` of the same id will truncate
   `raw.log` and `signals.log` anyway (`start.go:152,159` via
   `os.WriteFile(path, nil)`).
2. **Long-running blocking sensors grow `raw.log` to non-trivial size.**
   Dev servers, observers, and other blocking commands write to
   `raw.log` continuously. A multi-hour session ends and the user has
   to `rm -rf` manually to reclaim space.
3. **`/tail-sensor` returns ghost signals after a stop.** Because
   `signals.log` survives the stop, an agent that tails a recently
   stopped sensor reads JSONL lines from the prior run and may treat
   them as fresh output.

A side-effect of the current policy: `skills/start-sensor/SKILL.md:56`
advises users to "periodically `/stop-sensor`/`/start-sensor` to keep
`.runtime/sensors/<id>/` from growing unboundedly." That mitigation
only works because the next start truncates the logs, not because the
stop cleans up — an incongruity that signals the current policy is
already half-abandoned.

The aggregate Signal emitted by `/stop-sensor` already carries the
durable audit record of the run: counts, evidence, exit code, watcher
kill telemetry, lifecycle teardown results. Line-by-line preservation
of `raw.log` and `signals.log` is rarely consulted in practice and
trades a real, recurring cost (FS clutter, ghosts) against a
hypothetical one (forensic deep-dive into a finished run that the
aggregate Signal already summarizes).

This spec inverts the policy. A successful `/stop-sensor` removes
`.runtime/sensors/<id>/` recursively. Failure paths (`not_running`,
`held`, `failed`) leave the directory untouched. Cleanup failure
itself never changes the aggregate verdict — it surfaces as a
diagnostic field on the aggregate Signal.

## What changes

1. **`skills/stop-sensor/scripts/stop.go`** — six new lines in the
   `kind=aggregate` happy path. After the `registry.WithFileLock` block
   that removes the entry from `running_sensors.json` and before the
   final `validateSignal`, call `os.RemoveAll(r.SensorDir(id))`. On
   non-nil error, mutate `sig["metadata"]["cleanup_warning"]` with a
   formatted string. Imports `os` and `fmt` already in scope.
2. **`skills/stop-sensor/scripts/stop_test.go`** — four new tests
   covering: cleanup on success, preservation on `held`,
   preservation on `not_running`, and `cleanup_warning` surfaced
   on forced RemoveAll failure without verdict change.
3. **`skills/stop-sensor/SKILL.md`** — `Notes` section line 38 rewrites
   the policy statement.
4. **`skills/start-sensor/SKILL.md`** — `Notes & limits` section line
   56 drops the periodic-stop mitigation note (now redundant).
5. **No change to** `schemas/sensor.json`, `schemas/signal.json`,
   `running_sensors.json` shape, `lib/registry/`, `lib/orchestrator/`,
   `skills/start-sensor/scripts/start.go`, or the watcher subprocess.
   The truncation in `start.go:152,159` stays as defense-in-depth for
   any path where cleanup fails to delete the directory.

## Architecture

### Placement in `stop.go`

The happy path in `stop.go` today ends with:

```go
// stop.go:132–148 (current, abridged)
sig := buildAggregate(res, id, sensorJSON, entry, individuals, agg,
    killedForcefully, reaped, teardownResults)
if md, ok := sig["metadata"].(map[string]interface{}); ok {
    md["watcher_kill_forced"] = watcherKillForced
    md["watcher_kill_latency_ms"] = watcherKillLatencyMS
}

if err := registry.WithFileLock(r.LockFile(), func() error {
    rs, err := registry.Load(r)
    if err != nil {
        return err
    }
    rs.RemoveEntry(id)
    return registry.Save(r, rs)
}); err != nil {
    return 1, validateSignal(v, simpleSignal(res, id, "error", "high",
        "failed", fmt.Sprintf("registry: %v", err)), id)
}
return 0, validateSignal(v, sig, id)
```

Cleanup inserts as the last step before the terminal `validateSignal`:

```go
// stop.go (new, between current line 147 and line 148)
if rmErr := os.RemoveAll(r.SensorDir(id)); rmErr != nil {
    if md, ok := sig["metadata"].(map[string]interface{}); ok {
        md["cleanup_warning"] = fmt.Sprintf("remove %s: %v",
            r.SensorDir(id), rmErr)
    }
}
return 0, validateSignal(v, sig, id)
```

The ordering is load-bearing:

| Step | When | Why ordering matters |
| --- | --- | --- |
| `stopWatcher` (linha ~100) | Before | Watcher is dead, no live writes to `raw.log`/`signals.log`/`watcher.log` racing the RemoveAll. |
| `readSignalsLog` inside `buildAggregate` (linha ~132) | Before | Aggregate is built from `signals.log`; cleanup AFTER the read. |
| `registry.WithFileLock { RemoveEntry; Save }` (linhas 138–147) | Before | The on-disk registry view is consistent with "sensor is gone" before its directory disappears. |
| `os.RemoveAll(r.SensorDir(id))` | **NEW** | Single side-effect. Idempotent (RemoveAll on missing path is nil). |
| `validateSignal` → return → main prints to stdout | After | Mutations to `sig["metadata"]` are visible in the printed Signal. |

### Cleanup contract by exit path

`stop.go` has four terminal paths. Verified by exhaustive scan of the
script:

| Path | Triggered by | Cleanup? | Rationale |
| --- | --- | --- | --- |
| `not_running` | Linha 97 (return). No live entry in `running_sensors.json`. | **No** | The directory, if it exists, is orphaned from a prior run we did not stop. Orphan cleanup is out of scope (see Open Questions). |
| `held` | Linha 108 (return after `IsHeld` check at line 100). Other holders remain after dropping the manual hold. | **No** | The subprocess is still running and still owns its log files. |
| `failed` (registry I/O) | Linha 146 (return). `registry.WithFileLock` failed. | **No** | State on disk is inconsistent; preserve everything for the human or the next reconciliation. |
| `aggregate` | Linha 148 (return). Watcher drained, signals aggregated, entry removed. | **Yes** | The aggregate Signal IS the audit record. Per-line history is consciously discarded. |

Each `not_running`/`held`/`failed` path returns before reaching the new
cleanup block. The structure of the function preserves this by
construction — no flag, no branch needed.

### `cleanup_warning` metadata field

Shape:

- Key: `cleanup_warning`. Type: string. Format:
  `"remove <abs-path>: <go-error-string>"`.
- Lives on the aggregate Signal's `metadata` object, alongside
  existing siblings (`watcher_kill_forced`, `watcher_kill_latency_ms`,
  `reaped_holders`, `dead_holders`, `error_excerpt`, etc.).
- **Absent when cleanup succeeds.** No empty string, no `null`. The
  presence of the key is itself the diagnostic signal.
- **Never changes `verdict` or `severity`.** The aggregate's verdict
  represents the result of stopping the subprocess and reading its
  output. A FS cleanup hiccup is a janitorial detail that doesn't
  invalidate the run.

Schema compatibility: `schemas/signal.json` already declares
`metadata` with `additionalProperties: true`, so no schema bump.

### Idempotency, races, and defense-in-depth

- **`os.RemoveAll` on a missing path returns nil.** A second call to
  `/stop-sensor` (in `not_running`) leaves the path absent and emits a
  `not_running` Signal — no cleanup attempt, no error.
- **Watcher writes after RemoveAll.** `stopWatcher` runs to completion
  before the aggregate is built. In the pathological case where the
  watcher survives SIGTERM/SIGKILL, POSIX `unlink` succeeds anyway
  (inode is held open until the last close), so RemoveAll succeeds
  and the surviving watcher writes go to a freed inode — wasted but
  not destructive.
- **`start-sensor` truncation stays.** `start.go:152,159` writes empty
  bytes to `raw.log` and `signals.log` at start time. After this
  change, the truncation is mostly redundant in the happy path but
  defends against the rare case where a cleanup_warning left the old
  files in place: the next start zeroes them out anyway.

### Files that change and don't

| Path | Change | Lines |
| --- | --- | --- |
| `skills/stop-sensor/scripts/stop.go` | Add cleanup block | ~+6 |
| `skills/stop-sensor/scripts/stop_test.go` | Add 4 tests | ~+120 |
| `skills/stop-sensor/SKILL.md` | Rewrite Notes line 38 | ~±4 |
| `skills/start-sensor/SKILL.md` | Drop mitigation line 56 | ~−2 |
| `schemas/*.json` | No change | 0 |
| `lib/registry/*.go` | No change | 0 |
| `lib/orchestrator/*.go` | No change | 0 |
| `skills/start-sensor/scripts/*.go` | No change | 0 |
| `hooks/*.go` | No change | 0 |

## Testing

Tests live in `skills/stop-sensor/scripts/stop_test.go`. The existing
fixture already builds a fake registry root and a fake sensor JSON;
all four new tests extend that scaffolding.

| Test | What it asserts |
| --- | --- |
| `TestStopAggregateRemovesSensorDir` | Set up a live registry entry with `SensorDir(id)` populated (`raw.log`, `signals.log`). Run stop. Assert: exit code 0, aggregate Signal `kind=aggregate`, `SensorDir(id)` no longer exists on disk, `metadata.cleanup_warning` absent. |
| `TestStopHeldPreservesSensorDir` | Live entry with two holders (manual + sensor). Stop with no `--reap-dead-holders`. Assert: exit code 0, Signal `kind=held`, `SensorDir(id)` still exists with both log files. |
| `TestStopNotRunningPreservesSensorDir` | No live entry. Pre-create `SensorDir(id)` with stale files. Stop. Assert: exit code 0, Signal `kind=not_running`, `SensorDir(id)` still exists. |
| `TestStopCleanupWarningOnFailure` | Live entry; before stop, replace `SensorDir(id)` with a setup that defeats RemoveAll (e.g. create an immutable subentry, or stub the underlying call via build-tag-controlled indirection). Run stop. Assert: exit code 0, Signal `kind=aggregate`, `metadata.cleanup_warning` present and starts with `"remove "`, `verdict` matches the same value the test would expect without the cleanup failure. |

Notes for the last test: macOS doesn't have a portable "immutable
subfile" knob, so the cleanest forced-failure technique is to inject
the FS call via a package-level var (`var removeAll = os.RemoveAll`)
overridable in tests. If that's deemed too invasive, an alternative
is to create a file inside `SensorDir(id)` whose parent permissions
deny write (`chmod 0500`); RemoveAll then fails on the inner unlink.
Either approach is acceptable; pick whichever fits the existing test
style.

## Documentation updates

**`skills/stop-sensor/SKILL.md`** — Notes section, current line 38:

> Per-sensor `.runtime/sensors/<id>/{raw.log, signals.log}` are NOT
> deleted by stop — auditable. `.runtime/sensors/<id>/` cleanup is
> manual.

Replaces with:

> On a successful stop (`metadata.kind=aggregate`),
> `.runtime/sensors/<id>/` is removed recursively after the
> aggregate's `signals.log` is read and the registry entry is
> committed. The aggregate Signal on stdout is the durable audit
> record of the run; per-line `raw.log`/`signals.log` history is
> discarded. On `not_running`, `held`, and `failed` paths the
> directory is left untouched. Cleanup failure (e.g. permission,
> open handles on Windows) surfaces as
> `metadata.cleanup_warning` and never changes the aggregate
> verdict or severity.

**`skills/start-sensor/SKILL.md`** — Notes & limits section, current
line 56:

> Logs are append-only; nothing is rotated. Long-running sessions
> should periodically `/stop-sensor`/`/start-sensor` to keep
> `.runtime/sensors/<id>/` from growing unboundedly.

Replaces with:

> Logs are append-only within a single run and not rotated. A
> successful `/stop-sensor` removes `.runtime/sensors/<id>/`
> entirely (see `stop-sensor` Notes), so long-running blocking
> sensors should be stopped and restarted to bound disk use.

## Out of scope

Documented and deferred:

- **Orphan-directory cleanup on `not_running`.** A sensor that crashes
  without `/stop-sensor` leaves its directory behind; subsequent
  `/stop-sensor` returns `not_running` and does not clean. Two
  follow-ups are plausible: (a) in `not_running`, if no live registry
  entry exists AND no watcher is alive for the path, clean the
  directory; (b) a dedicated `/clean-sensor-runtime` skill that GC's
  the whole `.runtime/sensors/` tree. Either is its own PRD.
- **Opt-out flag.** Single policy. If a user genuinely needs to
  preserve `raw.log` for an investigation, they copy it elsewhere
  before invoking `/stop-sensor`. Adding `--keep-logs` is a
  configurability surface this PRD declines.
- **Audit archive.** No "compress the directory into a `.tar.gz` under
  `.runtime/sensors/archive/`" move. The aggregate Signal is the
  archive.
- **Rotation during a run.** Orthogonal problem.
- **Schema field for cleanup status.** `additionalProperties: true`
  on `metadata` is sufficient; a typed slot would be churn for a
  feature whose value is "absent on success."

## Open questions

- **Forced-failure test technique.** Package-level overridable
  `removeAll` var vs. permission-based fault injection. Both work;
  the implementation plan picks one. No blocker.
- **Cleanup logging.** Should a `cleanup_warning` also be echoed to
  stderr (in addition to the metadata field)? Today's stop.go does
  not write stderr for happy-path diagnostics; staying silent on
  stderr keeps the precedent. Re-evaluate if downstream tooling
  asks for it.
