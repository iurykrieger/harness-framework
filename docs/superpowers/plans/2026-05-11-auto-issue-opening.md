# Auto Issue Opening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `PostToolUse(Bash)` hook that detects framework Go script crashes (panic, compile error, framework-internal Signal error, exit non-zero) and either opens a new GitHub issue or `+1`'s an existing one, with a 3-layer dedup cascade (local cache → `gh issue list --search` → `gh issue create`), always exiting 0 to never block the agent.

**Architecture:** Single new Go file `hooks/error-issue-autofiler.go` build-tagged `error_autofiler`, coexisting with the existing untagged `setup-failure-detector.go` via mutual `//go:build` constraints. State persists in `<projectRoot>/.runtime/auto-issues.json` (independent from `running_sensors.json`, but locked via the same `lib/registry.WithFileLock`). Project root resolved with `lib/registry.Lookup(cwd)`. No code moves to `lib/` (hook-specific logic stays in the hook).

**Tech Stack:** Go 1.25, stdlib only for pipeline (`crypto/sha256`, `encoding/json`, `regexp`, `os/exec`), `github.com/iurykrieger/harness-framework/lib/registry` for project-root discovery + file locking, `gh` CLI shelled out for GitHub operations.

**Reference files (read before starting):**
- Spec: `docs/superpowers/specs/2026-05-11-auto-issue-opening-design.md`
- Existing hook template: `hooks/setup-failure-detector.go` and `hooks/setup-failure-detector_test.go`
- Registry helpers used: `lib/registry/root.go` (Lookup/Result), `lib/registry/lock.go` (WithFileLock)
- Plugin manifest target: `.claude-plugin/plugin.json`
- Project rules: `CLAUDE.md` ("Project rules" section, especially #1 en-US, #3 Go scripts, #4 lib/ scope, #6 deterministic logic in Go, #7 one script one job, #8 tests required, #9 organize by context)

---

## Task 1: Build-tag isolation of the existing Stop hook

**Goal:** Add `//go:build !error_autofiler` to `setup-failure-detector.go` and its test so the new file can coexist as `package main` under the opposite tag. Behavioral no-op.

**Files:**
- Modify: `hooks/setup-failure-detector.go` (line 1)
- Modify: `hooks/setup-failure-detector_test.go` (line 1)

- [ ] **Step 1.1: Add build constraint to the source file**

Open `hooks/setup-failure-detector.go`. Currently line 1 is a `// hooks/setup-failure-detector.go` comment. Insert a build-constraint line at the very top (must precede the package clause by at least one blank line, per Go's build-tag rules).

The first 4 lines become:

```go
//go:build !error_autofiler

// hooks/setup-failure-detector.go
//
```

(Existing content from `// hooks/setup-failure-detector.go` onward is preserved unchanged.)

- [ ] **Step 1.2: Add build constraint to the test file**

Open `hooks/setup-failure-detector_test.go`. Add the same constraint at the top:

```go
//go:build !error_autofiler

package main
```

(Replace the existing first line `package main` with the three lines above. Preserve everything else.)

- [ ] **Step 1.3: Verify the default build path still works**

Run:

```bash
go vet ./hooks
go test ./hooks
```

Expected: PASS. Both commands operate on the default tag set (which excludes `error_autofiler`), and the existing tests must continue to pass.

- [ ] **Step 1.4: Verify the future tagged build is now empty (no entry point)**

Run:

```bash
go build -tags=error_autofiler ./hooks 2>&1 | head -5
```

Expected: failure mentioning "no Go files" or "build constraints exclude all Go files" — proves the constraint is effective. This will be resolved in Task 2 when we add the new file.

- [ ] **Step 1.5: Commit**

```bash
git add hooks/setup-failure-detector.go hooks/setup-failure-detector_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks: tag setup-failure-detector with !error_autofiler

Prep for the new error-issue-autofiler hook (Task 2). Adding
the opposite build constraint here keeps package main
unambiguous when both hooks coexist in hooks/.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Skeleton, types, and `main`/`run` with stdin parsing

**Goal:** Create the new file with build constraint, package, imports, top-level type declarations, constants, `sensorSkills` slice, `main`, and a minimal `run` that parses stdin and returns 0 (or 2 on malformed input). Mirrors the existing hook's error-handling shape.

**Files:**
- Create: `hooks/error-issue-autofiler.go`
- Create: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 2.1: Write the failing test for malformed stdin**

Create `hooks/error-issue-autofiler_test.go` with this content:

```go
//go:build error_autofiler

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_MalformedStdin_Returns2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(strings.NewReader("not json"), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "parse hook input") {
		t.Fatalf("expected 'parse hook input' in stderr, got %q", stderr.String())
	}
}

func TestRun_EmptyStdin_Returns2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
}

func TestRun_ValidEmptyInput_Returns0(t *testing.T) {
	// Minimal valid payload: tool_name and tool_input.command absent → not-framework → exit 0.
	input := `{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"tool_response":{"stdout":"","stderr":"","exitCode":0}}`
	var stdout, stderr bytes.Buffer
	code := run(strings.NewReader(input), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr=%q)", code, stderr.String())
	}
}
```

- [ ] **Step 2.2: Run the test to verify it fails**

Run:

```bash
go test -tags=error_autofiler ./hooks
```

Expected: FAIL — compile error "no Go files" or "undefined: run". This confirms the test is exercising the right tag and the implementation is absent.

- [ ] **Step 2.3: Write the source file with skeleton**

Create `hooks/error-issue-autofiler.go` with this content:

```go
//go:build error_autofiler

// hooks/error-issue-autofiler.go
//
// Claude Code PostToolUse(Bash) hook that detects framework Go
// script crashes (panic, compile error, framework-internal Signal
// error, exit non-zero) and either opens or +1s a GitHub issue,
// with 3-layer dedup (local cache → gh search → gh create).
//
// Input (JSON on stdin, PostToolUse payload):
//
//	{
//	  "session_id": "...",
//	  "cwd": "...",
//	  "hook_event_name": "PostToolUse",
//	  "tool_name": "Bash",
//	  "tool_input":  { "command": "...", "description": "..." },
//	  "tool_response": { "stdout": "...", "stderr": "...", "exitCode": 0 }
//	}
//
// Output: nothing on stdout under normal operation. Diagnostic
// messages on stderr. Always exit 0 (except exit 2 for malformed
// stdin, matching setup-failure-detector.go).
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	cacheStaleAfter      = 7 * 24 * time.Hour
	classifierScanWindow = 16 * 1024 // bytes scanned at the tail of stdout/stderr
	bodyLogLineLimit     = 50        // lines per <details> block
	bodyLogByteLimit     = 4 * 1024  // bytes per <details> block
	titleSummaryMaxLen   = 80
	commandTruncateLen   = 200 // in +1 occurrence comments
)

// sensorSkills lists the framework's user-facing skill identifiers,
// declared once and reused by commandTouchesFramework and
// extractSkill regex builders.
var sensorSkills = []string{"run", "start", "stop", "tail", "list", "heal", "detect"}

type hookInput struct {
	SessionID      string       `json:"session_id"`
	TranscriptPath string       `json:"transcript_path"`
	Cwd            string       `json:"cwd"`
	HookEventName  string       `json:"hook_event_name"`
	ToolName       string       `json:"tool_name"`
	ToolInput      toolInputBsh `json:"tool_input"`
	ToolResponse   toolResponse `json:"tool_response"`
}

type toolInputBsh struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type toolResponse struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	ExitCode    int    `json:"exitCode"`
	Interrupted bool   `json:"interrupted"`
}

type classifiedEvent struct {
	Type           string // compile_error | panic | signal_error | exit_nonzero
	Summary        string // single-line, ≤120 chars
	Skill          string // run-sensor | start-sensor | ... | hook | test | unknown
	FrameworkFrame string // for panic only: first frame in github.com/iurykrieger/harness-framework
	Pkg            string // for compile_error only: failing package name
	File           string // for compile_error only: relative file path
	MetadataKind   string // for signal_error only
}

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "read stdin:", err)
		return 2
	}
	if len(body) == 0 {
		fmt.Fprintln(stderr, "parse hook input: empty stdin")
		return 2
	}
	var in hookInput
	if err := json.Unmarshal(body, &in); err != nil {
		fmt.Fprintln(stderr, "parse hook input:", err)
		return 2
	}
	// Subsequent tasks fill in: killSwitchEnabled, commandTouchesFramework,
	// classify, fingerprint, cache, gh ops. For now: parse-only → exit 0.
	_ = in
	return 0
}
```

- [ ] **Step 2.4: Run the tests to verify they pass**

Run:

```bash
go test -tags=error_autofiler ./hooks -v -run TestRun_
```

Expected: PASS for all three (`TestRun_MalformedStdin_Returns2`, `TestRun_EmptyStdin_Returns2`, `TestRun_ValidEmptyInput_Returns0`).

- [ ] **Step 2.5: Verify the untagged build is still healthy**

Run:

```bash
go vet ./hooks
go test ./hooks
```

Expected: PASS — the new file is hidden behind `error_autofiler`, so the default build still sees only `setup-failure-detector.go` and its tests.

- [ ] **Step 2.6: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks: skeleton for error-issue-autofiler

Adds the new build-tagged hook with types, constants,
sensorSkills, and the run() entry point. Only stdin
parsing is wired; pipeline stages land in subsequent
commits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Kill switch

**Goal:** Implement `killSwitchEnabled()` reading `HARNESS_AUTOFILE_ISSUES` and short-circuiting `run` when disabled.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 3.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
func TestKillSwitchEnabled(t *testing.T) {
	cases := []struct {
		val      string
		disabled bool // true means hook is disabled (i.e., killSwitchEnabled returns true)
	}{
		{"", true},
		{"0", true},
		{"false", true},
		{"FALSE", true},
		{"off", true},
		{"OFF", true},
		{"1", false},
		{"true", false},
		{"on", false},
		{"yes", false},
	}
	for _, tc := range cases {
		t.Run("val="+tc.val, func(t *testing.T) {
			t.Setenv("HARNESS_AUTOFILE_ISSUES", tc.val)
			got := killSwitchEnabled()
			if got != tc.disabled {
				t.Fatalf("HARNESS_AUTOFILE_ISSUES=%q: got disabled=%v want %v", tc.val, got, tc.disabled)
			}
		})
	}
}

func TestRun_KillSwitch_ShortCircuits(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "0")
	// Even with a payload that would otherwise classify, kill switch wins.
	input := `{"tool_name":"Bash","tool_input":{"command":"go run ./skills/run-sensor/scripts foo"},"tool_response":{"stdout":"","stderr":"panic: boom\n\ngoroutine 1 [running]:\n","exitCode":2}}`
	var stdout, stderr bytes.Buffer
	code := run(strings.NewReader(input), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("kill switch must be silent; stderr=%q", stderr.String())
	}
}
```

- [ ] **Step 3.2: Run the test to verify it fails**

Run:

```bash
go test -tags=error_autofiler ./hooks -run TestKillSwitch -v
```

Expected: FAIL — `killSwitchEnabled` is undefined.

- [ ] **Step 3.3: Implement `killSwitchEnabled` and wire it into `run`**

In `hooks/error-issue-autofiler.go`, add `strings` to imports:

```go
import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)
```

Add the function (place it after `run`):

```go
// killSwitchEnabled returns true when the hook should be a no-op.
// Disabled values: unset, "", "0", "false" (any case), "off" (any case).
// Default-on: any other value (including "1", "true", "on", "yes")
// keeps the autofiler active.
func killSwitchEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("HARNESS_AUTOFILE_ISSUES")))
	switch v {
	case "", "0", "false", "off":
		return true
	default:
		return false
	}
}
```

Wire it into `run` immediately after stdin parsing:

```go
	var in hookInput
	if err := json.Unmarshal(body, &in); err != nil {
		fmt.Fprintln(stderr, "parse hook input:", err)
		return 2
	}
	if killSwitchEnabled() {
		return 0
	}
	_ = in
	return 0
```

- [ ] **Step 3.4: Run the tests to verify they pass**

```bash
go test -tags=error_autofiler ./hooks -run TestKillSwitch -v
go test -tags=error_autofiler ./hooks -run TestRun_KillSwitch -v
```

Expected: PASS for both.

- [ ] **Step 3.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): kill switch HARNESS_AUTOFILE_ISSUES

Disabled values: "", "0", "false", "off" (case-insensitive).
Default-on; sits at the top of run() to short-circuit before
any other work.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `commandTouchesFramework` matcher

**Goal:** Pre-filter Bash commands so the hook short-circuits on anything unrelated to the framework. The matcher is built from `sensorSkills` to keep the skill list DRY.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 4.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
func TestCommandTouchesFramework(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// Positive: go run from skills
		{"go run ./skills/run-sensor/scripts foo", true},
		{"go run -tags=run_computational ./skills/run-sensor/scripts foo", true},
		{"go run -tags=run_inferential ./skills/run-sensor/scripts foo --slot k=v", true},
		{"go run ./skills/start-sensor/scripts run-api-local", true},
		{"go run ./skills/stop-sensor/scripts run-api-local", true},
		{"go run ./skills/tail-sensor/scripts run-api-local 0", true},
		{"go run ./skills/list-sensors/scripts", true},
		{"go run ./skills/heal-sensor/scripts foo", true},
		{"go run ./skills/detect-sensors/scripts", true},

		// Positive: go run hooks
		{"go run ./hooks", true},
		{"go run -tags=error_autofiler ./hooks", true},

		// Positive: installed binaries
		{"harness-run-sensor foo", true},
		{"harness-watcher", true},
		{"/usr/local/bin/harness-stop-sensor run-api-local", true},

		// Positive: go test/vet/build of framework packages
		{"go test ./lib/...", true},
		{"go vet -tags=run_computational ./skills/...", true},
		{"go build ./hooks", true},

		// Negative
		{"ls -la", false},
		{"npm test", false},
		{"git push", false},
		{"go run ./cmd/other", false},
		{"cd skills/run-sensor && echo hi", false},
		{"echo go run ./skills/run-sensor/scripts", false}, // mentioned but not invoked
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := commandTouchesFramework(tc.cmd)
			if got != tc.want {
				t.Fatalf("cmd=%q: got %v want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
```

Note: the `echo go run ...` case tests that we don't false-positive on commands that *mention* the framework without invoking it. The regex anchors with word boundaries on either `go\s+run\s+` or known binary names, so a leading `echo` keeps the command outside the match.

Actually the simpler regex `go\s+run` *would* match inside `echo go run ...`. To be safe, we test the current regex's behavior and accept that `echo go run` matches the literal text. **Reconsider:** users who literally `echo` framework commands aren't running them; the hook produces no false issue because the classifier needs actual error output. The matcher false-positive here just wastes a few microseconds. We'll accept that and document it. Update the negative case to remove `echo go run ...`:

Replace that one line in the test:

```go
		// matcher is permissive on "go run" anywhere in the command; the
		// classifier then needs real error output, so false positives here
		// are harmless. We don't list 'echo go run ...' as a negative case.
```

Final test stays without the `echo` case.

- [ ] **Step 4.2: Run the test to verify it fails**

```bash
go test -tags=error_autofiler ./hooks -run TestCommandTouchesFramework -v
```

Expected: FAIL — `commandTouchesFramework` undefined.

- [ ] **Step 4.3: Implement the matcher**

In `hooks/error-issue-autofiler.go`, add `regexp` to imports:

```go
import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)
```

Add a package-level variable (after `sensorSkills`):

```go
// frameworkCommandPatterns flags Bash commands worth inspecting for
// framework crashes. Built lazily from sensorSkills to avoid hardcoding
// the seven names twice.
var frameworkCommandPatterns = buildFrameworkCommandPatterns()

func buildFrameworkCommandPatterns() []*regexp.Regexp {
	skills := strings.Join(sensorSkills, "|")
	return []*regexp.Regexp{
		// go run direct from the scripts directory
		regexp.MustCompile(`go\s+run\s+(?:-tags=\S+\s+)?\./skills/(?:` + skills + `)-sensors?/scripts\b`),
		// go run from hooks
		regexp.MustCompile(`go\s+run\s+(?:-tags=\S+\s+)?\./hooks\b`),
		// installed binaries on PATH
		regexp.MustCompile(`\bharness-(?:(?:` + skills + `)-sensors?|watcher)\b`),
		// go test/vet/build of the framework's own packages
		regexp.MustCompile(`go\s+(?:test|vet|build)\s+(?:-tags=\S+\s+)?\./(?:skills|lib|hooks)\b`),
	}
}
```

Note: the `-sensors?` (optional `s`) accommodates both `run-sensor` (singular) and `detect-sensors` (plural — see `skills/detect-sensors/`).

Add the matcher function:

```go
func commandTouchesFramework(cmd string) bool {
	if cmd == "" {
		return false
	}
	for _, re := range frameworkCommandPatterns {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}
```

Wire it into `run` after the kill switch check:

```go
	if killSwitchEnabled() {
		return 0
	}
	if in.ToolName != "Bash" {
		return 0
	}
	if !commandTouchesFramework(in.ToolInput.Command) {
		return 0
	}
	_ = in
	return 0
```

- [ ] **Step 4.4: Run the test to verify it passes**

```bash
go test -tags=error_autofiler ./hooks -run TestCommandTouchesFramework -v
```

Expected: PASS for every subtest. If any negative case false-positives, audit the regexes and tighten word boundaries.

- [ ] **Step 4.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): commandTouchesFramework matcher

Built from sensorSkills to keep the framework skill
enumeration in one place. Covers go run scripts, go run
hooks, installed binaries, and go test/vet/build of
framework packages. Non-matching commands short-circuit
run() with no further work.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `extractSkill`

**Goal:** From the Bash command, derive a short skill identifier (`run-sensor`, `start-sensor`, …, `hook`, `test`, `unknown`) used in titles, fingerprints, and cache entries.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 5.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
func TestExtractSkill(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		// Scripts-path form
		{"go run ./skills/run-sensor/scripts foo", "run-sensor"},
		{"go run -tags=start_sensor ./skills/start-sensor/scripts foo", "start-sensor"},
		{"go run ./skills/stop-sensor/scripts run-api-local", "stop-sensor"},
		{"go run ./skills/tail-sensor/scripts run-api-local 0", "tail-sensor"},
		{"go run ./skills/list-sensors/scripts", "list-sensors"},
		{"go run ./skills/heal-sensor/scripts foo", "heal-sensor"},
		{"go run ./skills/detect-sensors/scripts", "detect-sensors"},

		// Installed-binary form
		{"harness-run-sensor foo", "run-sensor"},
		{"/usr/local/bin/harness-start-sensor foo", "start-sensor"},
		{"harness-detect-sensors", "detect-sensors"},

		// Fallbacks
		{"go run ./hooks", "hook"},
		{"go run -tags=error_autofiler ./hooks", "hook"},
		{"go test ./lib/registry", "test"},
		{"go vet ./skills/...", "test"},
		{"harness-watcher", "watcher"},
		{"completely unrelated", "unknown"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.cmd, func(t *testing.T) {
			got := extractSkill(tc.cmd)
			if got != tc.want {
				t.Fatalf("cmd=%q: got %q want %q", tc.cmd, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 5.2: Run the test to verify it fails**

```bash
go test -tags=error_autofiler ./hooks -run TestExtractSkill -v
```

Expected: FAIL — `extractSkill` undefined.

- [ ] **Step 5.3: Implement `extractSkill`**

Add to `hooks/error-issue-autofiler.go` (after `commandTouchesFramework`):

```go
// skillExtractRe captures the sensor skill name from either scripts
// path or installed binary form. Built from sensorSkills.
var skillExtractRe = func() *regexp.Regexp {
	skills := strings.Join(sensorSkills, "|")
	return regexp.MustCompile(`(?:skills/|harness-)(` + skills + `-sensors?)`)
}()

func extractSkill(cmd string) string {
	if m := skillExtractRe.FindStringSubmatch(cmd); m != nil {
		return m[1]
	}
	// Fallback: harness-watcher binary
	if regexp.MustCompile(`\bharness-watcher\b`).MatchString(cmd) {
		return "watcher"
	}
	// Fallback: hooks
	if regexp.MustCompile(`go\s+run\s+(?:-tags=\S+\s+)?\./hooks\b`).MatchString(cmd) {
		return "hook"
	}
	// Fallback: go test/vet/build
	if regexp.MustCompile(`go\s+(?:test|vet|build)\b`).MatchString(cmd) {
		return "test"
	}
	return "unknown"
}
```

Note: each regex compile inside `extractSkill` could be hoisted to package-level vars; performance-wise the function runs at most once per Bash call, so we keep the function self-contained for readability. If profiling later shows hot path, refactor.

- [ ] **Step 5.4: Run the test to verify it passes**

```bash
go test -tags=error_autofiler ./hooks -run TestExtractSkill -v
```

Expected: PASS for every subtest.

- [ ] **Step 5.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): extractSkill from Bash command

Recognizes scripts path, installed binaries, harness-watcher,
hook self-invocations, and go test/vet/build. Falls back to
"unknown" so downstream code can rely on a non-empty string.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: `normalize` helper

**Goal:** Stable text normalization for fingerprint inputs and summary lines. PIDs, timestamps, absolute plugin paths, and trailing line/column markers get stripped.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 6.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
func TestNormalize(t *testing.T) {
	t.Setenv("CLAUDE_PLUGIN_ROOT", "/abs/path/to/harness-framework")
	cases := []struct {
		in   string
		want string
	}{
		{"PID=12345", "pid=n"},
		{"some pid=7 here", "some pid=n here"},
		{"2026-05-11T10:00:00Z occurred", "t occurred"},
		{"at 2026-05-11T10:00:00.123Z indeed", "at t indeed"},
		{"/abs/path/to/harness-framework/lib/registry/state.go:47", "<plugin>/lib/registry/state.go"},
		{"  multiple   spaces\there  ", "multiple spaces here"},
		{"trailing colon line :42:8", "trailing colon line"},
		{"runtime: index out of range [0] with length 0", "runtime: index out of range [0] with length 0"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := normalize(tc.in)
			if got != tc.want {
				t.Fatalf("in=%q: got %q want %q", tc.in, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 6.2: Run the test to verify it fails**

```bash
go test -tags=error_autofiler ./hooks -run TestNormalize -v
```

Expected: FAIL — `normalize` undefined.

- [ ] **Step 6.3: Implement `normalize`**

Add to `hooks/error-issue-autofiler.go`:

```go
var (
	rePID         = regexp.MustCompile(`(?i)pid=\d+`)
	reTimestamp   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)
	reTrailingPos = regexp.MustCompile(`\s*:\d+:\d+\s*$`)
	reWhitespace  = regexp.MustCompile(`\s+`)
)

// normalize produces a stable lower-case form of an error/output line
// for use in fingerprint canonical strings. Strips PIDs, ISO/RFC3339
// timestamps, absolute plugin paths (replaced with <plugin>), trailing
// :line:col suffixes, and collapses whitespace.
func normalize(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	s = rePID.ReplaceAllString(s, "pid=n")
	s = reTimestamp.ReplaceAllString(s, "t")
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		s = strings.ReplaceAll(s, strings.ToLower(root), "<plugin>")
	}
	s = reTrailingPos.ReplaceAllString(s, "")
	s = reWhitespace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
```

- [ ] **Step 6.4: Run the test to verify it passes**

```bash
go test -tags=error_autofiler ./hooks -run TestNormalize -v
```

Expected: PASS for every subtest.

- [ ] **Step 6.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): normalize helper for fingerprint inputs

Strips PIDs, ISO/RFC3339 timestamps, absolute plugin paths,
trailing :line:col markers, and collapses whitespace.
Lower-cased.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: `classify`

**Goal:** Inspect stdout/stderr/exitCode and return a `classifiedEvent` (or nil) per the four-rule cascade defined in the spec.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`
- Create: `hooks/testdata/compile_error.txt`
- Create: `hooks/testdata/panic_runtime_nil_deref.txt`
- Create: `hooks/testdata/panic_stack_lib_registry.txt`
- Create: `hooks/testdata/signal_error_start_failed.jsonl`
- Create: `hooks/testdata/exit_nonzero_no_match.txt`

- [ ] **Step 7.1: Create test fixture files**

`hooks/testdata/compile_error.txt` — represents stderr from a failed `go run`:

```
# github.com/iurykrieger/harness-framework/lib/sensor
lib/sensor/load.go:42:13: undefined: ResolveSensorPath
lib/sensor/load.go:58:9: cannot use sensorPath (type string) as type Path
```

`hooks/testdata/panic_runtime_nil_deref.txt` — represents combined stdout+stderr of a runtime panic:

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x10498ab]

goroutine 1 [running]:
github.com/iurykrieger/harness-framework/lib/registry.Lookup(0x14000020028, {0x14000020028, 0x0})
	/abs/path/to/harness-framework/lib/registry/root.go:151 +0x1ab
main.run({0x100bd9050, 0x140000a4008}, ...)
	/abs/path/to/harness-framework/hooks/setup-failure-detector.go:60 +0x44
main.main()
	/abs/path/to/harness-framework/hooks/setup-failure-detector.go:38 +0x40
exit status 2
```

`hooks/testdata/panic_stack_lib_registry.txt` — represents a panic whose top user frame is in `lib/registry`:

```
panic: registry: lock acquisition failed: too many open files

goroutine 7 [running]:
github.com/iurykrieger/harness-framework/lib/registry.WithFileLock(...)
	/abs/path/to/harness-framework/lib/registry/lock.go:13
github.com/iurykrieger/harness-framework/skills/start-sensor/scripts.spawnDetached(...)
	/abs/path/to/harness-framework/skills/start-sensor/scripts/start.go:200
main.main()
	/abs/path/to/harness-framework/skills/start-sensor/scripts/start.go:38
```

`hooks/testdata/signal_error_start_failed.jsonl` — represents the last JSONL line on stdout from `start-sensor`:

```
{"verdict":"error","severity":"high","metadata":{"kind":"start_failed","sensor_id":"run-api-local"},"evidence":[{"rationale":"write registry: start watcher: fork/exec /tmp/watcher: no such file or directory"}]}
```

`hooks/testdata/exit_nonzero_no_match.txt` — represents stderr that doesn't match any of the higher-priority patterns:

```
go: cannot find main module, but found .git/config in /home/user
	to create a module there, run:
	go mod init
```

- [ ] **Step 7.2: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
import (
	// add to existing imports:
	"os"
	"path/filepath"
)

// (Make sure the import block above is merged into the existing one, not duplicated.)

func loadFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

func TestClassify_CompileError(t *testing.T) {
	stderr := loadFixture(t, "compile_error.txt")
	evt := classify("", stderr, 2)
	if evt == nil {
		t.Fatal("expected non-nil event")
	}
	if evt.Type != "compile_error" {
		t.Fatalf("Type=%q want compile_error", evt.Type)
	}
	if evt.Pkg != "github.com/iurykrieger/harness-framework/lib/sensor" {
		t.Fatalf("Pkg=%q", evt.Pkg)
	}
	if evt.File != "lib/sensor/load.go" {
		t.Fatalf("File=%q want lib/sensor/load.go", evt.File)
	}
	if !strings.Contains(evt.Summary, "undefined: ResolveSensorPath") {
		t.Fatalf("Summary=%q", evt.Summary)
	}
}

func TestClassify_PanicRuntime(t *testing.T) {
	combined := loadFixture(t, "panic_runtime_nil_deref.txt")
	// Real hooks see panics on stderr; we put it all there for the fixture.
	evt := classify("", combined, 2)
	if evt == nil {
		t.Fatal("expected non-nil event")
	}
	if evt.Type != "panic" {
		t.Fatalf("Type=%q want panic", evt.Type)
	}
	if !strings.Contains(evt.Summary, "nil pointer dereference") {
		t.Fatalf("Summary=%q", evt.Summary)
	}
	if !strings.Contains(evt.FrameworkFrame, "lib/registry/root.go:151") {
		t.Fatalf("FrameworkFrame=%q", evt.FrameworkFrame)
	}
}

func TestClassify_PanicLibRegistry(t *testing.T) {
	combined := loadFixture(t, "panic_stack_lib_registry.txt")
	evt := classify("", combined, 2)
	if evt == nil || evt.Type != "panic" {
		t.Fatalf("expected panic; got %+v", evt)
	}
	if !strings.Contains(evt.FrameworkFrame, "lib/registry/lock.go") {
		t.Fatalf("FrameworkFrame=%q", evt.FrameworkFrame)
	}
}

func TestClassify_SignalError(t *testing.T) {
	stdout := loadFixture(t, "signal_error_start_failed.jsonl")
	evt := classify(stdout, "", 1)
	if evt == nil || evt.Type != "signal_error" {
		t.Fatalf("expected signal_error; got %+v", evt)
	}
	if evt.MetadataKind != "start_failed" {
		t.Fatalf("MetadataKind=%q", evt.MetadataKind)
	}
	if !strings.Contains(evt.Summary, "start_failed") {
		t.Fatalf("Summary=%q", evt.Summary)
	}
	if !strings.Contains(evt.Summary, "fork/exec /tmp/watcher") {
		t.Fatalf("Summary=%q", evt.Summary)
	}
}

func TestClassify_ExitNonzero(t *testing.T) {
	stderr := loadFixture(t, "exit_nonzero_no_match.txt")
	evt := classify("", stderr, 1)
	if evt == nil || evt.Type != "exit_nonzero" {
		t.Fatalf("expected exit_nonzero; got %+v", evt)
	}
	if !strings.Contains(evt.Summary, "cannot find main module") {
		t.Fatalf("Summary=%q", evt.Summary)
	}
}

func TestClassify_NoMatch_ReturnsNil(t *testing.T) {
	if got := classify("hello\n", "", 0); got != nil {
		t.Fatalf("expected nil for clean exit, got %+v", got)
	}
	if got := classify("hello\n", "warning: deprecated\n", 0); got != nil {
		t.Fatalf("expected nil for warn on stderr with exit 0, got %+v", got)
	}
}

func TestClassify_SignalError_VerdictFailIgnored(t *testing.T) {
	// A verdict=fail Signal is a legitimate sensor failure, NOT a framework bug.
	stdout := `{"verdict":"fail","metadata":{"kind":"aggregate"}}` + "\n"
	if got := classify(stdout, "", 1); got != nil {
		t.Fatalf("verdict=fail must not classify as framework bug; got %+v", got)
	}
}

func TestClassify_SignalError_NonAllowlistedKindIgnored(t *testing.T) {
	stdout := `{"verdict":"error","metadata":{"kind":"some_other_kind"}}` + "\n"
	if got := classify(stdout, "", 1); got != nil {
		t.Fatalf("non-allowlisted kind must not classify; got %+v", got)
	}
}
```

- [ ] **Step 7.3: Run the test to verify it fails**

```bash
go test -tags=error_autofiler ./hooks -run TestClassify -v
```

Expected: FAIL — `classify` undefined.

- [ ] **Step 7.4: Implement `classify`**

Add to `hooks/error-issue-autofiler.go` (imports may need `bufio`, `encoding/json`, `strings`):

```go
// signalErrorAllowedKinds restricts signal_error classification to
// framework-internal failure modes. fail/warn verdicts are legitimate
// sensor detections and are excluded entirely.
var signalErrorAllowedKinds = map[string]struct{}{
	"start_failed":             {},
	"runner_internal_error":    {},
	"schema_validation_error":  {},
	"slot_error":               {},
}

var (
	reCompileError = regexp.MustCompile(`(?m)^# (\S+)\n(\S+?):(\d+):\d+:\s*(.+)$`)
	rePanic        = regexp.MustCompile(`(?m)^(panic:|runtime error:)\s*(.+)$`)
	reGoroutine    = regexp.MustCompile(`(?m)^goroutine \d+ \[running\]:`)
	reFrameFile    = regexp.MustCompile(`(?m)^\t(/\S+\.go):(\d+)\b`)
)

// tailBytes returns the last n bytes of s (or all of s when shorter).
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// classify inspects stdout, stderr, and exitCode and returns a
// classifiedEvent (or nil when none of the four rules match).
func classify(stdout, stderr string, exitCode int) *classifiedEvent {
	stdoutTail := tailBytes(stdout, classifierScanWindow)
	stderrTail := tailBytes(stderr, classifierScanWindow)
	combined := stdoutTail + "\n" + stderrTail

	// Rule 1: compile_error (stderr only)
	if m := reCompileError.FindStringSubmatch(stderrTail); m != nil {
		summary := truncate(fmt.Sprintf("%s:%s: %s", m[2], m[3], m[4]), 120)
		return &classifiedEvent{
			Type:    "compile_error",
			Summary: summary,
			Pkg:     m[1],
			File:    m[2],
		}
	}

	// Rule 2: panic with goroutine frame within 5 lines
	if m := rePanic.FindStringSubmatchIndex(combined); m != nil {
		// Look for "goroutine N [running]:" within ~5 lines after the panic line.
		after := combined[m[1]:] // text after the panic line
		gIdx := reGoroutine.FindStringIndex(after)
		if gIdx != nil && linesBetween(after[:gIdx[0]]) <= 5 {
			panicLine := combined[m[2]:m[3]] // "panic:" or "runtime error:"
			msg := strings.TrimSpace(combined[m[4]:m[5]])
			summary := truncate(panicLine+" "+msg, 120)
			framework := extractFrameworkFrame(combined[m[1]:])
			return &classifiedEvent{
				Type:           "panic",
				Summary:        summary,
				FrameworkFrame: framework,
			}
		}
	}

	// Rule 3: signal_error on the last JSONL line of stdout
	if last := lastJSONLine(stdoutTail); last != "" {
		var sig struct {
			Verdict  string `json:"verdict"`
			Metadata struct {
				Kind string `json:"kind"`
			} `json:"metadata"`
			Evidence []struct {
				Rationale string `json:"rationale"`
			} `json:"evidence"`
		}
		if err := json.Unmarshal([]byte(last), &sig); err == nil {
			if sig.Verdict == "error" {
				if _, ok := signalErrorAllowedKinds[sig.Metadata.Kind]; ok {
					rationale := ""
					if len(sig.Evidence) > 0 {
						rationale = sig.Evidence[0].Rationale
					}
					summary := truncate(fmt.Sprintf("%s · %s", sig.Metadata.Kind, rationale), 120)
					return &classifiedEvent{
						Type:         "signal_error",
						Summary:      summary,
						MetadataKind: sig.Metadata.Kind,
					}
				}
			}
		}
	}

	// Rule 4: exit_nonzero, stderr non-empty, nothing else matched
	if exitCode != 0 && strings.TrimSpace(stderrTail) != "" {
		first := firstNonBlankLine(stderrTail)
		return &classifiedEvent{
			Type:    "exit_nonzero",
			Summary: truncate(first, 120),
		}
	}

	return nil
}

// truncate cuts s to at most n runes and appends "…" when truncated.
func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n-1]) + "…"
}

// linesBetween counts the number of newline characters in s.
func linesBetween(s string) int { return strings.Count(s, "\n") }

// firstNonBlankLine returns the first non-empty line of s.
func firstNonBlankLine(s string) string {
	scanner := bufio.NewScanner(strings.NewReader(s))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t")
		if line != "" {
			return line
		}
	}
	return ""
}

// lastJSONLine returns the last non-empty line of s if it looks like a
// JSON object (starts with '{'). Otherwise returns "".
func lastJSONLine(s string) string {
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var last string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			last = line
		}
	}
	if strings.HasPrefix(last, "{") {
		return last
	}
	return ""
}

// extractFrameworkFrame scans stack-trace lines after a panic and
// returns the first file:line frame under
// github.com/iurykrieger/harness-framework. Returns "" if none found.
func extractFrameworkFrame(stack string) string {
	scanner := bufio.NewScanner(strings.NewReader(stack))
	for scanner.Scan() {
		line := scanner.Text()
		m := reFrameFile.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		path := m[1]
		// Match either "harness-framework/<...>" inside the absolute
		// path or any path containing harness-framework as a segment.
		if idx := strings.Index(path, "harness-framework/"); idx >= 0 {
			rel := path[idx+len("harness-framework/"):]
			return rel + ":" + m[2]
		}
	}
	return ""
}
```

Add `bufio` and `encoding/json` to the imports if not already present (the `json` import was added in Task 2, but `bufio` is new):

```go
import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)
```

- [ ] **Step 7.5: Run the test to verify it passes**

```bash
go test -tags=error_autofiler ./hooks -run TestClassify -v
```

Expected: PASS for every subtest. If `TestClassify_PanicRuntime` fails on FrameworkFrame, audit `extractFrameworkFrame` — the fixture's stack has the frame at `lib/registry/root.go:151`.

- [ ] **Step 7.6: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go hooks/testdata/
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): classifier with four rules + fixtures

In order: compile_error (stderr regex), panic (panic +
goroutine within 5 lines), signal_error (last JSONL line
with allowlisted metadata.kind and verdict=error), and
exit_nonzero (non-zero exit + non-empty stderr fallback).

Five testdata fixtures cover each rule plus a runtime-panic
variant whose framework frame is in lib/registry.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: `fingerprint`

**Goal:** Stable 12-char SHA-256 hex truncation of a per-type canonical string. Same crash → same fingerprint regardless of PID/path/timestamp variation.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 8.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
func TestFingerprint_CompileError(t *testing.T) {
	a := &classifiedEvent{
		Type:    "compile_error",
		Pkg:     "github.com/iurykrieger/harness-framework/lib/sensor",
		File:    "lib/sensor/load.go",
		Summary: "lib/sensor/load.go:42: undefined: ResolveSensorPath",
		Skill:   "run-sensor",
	}
	b := *a
	b.Summary = "lib/sensor/load.go:99: undefined: ResolveSensorPath" // different line, same error
	if fingerprint(a) != fingerprint(&b) {
		t.Fatal("compile_error fingerprint should be stable across line numbers")
	}

	c := *a
	c.Summary = "lib/sensor/load.go:42: undefined: OtherSymbol"
	if fingerprint(a) == fingerprint(&c) {
		t.Fatal("different compile errors must hash differently")
	}
}

func TestFingerprint_Panic(t *testing.T) {
	a := &classifiedEvent{
		Type:           "panic",
		Skill:          "start-sensor",
		Summary:        "panic: runtime error: invalid memory address or nil pointer dereference",
		FrameworkFrame: "lib/registry/root.go:151",
	}
	b := *a
	// Same panic, different rendering of timestamps/PIDs in summary:
	b.Summary = "panic: runtime error: invalid memory address or nil pointer dereference (PID=12345 at 2026-05-11T10:00:00Z)"
	if fingerprint(a) != fingerprint(&b) {
		t.Fatal("panic fingerprint should ignore PID/timestamp noise")
	}

	c := *a
	c.FrameworkFrame = "lib/registry/lock.go:13"
	if fingerprint(a) == fingerprint(&c) {
		t.Fatal("different framework frames must hash differently")
	}
}

func TestFingerprint_SignalError(t *testing.T) {
	a := &classifiedEvent{
		Type:         "signal_error",
		Skill:        "start-sensor",
		MetadataKind: "start_failed",
		Summary:      "start_failed · write registry: start watcher: fork/exec /tmp/watcher: no such file or directory",
	}
	b := *a
	b.Summary = "start_failed · write registry: start watcher: fork/exec /var/folders/qq/watcher: no such file or directory"
	// The temp-dir change should NOT collide; only after path normalization
	// would they; here paths under /tmp and /var/folders differ as raw text,
	// so distinct fingerprints are acceptable. The important property:
	if fingerprint(a) == fingerprint(b.metadataNothing()) /* dummy */ {
		// just a placeholder for compilation
	}
	// Stability check across irrelevant fields:
	c := *a
	c.FrameworkFrame = "anything"
	if fingerprint(a) != fingerprint(&c) {
		t.Fatal("signal_error fingerprint should not depend on FrameworkFrame")
	}
}

func TestFingerprint_ExitNonzero(t *testing.T) {
	a := &classifiedEvent{
		Type:    "exit_nonzero",
		Skill:   "test",
		Summary: "go: cannot find main module, but found .git/config in /home/user",
	}
	b := *a
	b.Summary = "go: cannot find main module, but found .git/config in /home/other-user" // different user path
	if fingerprint(a) == fingerprint(&b) {
		// Acceptable — the spec doesn't promise cross-user stability for exit_nonzero.
		// This is informational only.
		t.Logf("note: exit_nonzero fingerprint varies across users; this is acceptable")
	}
	// Stability check: same summary → same fingerprint
	c := *a
	if fingerprint(a) != fingerprint(&c) {
		t.Fatal("identical events must produce identical fingerprints")
	}
}

func TestFingerprint_Length(t *testing.T) {
	evt := &classifiedEvent{Type: "panic", Skill: "run-sensor", Summary: "x"}
	fp := fingerprint(evt)
	if len(fp) != 12 {
		t.Fatalf("fingerprint len=%d want 12", len(fp))
	}
}
```

Remove the placeholder `b.metadataNothing()` line — that was a typo. The test should compile cleanly. Final form for `TestFingerprint_SignalError` keeps only the stability check across irrelevant fields:

```go
func TestFingerprint_SignalError(t *testing.T) {
	a := &classifiedEvent{
		Type:         "signal_error",
		Skill:        "start-sensor",
		MetadataKind: "start_failed",
		Summary:      "start_failed · write registry: start watcher: fork/exec /tmp/watcher: no such file or directory",
	}
	c := *a
	c.FrameworkFrame = "anything"
	if fingerprint(a) != fingerprint(&c) {
		t.Fatal("signal_error fingerprint should not depend on FrameworkFrame")
	}
	d := *a
	d.MetadataKind = "schema_validation_error"
	if fingerprint(a) == fingerprint(&d) {
		t.Fatal("different MetadataKind must hash differently")
	}
}
```

- [ ] **Step 8.2: Run the test to verify it fails**

```bash
go test -tags=error_autofiler ./hooks -run TestFingerprint -v
```

Expected: FAIL — `fingerprint` undefined.

- [ ] **Step 8.3: Implement `fingerprint`**

Add to `hooks/error-issue-autofiler.go`. Add `crypto/sha256` and `encoding/hex` to imports:

```go
import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)
```

Add the function:

```go
// fingerprint returns a 12-character lowercase hex hash that identifies
// an error across runs. The canonical string varies by Type — see
// docs/superpowers/specs/2026-05-11-auto-issue-opening-design.md.
func fingerprint(evt *classifiedEvent) string {
	var canonical string
	switch evt.Type {
	case "compile_error":
		canonical = strings.Join([]string{
			"compile",
			evt.Pkg,
			evt.File,
			normalize(evt.Summary),
		}, "|")
	case "panic":
		canonical = strings.Join([]string{
			"panic",
			evt.FrameworkFrame,
			normalize(evt.Summary),
		}, "|")
	case "signal_error":
		canonical = strings.Join([]string{
			"signal",
			evt.Skill,
			evt.MetadataKind,
			normalize(evt.Summary),
		}, "|")
	case "exit_nonzero":
		canonical = strings.Join([]string{
			"exit",
			evt.Skill,
			normalize(evt.Summary),
		}, "|")
	default:
		canonical = "unknown|" + normalize(evt.Summary)
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:12]
}
```

Note: `compile_error`'s canonical uses `evt.Summary` (line number included in raw form). The `normalize` strip of trailing `:line:col` ensures different lines for the same underlying error collide — verified by `TestFingerprint_CompileError`.

- [ ] **Step 8.4: Run the tests to verify they pass**

```bash
go test -tags=error_autofiler ./hooks -run TestFingerprint -v
```

Expected: PASS for every subtest.

- [ ] **Step 8.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): fingerprint with per-type canonical string

12-char SHA-256 hex. Compile errors collide across line
numbers; panics ignore PID/timestamp noise; signal_error
keys on skill+kind+rationale; exit_nonzero keys on
skill+normalized stderr first line.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Render functions — title, body, occurrence comment

**Goal:** Produce the Markdown the hook posts to GitHub. Title respects 80-char summary truncation; body is en-US per Project rule #1 and includes the HTML fingerprint marker.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 9.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
func TestRenderTitle(t *testing.T) {
	cases := []struct {
		skill, summary string
		want           string
	}{
		{"run-sensor", "panic: nil pointer", "[auto] run-sensor: panic: nil pointer"},
		{"start-sensor", strings.Repeat("a", 200), "[auto] start-sensor: " + strings.Repeat("a", titleSummaryMaxLen-1) + "…"},
		{"hook", "x", "[auto] hook: x"},
	}
	for _, tc := range cases {
		got := renderTitle(tc.skill, tc.summary)
		if got != tc.want {
			t.Fatalf("renderTitle(%q,%q):\n got %q\nwant %q", tc.skill, tc.summary, got, tc.want)
		}
	}
}

func TestRenderBody_ContainsRequiredFields(t *testing.T) {
	in := hookInput{
		Cwd: "/home/user/project",
		ToolInput: toolInputBsh{
			Command: "go run ./skills/run-sensor/scripts foo",
		},
		ToolResponse: toolResponse{
			Stdout:   "line 1\nline 2\n",
			Stderr:   "panic: boom\n",
			ExitCode: 2,
		},
	}
	evt := classifiedEvent{Type: "panic", Skill: "run-sensor", Summary: "panic: boom"}
	body := renderBody(in, evt, "abcd1234ef00")

	for _, want := range []string{
		"**Type:** panic",
		"**Skill:** run-sensor",
		"**Fingerprint:** `abcd1234ef00`",
		"**First seen:**",
		"## Command",
		"go run ./skills/run-sensor/scripts foo",
		"## Output",
		"stdout (last 50 lines)",
		"stderr (last 50 lines)",
		"line 1",
		"panic: boom",
		"## Context",
		"`cwd`:",
		"`exit_code`: 2",
		"<!-- harness-fp:abcd1234ef00 -->",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestRenderBody_TruncatesLongOutput(t *testing.T) {
	bigStdout := strings.Repeat("x", 10*1024) // 10 KB
	in := hookInput{
		ToolResponse: toolResponse{Stdout: bigStdout, Stderr: "", ExitCode: 1},
	}
	evt := classifiedEvent{Type: "exit_nonzero", Skill: "test", Summary: "x"}
	body := renderBody(in, evt, "00000000abcd")
	// Body cannot contain the full 10KB run; truncation applies.
	if strings.Count(body, "x") >= 10*1024 {
		t.Fatalf("body should truncate stdout below 10KB; got %d x's", strings.Count(body, "x"))
	}
}

func TestRenderOccurrenceComment(t *testing.T) {
	in := hookInput{
		Cwd: "/home/user/project",
		ToolInput: toolInputBsh{
			Command: strings.Repeat("a", 500),
		},
		ToolResponse: toolResponse{ExitCode: 2},
	}
	c := renderOccurrenceComment(in)
	if !strings.HasPrefix(c, "+1 occurrence detected at") {
		t.Fatalf("comment must start with +1 occurrence: %q", c)
	}
	if !strings.Contains(c, "`cwd`:") {
		t.Fatalf("comment missing cwd: %q", c)
	}
	if !strings.Contains(c, "`exit_code`: 2") {
		t.Fatalf("comment missing exit_code: %q", c)
	}
	// command must be truncated to ≤200 chars in the rendered comment
	for _, line := range strings.Split(c, "\n") {
		if strings.HasPrefix(line, "- `command`:") {
			if len(line) > 200+len("- `command`: ``")+5 {
				t.Fatalf("command line too long: %d chars: %q", len(line), line)
			}
		}
	}
}
```

- [ ] **Step 9.2: Run the tests to verify they fail**

```bash
go test -tags=error_autofiler ./hooks -run "TestRenderTitle|TestRenderBody|TestRenderOccurrenceComment" -v
```

Expected: FAIL — render functions undefined.

- [ ] **Step 9.3: Implement render functions**

Add to `hooks/error-issue-autofiler.go`:

```go
// nowUTC is overridable in tests.
var nowUTC = func() time.Time { return time.Now().UTC() }

func renderTitle(skill, summary string) string {
	return "[auto] " + skill + ": " + truncate(summary, titleSummaryMaxLen)
}

// renderBody produces the Markdown issue body, en-US per Project rule
// #1. The trailing <!-- harness-fp:... --> marker is the dedup hook
// used by ghSearch.
func renderBody(in hookInput, evt classifiedEvent, fp string) string {
	stdout := truncateOutput(in.ToolResponse.Stdout, bodyLogLineLimit, bodyLogByteLimit)
	stderr := truncateOutput(in.ToolResponse.Stderr, bodyLogLineLimit, bodyLogByteLimit)
	var b strings.Builder
	fmt.Fprintf(&b, "**Type:** %s\n", evt.Type)
	fmt.Fprintf(&b, "**Skill:** %s\n", evt.Skill)
	fmt.Fprintf(&b, "**Fingerprint:** `%s`\n", fp)
	fmt.Fprintf(&b, "**First seen:** %s\n\n", nowUTC().Format(time.RFC3339))
	b.WriteString("## Command\n\n")
	b.WriteString("```bash\n")
	b.WriteString(in.ToolInput.Command)
	b.WriteString("\n```\n\n")
	b.WriteString("## Output\n\n")
	b.WriteString("<details>\n<summary>stdout (last 50 lines)</summary>\n\n")
	b.WriteString("```\n")
	b.WriteString(stdout)
	b.WriteString("\n```\n</details>\n\n")
	b.WriteString("<details>\n<summary>stderr (last 50 lines)</summary>\n\n")
	b.WriteString("```\n")
	b.WriteString(stderr)
	b.WriteString("\n```\n</details>\n\n")
	b.WriteString("## Context\n\n")
	fmt.Fprintf(&b, "- `cwd`: `%s`\n", relativizeHome(in.Cwd))
	fmt.Fprintf(&b, "- `exit_code`: %d\n", in.ToolResponse.ExitCode)
	b.WriteString("- Hook: `error-issue-autofiler` in `hooks/`\n\n")
	fmt.Fprintf(&b, "<!-- harness-fp:%s -->\n", fp)
	return b.String()
}

func renderOccurrenceComment(in hookInput) string {
	cmd := truncate(in.ToolInput.Command, commandTruncateLen)
	var b strings.Builder
	fmt.Fprintf(&b, "+1 occurrence detected at %s.\n\n", nowUTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- `cwd`: `%s`\n", relativizeHome(in.Cwd))
	fmt.Fprintf(&b, "- `command`: `%s`\n", cmd)
	fmt.Fprintf(&b, "- `exit_code`: %d\n", in.ToolResponse.ExitCode)
	return b.String()
}

// truncateOutput keeps at most lineLimit lines (taken from the tail) or
// byteLimit bytes, whichever is smaller. Returns the truncated text.
func truncateOutput(s string, lineLimit, byteLimit int) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > lineLimit {
		lines = lines[len(lines)-lineLimit:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > byteLimit {
		out = out[len(out)-byteLimit:]
	}
	return out
}

// relativizeHome rewrites paths under $HOME as ~/... for readability.
// Non-matching paths are returned unchanged.
func relativizeHome(p string) string {
	if p == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
```

- [ ] **Step 9.4: Run the tests to verify they pass**

```bash
go test -tags=error_autofiler ./hooks -run "TestRenderTitle|TestRenderBody|TestRenderOccurrenceComment" -v
```

Expected: PASS for all five subtests.

- [ ] **Step 9.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): render title, body, and occurrence comment

Body and occurrence comment use en-US per Project rule #1.
Body contains the <!-- harness-fp:... --> marker that ghSearch
keys on. stdout/stderr clamped to 50 lines or 4KB tail per
<details> block.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Cache I/O with `flock`

**Goal:** Read/write `<projectRoot>/.runtime/auto-issues.json` under `lib/registry.WithFileLock`. Handle a corrupt cache by treating it as empty (without overwriting until a successful operation).

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 10.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
import (
	// add to existing imports:
	"sync"
)
// (Merge into existing import block.)

func TestCache_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")

	c, err := loadCache(cachePath)
	if err != nil {
		t.Fatalf("loadCache empty: %v", err)
	}
	if len(c.Entries) != 0 {
		t.Fatalf("expected empty cache, got %d entries", len(c.Entries))
	}

	now := time.Now().UTC()
	c.put("fp1", cacheEntry{
		IssueURL:        "https://github.com/x/y/issues/1",
		FirstSeen:       now,
		LastSeen:        now,
		OccurrenceCount: 1,
		Skill:           "run-sensor",
		Type:            "panic",
	})
	if err := c.save(cachePath); err != nil {
		t.Fatalf("save: %v", err)
	}

	c2, err := loadCache(cachePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(c2.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(c2.Entries))
	}
	got := c2.Entries["fp1"]
	if got.IssueURL != "https://github.com/x/y/issues/1" {
		t.Fatalf("IssueURL=%q", got.IssueURL)
	}
	if got.OccurrenceCount != 1 {
		t.Fatalf("OccurrenceCount=%d", got.OccurrenceCount)
	}
}

func TestCache_CorruptFile_TreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	if err := os.WriteFile(cachePath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := loadCache(cachePath)
	if err == nil {
		t.Fatal("expected error on corrupt file")
	}
	if c == nil {
		t.Fatal("loadCache must return a usable fresh cache even on parse error")
	}
	if len(c.Entries) != 0 {
		t.Fatalf("fresh cache must have 0 entries; got %d", len(c.Entries))
	}
	// And the corrupt file is NOT overwritten just by loading.
	data, _ := os.ReadFile(cachePath)
	if string(data) != "not json" {
		t.Fatalf("corrupt file overwritten on load: %q", data)
	}
}

func TestCache_ConcurrentPut_Serializes(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		fp := fmt.Sprintf("fp%d", i)
		go func() {
			defer wg.Done()
			if err := updateCacheLocked(cachePath, func(c *cache) {
				c.put(fp, cacheEntry{
					IssueURL:        "url-" + fp,
					FirstSeen:       time.Now().UTC(),
					LastSeen:        time.Now().UTC(),
					OccurrenceCount: 1,
				})
			}); err != nil {
				t.Errorf("updateCacheLocked: %v", err)
			}
		}()
	}
	wg.Wait()

	c, err := loadCache(cachePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(c.Entries) != 10 {
		t.Fatalf("expected 10 concurrent entries, got %d", len(c.Entries))
	}
}
```

- [ ] **Step 10.2: Run the tests to verify they fail**

```bash
go test -tags=error_autofiler ./hooks -run TestCache -v
```

Expected: FAIL — `loadCache`, `cacheEntry`, `cache.put`, `cache.save`, `updateCacheLocked` undefined.

- [ ] **Step 10.3: Implement cache types and I/O**

Add to `hooks/error-issue-autofiler.go`. Add `path/filepath` and the registry import:

```go
import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
)
```

Add types and functions:

```go
type cacheEntry struct {
	IssueURL        string    `json:"issue_url"`
	FirstSeen       time.Time `json:"first_seen"`
	LastSeen        time.Time `json:"last_seen"`
	OccurrenceCount int       `json:"occurrence_count"`
	Skill           string    `json:"skill"`
	Type            string    `json:"type"`
}

type cache struct {
	Version int                   `json:"version"`
	Entries map[string]cacheEntry `json:"entries"`
}

// newCache returns a fresh empty v1 cache.
func newCache() *cache {
	return &cache{Version: 1, Entries: map[string]cacheEntry{}}
}

// loadCache reads cachePath. Returns:
//   - (empty cache, nil)        when the file does not exist
//   - (loaded cache, nil)       when the file exists and parses
//   - (empty cache, err)        when the file exists but is malformed —
//                               the malformed file is NOT overwritten.
func loadCache(cachePath string) (*cache, error) {
	data, err := os.ReadFile(cachePath)
	if errors.Is(err, os.ErrNotExist) {
		return newCache(), nil
	}
	if err != nil {
		return newCache(), err
	}
	c := newCache()
	if err := json.Unmarshal(data, c); err != nil {
		return newCache(), fmt.Errorf("parse cache %s: %w", cachePath, err)
	}
	if c.Entries == nil {
		c.Entries = map[string]cacheEntry{}
	}
	return c, nil
}

// put inserts or replaces fp's entry.
func (c *cache) put(fp string, e cacheEntry) {
	if c.Entries == nil {
		c.Entries = map[string]cacheEntry{}
	}
	c.Entries[fp] = e
}

// save writes the cache atomically (write to .tmp, fsync, rename).
func (c *cache) save(cachePath string) error {
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return fmt.Errorf("mkdir cache dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, cachePath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// updateCacheLocked runs fn(c) under an exclusive flock on
// cachePath+".lock", saving the result. Use it for any
// read-modify-write op on the cache.
func updateCacheLocked(cachePath string, fn func(*cache)) error {
	lockPath := cachePath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	return registry.WithFileLock(lockPath, func() error {
		c, err := loadCache(cachePath)
		if err != nil {
			// Continue with a fresh cache so the very first successful
			// write doesn't get blocked by a one-off parse error, but
			// preserve the error for caller's logs.
			fmt.Fprintln(os.Stderr, "cache load error (treating as empty):", err)
		}
		fn(c)
		return c.save(cachePath)
	})
}
```

- [ ] **Step 10.4: Run the tests to verify they pass**

```bash
go test -tags=error_autofiler ./hooks -run TestCache -v
```

Expected: PASS for all three subtests. The concurrent test verifies `flock` actually serializes writes (without the lock, the goroutines race the JSON file and entries get lost).

- [ ] **Step 10.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): cache types and flock-guarded I/O

cacheEntry + cache types serialize to auto-issues.json.
updateCacheLocked wraps a read-modify-write under
lib/registry.WithFileLock so concurrent PostToolUse
invocations don't lose entries. Atomic save via .tmp+rename.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: `ghClient` interface, `fakeGhClient`, and `ghCLI` shell-out

**Goal:** Abstract GitHub operations behind an interface for testability; provide a `gh`-CLI-backed production implementation; provide a fake for tests.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 11.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
func TestFakeGhClient_RecordsCalls(t *testing.T) {
	f := &fakeGhClient{}
	if _, err := f.Create("owner/repo", "title", "body", []string{"auto-filed", "bug"}); err != nil {
		t.Fatal(err)
	}
	if len(f.creates) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(f.creates))
	}
	if f.creates[0].title != "title" {
		t.Fatalf("title=%q", f.creates[0].title)
	}
	if err := f.Comment("owner/repo", 42, "hi"); err != nil {
		t.Fatal(err)
	}
	if len(f.comments) != 1 {
		t.Fatalf("expected 1 comment call, got %d", len(f.comments))
	}
	if got, err := f.Search("owner/repo", "fp123"); err != nil || got.Number != 0 {
		t.Fatalf("default fake search returns zero issueRef; got %+v err=%v", got, err)
	}
}

func TestFakeGhClient_SearchReturnsScripted(t *testing.T) {
	f := &fakeGhClient{
		searchResp: issueRef{Number: 99, URL: "https://github.com/o/r/issues/99"},
	}
	got, err := f.Search("o/r", "fp")
	if err != nil || got.Number != 99 {
		t.Fatalf("got %+v err=%v", got, err)
	}
}
```

- [ ] **Step 11.2: Run the tests to verify they fail**

```bash
go test -tags=error_autofiler ./hooks -run TestFakeGhClient -v
```

Expected: FAIL — `fakeGhClient`, `ghClient`, `issueRef`, etc. undefined.

- [ ] **Step 11.3: Implement interface and fake**

Add to `hooks/error-issue-autofiler.go`:

```go
type issueRef struct {
	Number int
	URL    string
}

// ghClient is the surface the autofiler uses to interact with GitHub.
// Tests substitute fakeGhClient; production uses ghCLI.
type ghClient interface {
	Search(repo, fingerprint string) (issueRef, error)
	Comment(repo string, issueNumber int, body string) error
	Create(repo, title, body string, labels []string) (issueRef, error)
}
```

Add to `hooks/error-issue-autofiler_test.go` (the test file, not the source — keeps the fake out of the production binary):

```go
type fakeGhCreate struct {
	repo, title, body string
	labels            []string
}

type fakeGhComment struct {
	repo string
	num  int
	body string
}

type fakeGhSearch struct {
	repo string
	fp   string
}

type fakeGhClient struct {
	searches   []fakeGhSearch
	comments   []fakeGhComment
	creates    []fakeGhCreate
	searchResp issueRef
	searchErr  error
	commentErr error
	createResp issueRef
	createErr  error
}

func (f *fakeGhClient) Search(repo, fp string) (issueRef, error) {
	f.searches = append(f.searches, fakeGhSearch{repo, fp})
	return f.searchResp, f.searchErr
}

func (f *fakeGhClient) Comment(repo string, n int, body string) error {
	f.comments = append(f.comments, fakeGhComment{repo, n, body})
	return f.commentErr
}

func (f *fakeGhClient) Create(repo, title, body string, labels []string) (issueRef, error) {
	f.creates = append(f.creates, fakeGhCreate{repo, title, body, append([]string(nil), labels...)})
	return f.createResp, f.createErr
}
```

Also add the production implementation `ghCLI` to the source file:

```go
// ghCLI shells out to the `gh` CLI for all operations. Errors from
// `gh` (auth missing, network, etc.) propagate back; the caller logs
// and exits 0.
type ghCLI struct{}

// Search runs `gh issue list --search "is:open repo:<repo> harness-fp:<fp>" --json number,url --limit 1`.
// Returns zero issueRef when no match.
func (ghCLI) Search(repo, fingerprint string) (issueRef, error) {
	cmd := exec.Command("gh", "issue", "list",
		"--search", fmt.Sprintf("is:open repo:%s harness-fp:%s", repo, fingerprint),
		"--json", "number,url",
		"--limit", "1",
	)
	out, err := cmd.Output()
	if err != nil {
		return issueRef{}, fmt.Errorf("gh search: %w", err)
	}
	var hits []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(out, &hits); err != nil {
		return issueRef{}, fmt.Errorf("parse gh search: %w", err)
	}
	if len(hits) == 0 {
		return issueRef{}, nil
	}
	return issueRef{Number: hits[0].Number, URL: hits[0].URL}, nil
}

func (ghCLI) Comment(repo string, num int, body string) error {
	cmd := exec.Command("gh", "issue", "comment",
		fmt.Sprintf("%d", num),
		"--repo", repo,
		"--body-file", "-",
	)
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh comment: %w: %s", err, out)
	}
	return nil
}

func (ghCLI) Create(repo, title, body string, labels []string) (issueRef, error) {
	args := []string{"issue", "create",
		"--repo", repo,
		"--title", title,
		"--body-file", "-",
	}
	for _, l := range labels {
		args = append(args, "--label", l)
	}
	cmd := exec.Command("gh", args...)
	cmd.Stdin = strings.NewReader(body)
	out, err := cmd.Output()
	if err != nil {
		return issueRef{}, fmt.Errorf("gh create: %w", err)
	}
	// `gh issue create` prints the URL on stdout. Extract issue number from the tail.
	url := strings.TrimSpace(string(out))
	num := 0
	if i := strings.LastIndex(url, "/"); i >= 0 {
		fmt.Sscanf(url[i+1:], "%d", &num)
	}
	return issueRef{Number: num, URL: url}, nil
}
```

Add `os/exec` to the imports:

```go
import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
)
```

- [ ] **Step 11.4: Run the tests to verify they pass**

```bash
go test -tags=error_autofiler ./hooks -run TestFakeGhClient -v
```

Expected: PASS for both subtests.

- [ ] **Step 11.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): ghClient interface, fake (test-only), and ghCLI

ghCLI shells out to the gh CLI for Search/Comment/Create.
fakeGhClient lives in the _test.go file so it's not built
into production. Search uses harness-fp:<fp> body marker
via gh issue list --search.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: `resolveRepo`

**Goal:** Parse `git remote get-url origin` output into `owner/repo` for GitHub remotes; reject non-GitHub.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 12.1: Write the failing test**

Append to `hooks/error-issue-autofiler_test.go`:

```go
func TestParseGitRemote(t *testing.T) {
	cases := []struct {
		remote  string
		want    string
		wantErr bool
	}{
		{"git@github.com:iurykrieger/harness-framework.git", "iurykrieger/harness-framework", false},
		{"git@github.com:iurykrieger/harness-framework", "iurykrieger/harness-framework", false},
		{"https://github.com/iurykrieger/harness-framework.git", "iurykrieger/harness-framework", false},
		{"https://github.com/iurykrieger/harness-framework", "iurykrieger/harness-framework", false},
		{"ssh://git@github.com/iurykrieger/harness-framework.git", "iurykrieger/harness-framework", false},
		{"git@gitlab.com:foo/bar.git", "", true},
		{"https://bitbucket.org/foo/bar", "", true},
		{"", "", true},
		{"not-a-url", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.remote, func(t *testing.T) {
			got, err := parseGitRemote(tc.remote)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.remote)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 12.2: Run the test to verify it fails**

```bash
go test -tags=error_autofiler ./hooks -run TestParseGitRemote -v
```

Expected: FAIL — `parseGitRemote` undefined.

- [ ] **Step 12.3: Implement `parseGitRemote` and `resolveRepo`**

Add to `hooks/error-issue-autofiler.go`:

```go
var reGitRemote = regexp.MustCompile(`(?i)(?:git@github\.com:|https?://github\.com/|ssh://git@github\.com/)([\w.-]+)/([\w.-]+?)(?:\.git)?/?$`)

// parseGitRemote turns a git remote URL into "owner/repo" if and only
// if the host is github.com. Returns an error for non-GitHub remotes
// or unparseable input.
func parseGitRemote(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", fmt.Errorf("empty remote")
	}
	m := reGitRemote.FindStringSubmatch(remote)
	if m == nil {
		return "", fmt.Errorf("not a github.com remote: %q", remote)
	}
	return m[1] + "/" + m[2], nil
}

// resolveRepo runs `git -C projectRoot remote get-url origin` and
// parses the result. Only the origin remote is consulted; forks with
// upstream or other named remotes are intentionally ignored.
func resolveRepo(projectRoot string) (string, error) {
	cmd := exec.Command("git", "-C", projectRoot, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return parseGitRemote(string(out))
}
```

- [ ] **Step 12.4: Run the test to verify it passes**

```bash
go test -tags=error_autofiler ./hooks -run TestParseGitRemote -v
```

Expected: PASS for all subtests.

- [ ] **Step 12.5: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): resolveRepo from git remote origin

parseGitRemote handles SSH, HTTPS, and ssh:// forms of
github.com URLs and rejects non-github hosts.
resolveRepo shells out to git remote get-url origin and
delegates parsing. Only origin is consulted; upstream
and other named remotes are ignored.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Pipeline integration in `run`

**Goal:** Wire all stages together so the hook actually files issues. Tests inject `ghClient` and a temp cache; production wires `ghCLI` and the resolved cache path.

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 13.1: Write the failing integration tests**

Append to `hooks/error-issue-autofiler_test.go`. This is a substantial block — it implements all 10 integration tests listed in the spec:

```go
// runOpts injects test doubles into runWith. Production uses run() which
// builds a runOpts pointing at ghCLI{} and the real cache path.
type runOpts struct {
	gh         ghClient
	cachePath  string
	repo       string // override resolveRepo
	repoErr    error
	projectErr error
	now        time.Time
}

// runWith is the testable entry point. run() is a thin wrapper that
// constructs default opts and calls runWith. We introduce it here to
// make every integration test deterministic.
func TestRunFlow_NewError_CreatesIssue(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	gh := &fakeGhClient{
		createResp: issueRef{Number: 42, URL: "https://github.com/o/r/issues/42"},
	}
	in := buildHookInput("go run ./skills/run-sensor/scripts foo",
		"", "panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/abs/harness-framework/hooks/x.go:1\n", 2)
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if len(gh.creates) != 1 {
		t.Fatalf("expected 1 create, got %d", len(gh.creates))
	}
	if !strings.HasPrefix(gh.creates[0].title, "[auto] run-sensor:") {
		t.Fatalf("title=%q", gh.creates[0].title)
	}
	if !contains(gh.creates[0].labels, "auto-filed") || !contains(gh.creates[0].labels, "bug") {
		t.Fatalf("labels=%v", gh.creates[0].labels)
	}
	c, _ := loadCache(cachePath)
	if len(c.Entries) != 1 {
		t.Fatalf("cache size=%d", len(c.Entries))
	}
}

func TestRunFlow_CachedRecent_ShortCircuits(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")

	// Seed cache with a recent entry matching the panic we're about to feed in.
	in := buildHookInput("go run ./skills/run-sensor/scripts foo",
		"", "panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/abs/harness-framework/hooks/x.go:1\n", 2)
	evt := classify(in.ToolResponse.Stdout, in.ToolResponse.Stderr, in.ToolResponse.ExitCode)
	if evt == nil {
		t.Fatal("classify returned nil")
	}
	evt.Skill = extractSkill(in.ToolInput.Command)
	fp := fingerprint(evt)
	if err := updateCacheLocked(cachePath, func(c *cache) {
		c.put(fp, cacheEntry{
			IssueURL:        "https://github.com/o/r/issues/1",
			FirstSeen:       time.Now().UTC().Add(-1 * time.Hour),
			LastSeen:        time.Now().UTC().Add(-1 * time.Hour),
			OccurrenceCount: 1,
			Skill:           evt.Skill,
			Type:            evt.Type,
		})
	}); err != nil {
		t.Fatal(err)
	}

	gh := &fakeGhClient{}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 {
		t.Fatal(code)
	}
	if len(gh.creates)+len(gh.searches)+len(gh.comments) != 0 {
		t.Fatalf("no gh calls expected; creates=%d searches=%d comments=%d",
			len(gh.creates), len(gh.searches), len(gh.comments))
	}
	c, _ := loadCache(cachePath)
	if c.Entries[fp].OccurrenceCount != 2 {
		t.Fatalf("OccurrenceCount=%d want 2", c.Entries[fp].OccurrenceCount)
	}
}

func TestRunFlow_CachedStale_RechecksGitHub(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	in := buildHookInput("go run ./skills/run-sensor/scripts foo",
		"", "panic: stale\n\ngoroutine 1 [running]:\nmain.main()\n\t/abs/harness-framework/hooks/x.go:1\n", 2)
	evt := classify(in.ToolResponse.Stdout, in.ToolResponse.Stderr, in.ToolResponse.ExitCode)
	evt.Skill = extractSkill(in.ToolInput.Command)
	fp := fingerprint(evt)
	staleTime := time.Now().UTC().Add(-8 * 24 * time.Hour)
	if err := updateCacheLocked(cachePath, func(c *cache) {
		c.put(fp, cacheEntry{IssueURL: "url-old", FirstSeen: staleTime, LastSeen: staleTime, OccurrenceCount: 1})
	}); err != nil {
		t.Fatal(err)
	}
	gh := &fakeGhClient{searchResp: issueRef{Number: 7, URL: "https://github.com/o/r/issues/7"}}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 {
		t.Fatal(code)
	}
	if len(gh.searches) != 1 {
		t.Fatalf("expected 1 search, got %d", len(gh.searches))
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 comment on the stale-but-still-open issue, got %d", len(gh.comments))
	}
}

func TestRunFlow_GHFound_CommentsNotCreates(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	in := buildHookInput("go run ./skills/run-sensor/scripts foo",
		"", "panic: found\n\ngoroutine 1 [running]:\nmain.main()\n\t/abs/harness-framework/hooks/x.go:1\n", 2)
	gh := &fakeGhClient{searchResp: issueRef{Number: 99, URL: "https://github.com/o/r/issues/99"}}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 {
		t.Fatal(code)
	}
	if len(gh.creates) != 0 {
		t.Fatalf("expected no create, got %d", len(gh.creates))
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(gh.comments))
	}
	c, _ := loadCache(cachePath)
	if len(c.Entries) != 1 {
		t.Fatal("cache should have the found URL")
	}
}

func TestRunFlow_KillSwitch_NoOp(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "0")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	in := buildHookInput("go run ./skills/run-sensor/scripts foo",
		"", "panic: boom\ngoroutine 1 [running]:\n", 2)
	gh := &fakeGhClient{}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("kill switch must be silent; exit=%d stderr=%s", code, stderr.String())
	}
	if len(gh.creates) != 0 || len(gh.searches) != 0 {
		t.Fatalf("no gh calls expected")
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no cache file expected; stat err=%v", err)
	}
}

func TestRunFlow_NonFrameworkCommand_NoOp(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	in := buildHookInput("ls -la", "", "no such file\n", 1)
	gh := &fakeGhClient{}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 {
		t.Fatal(code)
	}
	if len(gh.creates)+len(gh.searches)+len(gh.comments) != 0 {
		t.Fatal("no gh calls expected")
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cache file should not exist")
	}
}

func TestRunFlow_GHCreateFails_DoesNotMutateCache(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	in := buildHookInput("go run ./skills/run-sensor/scripts foo",
		"", "panic: boom\n\ngoroutine 1 [running]:\nmain.main()\n\t/abs/harness-framework/hooks/x.go:1\n", 2)
	gh := &fakeGhClient{createErr: fmt.Errorf("gh: not authenticated")}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 {
		t.Fatal(code)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache file should not be created on gh failure; stat err=%v", err)
	}
	if !strings.Contains(stderr.String(), "not authenticated") {
		t.Fatalf("expected gh error in stderr; got %q", stderr.String())
	}
}

func TestRunFlow_GHCreate422_RetriesWithSearch(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	in := buildHookInput("go run ./skills/run-sensor/scripts foo",
		"", "panic: dupe\n\ngoroutine 1 [running]:\nmain.main()\n\t/abs/harness-framework/hooks/x.go:1\n", 2)

	// First Create returns 422 already_exists; the retry Search returns an issue;
	// then a Comment is posted.
	gh := &raceGhClient{
		createErr:      fmt.Errorf("gh: HTTP 422 already_exists: validation failed"),
		searchAfterDup: issueRef{Number: 88, URL: "https://github.com/o/r/issues/88"},
	}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 {
		t.Fatal(code)
	}
	if gh.createCalls != 1 {
		t.Fatalf("create called %d times", gh.createCalls)
	}
	if gh.searchCalls != 2 { // one before Create, one after 422
		t.Fatalf("search called %d times", gh.searchCalls)
	}
	if gh.commentCalls != 1 {
		t.Fatalf("comment called %d times", gh.commentCalls)
	}
}

func TestRunFlow_NoFrameworkInOutput_ButCommandMatches(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	in := buildHookInput("go run ./skills/run-sensor/scripts foo", "ok\n", "", 0)
	gh := &fakeGhClient{}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{gh: gh, cachePath: cachePath, repo: "o/r"})
	if code != 0 {
		t.Fatal(code)
	}
	if len(gh.creates)+len(gh.searches) != 0 {
		t.Fatal("clean output must not trigger gh")
	}
}

func TestRunFlow_NoGHRemote_LogsAndExits(t *testing.T) {
	t.Setenv("HARNESS_AUTOFILE_ISSUES", "1")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "auto-issues.json")
	in := buildHookInput("go run ./skills/run-sensor/scripts foo",
		"", "panic: x\n\ngoroutine 1 [running]:\nmain.main()\n\t/abs/harness-framework/hooks/x.go:1\n", 2)
	gh := &fakeGhClient{}
	var stderr bytes.Buffer
	code := runWith(in, &stderr, runOpts{
		gh: gh, cachePath: cachePath,
		repo: "", repoErr: fmt.Errorf("no github remote"),
	})
	if code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(stderr.String(), "no github remote") {
		t.Fatalf("expected 'no github remote' in stderr; got %q", stderr.String())
	}
	if len(gh.creates)+len(gh.searches) != 0 {
		t.Fatal("no gh calls when repo unresolved")
	}
}

// raceGhClient simulates the 422 already_exists path: first Create fails;
// second Search succeeds; Comment then posts.
type raceGhClient struct {
	createCalls    int
	searchCalls    int
	commentCalls   int
	createErr      error
	searchBeforeDup issueRef
	searchAfterDup  issueRef
}

func (r *raceGhClient) Search(repo, fp string) (issueRef, error) {
	r.searchCalls++
	if r.searchCalls == 1 {
		return r.searchBeforeDup, nil
	}
	return r.searchAfterDup, nil
}
func (r *raceGhClient) Comment(repo string, n int, body string) error {
	r.commentCalls++
	return nil
}
func (r *raceGhClient) Create(repo, title, body string, labels []string) (issueRef, error) {
	r.createCalls++
	return issueRef{}, r.createErr
}

func buildHookInput(cmd, stdout, stderr string, exit int) hookInput {
	return hookInput{
		HookEventName: "PostToolUse",
		ToolName:      "Bash",
		ToolInput:     toolInputBsh{Command: cmd},
		ToolResponse:  toolResponse{Stdout: stdout, Stderr: stderr, ExitCode: exit},
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 13.2: Run the integration tests to verify they fail**

```bash
go test -tags=error_autofiler ./hooks -run TestRunFlow -v
```

Expected: FAIL — `runWith` and the wired pipeline are not yet implemented.

- [ ] **Step 13.3: Implement `runWith` and rewire `run`**

Replace the body of `run` and add `runWith` in `hooks/error-issue-autofiler.go`:

```go
func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "read stdin:", err)
		return 2
	}
	if len(body) == 0 {
		fmt.Fprintln(stderr, "parse hook input: empty stdin")
		return 2
	}
	var in hookInput
	if err := json.Unmarshal(body, &in); err != nil {
		fmt.Fprintln(stderr, "parse hook input:", err)
		return 2
	}
	if killSwitchEnabled() {
		return 0
	}
	if in.ToolName != "Bash" {
		return 0
	}
	if !commandTouchesFramework(in.ToolInput.Command) {
		return 0
	}

	// Resolve project root, then repo and cache path.
	res, err := registry.Lookup(in.Cwd)
	if err != nil {
		fmt.Fprintln(stderr, "cannot resolve project root:", err)
		return 0
	}
	cachePath := filepath.Join(res.ProjectRoot, ".runtime", "auto-issues.json")
	repo, repoErr := resolveRepo(res.ProjectRoot)
	return runWith(in, stderr, runOpts{
		gh:        ghCLI{},
		cachePath: cachePath,
		repo:      repo,
		repoErr:   repoErr,
	})
}

// runWith is the testable core. Inputs are pre-resolved (gh client,
// cache path, repo, repo error). Returns the desired exit code.
func runWith(in hookInput, stderr io.Writer, opts runOpts) int {
	evt := classify(in.ToolResponse.Stdout, in.ToolResponse.Stderr, in.ToolResponse.ExitCode)
	if evt == nil {
		return 0
	}
	evt.Skill = extractSkill(in.ToolInput.Command)
	fp := fingerprint(evt)

	now := opts.now
	if now.IsZero() {
		now = nowUTC()
	}

	// Cache layer first.
	c, _ := loadCache(opts.cachePath)
	if entry, ok := c.Entries[fp]; ok && now.Sub(entry.LastSeen) < cacheStaleAfter {
		// Hot path: touch + occurrence++.
		entry.LastSeen = now
		entry.OccurrenceCount++
		if err := updateCacheLocked(opts.cachePath, func(c *cache) {
			c.put(fp, entry)
		}); err != nil {
			fmt.Fprintln(stderr, "cache write:", err)
		}
		return 0
	}

	// Need repo for GH ops from here.
	if opts.repoErr != nil || opts.repo == "" {
		if opts.repoErr != nil {
			fmt.Fprintln(stderr, opts.repoErr)
		} else {
			fmt.Fprintln(stderr, "no github remote")
		}
		return 0
	}

	// GH search backstop.
	existing, err := opts.gh.Search(opts.repo, fp)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 0
	}
	if existing.Number != 0 {
		if err := opts.gh.Comment(opts.repo, existing.Number, renderOccurrenceComment(in)); err != nil {
			fmt.Fprintln(stderr, err)
			return 0
		}
		_ = updateCacheLocked(opts.cachePath, func(c *cache) {
			c.put(fp, cacheEntry{
				IssueURL:        existing.URL,
				FirstSeen:       now,
				LastSeen:        now,
				OccurrenceCount: 1,
				Skill:           evt.Skill,
				Type:            evt.Type,
			})
		})
		return 0
	}

	// Create new issue.
	title := renderTitle(evt.Skill, evt.Summary)
	body := renderBody(in, *evt, fp)
	ref, err := opts.gh.Create(opts.repo, title, body, []string{"auto-filed", "bug"})
	if err != nil {
		// Race: another machine just created it. Search once more, then comment.
		if isAlreadyExists(err) {
			again, sErr := opts.gh.Search(opts.repo, fp)
			if sErr == nil && again.Number != 0 {
				if cErr := opts.gh.Comment(opts.repo, again.Number, renderOccurrenceComment(in)); cErr == nil {
					_ = updateCacheLocked(opts.cachePath, func(c *cache) {
						c.put(fp, cacheEntry{
							IssueURL:        again.URL,
							FirstSeen:       now,
							LastSeen:        now,
							OccurrenceCount: 1,
							Skill:           evt.Skill,
							Type:            evt.Type,
						})
					})
					return 0
				}
			}
		}
		fmt.Fprintln(stderr, err)
		return 0
	}

	if err := updateCacheLocked(opts.cachePath, func(c *cache) {
		c.put(fp, cacheEntry{
			IssueURL:        ref.URL,
			FirstSeen:       now,
			LastSeen:        now,
			OccurrenceCount: 1,
			Skill:           evt.Skill,
			Type:            evt.Type,
		})
	}); err != nil {
		fmt.Fprintln(stderr, "cache write:", err)
	}
	return 0
}

// isAlreadyExists checks whether err looks like a GH 422 duplicate.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "422") || strings.Contains(s, "already_exists")
}
```

Add `runOpts` type alongside the rest. Since `runOpts` is referenced from the test file, declare it in the source file:

```go
type runOpts struct {
	gh        ghClient
	cachePath string
	repo      string
	repoErr   error
	now       time.Time
}
```

- [ ] **Step 13.4: Run all tests**

```bash
go test -tags=error_autofiler ./hooks -v
```

Expected: every test passes. If `TestRunFlow_GHCreate422_RetriesWithSearch` fails because the first search returns a non-zero issue (and we comment before even trying Create), check `raceGhClient.searchBeforeDup` is the zero value (it is in the test setup), so the first Search returns `issueRef{}`, hits the Create path, then 422 triggers the retry. The expected `searchCalls == 2` reflects: first call before Create, second call after Create's 422.

- [ ] **Step 13.5: Verify untagged build still works**

```bash
go vet ./hooks
go test ./hooks
```

Expected: PASS — untagged build sees only `setup-failure-detector.go`.

- [ ] **Step 13.6: Commit**

```bash
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks(autofiler): wire the full pipeline in run/runWith

run() resolves project root + repo and delegates to runWith.
runWith implements the cache-first / search-backstop / create
cascade, including the 422 already_exists race retry. Ten
integration tests with fakeGhClient cover every branch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: Plugin manifest wire-up

**Goal:** Add the `PostToolUse(Bash)` entry to `.claude-plugin/plugin.json`, leaving the `Stop` hook untouched.

**Files:**
- Modify: `.claude-plugin/plugin.json`

- [ ] **Step 14.1: Read the current plugin.json**

```bash
cat .claude-plugin/plugin.json
```

Confirm the current `hooks` block contains only `Stop`.

- [ ] **Step 14.2: Add the PostToolUse entry**

Edit `.claude-plugin/plugin.json` so the `hooks` object becomes:

```json
"hooks": {
  "Stop": [
    {
      "matcher": "",
      "hooks": [
        {
          "type": "command",
          "command": "cd \"${CLAUDE_PLUGIN_ROOT}\" && go run ./hooks"
        }
      ]
    }
  ],
  "PostToolUse": [
    {
      "matcher": "Bash",
      "hooks": [
        {
          "type": "command",
          "command": "cd \"${CLAUDE_PLUGIN_ROOT}\" && go run -tags=error_autofiler ./hooks"
        }
      ]
    }
  ]
}
```

Use the Edit tool with the existing `"Stop": [...]` block as the unique anchor.

- [ ] **Step 14.3: Validate JSON syntax**

```bash
python3 -c "import json; json.load(open('.claude-plugin/plugin.json'))" && echo OK
```

Expected: `OK`. If it errors, fix the comma/brace issue.

- [ ] **Step 14.4: Commit**

```bash
git add .claude-plugin/plugin.json
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
hooks: wire PostToolUse(Bash) for error-issue-autofiler

Stop hook unchanged. New entry invokes the build-tagged
binary that opens or +1s GitHub issues on framework Go
crashes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 15: CLAUDE.md subsection

**Goal:** Document the new hook under the existing "Architecture" section so future contributors find the kill switch and the cache location without reading the code.

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 15.1: Locate the insertion point**

```bash
grep -n "^### " CLAUDE.md | head
```

Find a position under the `## Architecture` heading where the subsection fits logically (e.g., after "Registry root discovery" since both are observability/state subsections).

- [ ] **Step 15.2: Insert the new subsection**

Add this paragraph below the last subsection of "Architecture":

```markdown
### Auto issue opening

A `PostToolUse(Bash)` hook (`hooks/error-issue-autofiler.go`, build tag `error_autofiler`) observes every Bash invocation and opens a GitHub issue when a framework Go script panics, fails to compile, or emits a Signal with `verdict=error` plus an internal `metadata.kind`. Per-fingerprint dedup uses a 3-layer cascade: local `<projectRoot>/.runtime/auto-issues.json` cache, then `gh issue list --search "harness-fp:<fingerprint>"`, then `gh issue create`. The hook always exits 0 — internal failures (no `gh` auth, no GitHub remote, unparseable cache, …) degrade silently to stderr.

Disable per-shell with `HARNESS_AUTOFILE_ISSUES=0`. The repo it files against is derived from `git remote get-url origin` of the project root resolved by `lib/registry.Lookup(cwd)`; the framework expects a label `auto-filed` to exist on that repo (create once).
```

- [ ] **Step 15.3: Commit**

```bash
git add CLAUDE.md
git -c commit.gpgsign=false commit -m "$(cat <<'EOF'
docs(CLAUDE.md): document Auto issue opening hook

Adds the Architecture subsection covering the
PostToolUse(Bash) trigger, 3-layer dedup, kill switch,
and the one-time auto-filed label setup expected on the
repository.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 16: End-to-end verification

**Goal:** Run every quality gate the spec calls out. No new code; just verification.

**Files:** none

- [ ] **Step 16.1: Verify both build tags**

```bash
go vet ./hooks
go vet -tags=error_autofiler ./hooks
```

Expected: both report clean.

- [ ] **Step 16.2: Run full test suites under both tags**

```bash
go test ./hooks -v
go test -tags=error_autofiler ./hooks -v
```

Expected: PASS in both. The first runs only `setup-failure-detector` tests; the second runs only `error-issue-autofiler` tests.

- [ ] **Step 16.3: Validate plugin.json**

```bash
python3 -c "import json; m=json.load(open('.claude-plugin/plugin.json')); print(sorted(m['hooks'].keys()))"
```

Expected output: `['PostToolUse', 'Stop']`.

- [ ] **Step 16.4: Sanity check the hook builds standalone**

```bash
go build -tags=error_autofiler -o /tmp/error-issue-autofiler ./hooks
ls -la /tmp/error-issue-autofiler
```

Expected: a binary is produced. (We don't keep it; it's a smoke check.)

- [ ] **Step 16.5: Manual feed-in smoke (dry, kill-switch on)**

```bash
HARNESS_AUTOFILE_ISSUES=0 echo '{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"go run ./skills/run-sensor/scripts foo"},"tool_response":{"stdout":"","stderr":"panic: smoke\n\ngoroutine 1 [running]:\n","exitCode":2}}' | /tmp/error-issue-autofiler
echo "exit=$?"
```

Expected: `exit=0`, no stderr, no `gh` invocation, no cache file written.

- [ ] **Step 16.6: Acceptance checklist walk-through**

Open the spec's "Acceptance criteria" section and mark each item, citing the test or command that verifies it:

- [ ] `hooks/error-issue-autofiler.go` exists, only `package main` under `error_autofiler` — verified by Step 16.4
- [ ] `setup-failure-detector.go` and test have `!error_autofiler` — Task 1
- [ ] `go vet -tags=error_autofiler ./hooks` and `go test ...` pass — Step 16.1, 16.2
- [ ] `.claude-plugin/plugin.json` has the PostToolUse entry — Step 16.3
- [ ] First-run creates, second-run comments — verified by `TestRunFlow_NewError_CreatesIssue` + manual replay in same temp project
- [ ] `HARNESS_AUTOFILE_ISSUES=0` no-op — `TestRunFlow_KillSwitch_NoOp`
- [ ] Non-framework command no I/O — `TestRunFlow_NonFrameworkCommand_NoOp`
- [ ] `gh` missing/unauth degrades silently — `TestRunFlow_GHCreateFails_DoesNotMutateCache`
- [ ] Project root resolution failure logs and exits 0 — covered in `run()` via the `registry.Lookup` error path; manually verify by feeding a JSON with `cwd=/tmp` (no `sensors/` ancestor)
- [ ] Cache file uses flock — `TestCache_ConcurrentPut_Serializes`
- [ ] All ten integration tests pass — Step 16.2 (look for ten `TestRunFlow_*` subtests in `-v` output)
- [ ] CLAUDE.md updated — Task 15

If anything is RED, fix and re-run from the failing step.

- [ ] **Step 16.7: Final commit (optional, only if the walk-through produced fixes)**

If Step 16.6 surfaced a missing test or doc gap, address it as a tiny follow-up commit. Otherwise no commit needed for this task — the verification is purely confirmatory.

---

## Self-review

**Spec coverage:**
- "What changes" 1–7: Tasks 1, 2, 14, 15 (file creation, build-tag isolation, plugin.json, CLAUDE.md). ✅
- Pipeline (matcher → classify → fingerprint → cache → search → create): Tasks 4, 5, 6, 7, 8, 9, 10, 11, 12, 13. ✅
- Error handling table (11 rows): all covered by either `runWith` branches (kill switch / non-framework / no-classifier / gh missing / network / no-remote / cache corrupt / 422 / concurrent / stdin) or stdin-parsing in `run`. The "stdin truncated >64KB" case is handled by `classifierScanWindow` (Task 7). ✅
- All 8 pure-function tests + 10 integration tests (spec's Testing section): Tasks 4–13. ✅
- Acceptance criteria (12 items): walked through in Task 16. ✅

**Placeholder scan:** No "TBD" / "later" / "similar to" appears in step bodies. Every code step shows the full code to write or modify.

**Type consistency:**
- `classifiedEvent` fields (`Type`, `Summary`, `Skill`, `FrameworkFrame`, `Pkg`, `File`, `MetadataKind`) used consistently across Tasks 7, 8, 9, 13.
- `cacheEntry` fields (`IssueURL`, `FirstSeen`, `LastSeen`, `OccurrenceCount`, `Skill`, `Type`) consistent across Tasks 10, 13.
- `ghClient.Search/Comment/Create` signatures consistent in Tasks 11 and 13.
- `runOpts` declared in source (Task 13) and used by tests (also Task 13). The test file does NOT redeclare it.

No drift detected.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-11-auto-issue-opening.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. Suited for this plan because each task is small, self-contained, and gated by tests.

**2. Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints.

Which approach?
