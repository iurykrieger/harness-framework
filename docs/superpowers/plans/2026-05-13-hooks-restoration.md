# Hooks restoration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore both Claude Code hooks (`setup-failure-detector` and `error-issue-autofiler`) to a working state by extracting a shared `lib/transcript` parser that understands the real JSONL transcript shape, fixing the autofiler regex's `-C` placement and dropping its dead binary patterns, and bringing the `.claude-plugin/plugin.json` invocation in line with the canonical `go run -C` contract documented in CLAUDE.md.

**Architecture:** A new `lib/transcript` package becomes the single source of truth for reading `~/.claude/projects/<slug>/<sessionID>.jsonl` files; the `Stop` hook consumes it. The `PostToolUse(Bash)` hook stays self-contained but its regex is repaired and its dead binary branches removed. Plugin manifest is updated to use `GOWORK=off go run -C "${CLAUDE_PLUGIN_ROOT}"`.

**Tech Stack:** Go 1.25, `encoding/json`, `regexp`, `testing`, Claude Code plugin hooks (Stop, PostToolUse), `gh` CLI (already wired).

**Spec:** `docs/superpowers/specs/2026-05-13-hooks-restoration-design.md`

---

## File Structure

**New files:**
- `lib/transcript/entry.go` — `Entry`, `Message`, `Block` types and accessors (`Text()`, `ToolUses()`, `ToolResults()`).
- `lib/transcript/scan.go` — `Scan(path) ([]Entry, error)` reads the JSONL.
- `lib/transcript/slashcmd.go` — `ParseSlashCommand(text) (name, args string, ok bool)`.
- `lib/transcript/entry_test.go` — table-driven tests of accessors.
- `lib/transcript/scan_test.go` — fixture-based tests of `Scan`.
- `lib/transcript/slashcmd_test.go` — table-driven tests of the parser.
- `lib/transcript/testdata/run-sensor-error.jsonl` — real-shape transcript: `/run-sensor` → Bash → tool_result with aggregate Signal (verdict=error, missing env).
- `lib/transcript/testdata/run-sensor-pass.jsonl` — same shape, verdict=pass.
- `lib/transcript/testdata/cascade-failed-dep.jsonl` — dep aggregate + cascade aggregate in one tool_result.
- `lib/transcript/testdata/already-healed.jsonl` — failure followed by `/heal-sensor` invocation.
- `lib/transcript/testdata/empty.jsonl` — zero entries (edge case).
- `lib/transcript/transcripttest/load.go` — `Load(t, name) (entries []transcript.Entry, cwd string)` test helper.

**Modified files:**
- `hooks/setup-failure-detector.go` — rewritten over `lib/transcript`. Drops local `transcriptEntry`, `contentText`. Slash-command match via `transcript.ParseSlashCommand`.
- `hooks/setup-failure-detector_test.go` — switched to `transcripttest.Load`. Old inline transcript builders removed.
- `hooks/error-issue-autofiler.go` — regex repair (`-C` after `run`), dead binary branches removed.
- `hooks/error-issue-autofiler_test.go` — test cases moved to canonical command shape; `harness-*` cases dropped; new negative case for the legacy `cd && go run` form.
- `.claude-plugin/plugin.json` — both hook commands rewritten to `GOWORK=off go run -C "${CLAUDE_PLUGIN_ROOT}" [-tags=…] ./hooks`.

**Untouched (anti-scope):** `lib/heal/*`, `lib/registry/*`, all sensor runner scripts, all schemas.

---

## Task 1: lib/transcript — types and accessors

**Files:**
- Create: `lib/transcript/entry.go`
- Create: `lib/transcript/entry_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/transcript/entry_test.go`:

```go
package transcript

import (
	"encoding/json"
	"testing"
)

func TestEntry_Text_StringContent(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"role":"user","content":"hello"}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if got := e.Text(); got != "hello" {
		t.Fatalf("Text()=%q want %q", got, "hello")
	}
}

func TestEntry_Text_BlockArrayContent(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"tool_use","id":"u1","name":"Bash","input":{"command":"ls"}},{"type":"text","text":"second"}]}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	if got := e.Text(); got != "firstsecond" {
		t.Fatalf("Text()=%q want %q (text blocks only, concatenated)", got, "firstsecond")
	}
}

func TestEntry_ToolUses(t *testing.T) {
	raw := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"u1","name":"Bash","input":{"command":"go run ./x"}}]}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	uses := e.ToolUses()
	if len(uses) != 1 {
		t.Fatalf("got %d tool_uses, want 1", len(uses))
	}
	if uses[0].Name != "Bash" {
		t.Fatalf("Name=%q", uses[0].Name)
	}
	if got := string(uses[0].Input); got != `{"command":"go run ./x"}` {
		t.Fatalf("Input=%s", got)
	}
}

func TestEntry_ToolResults_StringContent(t *testing.T) {
	raw := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":"line1\nline2"}]}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	results := e.ToolResults()
	if len(results) != 1 {
		t.Fatalf("got %d tool_results, want 1", len(results))
	}
	if got := results[0].ResultText(); got != "line1\nline2" {
		t.Fatalf("ResultText()=%q", got)
	}
}

func TestEntry_ToolResults_BlockArrayContent(t *testing.T) {
	// Real Claude Code sometimes nests tool_result content as a block array.
	raw := []byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","content":[{"type":"text","text":"line1\nline2"}]}]}}`)
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatal(err)
	}
	results := e.ToolResults()
	if len(results) != 1 {
		t.Fatalf("got %d tool_results, want 1", len(results))
	}
	if got := results[0].ResultText(); got != "line1\nline2" {
		t.Fatalf("ResultText()=%q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./lib/transcript/...
```
Expected: FAIL — package does not compile (Entry, Block, etc. not defined).

- [ ] **Step 3: Implement minimal types**

Create `lib/transcript/entry.go`:

```go
// Package transcript reads Claude Code session transcripts in their
// real on-disk JSONL shape and exposes a small, ergonomic API to
// callers (the Stop hook today, future hooks tomorrow).
//
// The JSONL shape we care about:
//
//	{"type":"user","message":{"role":"user","content":"…"}}
//	{"type":"assistant","message":{"role":"assistant","content":[{"type":"text",…},{"type":"tool_use",…}]}}
//	{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"…","content":"…"|[…]}]}}
//
// Unknown top-level fields are ignored (encoding/json default).
package transcript

import (
	"encoding/json"
	"strings"
)

type Entry struct {
	Type    string  `json:"type"`
	Message Message `json:"message"`
}

type Message struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

// Content holds either a plain string ("…") or a list of typed blocks.
// Exactly one of String or Blocks is populated after UnmarshalJSON.
type Content struct {
	String string
	Blocks []Block
}

func (c *Content) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		c.String = s
		c.Blocks = nil
		return nil
	}
	var blocks []Block
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	c.String = ""
	c.Blocks = blocks
	return nil
}

type Block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`       // type="text"
	ToolUseID string          `json:"tool_use_id,omitempty"` // type="tool_result"
	ID        string          `json:"id,omitempty"`         // type="tool_use"
	Name      string          `json:"name,omitempty"`       // type="tool_use"
	Input     json.RawMessage `json:"input,omitempty"`      // type="tool_use"
	// Content is the raw value of a tool_result's nested content,
	// which is itself either a string or a block array. Decode lazily
	// via ResultText().
	Content json.RawMessage `json:"content,omitempty"` // type="tool_result"
}

// ResultText returns the textual payload of a tool_result block,
// flattening a nested block array if present.
func (b Block) ResultText() string {
	if len(b.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(b.Content, &s); err == nil {
		return s
	}
	var blocks []Block
	if err := json.Unmarshal(b.Content, &blocks); err == nil {
		var out strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" {
				out.WriteString(blk.Text)
			}
		}
		return out.String()
	}
	return ""
}

// Text returns the flat textual content of an Entry's message,
// concatenating type="text" blocks when content is a block array,
// or the raw string when content is a string.
func (e Entry) Text() string {
	if e.Message.Content.String != "" {
		return e.Message.Content.String
	}
	var out strings.Builder
	for _, b := range e.Message.Content.Blocks {
		if b.Type == "text" {
			out.WriteString(b.Text)
		}
	}
	return out.String()
}

// ToolUses returns the tool_use blocks in an Entry's message content,
// empty when the content is a plain string or contains no tool_uses.
func (e Entry) ToolUses() []Block {
	var out []Block
	for _, b := range e.Message.Content.Blocks {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

// ToolResults returns the tool_result blocks in an Entry's message
// content, empty when the content is a plain string or contains no
// tool_results.
func (e Entry) ToolResults() []Block {
	var out []Block
	for _, b := range e.Message.Content.Blocks {
		if b.Type == "tool_result" {
			out = append(out, b)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./lib/transcript/...
```
Expected: PASS, 5 tests.

- [ ] **Step 5: Commit**

```
git add lib/transcript/entry.go lib/transcript/entry_test.go
git commit -m "feat(transcript): Entry/Message/Block types with content-shape tolerance"
```

---

## Task 2: lib/transcript — Scan from disk

**Files:**
- Create: `lib/transcript/scan.go`
- Create: `lib/transcript/scan_test.go`
- Create: `lib/transcript/testdata/empty.jsonl`
- Create: `lib/transcript/testdata/run-sensor-error.jsonl`
- Create: `lib/transcript/testdata/run-sensor-pass.jsonl`
- Create: `lib/transcript/testdata/already-healed.jsonl`
- Create: `lib/transcript/testdata/cascade-failed-dep.jsonl`

- [ ] **Step 1: Write fixture files**

Create `lib/transcript/testdata/empty.jsonl`:
```

```
(zero bytes)

Create `lib/transcript/testdata/run-sensor-error.jsonl`:
```
{"type":"user","message":{"role":"user","content":"<command-message>run-sensor</command-message>\n<command-name>/run-sensor</command-name>\n<command-args>watch-logs</command-args>"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Running watch-logs sensor."},{"type":"tool_use","id":"u_001","name":"Bash","input":{"command":"GOWORK=off go run -C /plugin -tags=run_computational ./skills/run-sensor/scripts watch-logs","description":"Run watch-logs sensor"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u_001","content":"{\"sensor_id\":\"watch-logs\",\"verdict\":\"error\",\"severity\":\"high\",\"evidence\":[{\"rationale\":\"Required environment variable RSA_PRIVATE_KEY not set\"}],\"metadata\":{\"kind\":\"aggregate\"}}"}]}}
```

Create `lib/transcript/testdata/run-sensor-pass.jsonl`:
```
{"type":"user","message":{"role":"user","content":"<command-message>run-sensor</command-message>\n<command-name>/run-sensor</command-name>\n<command-args>lint-gofmt</command-args>"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"u_002","name":"Bash","input":{"command":"GOWORK=off go run -C /plugin -tags=run_computational ./skills/run-sensor/scripts lint-gofmt"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u_002","content":"{\"sensor_id\":\"lint-gofmt\",\"verdict\":\"pass\",\"severity\":\"info\",\"metadata\":{\"kind\":\"aggregate\"}}"}]}}
```

Create `lib/transcript/testdata/already-healed.jsonl`:
```
{"type":"user","message":{"role":"user","content":"<command-message>run-sensor</command-message>\n<command-name>/run-sensor</command-name>\n<command-args>watch-logs</command-args>"}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u_003","content":"{\"sensor_id\":\"watch-logs\",\"verdict\":\"error\",\"severity\":\"high\",\"evidence\":[{\"rationale\":\"Required environment variable RSA_PRIVATE_KEY not set\"}],\"metadata\":{\"kind\":\"aggregate\"}}"}]}}
{"type":"user","message":{"role":"user","content":"<command-message>heal-sensor</command-message>\n<command-name>/heal-sensor</command-name>\n<command-args>--signal-from=transcript --sensor=watch-logs</command-args>"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Healed watch-logs by setting RSA_PRIVATE_KEY."}]}}
```

Create `lib/transcript/testdata/cascade-failed-dep.jsonl`:
```
{"type":"user","message":{"role":"user","content":"<command-message>run-sensor</command-message>\n<command-name>/run-sensor</command-name>\n<command-args>run-project</command-args>"}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"u_004","content":"{\"sensor_id\":\"setup-env\",\"verdict\":\"error\",\"severity\":\"high\",\"evidence\":[{\"rationale\":\"Required environment variable RSA_PRIVATE_KEY not set\"}],\"metadata\":{\"kind\":\"aggregate\"}}\n{\"sensor_id\":\"run-project\",\"verdict\":\"error\",\"severity\":\"high\",\"evidence\":[],\"metadata\":{\"kind\":\"cascade\",\"failed_dep_id\":\"setup-env\"}}"}]}}
```

- [ ] **Step 2: Write the failing test**

Create `lib/transcript/scan_test.go`:

```go
package transcript

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestScan_Empty(t *testing.T) {
	entries, err := Scan(filepath.Join("testdata", "empty.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries; want 0", len(entries))
	}
}

func TestScan_RunSensorError(t *testing.T) {
	entries, err := Scan(filepath.Join("testdata", "run-sensor-error.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries; want 3", len(entries))
	}
	// 0: user slash command
	if got := entries[0].Text(); !strings.Contains(got, "<command-name>/run-sensor</command-name>") {
		t.Fatalf("entry 0 Text=%q", got)
	}
	// 1: assistant with tool_use
	uses := entries[1].ToolUses()
	if len(uses) != 1 || uses[0].Name != "Bash" {
		t.Fatalf("entry 1 ToolUses=%+v", uses)
	}
	// 2: user with tool_result containing aggregate JSONL
	results := entries[2].ToolResults()
	if len(results) != 1 {
		t.Fatalf("entry 2 ToolResults=%+v", results)
	}
	body := results[0].ResultText()
	if !strings.Contains(body, `"verdict":"error"`) {
		t.Fatalf("tool_result text missing verdict: %q", body)
	}
}

func TestScan_NonexistentPath(t *testing.T) {
	_, err := Scan("does-not-exist.jsonl")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```
go test ./lib/transcript/... -run TestScan
```
Expected: FAIL — `Scan` undefined.

- [ ] **Step 4: Implement Scan**

Create `lib/transcript/scan.go`:

```go
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// Scan reads a Claude Code transcript JSONL file and returns one Entry
// per parseable line. Lines that fail to decode are skipped silently —
// transcripts contain heterogeneous record types and we only care about
// user/assistant messages.
func Scan(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("transcript open %s: %w", path, err)
	}
	defer f.Close()

	var out []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("transcript scan %s: %w", path, err)
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

```
go test ./lib/transcript/...
```
Expected: PASS — all 8 tests.

- [ ] **Step 6: Commit**

```
git add lib/transcript/scan.go lib/transcript/scan_test.go lib/transcript/testdata/
git commit -m "feat(transcript): Scan and real-shape fixtures"
```

---

## Task 3: lib/transcript — ParseSlashCommand

**Files:**
- Create: `lib/transcript/slashcmd.go`
- Create: `lib/transcript/slashcmd_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/transcript/slashcmd_test.go`:

```go
package transcript

import "testing"

func TestParseSlashCommand(t *testing.T) {
	cases := []struct {
		in       string
		wantName string
		wantArgs string
		wantOK   bool
	}{
		{
			in:       "<command-message>run-sensor</command-message>\n<command-name>/run-sensor</command-name>\n<command-args>watch-logs</command-args>",
			wantName: "/run-sensor",
			wantArgs: "watch-logs",
			wantOK:   true,
		},
		{
			in:       "<command-name>/heal-sensor</command-name>\n<command-args>--signal-from=transcript --sensor=watch-logs</command-args>",
			wantName: "/heal-sensor",
			wantArgs: "--signal-from=transcript --sensor=watch-logs",
			wantOK:   true,
		},
		{
			in:       "<command-name>/list-sensors</command-name>\n<command-args></command-args>",
			wantName: "/list-sensors",
			wantArgs: "",
			wantOK:   true,
		},
		{"plain text, no command", "", "", false},
		{"<command-name>foo</command-name>", "", "", false}, // missing leading slash
		{"", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.wantName+"::"+tc.wantArgs, func(t *testing.T) {
			name, args, ok := ParseSlashCommand(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v want %v", ok, tc.wantOK)
			}
			if name != tc.wantName {
				t.Fatalf("name=%q want %q", name, tc.wantName)
			}
			if args != tc.wantArgs {
				t.Fatalf("args=%q want %q", args, tc.wantArgs)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```
go test ./lib/transcript/... -run TestParseSlashCommand
```
Expected: FAIL — `ParseSlashCommand` undefined.

- [ ] **Step 3: Implement ParseSlashCommand**

Create `lib/transcript/slashcmd.go`:

```go
package transcript

import "regexp"

var (
	reSlashName = regexp.MustCompile(`<command-name>(/[^<]+)</command-name>`)
	reSlashArgs = regexp.MustCompile(`<command-args>([^<]*)</command-args>`)
)

// ParseSlashCommand extracts the slash command name and arguments from
// the Claude Code wrapping convention:
//
//	<command-message>run-sensor</command-message>
//	<command-name>/run-sensor</command-name>
//	<command-args>my-sensor</command-args>
//
// Returns (name, args, true) on a successful match, ("", "", false)
// otherwise. The leading slash is preserved in name. args may be
// the empty string (a slash command invoked without arguments).
func ParseSlashCommand(s string) (name string, args string, ok bool) {
	m := reSlashName.FindStringSubmatch(s)
	if m == nil {
		return "", "", false
	}
	name = m[1]
	if a := reSlashArgs.FindStringSubmatch(s); a != nil {
		args = a[1]
	}
	return name, args, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./lib/transcript/...
```
Expected: PASS — all transcript tests.

- [ ] **Step 5: Commit**

```
git add lib/transcript/slashcmd.go lib/transcript/slashcmd_test.go
git commit -m "feat(transcript): ParseSlashCommand for <command-name>/x</command-name>"
```

---

## Task 4: lib/transcript/transcripttest — cross-package test helper

**Files:**
- Create: `lib/transcript/transcripttest/load.go`

- [ ] **Step 1: Implement the helper**

Create `lib/transcript/transcripttest/load.go`:

```go
// Package transcripttest is a Go test helper for consumers of
// lib/transcript that need fixture-loaded entries. Follows the
// convention of net/http/httptest and testing/iotest: a sibling
// _test_helper_ package whose only client is *_test.go files in
// the broader codebase. Per project rule 11, this is the canonical
// way to share transcript fixtures across packages.
package transcripttest

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/transcript"
)

// Load reads a fixture JSONL file from lib/transcript/testdata/<name>
// (resolved relative to this package's source location) and returns
// the parsed Entry slice. Path resolution uses runtime.Caller so the
// helper works regardless of the test's working directory.
func Load(t *testing.T, name string) []transcript.Entry {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file is .../lib/transcript/transcripttest/load.go
	// testdata is at .../lib/transcript/testdata/
	dir := filepath.Dir(filepath.Dir(file))
	path := filepath.Join(dir, "testdata", name)
	entries, err := transcript.Scan(path)
	if err != nil {
		t.Fatalf("transcripttest.Load(%q): %v", name, err)
	}
	return entries
}

// Path returns the absolute path of a testdata fixture without parsing
// it. Useful when a consumer needs to pass the path to a function that
// re-opens the file (e.g. setup-failure-detector's hook input).
func Path(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(filepath.Dir(file))
	return filepath.Join(dir, "testdata", name)
}
```

- [ ] **Step 2: Verify it compiles**

```
go build ./lib/transcript/transcripttest/...
```
Expected: success (no errors).

- [ ] **Step 3: Commit**

```
git add lib/transcript/transcripttest/load.go
git commit -m "feat(transcripttest): Load and Path helpers for cross-package fixtures"
```

---

## Task 5: setup-failure-detector — rewrite over lib/transcript

**Files:**
- Modify: `hooks/setup-failure-detector.go`
- Modify: `hooks/setup-failure-detector_test.go`

- [ ] **Step 1: Rewrite the test file**

Replace the entire contents of `hooks/setup-failure-detector_test.go`:

```go
//go:build !error_autofiler

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/transcript/transcripttest"
)

// withSensor writes a sensor JSON at <cwd>/.harness/sensors/<id>.json so
// loadFailedSensorView can find it. Returns the cwd to feed into the
// hook input.
func withSensor(t *testing.T, id string, requires []map[string]interface{}) string {
	t.Helper()
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]interface{}{
		"id":       id,
		"requires": requires,
	})
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd
}

func TestHook_RunSensorError_EmitsHealInjection(t *testing.T) {
	cwd := withSensor(t, "watch-logs", []map[string]interface{}{
		{"kind": "env", "name": "RSA_PRIVATE_KEY"},
	})
	transcriptPath := transcripttest.Path(t, "run-sensor-error.jsonl")
	hookIn, _ := json.Marshal(map[string]string{
		"transcript_path": transcriptPath,
		"cwd":             cwd,
	})

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader(hookIn), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "/heal-sensor") {
		t.Fatalf("expected injection mentioning /heal-sensor; got %q", out)
	}
	if !strings.Contains(out, "watch-logs") {
		t.Fatalf("expected sensor id in injection; got %q", out)
	}
}

func TestHook_RunSensorPass_NoInjection(t *testing.T) {
	cwd := withSensor(t, "lint-gofmt", nil)
	transcriptPath := transcripttest.Path(t, "run-sensor-pass.jsonl")
	hookIn, _ := json.Marshal(map[string]string{"transcript_path": transcriptPath, "cwd": cwd})

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader(hookIn), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout; got %q", stdout.String())
	}
}

func TestHook_AlreadyHealed_NoLoop(t *testing.T) {
	cwd := withSensor(t, "watch-logs", []map[string]interface{}{
		{"kind": "env", "name": "RSA_PRIVATE_KEY"},
	})
	transcriptPath := transcripttest.Path(t, "already-healed.jsonl")
	hookIn, _ := json.Marshal(map[string]string{"transcript_path": transcriptPath, "cwd": cwd})

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader(hookIn), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no-op when /heal-sensor already in transcript; got %q", stdout.String())
	}
}

func TestHook_CascadeFromFailedDep_TargetsDep(t *testing.T) {
	// run-project depends on setup-env; setup-env failed due to missing env.
	// The cascade aggregate is the LAST JSONL line on the tool_result.
	// Hook must walk back to setup-env's real aggregate, classify on that,
	// and emit an injection targeting setup-env.
	cwd := withSensor(t, "run-project", []map[string]interface{}{
		{"kind": "sensor", "id": "setup-env"},
	})
	// Also write setup-env so loadFailedSensorView can find it.
	depDir := filepath.Join(cwd, ".harness", "sensors")
	depBody, _ := json.Marshal(map[string]interface{}{
		"id":       "setup-env",
		"requires": []map[string]interface{}{{"kind": "env", "name": "RSA_PRIVATE_KEY"}},
	})
	if err := os.WriteFile(filepath.Join(depDir, "setup-env.json"), depBody, 0o644); err != nil {
		t.Fatal(err)
	}
	transcriptPath := transcripttest.Path(t, "cascade-failed-dep.jsonl")
	hookIn, _ := json.Marshal(map[string]string{"transcript_path": transcriptPath, "cwd": cwd})

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader(hookIn), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "setup-env") {
		t.Fatalf("expected injection to target the failing dep setup-env; got %q", out)
	}
	if !strings.Contains(out, "run-project") {
		t.Fatalf("expected injection to mention requested sensor run-project; got %q", out)
	}
}

func TestHook_MalformedStdin_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte("not json")), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
}

func TestHook_NoTranscriptPath_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{}`)), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d want 2", code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test ./hooks/... -run 'TestHook'
```
Expected: FAIL — `setup-failure-detector.go` still parses the old shape, the new fixtures yield empty scans, no injection emitted.

- [ ] **Step 3: Rewrite the hook**

Replace the entire contents of `hooks/setup-failure-detector.go`:

```go
//go:build !error_autofiler

// hooks/setup-failure-detector.go
//
// Claude Code Stop hook that classifies the most-recent /run-sensor,
// /start-sensor, or /stop-sensor aggregate Signal in the conversation
// transcript. On setup-shaped failure, emits additionalContext on
// stdout instructing the LLM to invoke /heal-sensor.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/heal/rules"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/transcript"
)

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

type hookInput struct {
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
}

var sensorCommands = map[string]bool{
	"/run-sensor":   true,
	"/start-sensor": true,
	"/stop-sensor":  true,
}

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "read stdin:", err)
		return 2
	}
	var in hookInput
	if err := json.Unmarshal(body, &in); err != nil {
		fmt.Fprintln(stderr, "parse hook input:", err)
		return 2
	}
	if in.TranscriptPath == "" {
		fmt.Fprintln(stderr, "transcript_path missing")
		return 2
	}

	entries, err := transcript.Scan(in.TranscriptPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 0
	}

	scan, ok := findFailingInvocation(entries, in.Cwd)
	if !ok {
		return 0
	}
	if anyHealAfter(entries[scan.index+1:]) {
		return 0
	}

	failed, err := loadFailedSensorView(scan.SensorPath)
	if err != nil {
		return 0
	}

	res, matched := heal.ClassifyWith(rules.Registered(), scan.Signal, failed)
	if !matched {
		return 0
	}
	emitInjection(stdout, scan, failed.ID, res)
	return 0
}

type scanResult struct {
	Signal              heal.Signal
	SensorPath          string
	Command             string
	OriginalRequestedID string
	index               int // index into entries[] of the tool_result that produced this Signal
}

// findFailingInvocation walks entries backward and returns the most
// recent sensor-command failure that warrants heal classification.
func findFailingInvocation(entries []transcript.Entry, cwd string) (scanResult, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		for _, tr := range entries[i].ToolResults() {
			lines := strings.Split(strings.TrimRight(tr.ResultText(), "\n"), "\n")
			if len(lines) == 0 {
				continue
			}
			var sigMap map[string]interface{}
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sigMap); err != nil {
				continue
			}
			md, _ := sigMap["metadata"].(map[string]interface{})
			if md == nil {
				continue
			}
			kind, _ := md["kind"].(string)

			inv := findSensorInvocation(entries[:i], cwd)
			if inv.SensorPath == "" {
				continue
			}

			var sensorPath, originalRequestedID string
			switch kind {
			case "aggregate", "start_failed", "failed":
				sensorPath = inv.SensorPath
			case "cascade":
				failedDepID, _ := md["failed_dep_id"].(string)
				if failedDepID == "" {
					continue
				}
				depMap, found := findDepAggregate(lines, failedDepID)
				if !found {
					continue
				}
				originalRequestedID, _ = sigMap["sensor_id"].(string)
				sigMap = depMap
				sensorPath = filepath.Join(filepath.Dir(inv.SensorPath), failedDepID+".json")
			default:
				continue
			}
			return scanResult{
				Signal:              signalFromMap(sigMap),
				SensorPath:          sensorPath,
				Command:             inv.Command,
				OriginalRequestedID: originalRequestedID,
				index:               i,
			}, true
		}
	}
	return scanResult{}, false
}

func findDepAggregate(lines []string, failedDepID string) (map[string]interface{}, bool) {
	for i := len(lines) - 2; i >= 0; i-- {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			continue
		}
		md, _ := m["metadata"].(map[string]interface{})
		if md == nil || md["kind"] != "aggregate" {
			continue
		}
		sid, _ := m["sensor_id"].(string)
		if sid != failedDepID {
			continue
		}
		v, _ := m["verdict"].(string)
		if v != "error" && v != "fail" {
			continue
		}
		return m, true
	}
	return nil, false
}

type sensorInvocation struct {
	Command    string
	SensorPath string
}

// findSensorInvocation walks entries backward for the most recent
// sensor slash command (preferred) or, as a fallback, a Bash tool_use
// whose command literally contains "/run-sensor " etc. The slash-command
// path handles user-typed invocations; the fallback covers agent-driven
// invocations from sub-agents.
func findSensorInvocation(entries []transcript.Entry, cwd string) sensorInvocation {
	for i := len(entries) - 1; i >= 0; i-- {
		// Preferred: slash-command form in a user entry.
		name, args, ok := transcript.ParseSlashCommand(entries[i].Text())
		if ok && sensorCommands[name] {
			arg := firstPositionalArg(strings.Fields(args))
			if arg == "" {
				continue
			}
			return sensorInvocation{
				Command:    name,
				SensorPath: resolveSensorTarget(arg, cwd),
			}
		}
		// Fallback: literal "/run-sensor X" string inside any block.
		for cmd := range sensorCommands {
			needle := cmd + " "
			text := entries[i].Text()
			if idx := strings.Index(text, needle); idx >= 0 {
				rest := strings.Fields(text[idx+len(needle):])
				arg := firstPositionalArg(rest)
				if arg == "" {
					continue
				}
				return sensorInvocation{Command: cmd, SensorPath: resolveSensorTarget(arg, cwd)}
			}
			// Also look inside assistant tool_use Bash commands.
			for _, tu := range entries[i].ToolUses() {
				if tu.Name != "Bash" {
					continue
				}
				var ti struct{ Command string }
				_ = json.Unmarshal(tu.Input, &ti)
				if strings.Contains(ti.Command, needle) {
					idx := strings.Index(ti.Command, needle)
					rest := strings.Fields(ti.Command[idx+len(needle):])
					arg := firstPositionalArg(rest)
					if arg != "" {
						return sensorInvocation{Command: cmd, SensorPath: resolveSensorTarget(arg, cwd)}
					}
				}
			}
		}
	}
	return sensorInvocation{}
}

func firstPositionalArg(parts []string) string {
	for _, p := range parts {
		if strings.HasPrefix(p, "-") {
			continue
		}
		return p
	}
	return ""
}

func resolveSensorTarget(arg, cwd string) string {
	arg = strings.TrimPrefix(arg, "@")
	if strings.ContainsAny(arg, "/\\") || strings.HasSuffix(arg, ".json") {
		return arg
	}
	if cwd == "" {
		return arg
	}
	return filepath.Join(cwd, ".harness", "sensors", arg+".json")
}

func anyHealAfter(entries []transcript.Entry) bool {
	for _, e := range entries {
		if name, _, ok := transcript.ParseSlashCommand(e.Text()); ok && name == "/heal-sensor" {
			return true
		}
		if strings.Contains(e.Text(), "/heal-sensor") {
			return true
		}
	}
	return false
}

func signalFromMap(m map[string]interface{}) heal.Signal {
	var s heal.Signal
	if v, ok := m["verdict"].(string); ok {
		s.Verdict = v
	}
	if v, ok := m["severity"].(string); ok {
		s.Severity = v
	}
	if ev, ok := m["evidence"].([]interface{}); ok {
		for _, e := range ev {
			em, _ := e.(map[string]interface{})
			if em == nil {
				continue
			}
			r, _ := em["rationale"].(string)
			ex, _ := em["excerpt"].(string)
			s.Evidence = append(s.Evidence, heal.SignalEvidence{Rationale: r, Excerpt: ex})
		}
	}
	if md, ok := m["metadata"].(map[string]interface{}); ok {
		if h, ok := md["heal_hint"].(string); ok {
			s.Metadata.HealHint = h
		}
		if ec, ok := md["exit_code"].(float64); ok {
			n := int(ec)
			s.Metadata.ExitCode = &n
		}
		if lc, ok := md["lifecycle"].(map[string]interface{}); ok {
			if pp, ok := lc["prepare"].([]interface{}); ok {
				for _, p := range pp {
					pm, _ := p.(map[string]interface{})
					if pm == nil {
						continue
					}
					cmd, _ := pm["command"].(string)
					vrd, _ := pm["verdict"].(string)
					s.Metadata.Lifecycle.Prepare = append(s.Metadata.Lifecycle.Prepare, heal.SignalLifecycleStep{Command: cmd, Verdict: vrd})
				}
			}
		}
	}
	return s
}

func loadFailedSensorView(path string) (heal.FailedSensor, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return heal.FailedSensor{}, err
	}
	var s map[string]interface{}
	if err := json.Unmarshal(body, &s); err != nil {
		return heal.FailedSensor{}, err
	}
	id, _ := s["id"].(string)

	var envs []string
	for _, e := range sensor.Project(s, "env") {
		if name, ok := e["name"].(string); ok && name != "" {
			envs = append(envs, name)
		}
	}
	var tools []string
	for _, t := range sensor.Project(s, "tool") {
		if name, ok := t["name"].(string); ok && name != "" {
			tools = append(tools, name)
		}
	}
	var contexts []string
	for _, c := range sensor.Project(s, "context") {
		if p, ok := c["path"].(string); ok && p != "" {
			contexts = append(contexts, p)
		}
	}
	return heal.FailedSensor{ID: id, EnvNames: envs, Tools: tools, Context: contexts}, nil
}

func emitInjection(stdout io.Writer, scan scanResult, sensorID string, res heal.Result) {
	cmd := scan.Command
	if cmd == "" {
		cmd = "/run-sensor"
	}
	var msg string
	if scan.OriginalRequestedID != "" && scan.OriginalRequestedID != sensorID {
		msg = fmt.Sprintf(
			"The previous %s invocation for sensor %q failed because dep %q failed setup-shape (rule=%s, shape=%s, detail=%q). Invoke `/heal-sensor --signal-from=transcript --sensor=%s` to heal the dep before reporting the failure to the user.",
			cmd, scan.OriginalRequestedID, sensorID, res.Rule, res.Shape, res.Detail, scan.SensorPath,
		)
	} else {
		msg = fmt.Sprintf(
			"The previous %s invocation for sensor %q produced a setup-shaped failure (rule=%s, shape=%s, detail=%q). Invoke `/heal-sensor --signal-from=transcript --sensor=%s` to attempt automatic recovery before reporting the failure to the user.",
			cmd, sensorID, res.Rule, res.Shape, res.Detail, scan.SensorPath,
		)
	}
	out := map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":     "Stop",
			"additionalContext": msg,
		},
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test ./hooks/... -run 'TestHook'
```
Expected: PASS — 6 tests.

- [ ] **Step 5: Run vet for the !error_autofiler build**

```
go vet ./hooks/...
```
Expected: no warnings.

- [ ] **Step 6: Commit**

```
git add hooks/setup-failure-detector.go hooks/setup-failure-detector_test.go
git commit -m "fix(hooks): setup-failure-detector parses the real transcript shape via lib/transcript"
```

---

## Task 6: error-issue-autofiler — regex repair and dead-pattern removal

**Files:**
- Modify: `hooks/error-issue-autofiler.go`
- Modify: `hooks/error-issue-autofiler_test.go`

- [ ] **Step 1: Update the test cases first (red phase)**

In `hooks/error-issue-autofiler_test.go`, locate `TestCommandTouchesFramework` (around line 89) and replace its `cases` slice with the canonical-shape cases below; locate `TestExtractSkill` (around line 139) and similarly replace its `cases` slice:

```go
// TestCommandTouchesFramework — replace cases slice
cases := []struct {
    cmd  string
    want bool
}{
    // Positive: canonical go run -C form from skills
    {`go run -C "/plugin" -tags=run_computational ./skills/run-sensor/scripts foo`, true},
    {`go run -C /plugin -tags=run_inferential ./skills/run-sensor/scripts foo --slot k=v`, true},
    {`go run -C /plugin -tags=start_sensor ./skills/start-sensor/scripts run-api-local`, true},
    {`go run -C /plugin -tags=stop_sensor ./skills/stop-sensor/scripts run-api-local`, true},
    {`go run -C /plugin -tags=tail_sensor ./skills/tail-sensor/scripts run-api-local 0`, true},
    {`go run -C /plugin -tags=list_sensors ./skills/list-sensors/scripts`, true},
    {`go run -C /plugin -tags=heal_retry_original ./skills/heal-sensor/scripts foo`, true},
    {`go run -C /plugin -tags=write_stack ./skills/detect-sensors/scripts`, true},

    // Positive: -C absent (test invocations occasionally lack it)
    {"go run ./skills/run-sensor/scripts foo", true},
    {"go run -tags=run_computational ./skills/run-sensor/scripts foo", true},

    // Positive: hooks
    {`go run -C /plugin ./hooks`, true},
    {`go run -C /plugin -tags=error_autofiler ./hooks`, true},
    {"go run ./hooks", true},

    // Positive: go test/vet/build of framework packages
    {`go test ./lib/...`, true},
    {`go vet -C /plugin -tags=run_computational ./skills/...`, true},
    {`go build -C /plugin ./hooks`, true},

    // Negative
    {"ls -la", false},
    {"npm test", false},
    {"git push", false},
    {"go run ./cmd/other", false},
    {`cd "/plugin" && go run ./hooks`, false}, // legacy plugin.json form: NOT recognized
    {"", false},
}
```

```go
// TestExtractSkill — replace cases slice
cases := []struct {
    cmd  string
    want string
}{
    {`go run -C /plugin -tags=run_computational ./skills/run-sensor/scripts foo`, "run-sensor"},
    {`go run -C /plugin -tags=start_sensor ./skills/start-sensor/scripts foo`, "start-sensor"},
    {`go run -C /plugin -tags=stop_sensor ./skills/stop-sensor/scripts run-api-local`, "stop-sensor"},
    {`go run -C /plugin -tags=tail_sensor ./skills/tail-sensor/scripts run-api-local 0`, "tail-sensor"},
    {`go run -C /plugin -tags=list_sensors ./skills/list-sensors/scripts`, "list-sensors"},
    {`go run -C /plugin -tags=heal_retry_original ./skills/heal-sensor/scripts foo`, "heal-sensor"},
    {`go run -C /plugin -tags=write_stack ./skills/detect-sensors/scripts`, "detect-sensors"},

    // Fallbacks
    {`go run -C /plugin ./hooks`, "hook"},
    {`go run -C /plugin -tags=error_autofiler ./hooks`, "hook"},
    {`go test ./lib/registry`, "test"},
    {`go vet ./skills/...`, "test"},
    {"completely unrelated", "unknown"},
    {"", "unknown"},
}
```

- [ ] **Step 2: Run tests to verify they fail**

```
go test -tags=error_autofiler ./hooks/... -run 'TestCommandTouchesFramework|TestExtractSkill'
```
Expected: FAIL — current regex puts `-C` before `run`, so `go run -C …` cases miss; also the `harness-*` cases were removed.

- [ ] **Step 3: Repair `buildFrameworkCommandPatterns` and `extractSkill`**

In `hooks/error-issue-autofiler.go`, replace `buildFrameworkCommandPatterns` (the function around lines 63–77):

```go
func buildFrameworkCommandPatterns() []*regexp.Regexp {
	skills := strings.Join(sensorSkills, "|")
	// optional `-C <path>` AFTER the verb (run/test/vet/build), per the
	// canonical invocation contract in CLAUDE.md.
	chDir := `(?:-C\s+\S+\s+)?`
	tagsAny := `(?:-tags=\S+\s+)*`
	return []*regexp.Regexp{
		// go run [-C <path>] [-tags=…]* ./skills/<skill>-sensors?/scripts
		regexp.MustCompile(`go\s+run\s+` + chDir + tagsAny + `\./skills/(?:` + skills + `)-sensors?/scripts\b`),
		// go run [-C <path>] [-tags=…]* ./hooks
		regexp.MustCompile(`go\s+run\s+` + chDir + tagsAny + `\./hooks\b`),
		// go (test|vet|build) [-C <path>] [-tags=…]* ./{skills,lib,hooks}
		regexp.MustCompile(`go\s+(?:test|vet|build)\s+` + chDir + tagsAny + `\./(?:skills|lib|hooks)\b`),
	}
}
```

Replace `extractSkill` (function around lines 98–115):

```go
var skillExtractRe = func() *regexp.Regexp {
	skills := strings.Join(sensorSkills, "|")
	return regexp.MustCompile(`skills/((?:` + skills + `)-sensors?)\b`)
}()

func extractSkill(cmd string) string {
	if m := skillExtractRe.FindStringSubmatch(cmd); m != nil {
		return m[1]
	}
	if regexp.MustCompile(`go\s+run\s+(?:-C\s+\S+\s+)?(?:-tags=\S+\s+)*\./hooks\b`).MatchString(cmd) {
		return "hook"
	}
	if regexp.MustCompile(`go\s+(?:test|vet|build)\b`).MatchString(cmd) {
		return "test"
	}
	return "unknown"
}
```

- [ ] **Step 4: Run tests to verify they pass**

```
go test -tags=error_autofiler ./hooks/... -run 'TestCommandTouchesFramework|TestExtractSkill'
```
Expected: PASS.

- [ ] **Step 5: Run the full autofiler test suite**

```
go test -tags=error_autofiler ./hooks/...
```
Expected: PASS — no regression in classifier/dedup/cache tests.

- [ ] **Step 6: Commit**

```
git add hooks/error-issue-autofiler.go hooks/error-issue-autofiler_test.go
git commit -m "fix(hooks): error-issue-autofiler regex matches canonical go run -C invocations"
```

---

## Task 7: plugin.json — canonical invocation contract

**Files:**
- Modify: `.claude-plugin/plugin.json`

- [ ] **Step 1: Update the hook commands**

Replace the `hooks` block in `.claude-plugin/plugin.json` with:

```json
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "GOWORK=off go run -C \"${CLAUDE_PLUGIN_ROOT}\" ./hooks"
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
            "command": "GOWORK=off go run -C \"${CLAUDE_PLUGIN_ROOT}\" -tags=error_autofiler ./hooks"
          }
        ]
      }
    ]
  }
```

- [ ] **Step 2: Validate JSON**

```
python3 -c "import json,sys; json.load(open('.claude-plugin/plugin.json'))" && echo OK
```
Expected: `OK`.

- [ ] **Step 3: Spot-check that the manifest validates against the framework's own meta-sensor if one exists**

```
ls .harness/sensors/validate-plugin-manifest.json && go run -C "$(pwd)" -tags=run_computational ./skills/run-sensor/scripts validate-plugin-manifest 2>&1 | tail -20
```
Expected: aggregate Signal with `verdict=pass` on the last line. If the validator does not exist or is unrelated to plugin.json, skip — the JSON load in step 2 is enough.

- [ ] **Step 4: Commit**

```
git add .claude-plugin/plugin.json
git commit -m "fix(plugin): hooks use canonical GOWORK=off go run -C contract"
```

---

## Task 8: end-to-end smoke and final verification

**Files:** none (verification only)

- [ ] **Step 1: Run the full Go test suite**

```
go test ./lib/...
go test ./hooks/...
go test -tags=error_autofiler ./hooks/...
```
Expected: all PASS.

- [ ] **Step 2: Run `go vet` for both build tags**

```
go vet ./lib/...
go vet ./hooks/...
go vet -tags=error_autofiler ./hooks/...
```
Expected: no warnings.

- [ ] **Step 3: Manual smoke for setup-failure-detector (DoD item 8)**

Pipe a real-shape fixture through the hook to confirm production-like behavior:

```
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "$(pwd)" ./hooks <<'EOF'
{
  "transcript_path": "lib/transcript/testdata/run-sensor-error.jsonl",
  "cwd": "/tmp/nonexistent"
}
EOF
```
Expected: prints a JSON object with `hookSpecificOutput.additionalContext` mentioning `/heal-sensor`. (Cwd is intentionally bogus; the hook returns 0 with empty stdout because `loadFailedSensorView` fails — confirm by checking that stderr is empty and exit code is 0. Then re-run with a properly populated `.harness/sensors/watch-logs.json` under cwd to see the injection.)

For the populated run:
```
TMPDIR=$(mktemp -d) && mkdir -p "$TMPDIR/.harness/sensors" && \
  printf '{"id":"watch-logs","requires":[{"kind":"env","name":"RSA_PRIVATE_KEY"}]}' > "$TMPDIR/.harness/sensors/watch-logs.json" && \
  GOWORK=off go run -C "$(pwd)" ./hooks <<EOF
{ "transcript_path": "$(pwd)/lib/transcript/testdata/run-sensor-error.jsonl", "cwd": "$TMPDIR" }
EOF
```
Expected: JSON output containing `"/heal-sensor"` and `"watch-logs"`.

- [ ] **Step 4: Manual smoke for error-issue-autofiler (DoD item 4)**

```
GOWORK=off go run -C "$(pwd)" -tags=error_autofiler ./hooks <<'EOF'
{
  "session_id": "smoke",
  "cwd": "/tmp",
  "hook_event_name": "PostToolUse",
  "tool_name": "Bash",
  "tool_input": { "command": "go run -C /plugin -tags=run_computational ./skills/run-sensor/scripts foo", "description": "" },
  "tool_response": { "stdout": "", "stderr": "panic: runtime error: invalid memory address\n\ngoroutine 1 [running]:\n\tmain.main()\n", "exitCode": 2 }
}
EOF
```
Expected: exit 0, stderr empty or carrying only "no github remote" / "cache write" messages. The hook should reach `classify` (proving `commandTouchesFramework` returned true). If `gh` is unavailable or no remote is configured, the hook stops cleanly without panicking — that is the correct degraded behavior.

- [ ] **Step 5: Final commit (none expected)**

```
git status
```
Expected: clean (every change already committed in tasks 1–7).

---

## Self-review notes

- **Spec section "1. New shared package"** → Tasks 1, 2, 3, 4 (entry, scan, slashcmd, transcripttest). Covered.
- **Spec section "2. setup-failure-detector reworked"** → Task 5. Covered, including the literal-text fallback in `findSensorInvocation`.
- **Spec section "3. error-issue-autofiler regex repair"** → Task 6. Covered, including dead-pattern removal in `buildFrameworkCommandPatterns` and `extractSkill`.
- **Spec section "4. plugin.json alignment"** → Task 7. Covered.
- **Spec section "5. Tests"** → Tasks 1–6 (TDD inline) + Task 8 verification. Covered.
- **DoD 1–3 (setup-failure-detector behavior)** → Task 5 tests + Task 8 manual smoke.
- **DoD 4 (commandTouchesFramework + extractSkill)** → Task 6 tests.
- **DoD 5 (legacy `cd && go run` NOT matched)** → Task 6 negative case.
- **DoD 6 (test suites green)** → Task 8 step 1.
- **DoD 7 (plugin.json shape)** → Task 7 + step 2 JSON validate.
- **DoD 8 (manual smoke)** → Task 8 step 3.

No placeholders. No "TODO". Every code step shows the actual content. Type names consistent: `Entry`, `Message`, `Content`, `Block` used throughout; `scanResult`, `sensorInvocation`, `hookInput` retain their existing shapes (hook-internal).
