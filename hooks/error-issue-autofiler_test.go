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
