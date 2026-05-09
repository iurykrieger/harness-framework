// lib/heal/rule_exit_code_127.go
package heal

import "strings"

// ruleExitCode127 fires when the subprocess exited 127 (sh: command
// not found) AND the failed sensor declared at least one tool in
// requires.tools — the missing binary is one of them.
type ruleExitCode127 struct{}

func (ruleExitCode127) Name() string { return "exit-code-127" }

func (ruleExitCode127) Match(signal Signal, failed FailedSensor) (bool, Shape, string) {
	if signal.Metadata.ExitCode == nil || *signal.Metadata.ExitCode != 127 {
		return false, "", ""
	}
	if len(failed.Tools) == 0 {
		return false, "", ""
	}
	return true, ShapeBinaryNotFound, strings.Join(failed.Tools, ",")
}
