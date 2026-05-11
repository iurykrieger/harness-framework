//go:build error_autofiler

package main

import (
	"bytes"
	"os"
	"path/filepath"
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
