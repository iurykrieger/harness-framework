// lib/heal/rule_exit_code_127_test.go
package heal

import "testing"

func intp(n int) *int { return &n }

func TestRuleExit127_Positive(t *testing.T) {
	r := ruleExitCode127{}
	sig := Signal{Metadata: SignalMetadata{ExitCode: intp(127)}}
	failed := FailedSensor{Tools: []string{"pnpm"}}
	matched, shape, detail := r.Match(sig, failed)
	if !matched {
		t.Fatal("expected match")
	}
	if shape != ShapeBinaryNotFound {
		t.Errorf("shape=%q", shape)
	}
	if detail != "pnpm" {
		t.Errorf("detail=%q", detail)
	}
}

func TestRuleExit127_NoTools(t *testing.T) {
	r := ruleExitCode127{}
	sig := Signal{Metadata: SignalMetadata{ExitCode: intp(127)}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("no requires.tools — must not match")
	}
}

func TestRuleExit127_OtherCode(t *testing.T) {
	r := ruleExitCode127{}
	sig := Signal{Metadata: SignalMetadata{ExitCode: intp(1)}}
	matched, _, _ := r.Match(sig, FailedSensor{Tools: []string{"pnpm"}})
	if matched {
		t.Fatal("non-127 must not match")
	}
}

func TestRuleExit127_NoExitCode(t *testing.T) {
	r := ruleExitCode127{}
	matched, _, _ := r.Match(Signal{}, FailedSensor{Tools: []string{"pnpm"}})
	if matched {
		t.Fatal("missing exit_code must not match")
	}
}
