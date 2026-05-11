//go:build error_autofiler

package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
		// matcher is permissive on "go run" anywhere; we don't list
		// `echo go run ./skills/...` as a negative case because the
		// classifier needs real error output for false positives to
		// matter.
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

func TestFingerprint_ExitNonzero(t *testing.T) {
	a := &classifiedEvent{
		Type:    "exit_nonzero",
		Skill:   "test",
		Summary: "go: cannot find main module, but found .git/config in /home/user",
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
	if gh.searchCalls != 2 {
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
	createCalls     int
	searchCalls     int
	commentCalls    int
	createErr       error
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
