// lib/heal/rules/missing_env.go
package rules

import (
	"regexp"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

// missingEnv fires when verdict=error AND an evidence rationale
// matches "required environment variable <NAME> not set" AND <NAME>
// is declared in the failed sensor's requires[kind=env].
type missingEnv struct{}

var missingEnvRegex = regexp.MustCompile(`(?i)required env(?:ironment)? variable\s+([A-Z_][A-Z0-9_]*)\s+(?:is\s+)?not\s+set`)

func (missingEnv) Name() string { return "missing-env" }

func (missingEnv) Match(signal heal.Signal, failed heal.FailedSensor) (bool, heal.Shape, string) {
	if signal.Verdict != "error" {
		return false, "", ""
	}
	for _, ev := range signal.Evidence {
		m := missingEnvRegex.FindStringSubmatch(ev.Rationale)
		if m == nil {
			continue
		}
		name := m[1]
		for _, declared := range failed.EnvNames {
			if declared == name {
				return true, heal.ShapeMissingEnv, name
			}
		}
	}
	return false, "", ""
}
