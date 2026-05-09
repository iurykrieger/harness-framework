// lib/heal/rules/exit_code_127.go
package rules

import (
	"strings"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

// exitCode127 fires when the subprocess exited 127 (sh: command not
// found) AND the failed sensor declared at least one tool in
// requires.tools — the missing binary is one of them.
type exitCode127 struct{}

func (exitCode127) Name() string { return "exit-code-127" }

func (exitCode127) Match(signal heal.Signal, failed heal.FailedSensor) (bool, heal.Shape, string) {
	if signal.Metadata.ExitCode == nil || *signal.Metadata.ExitCode != 127 {
		return false, "", ""
	}
	if len(failed.Tools) == 0 {
		return false, "", ""
	}
	return true, heal.ShapeBinaryNotFound, strings.Join(failed.Tools, ",")
}
