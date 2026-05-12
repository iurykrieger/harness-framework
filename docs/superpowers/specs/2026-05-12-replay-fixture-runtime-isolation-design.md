# Replay-fixture runtime isolation design

Status: proposed (v3 — rebased on PRs #29 and #30)
Date: 2026-05-12
Related: issue [#28](https://github.com/iurykrieger/harness-framework/issues/28), `skills/detect-sensors/`, `lib/registry/`, `lib/orchestrator/`, `lib/sensor/`

## Why

Issue #28 reports that after PRs #25 (`feat: runtime persistence + lifecycle parity for /run-sensor and /start-sensor`) and #26 (`feat: resolve full requires[] set and persist per-dep .runtime logs`), every full cycle of `/detect-sensors` leaves the project's runtime tree polluted with sibling directories of the form `<runtime-root>/replay-<id>/` alongside the canonical `<runtime-root>/<id>/`. At the time the issue was filed the runtime root was `.runtime/sensors/`; PR #30 (`feat: stack-driven observation sensors + .harness/ layout`) moved it to `.harness/runtime/`. The bug semantics are unchanged — the runner persists keyed on `sensor.id`, and a mutated id leaks into the layout — only the on-disk directory name is different. Both shapes appear in the wild on existing checkouts:

```
.runtime/sensors/validate-plugin-manifest/         <- legacy canonical (pre-#30)
.runtime/sensors/replay-validate-plugin-manifest/  <- legacy pollution (pre-#30)
.harness/runtime/validate-plugin-manifest/         <- canonical (post-#30)
.harness/runtime/replay-validate-plugin-manifest/  <- pollution (post-#30, if step 7 runs)
...
```

The single point of injection is the fixture-replay snippet in `skills/detect-sensors/SKILL.md` (currently lines 357–363 post-#30; was lines 286–292 pre-#30):

```bash
TMP=$(mktemp /tmp/replay-XXXX.json)
jq --arg cmd "cat .harness/sensors/fixtures/<group>/<case>.txt" \
   '.execution.command = $cmd | .id = "replay-" + .id' \
   .harness/sensors/<id>.json > "$TMP"
go run -tags=run_computational ./skills/run-sensor/scripts "$TMP" | tail -n 1 | ...
rm "$TMP"
```

The snippet mutates `.id` to `"replay-" + .id` before piping the temp sensor to the runner. `lib/registry/paths.go::SensorDir(id)` derives the runtime directory directly from `sensor.id`, so the mutated id becomes the persisted path. Before #25/#26 the runner did not persist runtime artifacts, so the trick was invisible. Now it leaks into the runtime root.

Why the user also observed pollution during `/heal-sensor` cycles: `/heal-sensor` itself does not mutate sensor ids (`skills/heal-sensor/scripts/retry-original.go` shells out with the original sensor file). The pollution comes from the surrounding `/detect-sensors` loop, which invokes `/heal-sensor` from step 7.5 after a step-7 verification failure and then re-enters step 7 on return. The visible pattern is "ran detect/heal, got new `replay-*` folders" but the root cause is exclusively the step-7 snippet.

There is a second motivation for the same fix: the snippet violates project rule #6 of CLAUDE.md ("Deterministic logic belongs in Go, never in skill markdown"). Tempfile creation, JSON id mutation, command substitution, runner invocation, and cleanup are all deterministic — by rule, they belong in a Go script under `skills/detect-sensors/scripts/`, not in `SKILL.md` prose.

The fix is to extract the verification logic into a dedicated Go script that preserves `sensor.id` and isolates the runner's runtime persistence via an ephemeral `HARNESS_REGISTRY_ROOT`. `lib/registry.Discover` already accepts any absolute, existing directory for the env var (`lib/registry/root.go::validateEnvRoot` requires absolute path + existing directory + symlinks resolved, but does NOT require a `.harness/` child the way the walk-up path does), so a `mktemp -d` is a valid target.

### Discovery during rebase: `RunWithDepsRoot` migration is incomplete

PR #29 introduced `sensor.Resolve(idOrPath, projectRoot)` — a path-aware sensor reference resolver — and migrated some callers. PR #30 retargeted paths to `.harness/`. Neither completed the migration of `lib/orchestrator/live_deps.go::RunWithDepsRoot`, which still hardcodes the bare-id layout:

```go
// lib/orchestrator/live_deps.go:36 (current state on main)
func RunWithDepsRoot(ctx context.Context, id, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
    path := filepath.Join(projectRoot, ".harness", "sensors", id+".json")
    root := registry.NewRoot(projectRoot)
    return runWithDepsImpl(ctx, path, schemasDir, &root, stdout, stderr)
}
```

`run-computational.go` passes its raw `id` argument straight through. The documented happy-path command — `go run … @.harness/sensors/<id>.json` — works only by coincidence: the malformed path produced by the `filepath.Join` is unmade by `runWithDepsImpl`'s three-level `filepath.Dir` chain plus `loadRecursive`'s own call to `sensor.Resolve`. For an out-of-project absolute path (`/var/folders/.../sensor-XXX.json`, the shape `os.CreateTemp` produces in this spec's design), the unwind fails and the runner errors. So this spec MUST complete the `RunWithDepsRoot` migration before the replay-fixture script can pass `tempSensor.Name()` to the runner.

The completion is three lines, mirrors what `run-computational.go` does on line 76 anyway, and turns `RunWithDepsRoot` into a thin wrapper around `runWithDepsImpl` + `sensor.Resolve` (where it belongs).

## What changes

1. **Complete the `sensor.Resolve` migration in `lib/orchestrator/live_deps.go::RunWithDepsRoot`**. Three lines:

   ```go
   func RunWithDepsRoot(ctx context.Context, id, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
       path, err := sensor.Resolve(id, projectRoot)
       if err != nil {
           fmt.Fprintln(stderr, "error: resolve:", err)
           return 2
       }
       root := registry.NewRoot(projectRoot)
       return runWithDepsImpl(ctx, path, schemasDir, &root, stdout, stderr)
   }
   ```

   Plus a regression test in `lib/orchestrator/live_deps_test.go` asserting that a `tempSensor.Name()`-shaped absolute path resolves and runs. Mirrors what PR #29 did for `run-computational.go:76`; finishes the migration that PR #29 and PR #30 left half-done. After this commit, the runner accepts arbitrary absolute or `@`-prefixed sensor paths through both the read step AND the orchestrator dispatch.

2. **New `skills/detect-sensors/scripts/replay-fixture.go`** with build tag `replay_fixture`. CLI:

   ```
   go run -tags=replay_fixture ./skills/detect-sensors/scripts \
     --sensor=PATH --fixture=PATH
   ```

   Behavior:
   - Reads `--sensor` JSON, extracts `id` and `type` (computational | inferential).
   - Creates a tempdir `T` (`os.MkdirTemp("", "harness-replay-")`) and tempfile `S` (`os.CreateTemp(T, "sensor-*.json")`).
   - Writes to `S` a copy of the sensor with **only** `execution.command` overwritten to `cat <abs-fixture-path>`. **`id` is preserved verbatim.**
   - Spawns the runner: `go run -tags=<run_computational|run_inferential> ./skills/run-sensor/scripts <S>` with `HARNESS_REGISTRY_ROOT=T` appended to `os.Environ()`. Stdout and stderr stream through.
   - Defers `os.RemoveAll(T)` for cleanup (best-effort; failure logged to stderr).
   - Propagates the runner's exit code.

3. **New `skills/detect-sensors/scripts/replay-fixture_test.go`** — table-driven, covering the cases in the Testing section below.

4. **Update `skills/detect-sensors/SKILL.md` lines 357–363** to replace the shell snippet with an invocation of the new script (see "SKILL.md prose change" below).

5. **No change to** the runner script (`skills/run-sensor/`), the registry library (`lib/registry/`), the schemas, or `/heal-sensor`.

6. **No change to** the build-tag layout in `skills/detect-sensors/scripts/`. PR #30 already added `//go:build write_sensor` to `write-sensor.go` and the matching test file, and updated the live SKILL.md invocation at the persist step. The earlier draft of this spec (v1, v2) prescribed that work; the rebase on PR #30 dropped it as redundant.

7. **No change to** the on-disk shape of `<projectRoot>/.harness/runtime/` for production runs. Only verification runs are redirected to ephemeral storage; the production happy-path remains under the project's `<projectRoot>/.harness/runtime/<id>/<run-id>/`.

8. **No retroactive cleanup of existing `replay-*` directories from previous plugin versions** done in code. The user removes them once manually; subsequent runs do not regenerate them. A migration note in the SKILL.md update covers both legacy (`.runtime/sensors/replay-*`) and post-#30 (`.harness/runtime/replay-*`) paths.

## Architecture

### Invariants

| Invariant | Where enforced |
| --- | --- |
| `sensor.id` is preserved through the replay path. | `replay-fixture.go` reads the JSON, mutates only `execution.command`, marshals back. The id field is never touched. |
| Verification runs never write to the project's `.harness/runtime/`. | `HARNESS_REGISTRY_ROOT=T` overrides `lib/registry.Discover`, which prefers the env var over walk-up (`lib/registry/root.go::Discover`). |
| The orchestrator accepts absolute paths as the sensor reference. | `RunWithDepsRoot` calls `sensor.Resolve(id, projectRoot)` (this spec's first change). `Resolve` detects path-shaped inputs and resolves them as absolute paths. |
| Cleanup happens regardless of runner outcome. | `defer os.RemoveAll(T)` in `main`. |
| The script does not invent its own runtime layout. | The runner is invoked verbatim; only the registry root is overridden. Whatever layout the runner writes inside `T` is the runner's business. |

### Component shape

```
lib/orchestrator/
├── live_deps.go                     (existing; modifies RunWithDepsRoot — 3 lines)
└── live_deps_test.go                (existing; adds regression test)

skills/detect-sensors/scripts/
├── write-sensor.go                  (existing — left as-is, already tagged by PR #30)
├── write-sensor_test.go             (existing — same)
├── write-stack.go                   (existing — sibling from PR #30, tagged write_stack)
├── write-stack_test.go              (existing — same)
├── replay-fixture.go                (NEW; //go:build replay_fixture)
└── replay-fixture_test.go           (NEW; //go:build replay_fixture)
```

The build tag pattern (`replay_fixture` alongside `write_sensor` and `write_stack`) matches `skills/heal-sensor/scripts/` and `skills/run-sensor/scripts/`. Each `package main` script coexists under its own positive build tag per project rule #7.

### `replay-fixture.go` outline

```go
//go:build replay_fixture

// Command replay-fixture runs a sensor against a fixture file without
// polluting the project's .runtime/sensors/ tree. It loads the sensor
// JSON, overrides execution.command to "cat <fixture>", and invokes
// the runner with HARNESS_REGISTRY_ROOT pointed at an ephemeral
// tempdir. The runner's stdout/stderr stream through; the temp tree
// is removed on exit.
//
// Usage:
//
//   go run -tags=replay_fixture ./skills/detect-sensors/scripts \
//     --sensor=PATH --fixture=PATH
//
// Exit codes: same as the underlying runner. 2 on usage / I/O error
// before the runner is spawned.
package main

import (
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "os"
    "os/exec"
    "path/filepath"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
    fs := flag.NewFlagSet("replay-fixture", flag.ContinueOnError)
    fs.SetOutput(stderr)
    var sensorPath, fixturePath string
    fs.StringVar(&sensorPath, "sensor", "", "path to the sensor JSON (required)")
    fs.StringVar(&fixturePath, "fixture", "", "path to the fixture file (required)")
    if err := fs.Parse(args); err != nil {
        return 2
    }
    if sensorPath == "" || fixturePath == "" {
        fmt.Fprintln(stderr, "usage: replay-fixture --sensor=PATH --fixture=PATH")
        return 2
    }

    body, err := os.ReadFile(sensorPath)
    if err != nil {
        fmt.Fprintln(stderr, "read sensor:", err)
        return 2
    }
    var raw map[string]interface{}
    if err := json.Unmarshal(body, &raw); err != nil {
        fmt.Fprintln(stderr, "parse sensor:", err)
        return 2
    }
    sensorType, _ := raw["type"].(string)
    tag := "run_computational"
    if sensorType == "inferential" {
        tag = "run_inferential"
    }
    absFixture, err := filepath.Abs(fixturePath)
    if err != nil {
        fmt.Fprintln(stderr, "abs fixture:", err)
        return 2
    }
    execBlock, ok := raw["execution"].(map[string]interface{})
    if !ok {
        fmt.Fprintln(stderr, "sensor.execution is not an object")
        return 2
    }
    execBlock["command"] = fmt.Sprintf("cat %q", absFixture)

    tempRoot, err := os.MkdirTemp("", "harness-replay-")
    if err != nil {
        fmt.Fprintln(stderr, "mkdtemp:", err)
        return 2
    }
    defer func() {
        if err := os.RemoveAll(tempRoot); err != nil {
            fmt.Fprintln(stderr, "cleanup:", err)
        }
    }()

    tempSensor, err := os.CreateTemp(tempRoot, "sensor-*.json")
    if err != nil {
        fmt.Fprintln(stderr, "create temp sensor:", err)
        return 2
    }
    enc := json.NewEncoder(tempSensor)
    enc.SetIndent("", "  ")
    if err := enc.Encode(raw); err != nil {
        tempSensor.Close()
        fmt.Fprintln(stderr, "marshal temp sensor:", err)
        return 2
    }
    if err := tempSensor.Close(); err != nil {
        fmt.Fprintln(stderr, "close temp sensor:", err)
        return 2
    }

    cmd := exec.Command("go", "run", "-tags="+tag, "./skills/run-sensor/scripts", tempSensor.Name())
    if root := repoRoot(); root != "" {
        cmd.Dir = root
    }
    cmd.Env = append(os.Environ(), "HARNESS_REGISTRY_ROOT="+tempRoot)
    cmd.Stdout = stdout
    cmd.Stderr = stderr
    if err := cmd.Run(); err != nil {
        if exitErr, ok := err.(*exec.ExitError); ok {
            return exitErr.ExitCode()
        }
        fmt.Fprintln(stderr, "exec:", err)
        return 2
    }
    return 0
}

// repoRoot mirrors skills/heal-sensor/scripts/retry-original.go::repoRoot.
// Walks up from cwd looking for a directory that contains both go.mod
// and skills/run-sensor/scripts.
func repoRoot() string {
    cwd, err := os.Getwd()
    if err != nil {
        return ""
    }
    dir := cwd
    for i := 0; i < 8; i++ {
        if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
            if _, err := os.Stat(filepath.Join(dir, "skills", "run-sensor", "scripts")); err == nil {
                return dir
            }
        }
        parent := filepath.Dir(dir)
        if parent == dir {
            break
        }
        dir = parent
    }
    return ""
}
```

The `repoRoot` helper is duplicated from `skills/heal-sensor/scripts/retry-original.go` per project rule #4 ("Duplicate scripts before coupling"). When a third consumer appears, the helper graduates to `lib/cli/`; until then, two copies of an eight-line function is cheaper than a shared package.

### SKILL.md prose change

Replace `skills/detect-sensors/SKILL.md` lines 357–363 (the shell snippet that starts with `TMP=$(mktemp /tmp/replay-XXXX.json)` and ends with `rm "$TMP"`) with:

```bash
# 2) Replay each fail/warn fixture to prove the unhappy paths.
#    The Go script preserves sensor.id and isolates the runner's runtime
#    persistence via an ephemeral HARNESS_REGISTRY_ROOT, so this never
#    writes to the project's .harness/runtime/ tree.
go run -tags=replay_fixture ./skills/detect-sensors/scripts \
   --sensor=.harness/sensors/<id>.json --fixture=.harness/sensors/fixtures/<group>/<case>.txt \
   | tail -n 1 | jq -c '{verdict, severity, individuals: (.evidence|length)}'
```

Migration note added below step 7: "If your project has `.runtime/sensors/replay-*` directories from a pre-PR-#30 plugin version, or `.harness/runtime/replay-*` directories from a pre-PR-#28 plugin version, remove them once with `rm -rf .runtime/sensors/replay-* .harness/runtime/replay-*`. Subsequent `/detect-sensors` runs do not regenerate them."

## Testing

### Table-driven cases for `replay-fixture_test.go`

| Case | Setup | Expected |
| --- | --- | --- |
| happy-path | Existing fixture in `sensors/fixtures/<group>/<case>.txt` whose content makes the sensor emit `verdict=pass`. | Exit 0; aggregate JSONL on stdout has `sensor_id == <original-id>` (no `replay-` prefix); aggregate `verdict == "pass"`. |
| golden-case-fail | A fixture whose content triggers `verdict=fail` per the sensor's `verification.golden_cases[]`. | Aggregate's `verdict` and `severity` match the declared `expected_verdict` / `expected_severity`. Exit code reflects the runner's mapping. |
| runtime-isolation | Run replay-fixture from inside the harness checkout (where `.harness/` is the marker). | After the run, `<repoRoot>/.harness/runtime/<sensor.id>/` is absent. The ephemeral tempdir is gone (verified via `os.Stat`). |
| sensor-id-preserved | Any sensor + any fixture; capture the aggregate. | `aggregate.sensor_id` does NOT start with `"replay-"`. |
| missing-flags | Call with `--sensor` only, or with `--fixture` only. | Exit 2; stderr contains `"usage:"`. |
| sensor-not-json | Pass a non-JSON file as `--sensor`. | Exit 2; stderr contains parse error. |
| sensor-no-execution | Sensor JSON missing the `execution` block. | Exit 2; stderr names the missing field. |
| inferential-type | Sensor with `"type": "inferential"`. | Subprocess invoked with `-tags=run_inferential` (verify via a fake `go` shim in PATH). |

The runtime-isolation case is the load-bearing test: it directly asserts the bug from issue #28 cannot regress.

For the inferential-type case, the test substitutes `go` via `t.Setenv("PATH", ...)` pointing at a directory containing a `go` shell shim that echoes its argv. The test asserts the shim received `run -tags=run_inferential`. This avoids needing a real inferential runner in the test path. (If this proves too fragile, we drop the case and rely on a regular `go run` smoke against a minimal inferential sensor fixture — the trade-off is decided during implementation.)

### Library tests already covering related surfaces (no changes needed)

- `lib/registry/root_test.go::Test*EnvVar*` confirms `HARNESS_REGISTRY_ROOT` resolution semantics this script depends on.
- `lib/orchestrator/lifecycle_test.go` confirms `RunDir` derivation from `Root.RunDir(id, runID)`.

## Risks and mitigations

- **Parent shell sets `HARNESS_REGISTRY_ROOT` before invoking the script.** `cmd.Env = append(os.Environ(), "HARNESS_REGISTRY_ROOT="+tempRoot)` works because `os/exec` honors later entries on duplicate keys (POSIX `execve`). No defensive `os.Unsetenv` needed.
- **`os.RemoveAll(tempRoot)` fails (read-only fs, permission, zombie writer).** Logged to stderr; non-fatal. The script's purpose is the runner's stdout aggregate, not the cleanup. Operators can `rm -rf /tmp/harness-replay-*` periodically.
- **Tempdir on a different filesystem than the project root.** Irrelevant — the runner does not require co-location.
- **Runner regression that ignores `HARNESS_REGISTRY_ROOT`.** Caught by the `runtime-isolation` test, which fails if anything appears in the project's `.harness/runtime/`.
- **Re-encoded sensor JSON drifts from the on-disk file.** Acceptable — the temp sensor never persists; the runner reads it once, then it is removed. Round-trip stability is not a goal.
- **`cat` does not exist on the target system (Windows).** The plugin already targets POSIX (the existing snippet uses `cat`, `mktemp`, `jq`); this design does not change that envelope.

## Acceptance criteria (mirrors issue #28)

1. `find .harness/runtime -maxdepth 1 -type d -name "replay-*"` returns nothing after a full `/detect-sensors` cycle on a clean project.
2. Aggregate Signal of a fixture replay carries `sensor_id: "<original-id>"`, not `"replay-<original-id>"`.
3. `go test -tags=replay_fixture ./skills/detect-sensors/scripts/...` passes.
4. `go test ./lib/orchestrator/...` passes (the new regression test in `live_deps_test.go` proves `RunWithDepsRoot` accepts absolute paths).
5. `go vet -tags=replay_fixture ./...` passes.
6. `go vet ./...` (default build) passes (the `RunWithDepsRoot` change is in the default-build `lib/orchestrator` package, not tag-gated).
7. `/detect-sensors` end-to-end on a sample sensor with at least one fixture still produces the same aggregate verdict shape the previous snippet produced. The aggregate's `sensor_id` matches the on-disk sensor file's `id` field.

## Out of scope

- A general `--no-persist` flag on the runner. Could be added later if a non-detect-sensors caller wants the same isolation, but no caller exists today (YAGNI).
- Promoting `repoRoot()` to `lib/cli/`. Two duplicate copies is the cheaper state until a third consumer appears.
- Retroactively cleaning up existing `.runtime/sensors/replay-*` or `.harness/runtime/replay-*` directories from disk via code. Handled by the migration note in SKILL.md.
- Migrating the orchestrator's other `RunWithDeps*` entry points (already use path-shaped args from their callers; the `RunWithDepsRoot` fix targets only the id-shaped caller path).
- Any change to `/heal-sensor`. Its retry path is already correct.
