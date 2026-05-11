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
