package layer

import (
	"fmt"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// inferentialCommand returns a real shell invocation for an inferential
// sensor: it pipes the rendered HARNESS_PROMPT to the `claude` CLI and
// derives the exit code from the LLM's emitted verdict. Subprocess
// stdout is the LLM's raw response; the inferential runner's exit_code_map
// is what drives the aggregate Signal verdict.
//
// Verdict extraction uses sed against the raw stdout (rather than jq
// against parsed JSON) so the contract works even when the LLM wraps
// its JSON in prose, markdown fences, or other framing. The recipe-side
// extraction is intentionally permissive: it matches the FIRST
// "verdict":"<value>" pair the LLM emits anywhere in the stream.
//
// Contract:
//   - claude must be on PATH (always true inside Claude Code).
//   - The user_prompt_template instructs the LLM to emit a JSON object
//     containing a top-level "verdict" field.
//   - Exit 0 when verdict=pass; exit 1 when verdict ∈ {warn,fail,error};
//     exit 2 when no verdict line was emitted.
func inferentialCommand() string {
	// No outer `sh -c` wrapper: the runner already invokes via `sh -c`.
	return `out=$(claude --print "$HARNESS_PROMPT" 2>&1); ` +
		`printf "%s\n" "$out"; ` +
		`verdict=$(printf "%s" "$out" | sed -n 's/.*"verdict"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1); ` +
		`case "$verdict" in pass) exit 0;; warn|fail|error) exit 1;; *) exit 2;; esac`
}

// observationLogCommand returns a real shell invocation for an
// observation log-trace sensor: it lists the runtime signal log
// directories and emits a JSON marker line. Always exit 0; downstream
// patterns turn the marker into a pass Signal.
func observationLogCommand(usecaseID string) string {
	// No outer sh -c wrapper: the runner already invokes via sh -c.
	return fmt.Sprintf(
		`ls -1 .harness/runtime/ 2>/dev/null || true; `+
			`printf '{"observation":"baseline","usecase":"%s"}\n'`,
		usecaseID,
	)
}

// e2eReplayCommand returns a real shell invocation that exercises the
// trigger/outcome fixtures of the usecase. The recipe issues a fixture
// integrity check — every fixture file referenced by the usecase MUST
// exist and parse as JSON. This is strictly stronger than the previous
// `echo TODO ; false` placeholder and is the safest universal contract
// the planner can guarantee without project-specific knowledge: a real
// replay would need to know how to invoke the system under test with
// the trigger payload AND how to set up any preconditions the usecase
// implies (sensor_graph, db state, queue topology, …), and that
// knowledge is not derivable from stack.yaml alone. Operators tune the
// command per-usecase via /update-sensor once the replay shape is
// understood.
//
// assertJQ is accepted for API symmetry with future replay
// implementations; the current shape ignores it. The argument stays in
// the signature so callers (deriveScenarios) can carry per-scenario
// invariants forward without breaking when the replay path lands.
func e2eReplayCommand(_ stack.Stack, uc usecase.UseCase, _ string) string {
	triggerRef := fixtureRef(uc.Trigger.Fixture)
	outcomeRef := fixtureRef(uc.ExpectedOutcome.Fixture)
	triggerPath := ".harness/fixtures/" + triggerRef
	outcomePath := ".harness/fixtures/" + outcomeRef
	if triggerRef == "" {
		triggerPath = ""
	}
	if outcomeRef == "" {
		outcomePath = ""
	}
	return e2eFixtureCheckCommand(triggerPath, outcomePath)
}

// e2eFixtureCheckCommand validates fixture presence and JSON-parseability.
// Real work — not a placeholder — even when no replay path is known.
// Each path is independently optional: a usecase that declares only a
// trigger fixture still gets a real check.
func e2eFixtureCheckCommand(triggerPath, outcomePath string) string {
	// No outer sh -c wrapper: the runner already invokes via sh -c, so
	// nested single quotes do not need to be escaped.
	var parts []string
	parts = append(parts, "set -e")
	if triggerPath != "" {
		parts = append(parts,
			fmt.Sprintf("test -f %s", shellEscape(triggerPath)),
			fmt.Sprintf("jq -e . %s >/dev/null", shellEscape(triggerPath)),
		)
	}
	if outcomePath != "" {
		parts = append(parts,
			fmt.Sprintf("test -f %s", shellEscape(outcomePath)),
			fmt.Sprintf("jq -e . %s >/dev/null", shellEscape(outcomePath)),
		)
	}
	if len(parts) == 1 {
		// No fixtures declared on the usecase — nothing real to check.
		// Emit a single pass marker so the runner sees a real Signal.
		return `printf 'no fixtures declared on usecase; skipping fixture check\n'`
	}
	parts = append(parts, "printf 'fixtures-ok\\n'")
	return strings.Join(parts, " && ")
}

// shellEscape wraps s in single quotes, escaping embedded single quotes
// per POSIX shell rules (close, '\'', reopen).
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fixtureRef extracts the ref string from a UseCase.Trigger.Fixture or
// .ExpectedOutcome.Fixture (typed as `any` in the shape). The YAML form
// is `{ref: "..."}`, which decodes to map[string]any in Go. Returns ""
// when the input is nil, not a map, or lacks a ref key.
func fixtureRef(f any) string {
	m, ok := f.(map[string]any)
	if !ok {
		return ""
	}
	r, _ := m["ref"].(string)
	return r
}

// unitTestPattern derives a Go `-run` regex from the usecase id.
// Strategy: use the last hyphen-separated token as the keyword and
// match Test names ending in or containing that token (case-insensitive
// at the boundary). For "run-sensor-dep-cascade" this returns
// "Test.*[Cc]ascade" which matches TestRunWithDeps_CascadesOnDepFail,
// TestBuildCascadeSignal_*, etc.
//
// Why last token: the rightmost segment of a usecase id is the most
// specific concept and the one most likely to appear in test names.
// Earlier tokens (journey prefix, sub-context) are too coarse.
func unitTestPattern(uc usecase.UseCase) string {
	parts := strings.Split(uc.ID, "-")
	keyword := parts[len(parts)-1]
	if keyword == "" {
		return "Test"
	}
	first := keyword[0]
	if first >= 'a' && first <= 'z' {
		// Case-insensitive first letter so we match both PascalCase and
		// underscore-prefixed Go test names.
		return fmt.Sprintf("Test.*[%c%c]%s", first-32, first, keyword[1:])
	}
	return "Test.*" + keyword
}
