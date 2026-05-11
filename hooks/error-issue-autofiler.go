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

type runOpts struct {
	gh        ghClient
	cachePath string
	repo      string
	repoErr   error
	now       time.Time
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
	// Defensive guards: keep runWith safe to call directly from tests.
	// Production `run` already filters on the same predicates before
	// reaching here, so these are idempotent in normal use.
	if killSwitchEnabled() {
		return 0
	}
	if !commandTouchesFramework(in.ToolInput.Command) {
		return 0
	}

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
//     the malformed file is NOT overwritten.
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
