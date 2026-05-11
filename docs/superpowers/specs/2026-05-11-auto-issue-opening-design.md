# Auto issue opening hook design

Status: proposed
Date: 2026-05-11
Related: `hooks/setup-failure-detector.go`, `plugin.json`, `lib/registry/`, `skills/run-sensor/`, `skills/start-sensor/`, `skills/stop-sensor/`, `skills/heal-sensor/`, `skills/detect-sensors/`, `skills/list-sensors/`, `skills/tail-sensor/`

## Why

Today, when one of the framework's Go scripts crashes (panic, compile failure, schema-validation error, runner-internal failure), the only observer is the human who happens to be watching the agent's session. The error scrolls past in a Bash tool result, the agent moves on, and the failure goes unreported until someone notices the same issue twice and files it manually — or worse, until the same bug ships to another user.

The framework is itself an agent-facing observability layer. It is internally consistent that *the framework observes its own failures and feeds them back as GitHub issues*, the same way it feeds sensor failures back to agents as Signals. Manual triage of framework crashes is the analogue of "user manually checks `signals.log`" — exactly the loop we removed for sensors.

Two adjacent capabilities already exist and inform the design:

- `hooks/setup-failure-detector.go` shows the project's hook style: `package main` Go binary invoked by Claude Code via `go run ./hooks`, reading hook input JSON from stdin and emitting JSON on stdout. The new hook follows the same shape under a different build tag.
- The user-level `dedupe-issues` skill (`~/.claude/skills/dedupe-issues/SKILL.md`) handles cross-issue consolidation after the fact. The autofiler relies on per-fingerprint dedup at write-time to keep the tracker from drowning between consolidation runs; the two are complementary, not redundant.

The goal is a hook that fires after each `Bash` tool call, detects when a framework script just crashed, and either creates a new GitHub issue for the bug or appends a `+1 occurrence` comment on the existing issue — entirely in the background, exit 0 always, never interrupting the agent.

## What changes

1. **New `hooks/error-issue-autofiler.go`** with `//go:build error_autofiler`. Single-file Go binary implementing the full pipeline (matcher → classify → fingerprint → dedup → file). ~400 LOC.
2. **New `hooks/error-issue-autofiler_test.go`** with the same build tag. Table-driven tests over pure functions plus integration tests with mocked `gh` and cache.
3. **`hooks/setup-failure-detector.go` and `hooks/setup-failure-detector_test.go` gain `//go:build !error_autofiler`** to keep `package main` unambiguous when both files live in the same directory under different tags. Behavioral no-op.
4. **`.claude-plugin/plugin.json` gains a `PostToolUse` hook entry** with `matcher: "Bash"`, invoking the new build-tagged binary. The existing `Stop` hook entry is untouched.
5. **New cache file at `<projectRoot>/.runtime/auto-issues.json`** (created on first write). Project root is resolved via `lib/registry/Lookup(cwd)` so the autofiler reuses the registry root discovery logic — no third discovery strategy in the codebase.
6. **One new GitHub label expected**: `auto-filed`. Documented in CLAUDE.md; the user creates it once. If absent, `gh issue create --label` fails and the hook silently degrades.
7. **`CLAUDE.md` gains a short "Auto issue opening" subsection** under Architecture explaining the trigger surface, the kill switch, and the cache location.

Nothing in `schemas/`, `lib/`, or the seven `skills/` directories changes.

## Non-goals

- AI-generated issue titles or bodies. The template is deterministic and structured; stability of fingerprint and review predictability matter more than prose quality.
- Sentry-like external service integration. Issues stay in GitHub.
- Closing issues automatically when a fingerprint stops recurring — there is no clean "fixed" signal, only "not seen recently," and that is unsafe to act on.
- Covering errors in skills outside `harness-framework` (user-level skills like `dedupe-issues`, other plugins). The matcher is intentionally narrow.
- Dry-run mode, rate limiting, repo override env var. These were explicitly declined in design discussion; if a future need emerges, they can be added without restructuring.

## Architecture

### Hook lifecycle

Claude Code invokes `PostToolUse` hooks after every tool call matched by the entry. For this hook, the matcher is `Bash` — the hook is never invoked for `Edit`, `Read`, `Glob`, etc.

```
Claude Code agent runs Bash tool ──▶ tool completes ──▶ PostToolUse hooks fire
                                                              │
                                                              ▼
                       cd ${CLAUDE_PLUGIN_ROOT} && go run -tags=error_autofiler ./hooks
                                                              │
                                                              ▼
                                                stdin = HookInput JSON
                                                              │
                                                              ▼
                                                       autofiler runs
                                                              │
                                                              ▼
                                                        exit 0 (always)
```

The hook is a fresh process per invocation (`go run`); there is no shared state in memory between calls. State persists only via the cache file on disk.

### HookInput

PostToolUse delivers this JSON on stdin (excerpt of fields the autofiler uses):

```go
type hookInput struct {
    SessionID      string         `json:"session_id"`
    TranscriptPath string         `json:"transcript_path"`
    Cwd            string         `json:"cwd"`
    HookEventName  string         `json:"hook_event_name"`
    ToolName       string         `json:"tool_name"`
    ToolInput      toolInputBash  `json:"tool_input"`
    ToolResponse   toolResponse   `json:"tool_response"`
}

type toolInputBash struct {
    Command     string `json:"command"`
    Description string `json:"description"`
}

type toolResponse struct {
    Stdout      string `json:"stdout"`
    Stderr      string `json:"stderr"`
    ExitCode    int    `json:"exitCode"`
    Interrupted bool   `json:"interrupted"`
}
```

No need to read `TranscriptPath`. Everything required lives in this payload.

### Pipeline

```
unmarshal HookInput
        │
        ▼
killSwitchEnabled()? ───▶ exit 0
        │
        ▼
commandTouchesFramework(cmd)? ───▶ no: exit 0
        │ yes
        ▼
skill := extractSkill(cmd)
evt   := classify(stdout, stderr, exitCode)
        │
evt == nil ───▶ exit 0
        │
        ▼
fp := fingerprint(evt)         // evt.Skill is already set by classify()
        │
        ▼
projectRoot := registry.Lookup(cwd).ProjectRoot
        │ error
        ▼ log stderr, exit 0
cache := loadCache(projectRoot/.runtime/auto-issues.json)
        │
cache.has(fp) && cache[fp].LastSeen >= now-7d ───▶ touch + occurrence++, save, exit 0
        │
        ▼
repo := resolveRepo(projectRoot)
        │ no github remote
        ▼ log stderr, exit 0
existing := ghSearch(repo, fp)
        │
existing != nil ───▶ ghComment(existing, "+1 occurrence …"), cache.put(...), exit 0
        │
        ▼
url := ghCreate(repo, title, body, labels)
cache.put(fp, entry{URL: url, FirstSeen: now, LastSeen: now, Occurrences: 1})
exit 0
```

The 7-day re-check window strikes a balance between "open issue may have been closed without telling us" (so we want to re-check sometimes) and "don't burn GH API on every tool call" (so we don't re-check daily).

**Dedup precedence:** the local cache is the **primary** dedup mechanism. `gh issue list --search "harness-fp:<fp>"` is a **backstop** for two scenarios: (a) first-occurrence on a fresh machine where the cache is empty, and (b) cache stale beyond 7 days. GitHub's full-text search has indexing latency (sometimes a few minutes after creation), so two near-simultaneous first-occurrences on different machines may both miss the search and both attempt `Create`. The `422 already_exists` path handles that race — see "Error handling."

### Matcher

Two layers:

**Layer 1 — declarative in `.claude-plugin/plugin.json`:**

```json
"hooks": {
  "Stop": [
    {
      "matcher": "",
      "hooks": [
        { "type": "command", "command": "cd \"${CLAUDE_PLUGIN_ROOT}\" && go run ./hooks" }
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

**Layer 2 — programmatic in `commandTouchesFramework`:**

```go
var frameworkCommandPatterns = []*regexp.Regexp{
    // go run direct from the scripts
    regexp.MustCompile(`go\s+run\s+(?:-tags=\S+\s+)?\./skills/(run|start|stop|tail|list|heal|detect)-sensor/scripts\b`),
    // go run from hooks (so the autofiler observes its sibling hooks too)
    regexp.MustCompile(`go\s+run\s+(?:-tags=\S+\s+)?\./hooks\b`),
    // installed binaries on PATH
    regexp.MustCompile(`\b(harness-(?:run|start|stop|tail|list|heal|detect)-sensor|harness-watcher)\b`),
    // go test/vet/build of the framework's own packages
    regexp.MustCompile(`go\s+(test|vet|build)\s+(-tags=\S+\s+)?\./(skills|lib|hooks)\b`),
}
```

Non-matching commands short-circuit to `exit 0` before any other work, keeping latency for unrelated Bash calls below ~5ms.

### Skill extraction

```go
var skillNameRe = regexp.MustCompile(`(?:skills|harness)[/-](run|start|stop|tail|list|heal|detect)-sensor`)
```

Fallbacks: command matches `\./hooks\b` → `hook`; command matches `go (test|vet|build)` → `test`; nothing extractable → `unknown`.

### Classification

Run in order, first match wins:

| # | Type | Detector | Summary (truncated to 120 chars) |
|---|---|---|---|
| 1 | `compile_error` | stderr matches `^# \S+\n\S+:\d+:\d+:` | first `<file>:<line>: <msg>` line |
| 2 | `panic` | stdout+stderr contains `panic: ` or `runtime error: ` followed within 5 lines by `goroutine \d+ \[running\]:` | the `panic:` line |
| 3 | `signal_error` | last line of stdout JSONL-parses as a Signal with `verdict == "error"` and `metadata.kind ∈ {start_failed, runner_internal_error, schema_validation_error, slot_error}` | `<metadata.kind> · <first evidence.rationale>` |
| 4 | `exit_nonzero` | `exitCode != 0` and stderr non-empty and no rule above matched | first non-blank line of stderr |

The `signal_error` allowlist excludes legitimate sensor failure verdicts (`fail`, `warn`) — those report problems in the observed system, not framework bugs. Only `verdict=error` paired with a framework-internal `metadata.kind` qualifies.

The classifier scans only the **last 16 KB** of stdout and stderr. Panics and Go errors always appear at the tail; this bounds CPU for streaming sensors with thousands of lines.

### Fingerprint

SHA-256 of a canonical string, truncated to 12 hex characters. Canonical string varies by type:

| Type | Canonical string |
|---|---|
| `compile_error` | `compile\|<pkg>\|<file>\|<msg_normalized>` |
| `panic` | `panic\|<top_user_frame>\|<panic_msg_normalized>` |
| `signal_error` | `signal\|<skill>\|<metadata.kind>\|<rationale_normalized>` |
| `exit_nonzero` | `exit\|<skill>\|<exit_code>\|<stderr_first_line_normalized>` |

`top_user_frame` is the first `goroutine \d+ [running]:` frame whose source file is under `github.com/iurykrieger/harness-framework`. Stdlib and third-party frames are skipped.

Normalization (`normalize(s string) string`):

1. Lowercase
2. Collapse runs of whitespace to single space
3. Replace `pid=\d+` → `pid=N`
4. Replace ISO-8601 / RFC3339 timestamps → `T`
5. Replace absolute paths beginning with `${CLAUDE_PLUGIN_ROOT}` → `<plugin>`
6. Strip trailing `:line:col` patterns
7. Trim

The same crash produces the same fingerprint regardless of when and where the agent was running.

### Dedup cache

Path: `<projectRoot>/.runtime/auto-issues.json`. Schema:

```json
{
  "version": 1,
  "entries": {
    "<fingerprint>": {
      "issue_url": "https://github.com/iurykrieger/harness-framework/issues/42",
      "first_seen": "2026-05-11T10:00:00Z",
      "last_seen":  "2026-05-11T10:05:00Z",
      "occurrence_count": 3,
      "skill": "run-sensor",
      "type": "panic"
    }
  }
}
```

Read-modify-write under `syscall.Flock(LOCK_EX)` on `<projectRoot>/.runtime/auto-issues.json.lock` to handle concurrent hook invocations safely (same pattern as `lib/registry`). The lock is per-project, not global.

Cache stale threshold: 7 days. A hit older than that triggers a re-check against GitHub (the issue may have been closed externally).

### Project root resolution

The hook reuses `lib/registry.Lookup(cwd)` — same `HARNESS_REGISTRY_ROOT` env → `sensors/` walk-up cascade used by the four blocking-sensor skills. This keeps registry root discovery as a single concept across the codebase. The autofiler consumes only `Result.ProjectRoot` and `Result.Source`; `Result.Exists` (whether `running_sensors.json` is on disk) is ignored, because the autofiler's `auto-issues.json` cache is independent of the live-sensors registry — the two cohabit `.runtime/` but never read each other.

If `Lookup` returns an error (no env var, no `sensors/` marker anywhere up the tree), the autofiler logs `"cannot resolve project root: <err>"` to stderr and exits 0 — typical for Bash calls in cwds that are not harness projects (e.g., the user is in `~/Workspace/somewhere-else` and ran `go test ./...` on a different repo).

### Repo resolution

```go
func resolveRepo(projectRoot string) (string, error) {
    // git -C <projectRoot> remote get-url origin
    // parse to owner/name from either:
    //   git@github.com:owner/repo.git
    //   https://github.com/owner/repo(.git)?
    // non-github remotes → "", error
}
```

Only the `origin` remote is consulted. Forks with `upstream` or other named remotes are intentionally ignored — autofiled issues go to whatever the developer is pushing to. Non-GitHub `origin` remote → log "no github remote found", exit 0.

### Issue title and body

Title: `[auto] <skill>: <summary>`, summary truncated to 80 chars after the colon to keep total title ≤120.

Body (Markdown, en-US per Project rule #1):

```markdown
**Type:** <compile_error|panic|signal_error|exit_nonzero>
**Skill:** <skill>
**Fingerprint:** `<fingerprint>`
**First seen:** <first_seen ISO8601 UTC>

## Command

```bash
<tool_input.command>
```

## Output

<details>
<summary>stdout (last 50 lines)</summary>

```
<stdout truncated: last 50 lines OR 4KB, whichever first>
```
</details>

<details>
<summary>stderr (last 50 lines)</summary>

```
<stderr truncated: last 50 lines OR 4KB, whichever first>
```
</details>

## Context

- `cwd`: `<tool_input.cwd>` (rendered relative to `~` when possible)
- `exit_code`: <int>
- Hook: `error-issue-autofiler` in `hooks/`

<!-- harness-fp:<fingerprint> -->
```

The HTML marker `<!-- harness-fp:<fingerprint> -->` is invisible in rendered Markdown but indexed by `gh issue list --search` (full-text search across body), so a future hook invocation can find the existing issue without parsing titles. Search query: `is:open repo:<owner>/<repo> harness-fp:<fingerprint>`.

### `+1 occurrence` comment

When `ghSearch` finds an existing open issue, the hook appends (en-US):

```markdown
+1 occurrence detected at <timestamp ISO8601 UTC>.

- `cwd`: `<cwd relativized>`
- `command`: `<command, truncated to 200 chars>`
- `exit_code`: <int>
```

No template variations beyond that.

### Function layout

```go
//go:build error_autofiler
package main

import ( /* stdlib + github.com/iurykrieger/harness-framework/lib/registry */ )

func main() { os.Exit(run(os.Stdin, os.Stdout, os.Stderr)) }

type hookInput          struct { /* fields above */ }
type classifiedEvent    struct { Type, Summary, Skill string; FrameworkFrame string }
type cacheEntry         struct { /* fields above, JSON-tagged */ }
type cache              struct { Version int; Entries map[string]cacheEntry }
type ghClient           interface {
    Search(repo, fingerprint string) (issueRef, error)
    Comment(repo string, issueNumber int, body string) error
    Create(repo, title, body string, labels []string) (issueRef, error)
}
type issueRef struct { Number int; URL string }

func run(stdin io.Reader, stdout, stderr io.Writer) int

func killSwitchEnabled() bool
func commandTouchesFramework(cmd string) bool
func extractSkill(cmd string) string
func classify(stdout, stderr string, exitCode int) *classifiedEvent
func fingerprint(evt *classifiedEvent) string
func normalize(s string) string

func loadCache(path string) (*cache, error)
func (c *cache) put(fp string, e cacheEntry)
func (c *cache) save(path string) error

func resolveRepo(projectRoot string) (string, error)
func renderTitle(skill, summary string) string
func renderBody(in hookInput, evt classifiedEvent, fp string) string
func renderOccurrenceComment(in hookInput) string

// Default ghClient implementation shelling out to `gh`.
type ghCLI struct{}
func (ghCLI) Search(repo, fp string) (issueRef, error)
func (ghCLI) Comment(repo string, n int, body string) error
func (ghCLI) Create(repo, title, body string, labels []string) (issueRef, error)
```

Everything is `package main` top-level inside `hooks/error-issue-autofiler.go`. **No code moves to `lib/`** — the autofiler logic is hook-specific (per CLAUDE.md rule #4, `lib/` is for primitives several skills share). This matches the precedent set by `hooks/setup-failure-detector.go`, which keeps its full pipeline in a single file. The `ghClient` interface exists for test substitution, not for production polymorphism — there will only ever be one production implementation.

Constants for tunables (one declaration, used in pipeline and cache code):

```go
const (
    cacheStaleAfter      = 7 * 24 * time.Hour
    classifierScanWindow = 16 * 1024 // bytes scanned at the tail of stdout/stderr
    bodyLogLineLimit     = 50        // lines per <details> block
    bodyLogByteLimit     = 4 * 1024  // bytes per <details> block
    titleSummaryMaxLen   = 80
    commandTruncateLen   = 200       // in +1 occurrence comments
)
```

The list of framework skill identifiers is declared once (mirroring the `sensorCommands` slice in `setup-failure-detector.go`):

```go
var sensorSkills = []string{"run", "start", "stop", "tail", "list", "heal", "detect"}
```

Both `commandTouchesFramework` and `extractSkill` build their regexes from this slice rather than hardcoding the seven names twice.

## Error handling

The Claude Code hook protocol requires `exit 0` for normal execution. Any non-zero exit blocks the agent's turn. The autofiler treats every internal failure as `log to stderr + exit 0`. The only exception is `exit 2` for usage errors (malformed stdin), matching `setup-failure-detector.go:60-62`.

| Scenario | Detector | Behavior |
|---|---|---|
| Kill switch active | `HARNESS_AUTOFILE_ISSUES` in `{"0","false","off",""}` | exit 0 silently, no logging |
| Non-framework command | `commandTouchesFramework` returns false | exit 0 silently |
| No classifier matched | `classify` returns nil | exit 0 silently |
| `gh` binary missing | `exec.LookPath("gh")` fails before first invocation | log "gh not found", exit 0 |
| `gh` not authenticated | `gh issue list` returns exit ≠ 0 with stderr `not logged in` / `authentication required` | log "gh not authenticated", exit 0 |
| Network failure / 5xx | `gh` exit ≠ 0 with network-shaped stderr | log "github unreachable", cache untouched, exit 0 |
| No GitHub remote | `resolveRepo` cannot parse `github.com` host | log "no github remote", exit 0 |
| Project root resolution failure | `registry.Lookup(cwd)` errors | log "cannot resolve project root", exit 0 |
| Cache file malformed | JSON unmarshal fails on existing file | log "cache corrupt, treating as empty", proceed with fresh cache, **do not overwrite the bad file** until at least one successful op |
| GH create returns 422 (duplicate) | `gh` exit ≠ 0 with body containing "already_exists" | log "race detected, re-searching"; one retry of `ghSearch` + `ghComment`; if still fails, exit 0 |
| Concurrent hook invocation | `flock` blocks briefly on lock file | natural serialization; no special handling needed |
| stdin truncated / over 64 KB | `io.ReadAll` exceeds soft cap | scan only last 16 KB of stdout/stderr |

## Testing

All tests live in `hooks/error-issue-autofiler_test.go` with `//go:build error_autofiler`.

### Pure-function tests (table-driven)

- `TestCommandTouchesFramework` — ~12 cases. Positive: `go run ./skills/run-sensor/scripts foo`, `harness-run-sensor foo`, `go test ./lib/...`, `go run -tags=error_autofiler ./hooks`. Negative: `ls -la`, `npm test`, `git push`, `go run ./cmd/other`, `cd skills/run-sensor && echo hi`.
- `TestExtractSkill` — 7 skills × 3 forms (`./skills/X-sensor`, `harness-X-sensor`, paths with leading dirs), plus `./hooks` → `hook`, `go test` → `test`, garbage → `unknown`.
- `TestClassify` — fixtures in `testdata/`: `compile_error.txt`, `panic_runtime_nil_deref.txt`, `panic_stack_lib_registry.txt`, `signal_error_start_failed.jsonl`, `exit_nonzero_no_match.txt`. Each fixture asserts `Type`, `Summary`, and `FrameworkFrame` (where applicable).
- `TestFingerprint` — same panic with different PIDs, paths, and timestamps yields identical fingerprint; different panic messages yield different fingerprints; signal_error with same skill+kind+rationale collides.
- `TestRenderBody` — golden file comparison covering all four types, including the HTML marker.
- `TestRenderTitle` — truncation at 80 chars after the colon; no trailing whitespace.
- `TestRenderOccurrenceComment` — command truncation at 200 chars, timestamp formatting.
- `TestNormalize` — PID stripping, timestamp stripping, path normalization, whitespace collapse.

### Integration tests (mocked `gh` and cache)

A `fakeGhClient` records calls and returns scripted responses. `loadCache` and `(c *cache).save` operate on a `t.TempDir()`-backed file.

- `TestRunFlow_NewError_CreatesIssue` — fingerprint absent from cache, `Search` returns empty, `Create` is called once with expected title/body/labels, cache writes the new entry.
- `TestRunFlow_CachedRecent_ShortCircuits` — fingerprint present with `LastSeen` within 7 days; `Search`/`Create`/`Comment` not called; cache entry's `LastSeen` updates, `OccurrenceCount` increments.
- `TestRunFlow_CachedStale_RechecksGitHub` — fingerprint present with `LastSeen` 8 days old; `Search` is called; behavior continues per Search result.
- `TestRunFlow_GHFound_CommentsNotCreates` — fingerprint absent from cache but `Search` returns an open issue; `Comment` is called with `+1 occurrence` body; `Create` not called; cache updated with the found URL.
- `TestRunFlow_KillSwitch_NoOp` — `HARNESS_AUTOFILE_ISSUES=0`; no `gh` calls, no cache I/O, exit 0.
- `TestRunFlow_NonFrameworkCommand_NoOp` — input command `ls -la`; classifier never invoked; no `gh` calls, no cache I/O, exit 0.
- `TestRunFlow_GHCreateFails_DoesNotMutateCache` — `Create` returns an error; cache file unchanged.
- `TestRunFlow_GHCreate422_RetriesWithSearch` — `Create` returns 422 duplicate error; `Search` is called a second time; if successful, `Comment` is invoked.
- `TestRunFlow_NoFrameworkInOutput_ButCommandMatches` — command matches matcher but stdout/stderr is clean (exit 0, no errors). Classifier returns nil; no side effects.
- `TestRunFlow_NoGHRemote_LogsAndExits` — `resolveRepo` returns "no github remote"; no `gh` calls; cache unchanged.

### Out of test scope

- The `plugin.json` wire-up itself (Claude Code does not expose a hook harness for integration tests).
- The actual `git remote get-url origin` shell-out (smoke-tested manually; the parsing logic is tested via injection of remote strings).
- The actual `gh` CLI (mocked everywhere).

## Wire-up

### .claude-plugin/plugin.json

```json
{
  "name": "harness-framework",
  "version": "0.6.0",
  "...": "...",
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "cd \"${CLAUDE_PLUGIN_ROOT}\" && go run ./hooks" }
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
}
```

### Build-tag isolation in `hooks/`

The existing `setup-failure-detector.go` and its test gain a build constraint at the top:

```go
//go:build !error_autofiler
package main
```

This is purely structural — it ensures `package main` resolves to exactly one entry point per build invocation. The Stop hook continues to invoke `go run ./hooks` (default build tags, autofiler excluded), and the PostToolUse hook invokes `go run -tags=error_autofiler ./hooks` (autofiler included, setup-failure-detector excluded).

### CLAUDE.md update

A new subsection under "Architecture" (~12 lines):

> ### Auto issue opening
>
> A `PostToolUse(Bash)` hook (`hooks/error-issue-autofiler.go`, build tag `error_autofiler`) observes every Bash invocation and opens a GitHub issue when a framework Go script panics, fails to compile, or emits a Signal with `verdict=error` plus an internal `metadata.kind`. Per-fingerprint dedup uses a 3-layer cascade: local `<projectRoot>/.runtime/auto-issues.json` cache, then `gh issue list --search "harness-fp:<fingerprint>"`, then `gh issue create`. The hook always exits 0 — internal failures (no `gh` auth, no GitHub remote, unparseable cache, …) degrade silently to stderr.
>
> Disable per-shell with `HARNESS_AUTOFILE_ISSUES=0`. The repo it files against is derived from `git remote get-url origin` of the project root resolved by `lib/registry.Lookup(cwd)`; the framework expects a label `auto-filed` to exist on that repo (create once).

## Acceptance criteria

- [ ] `hooks/error-issue-autofiler.go` exists and is the only `package main` file built under `-tags=error_autofiler ./hooks`.
- [ ] `hooks/setup-failure-detector.go` and its test have `//go:build !error_autofiler`. The Stop hook still runs unchanged.
- [ ] `go vet -tags=error_autofiler ./hooks` and `go test -tags=error_autofiler ./hooks` both pass.
- [ ] `.claude-plugin/plugin.json` has a `PostToolUse` entry as specified.
- [ ] When an agent runs `go run ./skills/run-sensor/scripts <broken-sensor>` and the runner panics, a new issue is filed on first run; a second run within the same session adds a `+1 occurrence` comment instead of a duplicate issue.
- [ ] When `HARNESS_AUTOFILE_ISSUES=0`, no `gh` calls happen for any command.
- [ ] When the Bash command is unrelated to the framework, the hook performs no `gh` invocations and no cache I/O (verified by `TestRunFlow_NonFrameworkCommand_NoOp`). Low latency is a goal, not an asserted SLO — the matcher short-circuit before any other work makes this naturally fast.
- [ ] When `gh` is not installed or not authenticated, the hook logs to stderr and exits 0; the agent's turn is unaffected.
- [ ] When the project root cannot be resolved (no env var, no `sensors/` ancestor), the hook logs to stderr and exits 0.
- [ ] Cache file `<projectRoot>/.runtime/auto-issues.json` is created on first successful write and uses `flock` for concurrent-access safety.
- [ ] All ten integration tests listed under "Testing" pass.
- [ ] CLAUDE.md gains the Auto issue opening subsection.

## Open questions

None. Design discussion fully resolved every decision point. Implementation plan to follow via the `superpowers:writing-plans` skill.
