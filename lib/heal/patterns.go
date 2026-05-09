// lib/heal/patterns.go
//
// Curated stderr regex set used by rule_stderr_pattern.go.
//
// metadata.heal_hint contract (consumed by rule_heal_hint.go):
//
//   heal_hint := <shape> ":" <detail>
//   shape     := "missing-env" | "binary-not-found" | "env-file-absent" | "service-unavailable"
//   detail    := opaque string (var name, binary name, path, service)
//
// Adding a shape is a versioned plugin change; deleting one is a
// breaking change.
package heal

import "regexp"

type stderrPattern struct {
	re    *regexp.Regexp
	shape Shape
}

var stderrPatterns = []stderrPattern{
	{re: regexp.MustCompile(`\bENOENT\b.*\.env\b|\.env\b.*\bENOENT\b`), shape: ShapeEnvFileAbsent},
	{re: regexp.MustCompile(`permission denied:.*\.env\b`), shape: ShapeEnvFileAbsent},
	{re: regexp.MustCompile(`connection refused.*\b(postgres|mysql|redis|kafka)\b`), shape: ShapeServiceUnavailable},
	{re: regexp.MustCompile(`\bcommand not found\b`), shape: ShapeBinaryNotFound},
}

// MatchStderrPattern returns the shape associated with the first
// curated pattern that matches text, or ok=false when none match.
func MatchStderrPattern(text string) (Shape, bool) {
	for _, p := range stderrPatterns {
		if p.re.MatchString(text) {
			return p.shape, true
		}
	}
	return "", false
}
