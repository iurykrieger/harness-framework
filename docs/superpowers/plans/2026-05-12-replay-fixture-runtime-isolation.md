# Replay-fixture runtime isolation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate `.runtime/sensors/replay-*` directory pollution caused by `/detect-sensors` step 7's fixture-replay snippet, by extracting the verification into a Go script that preserves `sensor.id` and routes the runner's runtime root to an ephemeral tempdir.

**Architecture:** New script `skills/detect-sensors/scripts/replay-fixture.go` (build tag `replay_fixture`) — thin CLI that reads a sensor JSON, overrides only `execution.command` to `cat <fixture>`, and invokes the runner with `HARNESS_REGISTRY_ROOT=$(mktemp -d)` so persistence lands in a tempdir that is removed on exit. The existing `skills/detect-sensors/scripts/write-sensor.go` gains its own build tag `write_sensor` so the two `package main` files coexist (same pattern as `skills/heal-sensor/scripts/` and `skills/run-sensor/scripts/`).

**Tech Stack:** Go 1.25.3, `flag`, `encoding/json`, `os/exec`. Reuses `lib/testfixtures` for sensor fixtures in tests. No new dependencies.

**Spec reference:** [`docs/superpowers/specs/2026-05-12-replay-fixture-runtime-isolation-design.md`](../specs/2026-05-12-replay-fixture-runtime-isolation-design.md). Tracks issue [#28](https://github.com/iurykrieger/harness-framework/issues/28).

---

## File Structure

**Modify:**
- `skills/detect-sensors/scripts/write-sensor.go` — add `//go:build write_sensor` header; update docstring example.
- `skills/detect-sensors/scripts/write-sensor_test.go` — add `//go:build write_sensor` header.
- `skills/detect-sensors/SKILL.md` — replace line 245 (write-sensor invocation) with tag-qualified form; replace lines 286–292 (replay snippet) with new Go script invocation; add migration note.
- `.github/workflows/test.yml` — extend CI matrix with `write_sensor` and `replay_fixture` tag jobs.

**Create:**
- `skills/detect-sensors/scripts/replay-fixture.go` — the new script.
- `skills/detect-sensors/scripts/replay-fixture_test.go` — table-driven tests.

Each file has one responsibility: `replay-fixture.go` is the CLI/orchestration glue between a sensor file, a fixture file, and the runner. Tests live next to their target per project rule #4.

---

## Task 1: Tag the existing write-sensor.go scripts

This is the foundation: without disjoint build tags, the new sibling file in the same `package main` directory will fail to compile. No behavior change; pure tag migration.

**Files:**
- Modify: `skills/detect-sensors/scripts/write-sensor.go` (lines 1, 5–9)
- Modify: `skills/detect-sensors/scripts/write-sensor_test.go` (line 1)

- [ ] **Step 1: Verify current state of both files**

Run:
```bash
head -3 skills/detect-sensors/scripts/write-sensor.go skills/detect-sensors/scripts/write-sensor_test.go
```

Expected first line of each file:
```
// Command write-sensor reads a draft sensor JSON file and persists it
```
(write-sensor.go)
```
package main
```
(write-sensor_test.go — line 1 is `package main`; no `//go:build` directive).

- [ ] **Step 2: Verify the current test suite passes without a tag (baseline)**

Run:
```bash
go test ./skills/detect-sensors/scripts/...
```

Expected: PASS (tests currently compile under the default build because there's no tag gating them).

- [ ] **Step 3: Add `//go:build write_sensor` header to write-sensor.go**

Use Edit to prepend the build tag and a blank line ABOVE the existing first line. The first 12 lines should become:

```go
//go:build write_sensor

// Command write-sensor reads a draft sensor JSON file and persists it
// via lib/sensor.ValidateAndPersist (validate against schemas + atomic
// write). Thin CLI wrapper around the shared primitive.
//
// Usage:
//
//	go run -tags=write_sensor ./skills/detect-sensors/scripts \
//	  --out=<dir> [--schemas-dir=<dir>] <draft-sensor.json>
//
// Exit codes: 0 sensor written, 1 schema validation failed,
// 2 usage or I/O error.
package main
```

Two edits in this step:
1. Prepend `//go:build write_sensor\n\n` to the very top of the file.
2. Update line 7's docstring example from `go run ./skills/detect-sensors/scripts \` to `go run -tags=write_sensor ./skills/detect-sensors/scripts \`.

- [ ] **Step 4: Add `//go:build write_sensor` header to write-sensor_test.go**

Prepend to the top of the file:

```go
//go:build write_sensor

package main
```

The original `package main` line stays; only the two new lines (build tag + blank) are prepended.

- [ ] **Step 5: Verify the default build no longer sees these files**

Run:
```bash
go test ./skills/detect-sensors/scripts/...
```

Expected: `?   github.com/iurykrieger/harness-framework/skills/detect-sensors/scripts  [no test files]`. The package compiles to nothing because both `.go` files in it are tag-gated and the tag is not set.

- [ ] **Step 6: Verify the tagged build sees them and tests pass**

Run:
```bash
go test -tags=write_sensor ./skills/detect-sensors/scripts/...
```

Expected: PASS, with the same test count as the baseline in Step 2.

Run:
```bash
go vet -tags=write_sensor ./...
```

Expected: no output, exit 0.

- [ ] **Step 7: Verify the un-tagged invocation still works for the user-facing example**

This confirms the SKILL.md edit in Task 2 will be drop-in:
```bash
go run -tags=write_sensor ./skills/detect-sensors/scripts --help 2>&1 | head -3 || true
```
Expected: usage text emitted via stderr (mentions `--out`).

- [ ] **Step 8: Commit**

```bash
git add skills/detect-sensors/scripts/write-sensor.go skills/detect-sensors/scripts/write-sensor_test.go
git commit -m "$(cat <<'EOF'
refactor(detect-sensors): tag write-sensor for coexistence (#28)

Add //go:build write_sensor to write-sensor.go and its test. Same
pattern as skills/heal-sensor/scripts/ and skills/run-sensor/scripts/.
No behavior change. The user-facing invocation gains a -tags=write_sensor
flag (also reflected in the docstring example).

Prerequisite for the new replay-fixture.go sibling.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Update the live SKILL.md caller of write-sensor

Update the user-facing invocation in `skills/detect-sensors/SKILL.md:245` so the documented command works after Task 1.

**Files:**
- Modify: `skills/detect-sensors/SKILL.md` (line 245)

- [ ] **Step 1: Verify the current line**

Run:
```bash
sed -n '244,248p' skills/detect-sensors/SKILL.md
```

Expected:
```
```bash
go run ./skills/detect-sensors/scripts \
  --out=<project>/sensors \
  /tmp/<draft-name>.json
```
```

- [ ] **Step 2: Edit line 245 to add `-tags=write_sensor`**

Change:
```
go run ./skills/detect-sensors/scripts \
```
to:
```
go run -tags=write_sensor ./skills/detect-sensors/scripts \
```

(Use Edit with `old_string: "go run ./skills/detect-sensors/scripts \\\n  --out=<project>/sensors \\\n  /tmp/<draft-name>.json"` so the match is unambiguous.)

- [ ] **Step 3: Verify the edit applied**

Run:
```bash
grep -n "go run -tags=write_sensor ./skills/detect-sensors/scripts" skills/detect-sensors/SKILL.md
```

Expected: one match on (now) line 245.

- [ ] **Step 4: Verify no other lines in SKILL.md still reference the bare invocation of detect-sensors scripts**

Run:
```bash
grep -n "go run ./skills/detect-sensors/scripts" skills/detect-sensors/SKILL.md
```

Expected: no matches.

- [ ] **Step 5: Commit**

```bash
git add skills/detect-sensors/SKILL.md
git commit -m "$(cat <<'EOF'
docs(detect-sensors): tag-qualify write-sensor invocation in SKILL.md (#28)

Mirrors the build-tag added to write-sensor.go in the prior commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Failing test — replay-fixture preserves sensor.id and isolates the runtime root

TDD red phase. The test asserts the two load-bearing behaviors of the script: (a) the aggregate Signal carries the original `sensor_id` (no `replay-` prefix), and (b) running the script does NOT create a `.runtime/sensors/<sensor.id>/` directory under the project root.

**Files:**
- Create: `skills/detect-sensors/scripts/replay-fixture_test.go`

- [ ] **Step 1: Create the failing test**

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
// .runtime/sensors/ has no entry by this name after the script runs."
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
// land in the project's .runtime/sensors/ tree.
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

	// Runtime isolation: the project's .runtime/sensors/<id>/ MUST NOT
	// exist. The script set HARNESS_REGISTRY_ROOT to a tempdir for the
	// runner; any artifacts went there and were removed on exit.
	polluted := filepath.Join(repoRootDir(t), ".runtime", "sensors", id)
	if _, err := os.Stat(polluted); !os.IsNotExist(err) {
		t.Fatalf("runtime pollution: %s exists (stat err=%v)", polluted, err)
	}
}
```

- [ ] **Step 2: Run the test — it must fail (red phase)**

Run:
```bash
go test -tags=replay_fixture ./skills/detect-sensors/scripts/... -run TestReplayFixture_PreservesSensorIDAndIsolatesRuntime
```

Expected: build failure with `undefined: run` and `undefined: repoRoot`. That confirms the test is exercising the not-yet-written script.

---

## Task 4: Implement replay-fixture.go to make Task 3's test pass

Green phase. Write the minimal production code that makes the test pass.

**Files:**
- Create: `skills/detect-sensors/scripts/replay-fixture.go`

- [ ] **Step 1: Write the implementation**

```go
//go:build replay_fixture

// Command replay-fixture runs a sensor against a fixture file without
// polluting the project's .runtime/sensors/ tree. It loads the sensor
// JSON, overrides execution.command to "cat <fixture>", and invokes
// the runner (run-computational | run-inferential) with
// HARNESS_REGISTRY_ROOT pointed at an ephemeral tempdir. The runner's
// stdout/stderr stream through; the temp tree is removed on exit.
//
// The sensor.id field is preserved verbatim — earlier versions of this
// step (a shell snippet in skills/detect-sensors/SKILL.md) mutated id
// to "replay-" + id, which leaked into .runtime/sensors/replay-<id>/
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

- [ ] **Step 2: Run the Task 3 test — it must pass**

Run:
```bash
go test -tags=replay_fixture ./skills/detect-sensors/scripts/... -run TestReplayFixture_PreservesSensorIDAndIsolatesRuntime -v
```

Expected: `--- PASS: TestReplayFixture_PreservesSensorIDAndIsolatesRuntime`. Total runtime may be several seconds because the subprocess shells out to `go run -tags=run_computational ./skills/run-sensor/scripts`, which compiles the runner.

- [ ] **Step 3: Vet the tagged build**

Run:
```bash
go vet -tags=replay_fixture ./...
```

Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add skills/detect-sensors/scripts/replay-fixture.go skills/detect-sensors/scripts/replay-fixture_test.go
git commit -m "$(cat <<'EOF'
feat(detect-sensors): replay-fixture.go preserves sensor.id (#28)

New script under //go:build replay_fixture extracts the fixture-replay
verification step out of SKILL.md prose. It overrides
execution.command to "cat <fixture>" but leaves sensor.id untouched,
and points HARNESS_REGISTRY_ROOT at an ephemeral tempdir so the
runner's .runtime/sensors/<id>/<run-id>/ persistence never lands in
the project tree.

Test asserts both invariants: the aggregate sensor_id has no replay-
prefix, and the project's .runtime/sensors/<id>/ is absent after exit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Error-case tests + tagForType unit test

Cover argument-parsing failures, malformed inputs, and the tag selection branch.

**Files:**
- Modify: `skills/detect-sensors/scripts/replay-fixture_test.go` (append cases)

- [ ] **Step 1: Append the error-case tests + tagForType test**

Append these test functions to the end of `replay-fixture_test.go`:

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

- [ ] **Step 2: Run the new tests — they must pass**

Run:
```bash
go test -tags=replay_fixture ./skills/detect-sensors/scripts/... -v
```

Expected: every test passes. The error-case tests are fast (no subprocess); only `TestReplayFixture_PreservesSensorIDAndIsolatesRuntime` shells out.

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

## Task 6: Replace the replay snippet in SKILL.md + add migration note

Final user-facing change: swap the shell snippet (lines 286–292) for the new Go script invocation, and add a one-line note instructing users to clean up legacy `replay-*` directories once.

**Files:**
- Modify: `skills/detect-sensors/SKILL.md` (lines 280–293; insert note after line 300)

- [ ] **Step 1: Verify the current snippet**

Run:
```bash
sed -n '280,293p' skills/detect-sensors/SKILL.md
```

Expected: lines 280–293 contain the shell snippet that mutates `.id = "replay-" + .id`.

- [ ] **Step 2: Replace the snippet**

Use Edit to swap the block. `old_string` (preserve indentation exactly):

```
# 2) Replay each fail/warn fixture to prove the unhappy paths.
TMP=$(mktemp /tmp/replay-XXXX.json)
jq --arg cmd "cat sensors/fixtures/<group>/<case>.txt" \
   '.execution.command = $cmd | .id = "replay-" + .id' \
   sensors/<id>.json > "$TMP"
go run -tags=run_computational ./skills/run-sensor/scripts "$TMP" | tail -n 1 \
  | jq -c '{verdict, severity, individuals: (.evidence|length)}'
rm "$TMP"
```

`new_string`:

```
# 2) Replay each fail/warn fixture to prove the unhappy paths.
#    The Go script preserves sensor.id and isolates the runner's
#    runtime persistence via an ephemeral HARNESS_REGISTRY_ROOT,
#    so this never writes to the project's .runtime/sensors/ tree.
go run -tags=replay_fixture ./skills/detect-sensors/scripts \
  --sensor=sensors/<id>.json --fixture=sensors/fixtures/<group>/<case>.txt \
  | tail -n 1 | jq -c '{verdict, severity, individuals: (.evidence|length)}'
```

- [ ] **Step 3: Verify the replacement and that no legacy `replay-` mutation lingers**

Run:
```bash
grep -n "replay-fixture\|replay-XXXX\|\"replay-\" + .id" skills/detect-sensors/SKILL.md
```

Expected: matches reference `replay-fixture` (the new Go script) only; no occurrence of `replay-XXXX` or `"replay-" + .id`.

- [ ] **Step 4: Add migration note after step 7's existing "For each sensor…" closing prose**

Find the paragraph that begins (around line 299):
> If iteration changes `output`, `execution`, or `verification`, bump the sensor `version`…

Append a new paragraph immediately AFTER that one:

```
**Migration note for projects upgraded from a pre-#28 plugin version:** if your project already has `.runtime/sensors/replay-*` directories from previous `/detect-sensors` runs, remove them once with `rm -rf .runtime/sensors/replay-*`. The new replay-fixture script does not regenerate them.
```

Use Edit to insert this paragraph; pick a unique anchor from the existing line so the match is unambiguous.

- [ ] **Step 5: Verify the migration note landed in step 7's block**

Run:
```bash
grep -n "Migration note for projects upgraded" skills/detect-sensors/SKILL.md
```

Expected: one match somewhere between lines 295 and 320.

- [ ] **Step 6: Commit**

```bash
git add skills/detect-sensors/SKILL.md
git commit -m "$(cat <<'EOF'
docs(detect-sensors): replace replay snippet with Go script (#28)

Step 7's fixture-replay block now calls
  go run -tags=replay_fixture ./skills/detect-sensors/scripts ...
in place of the previous jq-based id-mutating shell snippet.

Also: one-line migration note instructing users on pre-#28 plugin
versions to clean up existing .runtime/sensors/replay-* directories.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Extend CI matrix for the new build tags

Without this, acceptance criteria #3–#6 (`go test -tags=replay_fixture` / `-tags=write_sensor` and the matching `go vet`s) are not enforced by CI. The existing CI only runs `run_computational` and `run_inferential` tag matrices.

**Files:**
- Modify: `.github/workflows/test.yml` (append jobs after the inferential runner section)

- [ ] **Step 1: Verify current CI matrix**

Run:
```bash
grep -n "tags=" .github/workflows/test.yml
```

Expected: matches for `run_computational` and `run_inferential` only.

- [ ] **Step 2: Add four new CI steps**

Use Edit to append the four steps below right after the existing `Test inferential runner` step (and before any "Build" steps if you prefer keeping tests-then-builds; insertion point is structural, not strict).

Append:

```yaml
      - name: Vet (write_sensor build tag)
        run: go vet -tags=write_sensor ./...

      - name: Vet (replay_fixture build tag)
        run: go vet -tags=replay_fixture ./...

      - name: Test write-sensor (write_sensor build tag)
        run: go test -race -count=1 -tags=write_sensor ./skills/detect-sensors/scripts/...

      - name: Test replay-fixture (replay_fixture build tag)
        run: go test -race -count=1 -tags=replay_fixture ./skills/detect-sensors/scripts/...
```

- [ ] **Step 3: Sanity-check YAML structure**

Run:
```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/test.yml'))" && echo OK
```

Expected: `OK`. (Python ships with PyYAML on most CI runners; if absent locally, `yamllint` or just visual inspection works — but YAML errors here would fail the next CI run loudly.)

- [ ] **Step 4: Locally simulate every new CI step**

Run all four in sequence:
```bash
go vet -tags=write_sensor ./...
go vet -tags=replay_fixture ./...
go test -race -count=1 -tags=write_sensor ./skills/detect-sensors/scripts/...
go test -race -count=1 -tags=replay_fixture ./skills/detect-sensors/scripts/...
```

Expected: each command exits 0 (the test commands print `ok` lines).

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/test.yml
git commit -m "$(cat <<'EOF'
ci: cover write_sensor and replay_fixture build tags (#28)

Adds four steps to the test workflow so the new tag-gated builds are
enforced on every PR. Mirrors the existing run_computational and
run_inferential matrix.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Full verification matrix + cleanup of legacy replay-* dirs

End-to-end check that all acceptance criteria from the spec are met, plus an opportunistic cleanup of the existing pollution in the current working tree.

- [ ] **Step 1: Run the full matrix that CI will run**

```bash
go test -race -count=1 ./lib/...
go test -race -count=1 -tags=run_computational ./skills/...
go test -race -count=1 -tags=run_inferential ./skills/...
go test -race -count=1 -tags=write_sensor ./skills/detect-sensors/scripts/...
go test -race -count=1 -tags=replay_fixture ./skills/detect-sensors/scripts/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
go vet -tags=write_sensor ./...
go vet -tags=replay_fixture ./...
```

Expected: every command exits 0.

- [ ] **Step 2: Smoke the new script against a real sensor**

Run:
```bash
go run -tags=replay_fixture ./skills/detect-sensors/scripts \
  --sensor=sensors/validate-plugin-manifest.json \
  --fixture=sensors/fixtures/validate-plugin-manifest/valid.json \
  | tail -n 1 | jq -c '{verdict, severity, sensor_id}'
```

Expected output (one JSON line):
```json
{"verdict":"pass","severity":"info","sensor_id":"validate-plugin-manifest"}
```

The `sensor_id` MUST be `validate-plugin-manifest` (no `replay-` prefix). That is acceptance criterion #2.

- [ ] **Step 3: Verify acceptance criterion #1 (no pollution)**

Run:
```bash
find .runtime/sensors -maxdepth 1 -type d -name "replay-*" 2>/dev/null
```

Expected: NO output (empty result). The smoke run in Step 2 did not create any `replay-*` directory.

- [ ] **Step 4: Clean up legacy pollution from previous plugin versions (per the migration note)**

The current working tree was populated by pre-fix runs and still contains `.runtime/sensors/replay-*` directories. This is a one-time cleanup, exactly what the migration note in SKILL.md instructs users to do:

```bash
ls .runtime/sensors/ | grep '^replay-' | head -5
rm -rf .runtime/sensors/replay-*
ls .runtime/sensors/ | grep '^replay-' | wc -l
```

Expected: the first `ls` shows the legacy directories (validate-plugin-manifest, schema-validate-json, …); the final count is `0`. Note: `.runtime/` is in `.gitignore`, so no git diff results from this step.

- [ ] **Step 5: Verify no uncommitted changes remain in tracked files**

Run:
```bash
git status
```

Expected: working tree clean (the `rm -rf` in Step 4 touched only gitignored paths).

- [ ] **Step 6: Inspect commit graph**

Run:
```bash
git log --oneline -7
```

Expected: the most recent commits, top to bottom:
1. ci: cover write_sensor and replay_fixture build tags (#28)
2. docs(detect-sensors): replace replay snippet with Go script (#28)
3. test(detect-sensors): cover replay-fixture error paths (#28)
4. feat(detect-sensors): replay-fixture.go preserves sensor.id (#28)
5. docs(detect-sensors): tag-qualify write-sensor invocation in SKILL.md (#28)
6. refactor(detect-sensors): tag write-sensor for coexistence (#28)
7. (the two spec commits, then earlier history)

The plan deliberately landed six commits, one per task, so each can be reverted independently if a regression appears.

---

## Self-Review

**Spec coverage:**

| Spec section | Covered by |
|---|---|
| What changes #1: New `replay-fixture.go` | Tasks 3, 4 |
| What changes #2: New `replay-fixture_test.go` | Tasks 3, 5 |
| What changes #3: Update SKILL.md lines 286–292 + migration note | Task 6 |
| What changes #4: Tag `write-sensor.go` and its test, update docstring, update SKILL.md:245, hooks/* tests unchanged | Tasks 1, 2 |
| What changes #5–#7: No-change-to clauses | Implicit — no task touches those files |
| Acceptance criterion #1: no `replay-*` after a cycle | Task 8 Step 3 |
| Acceptance criterion #2: aggregate `sensor_id` has no `replay-` prefix | Task 3 (unit test) + Task 8 Step 2 (smoke) |
| Acceptance criterion #3: `go test -tags=replay_fixture` passes | Task 4 Step 2, Task 5 Step 2, Task 7 Step 4, Task 8 Step 1 |
| Acceptance criterion #4: `go test -tags=write_sensor` passes | Task 1 Step 6, Task 7 Step 4, Task 8 Step 1 |
| Acceptance criterion #5: `go vet -tags=replay_fixture` passes | Task 4 Step 3, Task 7 Step 4, Task 8 Step 1 |
| Acceptance criterion #6: `go vet -tags=write_sensor` passes | Task 1 Step 6, Task 7 Step 4, Task 8 Step 1 |
| Acceptance criterion #7: end-to-end diff against old shell snippet | Task 8 Step 2 (asserts the shape; the explicit diff against the old snippet is a manual one-off the engineer can perform if curiosity strikes — the test suite already pins the shape) |

The CI extension (Task 7) is not enumerated as a "what changes" item in the spec but is implied by acceptance criteria #3–#6. Listed explicitly as a separate task with rationale.

**Placeholder scan:** no `TBD`, no `TODO`, no "implement later", no "similar to Task N", no "add appropriate error handling". Every code block has complete, runnable code. Every command has expected output.

**Type/signature consistency:**
- `run(args []string, stdout, stderr io.Writer) int` — same signature in `replay-fixture.go` and `replay-fixture_test.go`.
- `repoRoot() string` — same signature in script and test helper.
- `tagForType(sensorType string) string` — exported via unexported function (package-internal); tested directly.
- All test helper names (`uniqueSensorID`, `writeTempSensor`, `writeTempFixture`, `repoRootDir`) match between Task 3 and Task 5 usage.
- The build tag string `replay_fixture` is consistent across all files and CI steps.
- The build tag string `write_sensor` is consistent across all files, SKILL.md, and CI steps.
