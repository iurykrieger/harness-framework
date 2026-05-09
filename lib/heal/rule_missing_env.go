// lib/heal/rule_missing_env.go
package heal

import "regexp"

// ruleMissingEnv fires when verdict=error AND an evidence rationale
// matches "required environment variable <NAME> not set" AND <NAME>
// is declared in the failed sensor's requires.env[].
type ruleMissingEnv struct{}

var missingEnvRegex = regexp.MustCompile(`(?i)required env(?:ironment)? variable\s+([A-Z_][A-Z0-9_]*)\s+(?:is\s+)?not\s+set`)

func (ruleMissingEnv) Name() string { return "missing-env" }

func (ruleMissingEnv) Match(signal Signal, failed FailedSensor) (bool, Shape, string) {
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
				return true, ShapeMissingEnv, name
			}
		}
	}
	return false, "", ""
}
