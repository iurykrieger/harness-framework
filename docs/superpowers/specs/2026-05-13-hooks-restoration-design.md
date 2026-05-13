# Hooks restoration design

Status: proposed
Date: 2026-05-13
Related: `hooks/setup-failure-detector.go`, `hooks/error-issue-autofiler.go`, `.claude-plugin/plugin.json`, `lib/transcript/` (new), `CLAUDE.md`

## Why

The plugin ships two hooks that should activate without user intervention:

- **`Stop` → `setup-failure-detector`** — at the end of every Claude Code turn, scans the transcript for the most-recent `/run-sensor`, `/start-sensor`, or `/stop-sensor` failure. When the failure shape matches a setup-class rule (missing env var, missing tool, missing context file, …), prints a `hookSpecificOutput.additionalContext` directive instructing the agent to invoke `/heal-sensor` before reporting the failure to the user.
- **`PostToolUse(Bash)` → `error-issue-autofiler`** — after every Bash tool result, inspects whether the command touched a framework script and whether it crashed (panic, compile error, framework-internal Signal error, exit non-zero with stderr). On hit, opens (or +1s) a GitHub issue against the plugin repo using a 3-layer dedup cascade (local cache → `gh issue list --search` → `gh issue create`).

Both hooks are wired in `.claude-plugin/plugin.json` and Claude Code does invoke them on every Stop / PostToolUse event. Both hooks always `exit 0`. Neither has ever produced visible output in a real session.

Three independent bugs explain the silence; one cosmetic gap completes the picture.

### Bug 1 — `setup-failure-detector` parses a transcript shape that does not exist

`hooks/setup-failure-detector.go` defines:

```go
type transcriptEntry struct {
    Type    string          `json:"type"`
    Content json.RawMessage `json:"content"`
    Input   json.RawMessage `json:"input"`
}
```

…and reads `e.Content` directly off the top-level JSONL record. The real Claude Code transcript on disk (verified against `~/.claude/projects/<slug>/<sessionID>.jsonl`) is:

```jsonl
{"type":"user","message":{"role":"user","content":"<command-message>brainstorming</command-message>\n<command-name>/brainstorming</command-name>\n<command-args>…</command-args>"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"…"},{"type":"tool_use","id":"…","name":"Bash","input":{"command":"…"}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"…","content":"…JSONL…"}]}}
```

Two consequences:

1. `e.Content` is always empty because the content lives at `e.Message.Content`. `contentText()` returns `""` for every entry. `scanTranscript()` walks the full file and never finds a `tool_result` whose last JSONL line is an aggregate Signal.
2. Even if the JSON path were fixed, the literal substring `"/run-sensor "` does not appear in user messages — slash commands are wrapped in `<command-name>/run-sensor</command-name>\n<command-args>…</command-args>` blocks. `findSensorInvocation()` would still find nothing.

The existing unit tests (`hooks/setup-failure-detector_test.go`) construct fake transcripts in the old `{type, content}` shape, so the bug is invisible from the test suite.

### Bug 2 — `error-issue-autofiler` regex with `-C` in the wrong position

`buildFrameworkCommandPatterns` produces:

```
go\s+(?:-C\s+\S+\s+)?run\s+(?:-tags=\S+\s+)?\./skills/(...)-sensors?/scripts\b
```

The optional `-C <path>` is matched **between `go` and `run`**. The canonical invocation contract documented in `CLAUDE.md` and used by every skill in `skills/*/SKILL.md` (and every test in `lib/registry/discovery_e2e_test.go`) puts `-C` **after** `run`:

```
go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational ./skills/run-sensor/scripts foo
```

Result: `commandTouchesFramework()` returns `false` for every real sensor invocation. `run()` returns 0 at line 184 before any classification. No issue is ever filed.

The existing unit tests (`hooks/error-issue-autofiler_test.go` lines 92–116) use the **old** invocation form (`go run -tags=… ./skills/…`) and so happily pass against a regex that does not match production reality.

### Bug 3 — Dead regex branches for installed binaries

`buildFrameworkCommandPatterns` also matches `harness-<skill>-sensors?` and `harness-watcher` binaries on `PATH`. `CLAUDE.md` is explicit: *"The plugin does not ship pre-built binaries. Every script is invoked via `go run`."* These regex branches cannot match anything in real use; they exist only as carry-over from before commit `46aae8e` (`feat: go run invocation contract — closes #15`). Same observation for the binary fallback in `extractSkill`.

Leaving them in place is not strictly broken, but per project rule 10 *"No temporal or version-history content"* the hooks should not carry shapes that exist only to please an old reality. Strip them.

### Bug 4 — `.claude-plugin/plugin.json` does not follow the canonical contract

Today:

```json
"command": "cd \"${CLAUDE_PLUGIN_ROOT}\" && go run ./hooks"
"command": "cd \"${CLAUDE_PLUGIN_ROOT}\" && go run -tags=error_autofiler ./hooks"
```

CLAUDE.md's canonical contract is:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> \
  ./skills/<name>/scripts <args>
```

Two specific gaps:

- **Missing `GOWORK=off`.** If the user has a `go.work` in any ancestor of the plugin checkout (Stone-internal monorepos commonly do), `go` consults the workspace before the plugin's `go.mod`. Depending on the workspace's `use` directives, `go run ./hooks` either fails outright or silently resolves the wrong module.
- **`cd && go run ./hooks` vs `go run -C ./hooks`.** The current form does work today on a clean checkout, but it is inconsistent with every other invocation in the codebase and undermines rule 6 ("Deterministic logic in Go, not in skill markdown") at the manifest level too.

`HARNESS_REGISTRY_ROOT="$(pwd)"` is **not** needed for hooks because Claude Code already includes `cwd` in the hook input JSON; the hooks consume `in.Cwd` and pass it to `registry.Lookup`.

## What changes

### 1. New shared package `lib/transcript/`

Three files, each named for the action it performs (rule 9):

- `lib/transcript/scan.go` — `func Scan(path string) ([]Entry, error)` reads the JSONL transcript and returns normalized entries. Tolerant of unknown top-level fields (just `encoding/json` defaults).
- `lib/transcript/entry.go` — types and accessors:
  ```go
  type Entry struct {
      Type    string  // "user" | "assistant" | other
      Message Message // raw
  }
  type Message struct {
      Role    string
      Content Content // string OR []Block
  }
  type Block struct {
      Type       string // "text" | "tool_use" | "tool_result"
      Text       string // when Type == "text"
      ToolUseID  string // when Type == "tool_use" or "tool_result"
      Name       string // when Type == "tool_use" (e.g. "Bash")
      Input      json.RawMessage
      ContentRaw json.RawMessage // when Type == "tool_result"; string OR []Block
  }

  func (e Entry) Text() string          // flattens Message.Content to plain text
  func (e Entry) ToolResults() []Block  // assistant-side tool_results
  func (e Entry) ToolUses() []Block     // assistant-side tool_uses
  ```
- `lib/transcript/slashcmd.go` — `func ParseSlashCommand(text string) (name string, args string, ok bool)` extracts `/foo` and `args` from `<command-name>/foo</command-name>\n<command-args>…</command-args>` blocks.

Test taxonomy follows rule 11:

- `lib/transcript/testdata/` — real-shape fixtures: `run-sensor-error.jsonl`, `run-sensor-pass.jsonl`, `cascade-failed-dep.jsonl`, `already-healed.jsonl`, `slash-command-only.jsonl`.
- `lib/transcript/transcripttest/` — Go test helpers for cross-package consumers (the hooks). Single function `Load(t *testing.T, name string) []transcript.Entry`.

### 2. `hooks/setup-failure-detector.go` reworked over `lib/transcript`

- Drop the local `transcriptEntry` struct, `contentText`, and the ad-hoc `strings.Contains` slash-command matching.
- `scanTranscript`, `findSensorInvocation`, `anyHealAfter` rewritten in terms of `transcript.Entry` / `transcript.ParseSlashCommand`.
- For `tool_result.Content` (where the JSONL aggregate Signal lives), parse the **last** JSONL line of the block's content string — same algorithm as before, just sourced from the right field.
- Slash-command detection accepts both forms: (a) `ParseSlashCommand(entry.Text())` for the user-typed slash command, (b) literal-text fallback that matches `/run-sensor `, `/start-sensor `, `/stop-sensor ` substrings inside assistant tool_uses (covers cases where the agent itself runs the skill via a Bash shim, e.g. inside a sub-agent).

The `loadFailedSensorView`, `signalFromMap`, and `emitInjection` helpers are unchanged. The output JSON contract on stdout is unchanged. The hook still exits 0 on every path except malformed stdin.

### 3. `hooks/error-issue-autofiler.go` regex repair

- `buildFrameworkCommandPatterns` rewritten with `-C` after `run`:
  ```go
  return []*regexp.Regexp{
      regexp.MustCompile(`go\s+run\s+(?:-C\s+\S+\s+)?(?:-tags=\S+\s+)*\./skills/(?:` + skills + `)-sensors?/scripts\b`),
      regexp.MustCompile(`go\s+run\s+(?:-C\s+\S+\s+)?(?:-tags=\S+\s+)*\./hooks\b`),
      regexp.MustCompile(`go\s+(?:test|vet|build)\s+(?:-C\s+\S+\s+)?(?:-tags=\S+\s+)*\./(?:skills|lib|hooks)\b`),
  }
  ```
  Notes: `\s+` between `go` and `run` (single space in practice, but tolerant of multiple). `(?:-tags=\S+\s+)*` allows zero or more tags flags (just one in practice). The `-C <path>` group is correctly placed for both `run` and `test|vet|build`.
- Remove the `harness-<skill>-sensors?` and `harness-watcher` branches entirely. Same for the corresponding fallbacks in `extractSkill` — only the "scripts path" and "hooks" / "test" forms remain. The watcher's framework crashes still get caught when the watcher binary itself crashes via `go run` inside `start-sensor` (which is what actually runs in production), via the `./skills/start-sensor/scripts` regex.

### 4. `.claude-plugin/plugin.json` aligned with the canonical contract

```json
"hooks": {
  "Stop": [
    {
      "matcher": "",
      "hooks": [
        { "type": "command", "command": "GOWORK=off go run -C \"${CLAUDE_PLUGIN_ROOT}\" ./hooks" }
      ]
    }
  ],
  "PostToolUse": [
    {
      "matcher": "Bash",
      "hooks": [
        { "type": "command", "command": "GOWORK=off go run -C \"${CLAUDE_PLUGIN_ROOT}\" -tags=error_autofiler ./hooks" }
      ]
    }
  ]
}
```

### 5. Tests

- New: `lib/transcript/scan_test.go`, `slashcmd_test.go`, `entry_test.go` — table-driven against `lib/transcript/testdata/`.
- `hooks/setup-failure-detector_test.go` — all existing cases rewritten to consume `transcripttest.Load(t, "run-sensor-error.jsonl")` etc. Cascade test uses `cascade-failed-dep.jsonl`. The old-shape inline transcripts removed.
- `hooks/error-issue-autofiler_test.go` — `TestCommandTouchesFramework` and `TestExtractSkill` updated to the canonical command shape (`go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=… ./skills/…/scripts …`). All `harness-<skill>` and `harness-watcher` cases removed. New negative case: `cd "${CLAUDE_PLUGIN_ROOT}" && go run ./hooks` (the legacy form should NOT match — we have moved on).

## Anti-scope

- **No** changes to `lib/heal/`. The classifier was always correct; it was just receiving empty Signals.
- **No** new hooks, no new dedup layers, no new signal-shape inference.
- **No** behavioral change to `/heal-sensor`. The hook still emits the same `additionalContext` JSON contract.
- **No** rework of `lib/registry/Lookup` — the autofiler keeps using it as-is.
- **No** schema bumps. None of the three plugin schemas (`sensor.json`, `signal.json`, `stack.json`) are touched.

## Definition of Done (binary)

1. Running `setup-failure-detector` against `lib/transcript/testdata/run-sensor-error.jsonl` (a real-shape transcript where `/run-sensor` failed with a missing env var) prints exactly one `{"hookSpecificOutput": {"hookEventName":"Stop","additionalContext":"...`/heal-sensor`..."}}` JSON object to stdout.
2. Running `setup-failure-detector` against `lib/transcript/testdata/run-sensor-pass.jsonl` (real-shape, passing) prints nothing.
3. Running `setup-failure-detector` against `lib/transcript/testdata/already-healed.jsonl` (real-shape, failure followed by `/heal-sensor`) prints nothing.
4. `commandTouchesFramework("go run -C /plugin -tags=run_computational ./skills/run-sensor/scripts foo")` returns `true`. `commandTouchesFramework("go run -C /plugin -tags=error_autofiler ./hooks")` returns `true`. Both `extractSkill` calls return the correct skill name.
5. `commandTouchesFramework("cd /plugin && go run ./hooks")` returns `false` (the legacy plugin.json form is no longer a recognized framework command — and it does not need to be, because plugin.json itself no longer uses it).
6. `go test ./lib/transcript/... ./hooks/...` passes, including all new fixture-based cases. No test depends on the old `{type, content}` transcript shape or on `harness-<skill>` binary names.
7. `.claude-plugin/plugin.json` validates as JSON, the two command strings contain `GOWORK=off` and `go run -C "${CLAUDE_PLUGIN_ROOT}"`, and `cd` is absent.
8. Manual smoke: in a sample project where `/run-sensor watch-logs` fails because `RSA_PRIVATE_KEY` is unset, the next Stop hook firing surfaces a `/heal-sensor` directive in the conversation.

## Risks & trade-offs

- **First wave of auto-filed issues.** Until now the autofiler has been a no-op; after the fix it will start filing real issues against the plugin repo on the first framework crash that an agent hits. The dedup cascade (cache, `gh issue list --search "harness-fp:<fp>"`, `gh issue create`) keeps this bounded, but expect a small burst until fingerprints stabilize. Acceptable — that is the intended behavior.
- **Transcript fixture decay.** If Claude Code changes its transcript JSONL schema in a future release, `lib/transcript` and the fixtures break together. Mitigation: the parser only depends on `type`, `message.role`, and `message.content` shape; unknown fields are tolerated by default. We accept the cost of one focused fix if upstream changes those three fields.
- **Slash-command form coupling.** `ParseSlashCommand` assumes the `<command-name>/X</command-name><command-args>Y</command-args>` wrapping. If Claude Code drops these tags, the parser's slash-command branch returns `ok=false` and the literal-text fallback (`/run-sensor ` inside tool_uses) takes over. The hook degrades to "catches agent-driven invocations only" rather than failing closed.
- **`GOWORK=off` is unconditional in plugin.json.** This is intentional: the hook never has a legitimate reason to consult the user's workspace. It only ever runs the plugin's own Go module.
- **No new tests against a live Claude Code session.** Fixtures are anonymized snapshots; we are not running an E2E loop against a real `claude` binary. The trade-off is fast unit tests over a slow integration suite. The manual-smoke DoD item covers the gap.
