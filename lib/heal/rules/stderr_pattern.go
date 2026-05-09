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
		if shape, ok := heal.MatchStderrPattern(ev.Rationale); ok {
			return true, shape, ev.Rationale
		}
	}
	return false, "", ""
}
