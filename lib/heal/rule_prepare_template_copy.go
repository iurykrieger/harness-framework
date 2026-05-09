// lib/heal/rule_prepare_template_copy.go
package heal

import "regexp"

// rulePrepareTemplateCopy fires when a prepare step that copies a
// .example template file failed.
type rulePrepareTemplateCopy struct{}

var prepareCopyRegex = regexp.MustCompile(`\bcp\b\s+\S+\.example\b`)

func (rulePrepareTemplateCopy) Name() string { return "prepare-template-copy" }

func (rulePrepareTemplateCopy) Match(signal Signal, _ FailedSensor) (bool, Shape, string) {
	for _, step := range signal.Metadata.Lifecycle.Prepare {
		if step.Verdict == "fail" && prepareCopyRegex.MatchString(step.Command) {
			return true, ShapeEnvFileAbsent, step.Command
		}
	}
	return false, "", ""
}
