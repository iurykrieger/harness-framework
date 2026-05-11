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
