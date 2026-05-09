// lib/heal/rule_heal_hint.go
package heal

import "strings"

// ruleHealHint fires when metadata.heal_hint is set with a known
// "<shape>:<detail>" prefix. Fast-path emitted by lib/orchestrator
// when the runner can name the failure shape directly.
type ruleHealHint struct{}

func (ruleHealHint) Name() string { return "heal-hint" }

func (ruleHealHint) Match(signal Signal, _ FailedSensor) (bool, Shape, string) {
	hint := signal.Metadata.HealHint
	if hint == "" {
		return false, "", ""
	}
	idx := strings.Index(hint, ":")
	if idx < 0 {
		return false, "", ""
	}
	shape := Shape(hint[:idx])
	if !shape.IsKnown() {
		return false, "", ""
	}
	return true, shape, hint[idx+1:]
}
