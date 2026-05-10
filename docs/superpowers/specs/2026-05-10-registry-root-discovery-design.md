# Registry root discovery design

Status: proposed
Date: 2026-05-10
Related: issue [#6](https://github.com/iurykrieger/harness-framework/issues/6), `lib/registry/`, `skills/start-sensor/`, `skills/list-sensors/`, `skills/stop-sensor/`, `skills/tail-sensor/`, `lib/schema/discover.go`

## Why

The blocking-sensor registry at `<projectRoot>/.runtime/sensors/running_sensors.json` is the single source of truth for live sensors. Today, each registry-touching skill derives `projectRoot` by calling `os.Getwd()` directly:

- `skills/start-sensor/scripts/start.go:24`
- `skills/list-sensors/scripts/list.go:22`
- `skills/stop-sensor/scripts/stop.go:34`
- `skills/tail-sensor/scripts/tail.go:24`

This couples the registry path to whichever directory the user happened to be in when they invoked the skill. The result is a state-fragmentation bug:

- `/start-sensor run-api-local` from `payment-card-api/` writes the entry under `payment-card-api/.runtime/sensors/running_sensors.json`.
- `/list-sensors` from any other cwd (a sibling project, the plugin tree, an unrelated subdirectory) reads `<that-cwd>/.runtime/sensors/running_sensors.json` — usually nonexistent — and reports `verdict=pass` with `entries=[]`.
- The sensor process is alive (`ps -p 90006` confirms), but invisible to subsequent skills. Worse, a future blocking-dep attach via the orchestrator from a different cwd would re-spawn the same sensor instead of attaching, leaking the original.

`lib/registry/state.go::Load` further masks this: a missing `running_sensors.json` returns `RunningSensors{Version: 1}` (empty) without distinguishing "no live sensors" from "wrong directory entirely."

The fix is to make registry root discovery **deterministic and cwd-independent**, mirroring what `lib/schema/discover.go::FindSchemasDir` already does for schemas — but anchored on a project-tree marker rather than the plugin-tree `schemas/` directory, since the registry lives in user projects, not in the plugin.

## What changes

1. **New `lib/registry/root.go`** with two exported functions:
   - `Discover(startDir string) (projectRoot string, source Source, err error)` — pure root resolution.
   - `Lookup(startDir string) (Result, error)` — orchestrates `Discover` + `LoadOrEmpty`, returns the full surface a skill needs.
2. **New `lib/registry/state.go::LoadOrEmpty(r Root) (RunningSensors, exists bool, err error)`** — additive companion to the existing `Load`. Lets callers distinguish "registry exists but has no entries" from "registry file does not exist on disk."
3. **`skills/{start,list,stop,tail}-sensor/scripts/*.go` migrated** to call `registry.Lookup(cwd)` instead of using `os.Getwd()` as the project root.
4. **`/list-sensors` semantics extended**: when the registry file does not exist, emits `verdict=warn` with the searched path and remediation. When it exists (even if empty), keeps the current `verdict=pass`.
5. **`metadata.registry_path`, `metadata.registry_source`, `metadata.registry_exists`** added to every signal emitted by the four migrated skills, regardless of verdict, for diagnose.
6. **No change to** `signal.json` or `sensor.json` schemas — `metadata` is already an open object.
7. **No change to** `lib/sensor/load.go`, `lib/sensor/persist.go`, `skills/run-sensor/`, `skills/heal-sensor/`, `skills/detect-sensors/`. They have a similar cwd dependency for sensor-file resolution but that is a parallel concern (see Non-goals).

## Architecture

### Discovery precedence

`Discover(startDir)` resolves the project root in this order, returning at the first success:

1. **`HARNESS_REGISTRY_ROOT` env var.** If set and non-empty, must be an absolute path (validated by `filepath.IsAbs`) pointing to an existing directory (validated by `os.Stat` returning `IsDir() == true`). The env var names the **project root** — i.e., the directory that *contains* `sensors/`, not `sensors/` itself. Symlinks at the env path are resolved (`filepath.EvalSymlinks`); after resolution the path must still satisfy the absolute + IsDir checks. The env var is the explicit override — it always wins over filesystem walk-up. Returns `Source = SourceEnv`.
2. **Walk-up from `startDir` looking for a `sensors/` directory.** Starts at `filepath.Abs(startDir)` and walks parent-by-parent until either:
   - A directory whose `sensors/` child is itself a directory (`os.Stat(...).IsDir() == true`, symlinks to directories accepted) is found → return that ancestor as `projectRoot`, `Source = SourceWalkUp`. The `sensors/` directory **need not contain any files** — emptiness is allowed (a fresh project may have an empty `sensors/` placeholder).
   - The filesystem root is reached without a hit → return a discovery error.
3. **Failure.** No fallback to `cwd`. Returning a soft "use cwd" would silently reproduce the bug. The error message lists both strategies tried so the user knows what to fix.

The `sensors/` marker is chosen because it is the natural anchor of a "harness project" — the directory of sensor definitions. It already serves as the implicit anchor in `lib/sensor/load.go::ResolveSensorPath`. It exists from the very first `/start-sensor` invocation (a sensor must already exist to be started), so there is no chicken-and-egg with `.runtime/`.

### Public API of `lib/registry/root.go`

```go
package registry

// Source labels how Discover resolved the project root.
type Source string

const (
    SourceEnv    Source = "env"     // HARNESS_REGISTRY_ROOT
    SourceWalkUp Source = "walk_up" // sensors/ marker found by walking up
)

// Result aggregates discovery and state load in one return value so each
// skill needs a single call.
type Result struct {
    Root        Root           // ready-to-use, anchored at ProjectRoot
    ProjectRoot string         // absolute path
    Source      Source
    Exists      bool           // running_sensors.json present on disk
    State       RunningSensors // {Version: 1, Entries: nil} if !Exists
}

// Discover resolves the project root using HARNESS_REGISTRY_ROOT, then
// walking up from startDir looking for the sensors/ marker.
//
// Errors:
//   - HARNESS_REGISTRY_ROOT is set but not absolute or not an existing dir.
//   - Walk-up reached the filesystem root with no sensors/ found.
func Discover(startDir string) (projectRoot string, source Source, err error)

// Lookup resolves the project root, builds a Root, and loads the
// registry state. "Registry file does not exist" is NOT an error — it
// is reported as Result.Exists=false with an empty State.
//
// Errors mirror Discover plus parse failures from a malformed
// running_sensors.json on disk.
func Lookup(startDir string) (Result, error)
```

### Companion in `lib/registry/state.go`

```go
// LoadOrEmpty reads running_sensors.json. Unlike Load, it reports
// existence explicitly:
//   - file present and parseable → (state, true, nil)
//   - file absent                → (RunningSensors{Version: 1}, false, nil)
//   - file present but malformed → (zero, false, parse error)
//
// Load is preserved unchanged for callers that do not care about
// existence (orchestrator, watcher).
func LoadOrEmpty(r Root) (RunningSensors, bool, error)
```

### Skill migration pattern

Each of the four `main()` functions follows the same shape:

```go
// before
root, err := os.Getwd()
if err != nil { /* exit 2 */ }
exit, sig := runStart(root, os.Args[1:])

// after
startDir, err := os.Getwd()
if err != nil { /* exit 2 */ }
res, err := registry.Lookup(startDir)
if err != nil {
    sig := registry.DiscoveryErrorSignal(err) // helper, see §Signals
    _ = json.NewEncoder(os.Stdout).Encode(sig)
    os.Exit(1)
}
exit, sig := runStart(res, os.Args[1:])
```

`runStart`, `runList`, `runStop`, `runTail` change their signature from `(projectRoot string, …)` to `(res registry.Result, …)`. Inside, every `r := registry.NewRoot(projectRoot)` is replaced by `r := res.Root` — and the `Exists` flag drives any new branching (only `runList`).

`HARNESS_WATCHER_REGISTRY_ROOT` (passed by `start.go` to the watcher) keeps its existing semantics: it is a precise absolute path, not a discovery hint. The new `Lookup` produces it: `res.ProjectRoot`.

### Verdict semantics by skill

| Skill            | `Exists=false` (registry file missing) | `Exists=true` (registry file present) |
| ---------------- | -------------------------------------- | ------------------------------------- |
| `/start-sensor`  | first start → create normally, `verdict=pass` | current behavior (liveness check, then write) |
| `/list-sensors`  | **`verdict=warn`** with remediation    | current behavior (`verdict=pass`, even if `entries=[]`) |
| `/stop-sensor`   | **`verdict=error`** — sensor cannot be running if the registry file is absent | current behavior (lookup entry, send signal) |
| `/tail-sensor`   | **`verdict=error`** — same reasoning   | current behavior |

`/start-sensor` does not warn on a missing registry file because that is the canonical first-start case. The `Discover` failure (no marker, no env var) is the alarm-worthy event for `/start-sensor`, not a missing file.

### Signal metadata

Every signal emitted by the four migrated skills (success, error, warn) carries:

```json
"metadata": {
  "kind": "...",
  "registry_path": "/abs/path/.runtime/sensors/running_sensors.json",
  "registry_source": "env" | "walk_up",
  "registry_exists": true | false
}
```

This is purely additive. `signal.json` already declares `metadata` as `type: object` with no required keys, so no schema change is needed.

When `Discover` itself fails (no env var, no marker), the skill emits an error signal with `metadata.registry_path` absent (no path was resolved) and `evidence` with a `rationale` that names both fallback paths considered:

> "registry root discovery failed: HARNESS_REGISTRY_ROOT not set and no `sensors/` directory found walking up from `<cwd>`. Either run from a directory inside a project that contains `sensors/`, or set `HARNESS_REGISTRY_ROOT` to the project's absolute path."

This message is generated by a small helper `DiscoveryErrorSignal(err error, sensorID string) map[string]interface{}` exported from `lib/registry/root.go` to keep the four skills consistent. The return type matches the existing `errorSignal` shape in each skill (raw `map[string]interface{}` shaped to satisfy `signal.json`), avoiding a typed-struct conversion at every callsite. The `sensorID` argument is the skill-specific id (e.g., `"list-sensors"`, the sensor id passed to `/start-sensor`), so the emitted `sensor_id` field is correctly populated.

### `/list-sensors` warn signal shape

When `Exists == false`:

```json
{
  "verdict": "warn",
  "severity": "info",
  "evidence": [{
    "rationale": "registry not found at /abs/path/.runtime/sensors/running_sensors.json. /start-sensor was likely run from a different cwd, or this project has no live blocking sensors. If you expect sensors to be live, set HARNESS_REGISTRY_ROOT to the project root used at start time, or rerun /list-sensors from within that project."
  }],
  "metadata": {
    "kind": "list",
    "entries": [],
    "registry_path": "/abs/.../running_sensors.json",
    "registry_source": "walk_up",
    "registry_exists": false
  }
}
```

The empty `entries` field is preserved (consumers that only read `metadata.entries` continue to work).

## Tests

### Unit — `lib/registry/root_test.go` (new)

Table-driven tests for `Discover`:

- env var set, absolute, exists → `SourceEnv`, returns the env path.
- env var set but not absolute → error containing "absolute".
- env var set but path does not exist → error containing "not exist".
- env var unset, walk-up finds `sensors/` two levels up → `SourceWalkUp`, returns the ancestor.
- env var unset, walk-up reaches filesystem root → error mentioning both strategies.
- env var unset, `startDir` IS the project root (`sensors/` direct child) → `SourceWalkUp`, returns `startDir`.

Tests for `Lookup`:

- missing registry file → `Result{Exists: false, State: {Version: 1}}`, `nil` error.
- present, valid JSON → `Result{Exists: true, State: ...populated...}`, `nil` error.
- present, malformed JSON → non-nil parse error.
- discovery failure propagates → non-nil error of the discovery type.

Tests for `DiscoveryErrorSignal`:

- given a discovery error, returns a signal that validates against `signal.json` and includes the error string verbatim in `evidence[0].rationale`.

### Unit — `lib/registry/state_test.go` (extended)

Add cases for `LoadOrEmpty`:

- file absent → `(empty, false, nil)`.
- file present with one entry → `(state with that entry, true, nil)`.
- file present but malformed → `(zero, false, error)`.
- existing `Load` cases remain to lock in unchanged behavior.

### Skill unit tests (extended)

Each `skills/<skill>/scripts/*_test.go` already builds a tempdir-backed `registry.Root` via `registry.NewRoot(tmp)`. The new tests:

- inject the result via the new `runX(res, args)` signature directly (no env var, no walk-up — bypassing `Lookup`).
- add one new case per skill that exercises the `Exists=false` branch:
  - `/list-sensors` with `Exists=false` → asserts `verdict=warn`.
  - `/stop-sensor` with `Exists=false` → asserts `verdict=error`.
  - `/tail-sensor` with `Exists=false` → asserts `verdict=error`.
  - `/start-sensor` with `Exists=false` → asserts `verdict=pass` (first-start path).
- assert `metadata.registry_path`, `metadata.registry_source`, `metadata.registry_exists` are present on every emitted signal.

### Integration — `test/registry-discovery-e2e/` (new)

A black-box test built around real binaries (built with the `start_sensor`, `list_sensors`, `stop_sensor`, `tail_sensor` build tags), replicating the exact scenario in issue #6:

1. Create a tempdir `proj/` with `proj/sensors/<id>.json` defining a trivial blocking sensor (e.g., `sleep 30`).
2. Create a sub-directory `proj/nested/deep/`.
3. Build `start-sensor` and `list-sensors` binaries to a separate `bin/`.
4. From `proj/`, run `start-sensor <id>`. Assert exit 0 and a signal with `verdict=pass`, `metadata.registry_path` under `proj/.runtime/sensors/...`.
5. From `proj/nested/deep/` (different cwd, same project), run `list-sensors`. Assert `verdict=pass`, `metadata.entries` length 1 with the expected `sensor_id`. **This is the regression guard for issue #6.**
6. From a directory outside any project (e.g., a sibling tempdir with no `sensors/`), run `list-sensors`. Assert exit 1 and a discovery error signal naming both strategies.
7. From the same outside directory, run `list-sensors` with `HARNESS_REGISTRY_ROOT=<proj>` set. Assert `verdict=pass` with the same entry visible — confirms env-var override works.
8. Cleanup: `stop-sensor <id>` from any cwd inside the project (or with the env var set).

The test uses `t.Setenv` to scope `HARNESS_REGISTRY_ROOT` and `t.TempDir` for filesystem isolation. Test binaries are built once via `go build -tags=...` in `TestMain`.

## Non-goals

- **`lib/sensor/load.go` and `lib/sensor/persist.go` keep their walk-up logic unchanged.** They already have an ad-hoc walk-up for `sensors/` that works correctly. Unifying them with `registry.Discover` is a future opportunity, not part of issue #6's scope.
- **`/run-sensor` (computational and inferential), `/heal-sensor`, `/detect-sensors` not migrated.** They do not touch the registry. Their `os.Getwd()` calls relate to sensor-file resolution, which is a parallel concern and out of scope here.
- **`watcher_pid: -1` anomaly not addressed.** Issue #6 explicitly defers this to a separate ticket. The implementation will open that follow-up issue as part of the work, but the fix lives elsewhere.
- **No new sensor or signal schema fields.** All metadata additions ride on the open `metadata` object.

## Acceptance criteria

- [ ] `lib/registry/root.go` exists with `Discover` and `Lookup`, fully unit-tested.
- [ ] `lib/registry/state.go::LoadOrEmpty` exists; existing `Load` tests still pass.
- [ ] `skills/{start,list,stop,tail}-sensor/scripts/*.go` no longer call `os.Getwd()` to derive `projectRoot`; they call `registry.Lookup` instead.
- [ ] `/list-sensors` emits `verdict=warn` with `metadata.registry_path` when the registry file is absent; `verdict=pass` when present (even with zero entries).
- [ ] `/stop-sensor` and `/tail-sensor` emit `verdict=error` when the registry file is absent.
- [ ] All four skills include `metadata.{registry_path, registry_source, registry_exists}` in every emitted signal.
- [ ] Integration test `test/registry-discovery-e2e/` covers the cwd-A-vs-cwd-B regression and the env-var override.
- [ ] `HARNESS_REGISTRY_ROOT` is documented in `CLAUDE.md` under a new **"Registry root discovery"** subsection inserted between **"Dependencies and lifecycle"** and **"Build, validate, test"**. The subsection covers: the `sensors/` marker rule, the env-var override, and the verdict-by-skill table from this spec.
- [ ] Follow-up issue opened for `watcher_pid: -1`, linked from issue #6.
