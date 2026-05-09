// lib/heal/rules/prepare_template_copy.go
package rules

import (
	"regexp"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

// prepareTemplateCopy fires when a prepare step that copies a
// .example template file failed.
type prepareTemplateCopy struct{}

var prepareCopyRegex = regexp.MustCompile(`\bcp\b\s+\S+\.example\b`)

func (prepareTemplateCopy) Name() string { return "prepare-template-copy" }

func (prepareTemplateCopy) Match(signal heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	for _, step := range signal.Metadata.Lifecycle.Prepare {
		if step.Verdict == "fail" && prepareCopyRegex.MatchString(step.Command) {
			return true, heal.ShapeEnvFileAbsent, step.Command
		}
	}
	return false, "", ""
}
