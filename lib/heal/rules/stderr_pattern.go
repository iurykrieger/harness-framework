// lib/heal/rules/stderr_pattern.go
package rules

import "github.com/iurykrieger/harness-framework/lib/heal"

// stderrPatternRule fires when any curated stderr regex
// (lib/heal/patterns.go) matches an evidence rationale.
//
// The "Rule" suffix avoids a name clash with heal.stderrPattern (the
// curated regex+shape pair declared in lib/heal/patterns.go); even
// though that type is unexported and lives in a different package,
// keeping the suffix here makes the role obvious at the call site
// (rules.stderrPatternRule reads as "the stderr-pattern rule").
type stderrPatternRule struct{}

func (stderrPatternRule) Name() string { return "stderr-pattern" }

func (stderrPatternRule) Match(signal heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	for _, ev := range signal.Evidence {
		// Capturing patterns take priority: they return a meaningful detail
		// string (e.g. the tool name) rather than the full rationale line.
		if shape, detail, ok := heal.MatchStderrPatternCapturing(ev.Rationale); ok {
			return true, shape, detail
		}
		if shape, ok := heal.MatchStderrPattern(ev.Rationale); ok {
			return true, shape, ev.Rationale
		}
	}
	return false, "", ""
}
