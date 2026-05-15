// lib/heal/rules/subprocess_failed.go
package rules

import (
	"strings"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

// subprocessFailed fires when a non-zero exit code is paired with either
// a metadata.heal_hint = "subprocess-failed:<detail>" prefix (the fast
// path produced by buildHealHint / stopBlockingDep when the stderr text
// matched a curated pattern) or a matching pattern in evidence[].
//
// Registered before healHint so it claims the subprocess-failed shape
// exclusively; healHint keeps handling every other shape prefix.
type subprocessFailed struct{}

func (subprocessFailed) Name() string { return "subprocess-failed" }

func (subprocessFailed) Match(signal heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	if signal.Metadata.ExitCode == nil || *signal.Metadata.ExitCode == 0 {
		return false, "", ""
	}

	// Fast path: heal_hint already carries the shape.
	if hint := signal.Metadata.HealHint; hint != "" {
		if idx := strings.Index(hint, ":"); idx > 0 {
			if heal.Shape(hint[:idx]) == heal.ShapeSubprocessFailed {
				return true, heal.ShapeSubprocessFailed, hint[idx+1:]
			}
		}
	}

	// Slow path: scan evidence rationale and excerpt for curated patterns.
	for _, ev := range signal.Evidence {
		for _, line := range []string{ev.Excerpt, ev.Rationale} {
			if line == "" {
				continue
			}
			if shape, ok := heal.MatchStderrPattern(line); ok && shape == heal.ShapeSubprocessFailed {
				return true, heal.ShapeSubprocessFailed, line
			}
		}
	}

	return false, "", ""
}
