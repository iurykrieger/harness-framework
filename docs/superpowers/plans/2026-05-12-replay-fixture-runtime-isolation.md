# Replay-fixture runtime isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate `.harness/runtime/replay-*` directory pollution caused by `/detect-sensors` step 7's fixture-replay snippet, by extracting the verification into a Go script that preserves `sensor.id` and routes the runner's runtime root to an ephemeral tempdir.

**Architecture:** New script `skills/detect-sensors/scripts/replay-fixture.go` (build tag `replay_fixture`) — thin CLI that reads a sensor JSON, overrides only `execution.command` to `cat <fixture>`, and invokes the runner with `HARNESS_REGISTRY_ROOT=$(mktemp -d)` so persistence lands in a tempdir that is removed on exit. A prerequisite 3-line change to `lib/orchestrator/live_deps.go::RunWithDepsRoot` completes the `sensor.Resolve` migration started in PRs #29 and #30, allowing the runner to accept absolute paths as the sensor reference.

**Tech Stack:** Go 1.25.3, `flag`, `encoding/json`, `os/exec`. Reuses `lib/testfixtures` for sensor fixtures in tests. No new dependencies.

**Spec reference:** [`docs/superpowers/specs/2026-05-12-replay-fixture-runtime-isolation-design.md`](../specs/2026-05-12-replay-fixture-runtime-isolation-design.md) v3. Tracks issue [#28](https://github.com/iurykrieger/harness-framework/issues/28). Builds on `b09eb36` (PR #29) and `35d6d0a` (PR #30).

**Note on previous iterations:** The original v1/v2 of this plan had two additional foundation tasks (tagging `write-sensor.go` and updating `SKILL.md:245`). PR #30 landed those changes upstream; the rebase dropped the corresponding local commits. The current plan starts at what was previously Task 4a (orchestrator fix) and proceeds through the original T3–T8.

---

## File Structure

**Modify:**
- `lib/orchestrator/live_deps.go` — `RunWithDepsRoot` calls `sensor.Resolve(id, projectRoot)` instead of `filepath.Join(projectRoot, ".harness", "sensors", id+".json")`.
- `lib/orchestrator/live_deps_test.go` — add regression test asserting absolute-path acceptance.
- `skills/detect-sensors/SKILL.md` — replace lines 357–363 (replay snippet) with new Go script invocation; add migration note covering both legacy and post-#30 pollution paths.
- `.github/workflows/test.yml` — extend CI matrix with `replay_fixture` tag jobs.

**Create:**
- `skills/detect-sensors/scripts/replay-fixture.go` — the new script.
- `skills/detect-sensors/scripts/replay-fixture_test.go` — table-driven tests.

Each file has one responsibility: `replay-fixture.go` is CLI/orchestration glue between a sensor file, a fixture file, and the runner. The orchestrator fix completes a half-done migration. Tests live next to their target per project rule #4.

---

## Task 1: Complete `RunWithDepsRoot` migration to `sensor.Resolve`

PRs #29 and #30 introduced `sensor.Resolve(idOrPath, projectRoot)` and migrated some callers, but left `lib/orchestrator/live_deps.go::RunWithDepsRoot` still doing `filepath.Join(projectRoot, ".harness", "sensors", id+".json")`. Out-of-project absolute paths (the shape `os.CreateTemp` produces, which Task 3 will pass) fail through this path because `filepath.Join` strips the leading `/` and treats the components as relative.

**Files:**
- Modify: `lib/orchestrator/live_deps.go` (lines 35–39, the `RunWithDepsRoot` body)
- Modify: `lib/orchestrator/live_deps_test.go` (append regression test)

- [ ] **Step 1: Inspect the current state**

Run:
```bash
sed -n '30,40p' lib/orchestrator/live_deps.go
```

Expected (5 lines of body):
```go
// RunWithDepsRoot is the id-resolving variant of RunWithDeps. The
// requested sensor is identified by id (resolved to <root>/.harness/sensors/<id>.json),
// schemasDir is resolved by the schema package's discovery if empty.
// All blocking deps along the chain are started/attached before the
// requested sensor runs and stopped/detached after.
func RunWithDepsRoot(ctx context.Context, id, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
    path := filepath.Join(projectRoot, ".harness", "sensors", id+".json")
    root := registry.NewRoot(projectRoot)
    return runWithDepsImpl(ctx, path, schemasDir, &root, stdout, stderr)
}
```

- [ ] **Step 2: Verify imports include `sensor`**

```bash
grep -n "iurykrieger/harness-framework/lib/sensor" lib/orchestrator/live_deps.go
```

If the import is missing, the edit must add it. (At time of writing, `live_deps.go` does NOT import `lib/sensor` — Task 1 adds the import.)

- [ ] **Step 3: Edit `RunWithDepsRoot`**

Use Edit. `old_string`:
```go
func RunWithDepsRoot(ctx context.Context, id, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
	path := filepath.Join(projectRoot, ".harness", "sensors", id+".json")
	root := registry.NewRoot(projectRoot)
	return runWithDepsImpl(ctx, path, schemasDir, &root, stdout, stderr)
}
```

`new_string`:
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

- [ ] **Step 4: Add the `sensor` import if missing**

If Step 2 confirmed the import was absent, find the import block at the top of `live_deps.go` and add `"github.com/iurykrieger/harness-framework/lib/sensor"` to it. Also add `"fmt"` if it isn't already present (the new `fmt.Fprintln` call needs it).

Verify:
```bash
goimports -l lib/orchestrator/live_deps.go
```
(Empty output = imports are tidy.) If `goimports` isn't installed, `go build ./lib/orchestrator/...` will also flag missing imports.

- [ ] **Step 5: Verify the change compiles and existing tests pass**

```bash
go build ./lib/orchestrator/...
go test -race -count=1 ./lib/orchestrator/...
```

Both must exit 0. Existing tests that exercise `RunWithDepsRoot` with a bare id (e.g., `"target"`) MUST still pass because `sensor.Resolve` handles bare ids identically — it joins them to `<projectRoot>/.harness/sensors/<id>.json` after regex validation.

- [ ] **Step 6: Add the regression test**

Append the following test to `lib/orchestrator/live_deps_test.go`:

```go
func TestRunWithDepsRoot_AcceptsAbsolutePath(t *testing.T) {
	proj := t.TempDir()
	// Materialize a minimal valid computational sensor at an absolute path
	// OUTSIDE the project's .harness/sensors/ tree.
	s := testfixtures.ValidSensorComputational()
	s["id"] = "abs-path-target"
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	absSensorPath := filepath.Join(t.TempDir(), "sensor.json")
	if err := os.WriteFile(absSensorPath, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// projectRoot does NOT contain .harness/sensors/. The caller passes an
	// absolute path, which sensor.Resolve must accept verbatim.
	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	var stdout, stderr bytes.Buffer
	code := orchestrator.RunWithDepsRoot(context.Background(), absSensorPath, proj, testfixtures.RepoSchemasDir(t), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
	}
	// Aggregate is the last JSONL line on stdout.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		t.Fatalf("no stdout; stderr=%s", stderr.String())
	}
	lines := strings.Split(out, "\n")
	var agg map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &agg); err != nil {
		t.Fatalf("decode aggregate: %v; raw=%q", err, lines[len(lines)-1])
	}
	if got, _ := agg["sensor_id"].(string); got != "abs-path-target" {
		t.Errorf("aggregate.sensor_id = %q, want %q", got, "abs-path-target")
	}
}
```

If the test file already imports the required packages, no further imports needed. If not, add: `"bytes"`, `"context"`, `"encoding/json"`, `"os"`, `"path/filepath"`, `"strings"`, `"testing"`, plus `"github.com/iurykrieger/harness-framework/lib/orchestrator"` and `"github.com/iurykrieger/harness-framework/lib/testfixtures"`.

(If the test file is already package `orchestrator_test`, the `orchestrator.` prefix is required. If it's package `orchestrator`, call `RunWithDepsRoot` unqualified.)

- [ ] **Step 7: Run the new test**

```bash
go test -race -count=1 ./lib/orchestrator/... -run TestRunWithDepsRoot_AcceptsAbsolutePath -v
```

Expected: PASS.

- [ ] **Step 8: Full orchestrator suite + vet**

```bash
go test -race -count=1 ./lib/orchestrator/...
go vet ./lib/orchestrator/...
```

Both exit 0.

- [ ] **Step 9: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
fix(orchestrator): RunWithDepsRoot calls sensor.Resolve (#28 prereq)

PRs #29 and #30 introduced sensor.Resolve(idOrPath, projectRoot) and
migrated several callers, but RunWithDepsRoot still hardcoded
filepath.Join(projectRoot, ".harness", "sensors", id+".json"). That
shape rejects absolute paths (filepath.Join strips the leading slash
and treats the components as relative), so out-of-project sensor
files cannot be passed through the runner.

Three-line completion mirrors what run-computational.go:76 already
does on the read step. Regression test asserts an absolute sensor
path resolves and produces an aggregate Signal.

Prerequisite for the new replay-fixture script (#28), which spawns
the runner with an os.CreateTemp-shaped sensor path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 1B: Runner honors HARNESS_REGISTRY_ROOT

`run-computational.go::main` and `run-inferential.go::main` currently call `os.Getwd()` directly and pass that as `projectRoot` to `RunWithDepsRoot`. The `HARNESS_REGISTRY_ROOT` env var is only consulted by `registry.Lookup`, which the runner does not call. So setting the env var in `replay-fixture.go` has no effect on the runner's persistence path. Replace `os.Getwd()` with `registry.Discover(cwd)` so the env var works.

**Files:**
- Modify: `skills/run-sensor/scripts/run-computational.go` (the `main` function)
- Modify: `skills/run-sensor/scripts/run-inferential.go` (the `main` function — same shape, parity)
- Modify: `skills/run-sensor/scripts/run-computational_test.go` (regression test)

- [ ] **Step 1: Inspect the current `main` in each runner**

```bash
grep -n "func main\|os.Getwd\|registry.Discover\|registry.Lookup" skills/run-sensor/scripts/run-computational.go skills/run-sensor/scripts/run-inferential.go
```

Expected for both files: `main()` calls `cwd, _ := os.Getwd()` then passes `cwd` to `run(...)` — no `registry.Discover` or `registry.Lookup` call.

- [ ] **Step 2: Edit `run-computational.go::main`**

Use Edit. `old_string`:
```go
func main() {
	cwd, _ := os.Getwd()
	os.Exit(run(os.Args[1:], cwd, os.Stdout, os.Stderr))
}
```

`new_string`:
```go
func main() {
	cwd, _ := os.Getwd()
	// registry.Discover prefers HARNESS_REGISTRY_ROOT over walk-up, so an
	// operator can redirect the runner's runtime persistence by setting
	// that env var. When discovery fails (no marker, no env var), fall
	// back to cwd — preserves the pre-existing behavior for callers that
	// run inside a project.
	projectRoot, _, err := registry.Discover(cwd)
	if err != nil {
		projectRoot = cwd
	}
	os.Exit(run(os.Args[1:], projectRoot, os.Stdout, os.Stderr))
}
```

- [ ] **Step 3: Add the `registry` import to `run-computational.go` if missing**

```bash
grep -n "lib/registry" skills/run-sensor/scripts/run-computational.go
```

If absent, add `"github.com/iurykrieger/harness-framework/lib/registry"` to the import block. Build to verify:
```bash
go build -tags=run_computational ./skills/run-sensor/scripts
```

- [ ] **Step 4: Apply the same change to `run-inferential.go::main`**

Same `old_string` / `new_string` pattern (the inferential runner's `main` has the identical shape). Same import addition if needed. Build:
```bash
go build -tags=run_inferential ./skills/run-sensor/scripts
```

- [ ] **Step 5: Add a regression test for the computational runner**

Create or extend `skills/run-sensor/scripts/run-computational_test.go`. If the file already exists, append. Test body:

```go
func TestRunComputational_HarnessRegistryRootRedirectsPersistence(t *testing.T) {
	// Set the env var to an isolated tempdir. The runner should use this
	// as its project root, NOT the cwd that invoked it.
	tempRoot := t.TempDir()
	t.Setenv("HARNESS_REGISTRY_ROOT", tempRoot)

	// Materialize a minimal sensor at the canonical location inside tempRoot.
	if err := os.MkdirAll(filepath.Join(tempRoot, ".harness", "sensors"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := testfixtures.ValidSensorComputational()
	s["id"] = "redirect-test"
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempRoot, ".harness", "sensors", "redirect-test.json"), body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Invoke main()'s logic directly: build the projectRoot the same way
	// main() does.
	cwd := t.TempDir() // some unrelated cwd
	projectRoot, _, err := registry.Discover(cwd)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if projectRoot != tempRoot {
		// EvalSymlinks may have resolved tempRoot; compare resolved values.
		want, _ := filepath.EvalSymlinks(tempRoot)
		got, _ := filepath.EvalSymlinks(projectRoot)
		if got != want {
			t.Fatalf("Discover returned %q, want %q (HARNESS_REGISTRY_ROOT must win over cwd)", projectRoot, tempRoot)
		}
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"redirect-test"}, projectRoot, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run exit=%d, stderr=%s", code, stderr.String())
	}

	// Runtime artifacts must be under tempRoot, NOT under cwd.
	if _, err := os.Stat(filepath.Join(cwd, ".harness", "runtime")); !os.IsNotExist(err) {
		t.Errorf("runtime polluted cwd: %s/.harness/runtime exists", cwd)
	}
	if _, err := os.Stat(filepath.Join(tempRoot, ".harness", "runtime", "redirect-test")); err != nil {
		t.Errorf("runtime missing in tempRoot: %v", err)
	}
}
```

Add any missing imports to the test file: `"bytes"`, `"encoding/json"`, `"os"`, `"path/filepath"`, `"testing"`, plus `"github.com/iurykrieger/harness-framework/lib/registry"` and `"github.com/iurykrieger/harness-framework/lib/testfixtures"`.

If `run-inferential_test.go` exists with a similar smoke test pattern, mirror the test there too for parity. If it doesn't have a smoke test today, skip — keeping parity is nice but not blocking.

- [ ] **Step 6: Run the new test**

```bash
go test -race -count=1 -tags=run_computational ./skills/run-sensor/scripts/... -run TestRunComputational_HarnessRegistryRootRedirectsPersistence -v
```

Expected: PASS.

- [ ] **Step 7: Full runner suite + vet**

```bash
go test -race -count=1 -tags=run_computational ./skills/run-sensor/scripts/...
go test -race -count=1 -tags=run_inferential ./skills/run-sensor/scripts/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
```

All exit 0.

- [ ] **Step 8: Commit**

```bash
git add skills/run-sensor/scripts/run-computational.go \
        skills/run-sensor/scripts/run-inferential.go \
        skills/run-sensor/scripts/run-computational_test.go
git commit -m "$(cat <<'EOF'
fix(run-sensor): main honors HARNESS_REGISTRY_ROOT via registry.Discover (#28 prereq)

run-computational.go::main and run-inferential.go::main previously
passed os.Getwd() straight through as projectRoot. The env var was
documented as the operator's override for registry root, but only
the registry-touching skills (start/stop/list/tail) actually
consulted it via registry.Lookup. The runner ignored it.

Replace os.Getwd() with registry.Discover(cwd), with fallback to cwd
when discovery fails (preserves pre-existing behavior for the
in-project happy path). Regression test asserts that setting
HARNESS_REGISTRY_ROOT redirects the runner's .harness/runtime/
artifacts away from cwd.

Prerequisite for the new replay-fixture script (#28), which sets the
env var to an ephemeral tempdir to isolate verification runs from
the project tree.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Failing test — replay-fixture preserves sensor.id and isolates the runtime root

TDD red phase. The test asserts the two load-bearing behaviors of the to-be-built script: (a) the aggregate Signal carries the original `sensor_id` (no `replay-` prefix), and (b) running the script does NOT create a `.harness/runtime/<sensor.id>/` directory under the project root.

**Files:**
- Create: `skills/detect-sensors/scripts/replay-fixture_test.go`

- [ ] **Step 1: Create the failing test**

Write `skills/detect-sensors/scripts/replay-fixture_test.go` with EXACTLY this content:

```go
//go:build replay_fixture

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

// uniqueSensorID returns a schema-valid id ([a-z][a-z0-9-]*) that no
// previous test run could have created — used to assert "the real repo's
// .harness/runtime/ has no entry by this name after the script runs."
func uniqueSensorID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// writeTempSensor materializes a computational sensor JSON file with the
// given id. Returns the absolute file path.
func writeTempSensor(t *testing.T, id string) string {
	t.Helper()
	s := testfixtures.ValidSensorComputational()
	s["id"] = id
	body, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal sensor: %v", err)
	}
	path := filepath.Join(t.TempDir(), "sensor.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write sensor: %v", err)
	}
	return path
}

// writeTempFixture writes content to a tempfile and returns its absolute path.
func writeTempFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// repoRootDir resolves the harness-framework repo root from cwd, mirroring
// the script's own repoRoot() helper. Used to assert pollution did not
// land in the project's .harness/runtime/ tree.
func repoRootDir(t *testing.T) string {
	t.Helper()
	got := repoRoot()
	if got == "" {
		t.Fatal("repoRoot() returned empty; test cannot locate project root")
	}
	return got
}

func TestReplayFixture_PreservesSensorIDAndIsolatesRuntime(t *testing.T) {
	id := uniqueSensorID("replay-iso")
	sensorPath := writeTempSensor(t, id)
	fixturePath := writeTempFixture(t, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", sensorPath, "--fixture", fixturePath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}

	// The aggregate Signal is the last line of stdout (JSONL). Decode it.
	out := strings.TrimSpace(stdout.String())
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		t.Fatalf("no stdout; stderr=%s", stderr.String())
	}
	var agg map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &agg); err != nil {
		t.Fatalf("decode aggregate: %v; raw=%q", err, lines[len(lines)-1])
	}
	if got, _ := agg["sensor_id"].(string); got != id {
		t.Fatalf("aggregate.sensor_id = %q, want %q (no replay- prefix)", got, id)
	}

	// Runtime isolation: the project's .harness/runtime/<id>/ MUST NOT
	// exist. The script set HARNESS_REGISTRY_ROOT to a tempdir for the
	// runner; any artifacts went there and were removed on exit.
	polluted := filepath.Join(repoRootDir(t), ".harness", "runtime", id)
	if _, err := os.Stat(polluted); !os.IsNotExist(err) {
		t.Fatalf("runtime pollution: %s exists (stat err=%v)", polluted, err)
	}
}
```

- [ ] **Step 2: Run the test — it must fail to compile (red phase)**

```bash
go test -tags=replay_fixture ./skills/detect-sensors/scripts/... -run TestReplayFixture_PreservesSensorIDAndIsolatesRuntime
```

Expected: build failure with `undefined: run` and `undefined: repoRoot`.

- [ ] **Step 3: Do not commit yet.**

Task 3 will create `replay-fixture.go` and commit both files together.

---

## Task 3: Implement replay-fixture.go to make Task 2's test pass

Green phase. Write the minimal production code that makes the test pass.

**Files:**
- Create: `skills/detect-sensors/scripts/replay-fixture.go`

- [ ] **Step 1: Write the implementation**

Write `skills/detect-sensors/scripts/replay-fixture.go` with EXACTLY this content:

```go
//go:build replay_fixture

// Command replay-fixture runs a sensor against a fixture file without
// polluting the project's .harness/runtime/ tree. It loads the sensor
// JSON, overrides execution.command to "cat <fixture>", and invokes
// the runner (run-computational | run-inferential) with
// HARNESS_REGISTRY_ROOT pointed at an ephemeral tempdir. The runner's
// stdout/stderr stream through; the temp tree is removed on exit.
//
// The sensor.id field is preserved verbatim — earlier versions of this
// step (a shell snippet in skills/detect-sensors/SKILL.md) mutated id
// to "replay-" + id, which leaked into .harness/runtime/replay-<id>/
// once runtime persistence shipped (issue #28).
//
// Usage:
//
//	go run -tags=replay_fixture ./skills/detect-sensors/scripts \
//	  --sensor=PATH --fixture=PATH
//
// Exit codes: same as the underlying runner. 2 on usage or I/O error
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
	tag := tagForType(sensorType)
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

// tagForType selects the runner's build tag based on sensor.type.
// Defaults to run_computational when the field is empty or unknown —
// the schema's discriminator enforces the inferential branch, so
// any non-"inferential" value the runner sees will fail validation
// downstream regardless of our pick here.
func tagForType(sensorType string) string {
	if sensorType == "inferential" {
		return "run_inferential"
	}
	return "run_computational"
}

// repoRoot walks up from cwd looking for a directory that contains
// both go.mod and skills/run-sensor/scripts (the runner package).
// Mirrors skills/heal-sensor/scripts/retry-original.go::repoRoot
// — duplicated rather than coupled per project rule #4. Returns
// "" if no ancestor matches; the caller may fall back to cwd.
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

- [ ] **Step 2: Run the Task 2 test — it must pass**

```bash
go test -tags=replay_fixture ./skills/detect-sensors/scripts/... -run TestReplayFixture_PreservesSensorIDAndIsolatesRuntime -v
```

Expected: `--- PASS: TestReplayFixture_PreservesSensorIDAndIsolatesRuntime`. The test takes several seconds because the subprocess shells out to `go run -tags=run_computational ./skills/run-sensor/scripts`, which compiles the runner.

- [ ] **Step 3: Vet the tagged build**

```bash
go vet -tags=replay_fixture ./...
```

Expected: no output, exit 0.

- [ ] **Step 4: Commit both the test (uncommitted from Task 2) and the new production code**

```bash
git add skills/detect-sensors/scripts/replay-fixture.go skills/detect-sensors/scripts/replay-fixture_test.go
git commit -m "$(cat <<'EOF'
feat(detect-sensors): replay-fixture.go preserves sensor.id (#28)

New script under //go:build replay_fixture extracts the fixture-replay
verification step out of SKILL.md prose. It overrides
execution.command to "cat <fixture>" but leaves sensor.id untouched,
and points HARNESS_REGISTRY_ROOT at an ephemeral tempdir so the
runner's .harness/runtime/<id>/<run-id>/ persistence never lands in
the project tree.

Test asserts both invariants: the aggregate sensor_id has no replay-
prefix, and the project's .harness/runtime/<id>/ is absent after exit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Error-case tests + tagForType unit test

Cover argument-parsing failures, malformed inputs, and the tag selection branch.

**Files:**
- Modify: `skills/detect-sensors/scripts/replay-fixture_test.go` (append cases)

- [ ] **Step 1: Append the error-case tests + tagForType test**

Append these functions to the end of `replay-fixture_test.go`:

```go
func TestReplayFixture_MissingFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"both missing", []string{}},
		{"only sensor", []string{"--sensor", "/tmp/x.json"}},
		{"only fixture", []string{"--fixture", "/tmp/y.txt"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "usage:") {
				t.Fatalf("stderr lacks usage hint: %s", stderr.String())
			}
		})
	}
}

func TestReplayFixture_SensorNotJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("this is not json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fixturePath := writeTempFixture(t, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", path, "--fixture", fixturePath}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "parse sensor") {
		t.Fatalf("stderr lacks parse error: %s", stderr.String())
	}
}

func TestReplayFixture_SensorMissingExecution(t *testing.T) {
	bad := map[string]interface{}{
		"id":   "no-execution-block",
		"type": "computational",
		// no "execution" field
	}
	body, _ := json.Marshal(bad)
	sensorPath := filepath.Join(t.TempDir(), "no-exec.json")
	if err := os.WriteFile(sensorPath, body, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fixturePath := writeTempFixture(t, "")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", sensorPath, "--fixture", fixturePath}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "execution") {
		t.Fatalf("stderr does not name 'execution': %s", stderr.String())
	}
}

func TestReplayFixture_SensorFileMissing(t *testing.T) {
	fixturePath := writeTempFixture(t, "")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", "/nonexistent/sensor.json", "--fixture", fixturePath}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "read sensor") {
		t.Fatalf("stderr lacks read error: %s", stderr.String())
	}
}

func TestTagForType(t *testing.T) {
	cases := map[string]string{
		"computational": "run_computational",
		"inferential":   "run_inferential",
		"":              "run_computational",
		"unknown":       "run_computational",
	}
	for input, want := range cases {
		if got := tagForType(input); got != want {
			t.Errorf("tagForType(%q) = %q, want %q", input, got, want)
		}
	}
}
```

- [ ] **Step 2: Run all replay-fixture tests**

```bash
go test -tags=replay_fixture ./skills/detect-sensors/scripts/... -v
```

Expected: all tests pass. Only `TestReplayFixture_PreservesSensorIDAndIsolatesRuntime` shells out; the rest are fast.

- [ ] **Step 3: Commit**

```bash
git add skills/detect-sensors/scripts/replay-fixture_test.go
git commit -m "$(cat <<'EOF'
test(detect-sensors): cover replay-fixture error paths (#28)

Adds tests for missing-flags, malformed JSON, missing execution
block, missing sensor file, and the tagForType selector. These do
not shell out — they exercise the pre-spawn validation path only.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Replace the replay snippet in SKILL.md + add migration note

Swap the broken shell snippet (currently at `skills/detect-sensors/SKILL.md:357–363`) for an invocation of the new Go script, and add a migration note covering pre-#30 (`.runtime/sensors/replay-*`) and pre-#28 (`.harness/runtime/replay-*`) pollution.

**Files:**
- Modify: `skills/detect-sensors/SKILL.md` (lines 357–363, plus a paragraph insertion near line 370)

- [ ] **Step 1: Verify the current snippet**

```bash
sed -n '355,365p' skills/detect-sensors/SKILL.md
```

Expected: lines 357–363 contain the shell snippet that mutates `.id` via `jq`.

- [ ] **Step 2: Replace the snippet**

Use Edit. `old_string` (exact):
```
# 2) Replay each fail/warn fixture to prove the unhappy paths.
TMP=$(mktemp /tmp/replay-XXXX.json)
jq --arg cmd "cat .harness/sensors/fixtures/<group>/<case>.txt" \
   '.execution.command = $cmd | .id = "replay-" + .id' \
   .harness/sensors/<id>.json > "$TMP"
go run -tags=run_computational ./skills/run-sensor/scripts "$TMP" | tail -n 1 \
  | jq -c '{verdict, severity, individuals: (.evidence|length)}'
rm "$TMP"
```

`new_string`:
```
# 2) Replay each fail/warn fixture to prove the unhappy paths.
#    The Go script preserves sensor.id and isolates the runner's
#    runtime persistence via an ephemeral HARNESS_REGISTRY_ROOT,
#    so this never writes to the project's .harness/runtime/ tree.
go run -tags=replay_fixture ./skills/detect-sensors/scripts \
  --sensor=.harness/sensors/<id>.json --fixture=.harness/sensors/fixtures/<group>/<case>.txt \
  | tail -n 1 | jq -c '{verdict, severity, individuals: (.evidence|length)}'
```

- [ ] **Step 3: Verify the replacement**

```bash
grep -n "replay-fixture\|jq --arg cmd \"cat .harness/sensors/fixtures\|.id = \"replay-\" + .id" skills/detect-sensors/SKILL.md
```

Expected: matches only on the new `replay-fixture` reference; no occurrences of the old jq mutation.

- [ ] **Step 4: Add the migration note**

Find the paragraph that begins (around line 372 post-replacement):

> If iteration changes `output`, `execution`, or `verification`, bump the sensor `version` (e.g. `0.1.0` → `0.2.0`) and re-persist via the validator. The version stamp is the audit trail of which shape was actually verified.

Append a new paragraph IMMEDIATELY AFTER that one (use Edit with a unique anchor from the existing text):

```
**Migration note for projects upgraded from a pre-#28 plugin version:** if your project has `.runtime/sensors/replay-*` directories from a pre-PR-#30 plugin or `.harness/runtime/replay-*` directories from a pre-PR-#28 plugin, remove them once with `rm -rf .runtime/sensors/replay-* .harness/runtime/replay-*`. The new `replay-fixture` script does not regenerate them.
```

- [ ] **Step 5: Verify the migration note landed**

```bash
grep -n "Migration note for projects upgraded" skills/detect-sensors/SKILL.md
```

Expected: one match.

- [ ] **Step 6: Commit**

```bash
git add skills/detect-sensors/SKILL.md
git commit -m "$(cat <<'EOF'
docs(detect-sensors): replace replay snippet with Go script (#28)

Step 7's fixture-replay block now calls
  go run -tags=replay_fixture ./skills/detect-sensors/scripts ...
in place of the previous jq-based id-mutating shell snippet.

Migration note added covering both pre-#30 (.runtime/sensors/replay-*)
and pre-#28 (.harness/runtime/replay-*) pollution paths.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Extend CI matrix for the new build tag

The current `.github/workflows/test.yml` only runs `run_computational` and `run_inferential` tag matrices. Without an explicit `replay_fixture` step, acceptance criterion #3 is not enforced by CI. The `write_sensor` and `write_stack` matrices may already exist post-#30; check first.

**Files:**
- Modify: `.github/workflows/test.yml` (append jobs)

- [ ] **Step 1: Inspect current CI matrix**

```bash
grep -n "tags=" .github/workflows/test.yml
```

Note which tags are already covered. After this task, the matrix MUST include `replay_fixture` for both `go vet` and `go test`. If `write_sensor` and `write_stack` jobs are missing too (they shipped in PR #30 but CI may not have caught up), add them in the same edit.

- [ ] **Step 2: Add the `replay_fixture` CI steps**

Use Edit to append the two steps below right after the existing `Test inferential runner` step:

```yaml
      - name: Vet (replay_fixture build tag)
        run: go vet -tags=replay_fixture ./...

      - name: Test replay-fixture (replay_fixture build tag)
        run: go test -race -count=1 -tags=replay_fixture ./skills/detect-sensors/scripts/...
```

If `write_sensor` and `write_stack` jobs are missing, add the equivalent four-step pattern for them in the same Edit, mirroring the inferential layout (vet step, then test step, for each tag).

- [ ] **Step 3: Sanity-check YAML structure**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/test.yml'))" && echo OK
```

Expected: `OK`. If PyYAML is absent, fall back to visual inspection of indentation.

- [ ] **Step 4: Simulate every new CI step locally**

```bash
go vet -tags=replay_fixture ./...
go test -race -count=1 -tags=replay_fixture ./skills/detect-sensors/scripts/...
```

(Plus the `write_sensor` / `write_stack` variants if you added them.) Each exits 0.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/test.yml
git commit -m "$(cat <<'EOF'
ci: cover replay_fixture build tag (#28)

Adds vet + test steps to the test workflow so the new tag-gated
build is enforced on every PR. Mirrors the existing
run_computational and run_inferential matrix.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Full verification matrix + cleanup of legacy replay-* directories

End-to-end check that all acceptance criteria from the spec are met, plus an opportunistic cleanup of pre-existing pollution in the current working tree.

- [ ] **Step 1: Run the full matrix that CI will run**

```bash
go test -race -count=1 ./lib/...
go test -race -count=1 -tags=run_computational ./skills/...
go test -race -count=1 -tags=run_inferential ./skills/...
go test -race -count=1 -tags=replay_fixture ./skills/detect-sensors/scripts/...
go vet ./...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
go vet -tags=replay_fixture ./...
```

Expected: every command exits 0.

- [ ] **Step 2: Smoke the new script against a real sensor**

```bash
go run -tags=replay_fixture ./skills/detect-sensors/scripts \
  --sensor=.harness/sensors/validate-plugin-manifest.json \
  --fixture=.harness/sensors/fixtures/validate-plugin-manifest/valid.json \
  | tail -n 1 | jq -c '{verdict, severity, sensor_id}'
```

Expected output (one JSON line):
```json
{"verdict":"pass","severity":"info","sensor_id":"validate-plugin-manifest"}
```

The `sensor_id` MUST be `validate-plugin-manifest` (no `replay-` prefix). Acceptance criterion #2.

- [ ] **Step 3: Verify acceptance criterion #1 (no pollution)**

```bash
find .harness/runtime -maxdepth 1 -type d -name "replay-*" 2>/dev/null
```

Expected: NO output. The smoke run in Step 2 did not create any `replay-*` directory.

- [ ] **Step 4: Clean up legacy pollution from previous plugin versions**

The current working tree may still contain `.runtime/sensors/replay-*` (from pre-#30 plugin runs) or `.harness/runtime/replay-*` (from pre-#28 runs). Per the migration note in SKILL.md:

```bash
ls .runtime/sensors/ 2>/dev/null | grep '^replay-' | head -5
ls .harness/runtime/ 2>/dev/null | grep '^replay-' | head -5
rm -rf .runtime/sensors/replay-* .harness/runtime/replay-* 2>/dev/null
find .runtime/sensors -maxdepth 1 -type d -name "replay-*" 2>/dev/null | wc -l
find .harness/runtime -maxdepth 1 -type d -name "replay-*" 2>/dev/null | wc -l
```

Both counts must be `0` after the `rm -rf`. `.runtime/` and `.harness/runtime/` are in `.gitignore`, so no git diff results from this step.

- [ ] **Step 5: Verify the working tree is clean**

```bash
git status
```

Expected: nothing to commit.

- [ ] **Step 6: Inspect the commit graph**

```bash
git log --oneline -8
```

Expected (top to bottom):
1. ci: cover replay_fixture build tag (#28)
2. docs(detect-sensors): replace replay snippet with Go script (#28)
3. test(detect-sensors): cover replay-fixture error paths (#28)
4. feat(detect-sensors): replay-fixture.go preserves sensor.id (#28)
5. fix(orchestrator): RunWithDepsRoot calls sensor.Resolve (#28 prereq)
6. docs(spec): v3 — rebase on PR #29/#30 + RunWithDepsRoot completion (#28)
7. docs(plan): replay-fixture runtime isolation (#28)
8. (the two earlier spec commits)

Each task commit can be reverted independently.

---

## Self-Review

**Spec coverage (cross-reference to v3 spec):**

| Spec section | Covered by |
|---|---|
| What changes #1: `RunWithDepsRoot` migration | Task 1 |
| What changes #2: New `replay-fixture.go` | Task 3 |
| What changes #3: New `replay-fixture_test.go` | Tasks 2, 4 |
| What changes #4: Replace SKILL.md lines 357–363 + migration note | Task 5 |
| What changes #5–#8: No-change-to clauses | Implicit — no task touches those files |
| Acceptance criterion #1: no `.harness/runtime/replay-*` after a cycle | Task 7 Step 3 |
| Acceptance criterion #2: aggregate `sensor_id` has no `replay-` prefix | Task 2 (unit test) + Task 7 Step 2 (smoke) |
| Acceptance criterion #3: `go test -tags=replay_fixture` passes | Task 3 Step 2, Task 4 Step 2, Task 6 Step 4, Task 7 Step 1 |
| Acceptance criterion #4: `go test ./lib/orchestrator/...` passes | Task 1 Step 8, Task 7 Step 1 |
| Acceptance criterion #5: `go vet -tags=replay_fixture ./...` passes | Task 3 Step 3, Task 6 Step 4, Task 7 Step 1 |
| Acceptance criterion #6: `go vet ./...` (default) passes | Task 1 Step 5, Task 7 Step 1 |
| Acceptance criterion #7: end-to-end smoke produces same shape | Task 7 Step 2 |

**Placeholder scan:** no `TBD`, no `TODO`, no "implement later", no "similar to Task N", no "add appropriate error handling". Every code block has complete, runnable code. Every command has expected output.

**Type/signature consistency:**
- `run(args []string, stdout, stderr io.Writer) int` — same signature in `replay-fixture.go` and `replay-fixture_test.go`.
- `repoRoot() string` — same signature in script and test helper `repoRootDir`.
- `tagForType(sensorType string) string` — unexported (package-internal); tested directly.
- Test helper names (`uniqueSensorID`, `writeTempSensor`, `writeTempFixture`, `repoRootDir`) are consistent between Task 2 and Task 4 usage.
- The build tag string `replay_fixture` is consistent across all files and CI steps.
- The new test in Task 1 uses `testfixtures.RepoSchemasDir(t)` (an existing helper in `lib/testfixtures/paths.go`).
