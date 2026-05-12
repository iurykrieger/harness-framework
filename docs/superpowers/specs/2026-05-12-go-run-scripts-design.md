# `go run` invocation contract design

Status: proposed
Date: 2026-05-12
Related: GitHub issue #15, `.claude-plugin/plugin.json`, `lib/watcher/spawn.go`, `skills/start-sensor/scripts/start.go`, `skills/start-sensor/scripts/start_unix.go`, `skills/start-sensor/scripts/watcher.go`, `skills/heal-sensor/scripts/retry-original.go`, all seven `skills/*/SKILL.md`, `hooks/error-issue-autofiler.go`, `CLAUDE.md`, `README.md`

## Why

The plugin is invoked from the user's project directory, but the Go module that owns the scripts lives in the plugin's own checkout (e.g., `~/.claude/plugins/harness-framework/`). Today every `SKILL.md` instructs the agent to run

```bash
go run -tags=<tag> ./skills/<name>/scripts <args>
```

with no `cwd` discipline. Two concrete failures follow:

1. **Workspace pollution.** When the user's project has its own `go.mod` (or a `go.work` listing unrelated modules), `go run ./skills/...` resolves `./skills/...` against the user's module — there is no `skills/` directory there, the build aborts, and the agent surfaces a Go error to the user. This is the "Go workspaces set wrong" failure mode the issue raises.
2. **Watcher binary requirement (Issue #15).** `lib/watcher.Spawn` and `skills/start-sensor/scripts/start_unix.go::watcherBinaryPath()` both compute the watcher path as `filepath.Join(filepath.Dir(os.Executable()), "watcher")`. When `start-sensor` is run via `go run`, `os.Executable()` returns Go's temporary build directory; no `watcher` sibling exists. The plugin works only when *both* binaries have been pre-built with their respective build tags and placed next to each other — a setup invariant that lives nowhere except in the source.

The hooks already use the canonical fix for the `cwd` problem: `cd "${CLAUDE_PLUGIN_ROOT}" && go run ./hooks`. The skills were never converted, and the watcher spawn assumes a deployment shape (compiled binaries side-by-side) that the rest of the plugin no longer ships. The framework is internally inconsistent.

The goal is one invocation contract used everywhere — by skills, by internal `exec.Command` chains, and by the watcher spawn — that (a) isolates Go's resolution from the user's module/workspace, (b) preserves the agent's `cwd` as the project root for registry discovery, and (c) eliminates the sibling-binary requirement entirely. No `go build` step at any point in the plugin's life.

## What changes

1. **New invocation contract**: every entry point becomes

   ```bash
   HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
     go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> \
     ./skills/<name>/scripts <args>
   ```

   The three pieces:
   - `-C "${CLAUDE_PLUGIN_ROOT}"` (Go 1.20+) chdirs the `go` process itself to the plugin root before resolving modules or paths. The user's `go.mod` is no longer in the resolution chain.
   - `HARNESS_REGISTRY_ROOT="$(pwd)"` captures the agent's `cwd` (the user's project root) *before* `-C` moves `go`. Every registry-touching script honors this env var via `lib/registry.Lookup`; non-registry scripts (`run-sensor`, `detect-sensors`, `heal-sensor`) are extended to read the same env.
   - `GOWORK=off` neutralizes any `go.work` in the user's tree. Defense in depth.

2. **Production watcher spawn migrated from inline code to `lib/watcher.Spawn`.** Today `lib/watcher` exists as a package but is unreferenced — the watcher is spawned inline inside the flock callback in `skills/start-sensor/scripts/start.go` (around lines 153 + 266). The package has the right shape but was never wired in. We finish the wiring: `start.go` calls `lib/watcher.Spawn(opts)` in place of the inline `watcherBinaryPath()` + `os.StartProcess(watcherPath, ...)` block. **`lib/watcher.Spawn` is then reworked** to use `exec.Command("go", "-C", pluginRoot, "run", "-tags=start_watcher", "./skills/start-sensor/scripts")` instead of `os.StartProcess(<sibling binary>, ...)`. `BinaryPath()` deleted. `SpawnOpts` gains `PluginRoot string`. The call site reads `os.Getenv("CLAUDE_PLUGIN_ROOT")` and passes it in; empty value emits a final Signal with `metadata.cause=plugin_root_missing` and exits non-zero before spawning anything.

3. **`skills/start-sensor/scripts/start_unix.go` trimmed** — `watcherBinaryPath()` deleted; `killGroup`/`killPID` retained (still used by the SIGKILL fallback). The inline spawn block in `start.go` (lines 257–319 approx.) shrinks to a single `watcher.Spawn(opts)` call plus the registry/envelope wiring around it.

4. **`skills/heal-sensor/scripts/retry-original.go` updated** — line 58's `exec.Command("go", "run", "-tags="+tag, "./skills/run-sensor/scripts", sensorPath)` is replaced with `exec.Command("go", "-C", pluginRoot, "run", "-tags="+tag, "./skills/run-sensor/scripts", sensorPath)` plus `cmd.Env` setting `HARNESS_REGISTRY_ROOT`, `CLAUDE_PLUGIN_ROOT`, `GOWORK=off`. The local `repoRoot()` walk-up helper (lines 80–99) is **deleted**: with `-C "${CLAUDE_PLUGIN_ROOT}"` the directory of `cmd` no longer matters, and the walk-up exists today only to thread `cmd.Dir = root`.

5. **Seven `SKILL.md` files updated mechanically** — `run-sensor`, `start-sensor`, `stop-sensor`, `tail-sensor`, `list-sensors`, `detect-sensors`, `heal-sensor`. The `run-sensor/SKILL.md` is the canonical source for the contract explanation; the others link to it instead of repeating the rationale.

6. **`hooks/error-issue-autofiler.go` regex extended** — the current matcher targets `harness-<skill>` and `harness-watcher` binary names in panic/stderr output. Additional patterns are added for the new shape: `go run -tags=<tag> ... ./skills/<name>/scripts`. A `tag → skill` mapping table (`run_computational`/`run_inferential` → `run-sensor`, `start_sensor` → `start-sensor`, `start_watcher` → `start-sensor`, `stop_sensor` → `stop-sensor`, `tail_sensor` → `tail-sensor`, `list_sensors` → `list-sensors`, `heal_diagnose`/`heal_apply_safe`/`heal_apply_sensors`/`heal_retry_original` → `heal-sensor`) lives in the autofiler. The legacy regex is kept as a fallback so users with CI that pre-builds binaries still get crash autofiling.

7. **Test scaffolding shifted from "stub watcher binary" to "injectable `spawnFn`"** — `lib/watcher/spawn.go` exposes a package-level `var spawnFn = realSpawn`. Production points it at the new `exec.Command("go", "run", ...)` implementation. Tests substitute `spawnFn` with a closure returning a fake PID. Existing `lib/orchestrator/main_test.go` and `skills/start-sensor/scripts/start_test.go` TestMain blocks (which install `/usr/bin/true` as a stub watcher) are rewritten to swap `spawnFn` instead.

8. **`CLAUDE.md`, `README.md`, `CHANGELOG.md` documentation pass** — `CLAUDE.md` "Build, validate, test" section reflects the new contract; the watcher compile-on-each-start latency (~150–500ms warm cache, ~1s cold) is documented as a known trade-off; `README.md` (currently empty) gains a minimal quick-start; `CHANGELOG.md` records the contract change as breaking-ish (the invocation strings in `SKILL.md` are the breaking surface).

Nothing in `schemas/`, in the runtime `lib/` packages beyond `lib/watcher/`, or in the sensor lifecycle/orchestrator logic changes.

## Non-goals

- **Collapsing `start.go` and `watcher.go` into a single dispatching `main`.** This was the alternative explored as Approach A. It avoids the watcher compilation cost but violates project rule #7 ("one script, one clear job") with an internal `--watcher` flag. We accept the latency to keep the rule unbroken.
- **Removing the watcher subprocess entirely.** Moving parsing to on-demand inside `/tail-sensor` is a much larger refactor and was ruled out as out-of-scope.
- **Shipping pre-built binaries with the plugin.** The plugin remains source-only; users with custom CI that wraps it in binaries continue to be supported by the legacy autofiler regex, but the plugin itself never blesses that path.
- **Detecting the right `CLAUDE_PLUGIN_ROOT` automatically.** We assume Claude Code sets it in the agent's Bash environment for plugin-invoked sessions. If absent, scripts fail loudly with `plugin_root_missing`. We do not implement a fallback discovery (no walking up looking for `.claude-plugin/plugin.json`, no env-var alternatives) because every fallback is a new failure mode to maintain.
- **Backporting the contract to Go < 1.20.** The `go.mod` already declares `go 1.25`; the `-C` flag is non-negotiable.

## Architecture

### The invocation contract

Every command issued by the framework, whether typed by the agent (from `SKILL.md`) or constructed by Go code (`exec.Command`), takes the shape:

```
HARNESS_REGISTRY_ROOT=<project root>
GOWORK=off
  go -C <plugin root> run -tags=<build tag> ./skills/<name>/scripts <args...>
```

The flags and env vars are not optional and not order-sensitive (env vars are set before the binary, the `-C` flag is consumed by `go` itself before any subcommand). The agent's shell evaluates `"$(pwd)"` at command time, capturing the project root in `HARNESS_REGISTRY_ROOT`. `${CLAUDE_PLUGIN_ROOT}` is exposed by Claude Code to all plugin-originated commands; the framework treats it as required.

```
agent (cwd = project root, e.g. /Users/x/my-app)
  │  CLAUDE_PLUGIN_ROOT=/Users/x/.claude/plugins/harness-framework
  │
  └─ Bash tool: HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
                 go -C "${CLAUDE_PLUGIN_ROOT}" run -tags=start_sensor \
                 ./skills/start-sensor/scripts foo
        │
        └─ go (cwd chdir'd to /Users/x/.claude/plugins/harness-framework via -C)
              │  inherits HARNESS_REGISTRY_ROOT, CLAUDE_PLUGIN_ROOT, GOWORK=off
              │
              └─ start-sensor binary (compiled to /tmp/go-build.../start-sensor)
                    │  inherits all three env vars
                    │  reads HARNESS_REGISTRY_ROOT for registry.Lookup
                    │  reads CLAUDE_PLUGIN_ROOT for watcher spawn
                    │
                    ├─ sh -c <sensor.execution.command>     (the user's blocking sensor)
                    │       cwd = whatever `start-sensor` had at exec time
                    │       (which is the plugin root, post-chdir-by-go).
                    │       Sensor commands needing project-root cwd should
                    │       reference `$HARNESS_REGISTRY_ROOT` explicitly —
                    │       documented in sensor authoring guide.
                    │
                    └─ exec.Command("go", "-C", "${CLAUDE_PLUGIN_ROOT}", "run",
                                    "-tags=start_watcher", "./skills/start-sensor/scripts")
                          │  Setsid: true (session leader)
                          │  Env: HARNESS_WATCHER_* + GOWORK=off
                          │  Stderr -> .runtime/sensors/<id>/<run>/watcher.log
                          │
                          └─ go (cwd chdir'd to plugin root via -C)
                                └─ watcher binary (compiled to /tmp/go-build.../watcher)
                                      tails raw.log, emits to signals.log
```

Process tree depth: 5 (was 4). The added layer is the `go` wrapper around the watcher.

### The `cwd` of sensor commands

Today, when `/run-sensor` or `/start-sensor` spawns `sh -c <execution.command>`, the subprocess inherits the runner's `cwd`. The runner's `cwd` is the agent's (project root). After this change, the runner is invoked through `go -C <plugin root>`, so its `cwd` is the plugin root. **This is a behavior change visible to sensor authors.** Sensors that referenced relative paths in `execution.command` (e.g., `npm run lint`, `pytest`) implicitly relied on `cwd` being the project root.

Mitigation: the runner explicitly `os.Chdir(projectRoot)` before `subprocess.SpawnDetached` / before the `sh -c` exec, where `projectRoot = res.ProjectRoot` from `lib/registry.Lookup`. This restores the prior behavior. The change is invisible to sensor authors; the runner's own cwd during the orchestration steps (before spawn) doesn't matter because all paths into `lib/` are absolute or `projectRoot`-rooted already.

### The watcher spawn

`lib/watcher.Spawn` becomes:

```go
type SpawnOpts struct {
    PluginRoot     string  // NEW: required, sourced from CLAUDE_PLUGIN_ROOT
    ProjectRoot    string
    SensorID       string
    RunID          string
    RawLogPath     string
    SignalsLogPath string
    EnvelopeJSON   []byte
    PatternsJSON   []byte
    SubprocessPID  int
    WatcherLogPath string  // unchanged
}

var spawnFn = realSpawn  // package-level, swappable in tests

func Spawn(opts SpawnOpts) (int, error) {
    if opts.PluginRoot == "" {
        return 0, errors.New("plugin root not set (CLAUDE_PLUGIN_ROOT)")
    }
    return spawnFn(opts)
}

func realSpawn(opts SpawnOpts) (int, error) {
    logFile, err := openWatcherLog(opts)
    if err != nil { return 0, err }

    cmd := exec.Command("go", "-C", opts.PluginRoot, "run",
        "-tags=start_watcher", "./skills/start-sensor/scripts")
    cmd.Env = append(os.Environ(),
        "GOWORK=off",
        "HARNESS_WATCHER_RAW="+opts.RawLogPath,
        // ... 7 other HARNESS_WATCHER_* vars same as today
    )
    cmd.SysProcAttr = &sysProcAttr  // Setsid: true
    cmd.Stderr = logFile

    if err := cmd.Start(); err != nil {
        _ = logFile.Close()
        return 0, fmt.Errorf("start watcher: %w", err)
    }

    // Probe for early death (compile error in watcher.go, missing go binary)
    if alive, exitCode, stderr := earlyDeathProbe(cmd, logFile, 100*time.Millisecond); !alive {
        return 0, fmt.Errorf("watcher exited early (code %d): %s", exitCode, stderr)
    }

    pid := cmd.Process.Pid
    _ = cmd.Process.Release()
    _ = logFile.Close()  // parent's handle; child keeps its own fd
    return pid, nil
}
```

`earlyDeathProbe` waits up to 100ms via `syscall.Wait4(pid, WNOHANG)` polling. If the process has exited, it reads `watcher.log` tail for stderr context and returns `(false, exitCode, stderr)`. This catches compile errors in `watcher.go` that today would fail at build time but with `go run` only manifest after `Start()` returns. 100ms is below the latency floor of a normal watcher startup (the watcher's `runWatcher` goroutine has at least the fsnotify Add to complete) — false negatives on slow systems are tolerable because they just mean the probe says "alive" when the watcher would have died a moment later, in which case the next `/tail-sensor` reveals the absence of signals.

### The `spawnFn` injection point

Tests at three layers swap `spawnFn`:

- `lib/watcher/spawn_test.go` — most cases run `realSpawn` against a *fake `go` binary* installed via `PATH` prefix. The fake records args/env/cwd to files; the test reads them back. One case overrides `spawnFn` directly to verify the public `Spawn` wrapper preserves the `PluginRoot == ""` guard.
- `lib/orchestrator/main_test.go` — overrides `spawnFn` to a noop in `TestMain`. The orchestrator's tests never cared about real watcher behavior; they only needed `Spawn` to return a plausible PID. The previous stub-binary install is deleted.
- `skills/start-sensor/scripts/start_test.go` — same noop override in `TestMain`. The `os.Executable() + write watcher stub` block (lines ~25–35) deleted.

Production never touches `spawnFn` directly; `Spawn` is the only public entry point.

### Project root capture across scripts

Today three scripts (`run-sensor`, `detect-sensors`, `heal-sensor`) discover the project root by walking up from `os.Getwd()`. The other four (`list-sensors`, `start-sensor`, `stop-sensor`, `tail-sensor`) call `lib/registry.Lookup` which already honors `HARNESS_REGISTRY_ROOT`.

We unify: all seven scripts call `lib/registry.Lookup(os.Getwd())`. `Lookup` is unchanged — it already prefers `HARNESS_REGISTRY_ROOT` over walk-up. The non-registry-touching scripts (`run-sensor`, `detect-sensors`, `heal-sensor`) just need to swap their inline walk-up for the `Lookup` call. Schema discovery (which currently also walks up) keeps a separate helper `lib/schema.DiscoverSchemasDir` that prefers an explicit `--schemas-dir` flag, then `HARNESS_REGISTRY_ROOT/schemas/`, then walk-up — same precedence, same fallback as before.

### Diagnostics: `error-issue-autofiler` regex extension

Current `frameworkCommandPatterns` (`hooks/error-issue-autofiler.go:63-75`) **already** matches `go run -tags=<tag> ./skills/<name>/scripts` and `go run -tags=<tag> ./hooks`. What it does **not** match is the new shape with `-C <path>` between `go` and `run`:

```
go -C /Users/x/.claude/plugins/harness-framework run -tags=start_sensor ./skills/start-sensor/scripts foo
```

So the patch is narrow: in the two regexes that currently start with `go\s+run\s+...`, insert an optional `(?:-C\s+\S+\s+)?` between `go\s+` and `run`. The fourth pattern (`go (?:test|vet|build)`) gets the same insertion for consistency, since `go -C ... test ./...` is valid and the framework's own test runs may end up using it. The binary-name pattern (`harness-<skill>` / `harness-watcher`) stays as-is for legacy CI.

A tag→skill mapping table is added to support a richer "which skill crashed?" classification in future patches, but it is **not** required by the regex extension itself; the existing classifier already extracts the skill name from the `./skills/<name>/scripts` capture group. The mapping table is therefore deferred to a follow-up unless a concrete case in the test suite needs it. We document it here for completeness:

| Tag                                              | Skill          |
|--------------------------------------------------|----------------|
| `run_computational`, `run_inferential`           | `run-sensor`   |
| `start_sensor`, `start_watcher`                  | `start-sensor` |
| `stop_sensor`                                    | `stop-sensor`  |
| `tail_sensor`                                    | `tail-sensor`  |
| `list_sensors`                                   | `list-sensors` |
| `heal_diagnose`, `heal_apply_safe`,              |                |
|   `heal_apply_sensors`, `heal_retry_original`    | `heal-sensor`  |

Panics in `go run` produce stderr like `panic: ... goroutine ... /tmp/go-build.../start-sensor/start.go:42`. The fingerprint extraction keeps using the first `github.com/iurykrieger/harness-framework/...` frame, which the compiled binary still contains in its panic backtrace regardless of where the binary lives.

## Edge cases and failure modes

| Scenario | Detection | Response |
|---|---|---|
| `CLAUDE_PLUGIN_ROOT` unset in agent env | `go -C ""` fails with `chdir: empty path` | Autofiler captures via stderr regex; runners that managed to start also guard explicitly and emit `metadata.cause=plugin_root_missing` |
| `CLAUDE_PLUGIN_ROOT` points to non-existent dir | `go -C <path>: no such directory` | Same — autofiler captures; runners never get to start |
| `HARNESS_REGISTRY_ROOT` unset AND agent cwd has no `sensors/` ancestor | Existing `lib/registry.Lookup` failure | Existing `metadata.kind=registry_discovery_failed`; no regression |
| `go` not on PATH | `exec.LookPath: "go": executable file not found` on watcher spawn | Convert to `watcher_spawn_failed`; rollback subprocess (existing `killGroup` flow) |
| Watcher compile error (dev only) | `earlyDeathProbe` finds exited process within 100ms | Read `watcher.log` for stderr; surface as `watcher_spawn_failed` with rationale |
| `go.work` in user project listing conflicting modules | `GOWORK=off` neutralizes | E2E case covers this |
| User's `go.mod` shadows plugin imports | `-C` chdir defeats it | E2E case covers this |
| Build cache corrupted | Watcher compile fails → early death probe catches it | `watcher_spawn_failed`; not our problem to recover |
| `/stop-sensor` SIGTERMs `watcher_pid` (now the `go` PID) | `go run` forwards SIGTERM to its child process | Watcher does drain (existing logic in `watcher.go:60-63`); `go` waits and exits |
| Stale registry entry with `watcher_pid` pointing at a since-dead `go` PID | Existing `IsPIDAlive` check in `/list-sensors`/`/stop-sensor` | Reported as orphan; no behavior change |
| Go version < 1.20 (missing `-C`) | `flag provided but not defined: -C` on every invocation | Documented as a requirement; `go.mod` already pins 1.25 |
| Race: watcher subprocess survives `go run` parent | `go run` only exits after watcher does (it `Wait`s) | Not possible; this is `go run`'s contract |
| Latency: `/start-sensor` p99 | Manual benchmark before/after | Documented in CLAUDE.md as ~150–500ms warm, ~1s cold; acceptable for interactive command |

### The `cwd` change for sensor commands (called out)

This is the only externally-observable behavior change beyond performance. Today a sensor's `execution.command` runs in the agent's `cwd` (project root). Without mitigation, after this change it would run in the plugin root. We restore parity by `os.Chdir(projectRoot)` in the runner before subprocess spawn. Documented in `CLAUDE.md` under "Architecture → the runner" and verified by an E2E test that uses a sensor command referencing `./README.md` (which exists in the test project but not in the plugin).

## Testing strategy

### Unit (fast, mandatory)

**`lib/watcher/spawn_test.go`** (rewritten):

- `TestRealSpawn_Args` — fake `go` records `os.Args`. Asserts `-C <pluginRoot> run -tags=start_watcher ./skills/start-sensor/scripts`.
- `TestRealSpawn_Env` — fake `go` records env. Asserts all 8 `HARNESS_WATCHER_*` + `GOWORK=off`.
- `TestRealSpawn_Setsid` — fake `go` checks `getpgrp() != getppid()` and writes result. Asserts session leader.
- `TestRealSpawn_Release` — fake `go` lives 200ms+. Asserts `cmd.Process.Release()` succeeded (handle not retained); spawned process is independently `kill`-able.
- `TestRealSpawn_EarlyDeath` — fake `go` exits 1 within 50ms with stderr `mock compile error`. Asserts `Spawn` returns error containing `mock compile error`.
- `TestRealSpawn_EmptyPluginRoot` — public `Spawn` with `PluginRoot=""`. Asserts error before any process work.
- `TestRealSpawn_GoMissing` — `PATH=/dev/null`. Asserts `exec.LookPath` failure surfaced.

Helper `withFakeGo(t *testing.T, exitDelay time.Duration, exitCode int, stderr string)` provisions a temp dir, writes a bash script as `go`, prefixes `PATH`. ~60 LOC reusable across tests.

**`hooks/error-issue-autofiler_test.go`** extended cases:

- Panic in `/tmp/go-build.../start-sensor/start.go:42` maps to skill `start-sensor`.
- Crashed command `go run -C ... -tags=run_computational ./skills/run-sensor/scripts foo` maps to `run-sensor`.
- Legacy: `harness-watcher: panic` still maps to `start-sensor` (backwards compat).
- Unknown tag in `go run -tags=mystery_tag ./skills/some/scripts` → fingerprint with skill = `unknown`, still files (better noise than missed crash).

### Integration

**`skills/start-sensor/scripts/start_test.go`** (modified): the existing end-to-end `TestEndToEndStart`-style cases swap stub-watcher install for `watcher.SpawnFn = func(SpawnOpts) (int, error) { return fakePID, nil }`. Existing registry/signals asserts unchanged.

New cases:
- `CLAUDE_PLUGIN_ROOT=""` → final Signal `metadata.cause=plugin_root_missing`, exit 1.
- `CLAUDE_PLUGIN_ROOT="/nonexistent"` → fake `spawnFn` returns error; final Signal `metadata.cause=watcher_spawn_failed`.

**`lib/orchestrator/main_test.go`** (modified): stub watcher binary install (lines 14–25) deleted; replaced with `watcher.SpawnFn = noopSpawn` in `TestMain`. All existing orchestrator tests pass unchanged.

### E2E

**`test/registry-discovery-e2e/registry_discovery_e2e_test.go`** (extended):

- **`TestPluginVsProjectGoMod`** — temp project with its own `go.mod` (module `example.com/userapp`), plus `sensors/foo.json`. `CLAUDE_PLUGIN_ROOT=<repo root>`. From temp project's cwd, run `HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off go run -C "$CLAUDE_PLUGIN_ROOT" -tags=list_sensors ./skills/list-sensors/scripts`. Assert exit 0, `verdict=warn` (no registry), `metadata.registry_path` ends with `<temp>/.runtime/sensors/running_sensors.json`. Proves both `-C` and `HARNESS_REGISTRY_ROOT` work.
- **`TestGoWorkPollution`** — same temp project with an added `go.work` listing `example.com/userapp` and a fake module. Same command, same assertions. Proves `GOWORK=off` defends.
- **`TestSensorCwd`** — sensor `cwd-probe` whose `execution.command` is `cat README.md && echo PROJECTROOT-CONFIRMED`. Test project contains a `README.md` with sentinel content; plugin root does not contain `README.md` (or contains a different one). Run via `/run-sensor`. Assert aggregate Signal contains the project's `README.md` content, not the plugin's. Proves the `os.Chdir(projectRoot)` mitigation works.

### Real-`go` opt-in suite

**`skills/start-sensor/scripts/start_realgo_test.go`** (new, build tag `integration_realgo`):

- Real `go` on PATH, real watcher compilation. Sensor command: `sleep 30`. Asserts `/start-sensor` returns `started`; `.runtime/sensors/<id>/<run>/watcher.log` eventually contains the watcher's "tailing raw.log" startup log line; cleanup via `kill -TERM <watcher_pid>` terminates the watcher cleanly.
- Gated by `os.Getenv("HARNESS_TEST_REALGO") == "1"`. Skipped in CI default. Local sanity check.

### CI matrix

- macOS + Linux with Go 1.25 (declared in `go.mod`).
- `go test ./lib/... ./hooks/... ./skills/...` runs all unit + integration.
- E2E suite runs in its own job (slower; spins up temp dirs).
- `integration_realgo` is local-only.

### Smoke (post-merge, manual)

Install plugin fresh in a Node project (no Go toolchain in the user's project beyond what `go run` itself uses). Sequence:
1. `/list-sensors` — expect `warn` (no registry yet) without any `go: cannot find module` errors.
2. `/run-sensor` with a trivial sensor — expect Signal as today.
3. `/start-sensor` with a blocking sensor — expect `started`.
4. `/tail-sensor` — expect Signals streamed.
5. `/stop-sensor` — expect graceful stop.

Zero `go build`/`go install` should be required at any point. Close issue #15 with a link to the commit deleting `BinaryPath()`.

## Rollout

Single branch (`binary-compiling`), five commits in logical order; each leaves the test suite green so bisect/rollback is granular.

1. **`refactor(start-sensor): delegate watcher spawn to lib/watcher`** — `start.go` is refactored so the inline `watcherBinaryPath() + os.StartProcess(...)` block is replaced by a single `watcher.Spawn(opts)` call. `lib/watcher.Spawn` is **also** modified in this commit so its production behavior at the call site is preserved (still sibling-binary lookup at this point). The package gains the `var spawnFn = realSpawn` injection point; tests start using it. `lib/orchestrator/main_test.go` and `skills/start-sensor/scripts/start_test.go` swap stub-binary install for `spawnFn` override. **Parity note for the implementer**: today's inline spawn in `start.go` sets eight `HARNESS_WATCHER_*` env vars; `lib/watcher.Spawn`'s current env block has only seven (`HARNESS_WATCHER_RUN_ID` is missing despite `SpawnOpts.RunID` already existing). Add the missing line in this commit so semantics are preserved before commit 2 changes the spawn mechanism. Likewise, this commit decides whether `WatcherLogPath` is supplied explicitly by the caller (today's behavior — `start.go` constructs `runDir/watcher.log`) or relies on `lib/watcher.Spawn`'s default (`filepath.Dir(opts.SignalsLogPath)/watcher.log`); pick "explicit from caller" to match current paths exactly. This commit fixes the long-standing dead-code state of `lib/watcher` and isolates the test surface — *but it does not yet change watcher spawn semantics*.
2. **`feat(watcher): spawn via go run instead of sibling binary`** — `realSpawn` rewritten to `exec.Command("go", "-C", pluginRoot, "run", "-tags=start_watcher", "./skills/start-sensor/scripts")`. `BinaryPath()` and `start_unix.go::watcherBinaryPath` deleted. New `SpawnOpts.PluginRoot` plumbed from `start.go` (reads `os.Getenv("CLAUDE_PLUGIN_ROOT")`). `earlyDeathProbe` added. New unit tests via `withFakeGo` helper.
3. **`feat(skills): adopt go run -C invocation contract`** — all seven `SKILL.md` updated. `heal-sensor/scripts/retry-original.go` updated (line 58 contract; `repoRoot()` helper deleted). `run-sensor`, `detect-sensors`, `heal-sensor` walk-up replaced with `lib/registry.Lookup`. Runner adds `os.Chdir(projectRoot)` before subprocess spawn. Tests for the inline `exec.Command` (`run-computational_test.go:163`, `run-inferential_test.go:347`) updated to match the new contract.
4. **`feat(autofiler): match go -C run invocations`** — the four regexes in `buildFrameworkCommandPatterns` extended with optional `(?:-C\s+\S+\s+)?` between `go\s+` and the verb. Legacy binary-name regex retained. New test cases for `go -C <path> run -tags=...`.
5. **`docs: invocation contract, watcher latency, README`** — `CLAUDE.md` "Build, validate, test" section rewritten; `README.md` populated; `CHANGELOG.md` entry; `SKILL.md` cross-references consolidated.

Plus the E2E test additions, which fit naturally into commit 3.

Version bump to `1.1.0` in `.claude-plugin/plugin.json`. The invocation-string change in `SKILL.md` is the only externally-observable break for anyone who copy-pasted commands out of the skill bodies; for normal slash-command users, no break.

No migration is needed: the plugin never shipped pre-built binaries. Users with custom CI that compiled their own copy of `harness-watcher`/`harness-start-sensor` can keep doing so (the autofiler regex still recognizes them), but the plugin's own slash commands no longer depend on those binaries existing.

## Open questions

None blocking. The following were considered and resolved during brainstorming:

- *Should we collapse start.go + watcher.go into a single re-execing main?* No — Approach A; violates rule #7 with an internal `--watcher` flag. Accept latency, keep the rule.
- *Should we eliminate the watcher subprocess entirely?* No — out of scope; large architectural change.
- *Should `CLAUDE_PLUGIN_ROOT` have a fallback?* No — fail loudly with `plugin_root_missing`. Every fallback is a new failure mode.
- *Should `GOWORK=off` be conditional on detecting a `go.work` in the user's tree?* No — unconditional is simpler and equivalent.
- *Should we precompile the watcher to a known cache dir on first `/start-sensor`?* No — `go run`'s build cache already covers this (link step is what dominates after first run). Adding a manual cache duplicates Go's existing mechanism.
