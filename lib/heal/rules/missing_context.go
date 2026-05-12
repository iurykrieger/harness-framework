// lib/heal/rules/missing_context.go
package rules

import (
	"regexp"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

// missingContext fires when verdict=error AND an evidence rationale
// matches `Required context path "<PATH>" does not exist`. The PATH
// is returned as the rule's detail string so /heal-sensor can decide
// remediation (e.g. propose `mkdir -p <path>` or `touch <path>`).
type missingContext struct{}

var missingContextRegex = regexp.MustCompile(`Required context path "([^"]+)" does not exist`)

func (missingContext) Name() string { return "missing-context" }

func (missingContext) Match(signal heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	if signal.Verdict != "error" {
		return false, "", ""
	}
	for _, ev := range signal.Evidence {
		if m := missingContextRegex.FindStringSubmatch(ev.Rationale); m != nil {
			return true, heal.ShapeMissingContext, m[1]
		}
	}
	return false, "", ""
}
