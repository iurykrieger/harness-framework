// lib/heal/rules/heal_hint.go
package rules

import (
	"strings"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

// healHint fires when metadata.heal_hint is set with a known
// "<shape>:<detail>" prefix. Fast-path emitted by lib/orchestrator
// when the runner can name the failure shape directly.
type healHint struct{}

func (healHint) Name() string { return "heal-hint" }

func (healHint) Match(signal heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	hint := signal.Metadata.HealHint
	if hint == "" {
		return false, "", ""
	}
	idx := strings.Index(hint, ":")
	if idx < 0 {
		return false, "", ""
	}
	shape := heal.Shape(hint[:idx])
	if !shape.IsKnown() {
		return false, "", ""
	}
	return true, shape, hint[idx+1:]
}
