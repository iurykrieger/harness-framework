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
	"strings"
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
	if killSwitchEnabled() {
		return 0
	}
	// Subsequent tasks fill in: commandTouchesFramework,
	// classify, fingerprint, cache, gh ops. For now: parse-only → exit 0.
	_ = in
	return 0
}

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
