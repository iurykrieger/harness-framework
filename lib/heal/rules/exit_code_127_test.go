// lib/heal/rules/exit_code_127_test.go
package rules

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func intp(n int) *int { return &n }

func TestRuleExit127_Positive(t *testing.T) {
	r := exitCode127{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{ExitCode: intp(127)}}
	failed := heal.FailedSensor{Tools: []string{"pnpm"}}
	matched, shape, detail := r.Match(sig, failed)
	if !matched {
		t.Fatal("expected match")
	}
	if shape != heal.ShapeBinaryNotFound {
		t.Errorf("shape=%q", shape)
	}
	if detail != "pnpm" {
		t.Errorf("detail=%q", detail)
	}
}

func TestRuleExit127_NoTools(t *testing.T) {
	r := exitCode127{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{ExitCode: intp(127)}}
	matched, _, _ := r.Match(sig, heal.FailedSensor{})
	if matched {
		t.Fatal("no requires.tools — must not match")
	}
}

func TestRuleExit127_OtherCode(t *testing.T) {
	r := exitCode127{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{ExitCode: intp(1)}}
	matched, _, _ := r.Match(sig, heal.FailedSensor{Tools: []string{"pnpm"}})
	if matched {
		t.Fatal("non-127 must not match")
	}
}

func TestRuleExit127_NoExitCode(t *testing.T) {
	r := exitCode127{}
	matched, _, _ := r.Match(heal.Signal{}, heal.FailedSensor{Tools: []string{"pnpm"}})
	if matched {
		t.Fatal("missing exit_code must not match")
	}
}
