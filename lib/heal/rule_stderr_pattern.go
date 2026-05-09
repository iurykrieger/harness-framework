// lib/heal/rule_stderr_pattern.go
package heal

// ruleStderrPattern fires when any curated stderr regex (patterns.go)
// matches an evidence rationale.
type ruleStderrPattern struct{}

func (ruleStderrPattern) Name() string { return "stderr-pattern" }

func (ruleStderrPattern) Match(signal Signal, _ FailedSensor) (bool, Shape, string) {
	for _, ev := range signal.Evidence {
		if shape, ok := MatchStderrPattern(ev.Rationale); ok {
			return true, shape, ev.Rationale
		}
	}
	return false, "", ""
}
