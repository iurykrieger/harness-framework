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

// skillExtractRe captures the sensor skill name from either scripts
// path or installed binary form. Built from sensorSkills.
var skillExtractRe = func() *regexp.Regexp {
	skills := strings.Join(sensorSkills, "|")
	return regexp.MustCompile(`(?:skills/|harness-)((?:` + skills + `)-sensors?)`)
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
	if in.ToolName != "Bash" {
		return 0
	}
	if !commandTouchesFramework(in.ToolInput.Command) {
		return 0
	}
	// Subsequent tasks fill in: classify, fingerprint, cache, gh ops.
	_ = in
	return 0
}

var (
	rePID           = regexp.MustCompile(`(?i)pid=\d+`)
	reTimestamp     = regexp.MustCompile(`(?i)\d{4}-\d{2}-\d{2}t\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:z|[+-]\d{2}:\d{2})`)
	reGoPos         = regexp.MustCompile(`(\.go):\d+(?::\d+)?`)
	reTrailingParen = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
	reTrailingPos   = regexp.MustCompile(`\s*:\d+(?::\d+)?\s*$`)
	reWhitespace    = regexp.MustCompile(`\s+`)
)

// normalize produces a stable lower-case form of an error/output line
// for use in fingerprint canonical strings. Strips PIDs, ISO/RFC3339
// timestamps, absolute plugin paths (replaced with <plugin>), :line:col
// suffixes following ".go" file extensions, trailing parenthetical
// noise (e.g. "(pid=n at t)") left over from the previous substitutions,
// trailing :line:col suffixes, and collapses whitespace.
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
	s = reGoPos.ReplaceAllString(s, "$1")
	s = reTrailingParen.ReplaceAllString(s, "")
	s = reTrailingPos.ReplaceAllString(s, "")
	s = reWhitespace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
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

// signalErrorAllowedKinds restricts signal_error classification to
// framework-internal failure modes. fail/warn verdicts are legitimate
// sensor detections and are excluded entirely.
var signalErrorAllowedKinds = map[string]struct{}{
	"start_failed":            {},
	"runner_internal_error":   {},
	"schema_validation_error": {},
	"slot_error":              {},
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
		if idx := strings.Index(path, "harness-framework/"); idx >= 0 {
			rel := path[idx+len("harness-framework/"):]
			return rel + ":" + m[2]
		}
	}
	return ""
}

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
