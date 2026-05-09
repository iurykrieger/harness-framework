// Package rules holds the canonical ordered list of classification
// rules consumed by lib/heal.ClassifyWith.
//
// Adding a new rule is a single edit point: create a new
// lib/heal/rules/<name>.go file with a struct implementing
// heal.Rule, then insert one line into the slice returned by
// Registered. Order is deterministic and matters: more-specific
// rules go before more-generic ones (heal-hint is a fast-path before
// regex-based rules; missing-env runs first because it's the most
// common).
package rules

import "github.com/iurykrieger/harness-framework/lib/heal"

// Registered returns the canonical ordered list of classification
// rules. Production callers invoke
// heal.ClassifyWith(rules.Registered(), signal, failed) directly.
func Registered() []heal.Rule {
	return []heal.Rule{
		missingEnv{},
		healHint{},
		exitCode127{},
		prepareTemplateCopy{},
		stderrPatternRule{},
	}
}
