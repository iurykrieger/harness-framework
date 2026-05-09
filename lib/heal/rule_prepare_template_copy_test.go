// lib/heal/rule_prepare_template_copy_test.go
package heal

import "testing"

func TestRulePrepareTemplate_CopyExampleFailed(t *testing.T) {
	r := rulePrepareTemplateCopy{}
	sig := Signal{Metadata: SignalMetadata{Lifecycle: SignalLifecycle{Prepare: []SignalLifecycleStep{
		{Command: "cp config/.env.example config/.env", Verdict: "fail"},
	}}}}
	matched, shape, detail := r.Match(sig, FailedSensor{})
	if !matched {
		t.Fatal("expected match")
	}
	if shape != ShapeEnvFileAbsent {
		t.Errorf("shape=%q", shape)
	}
	if detail == "" {
		t.Errorf("detail empty")
	}
}

func TestRulePrepareTemplate_OtherCommand(t *testing.T) {
	r := rulePrepareTemplateCopy{}
	sig := Signal{Metadata: SignalMetadata{Lifecycle: SignalLifecycle{Prepare: []SignalLifecycleStep{
		{Command: "make protos", Verdict: "fail"},
	}}}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("non-cp command must not match")
	}
}

func TestRulePrepareTemplate_PassedStep(t *testing.T) {
	r := rulePrepareTemplateCopy{}
	sig := Signal{Metadata: SignalMetadata{Lifecycle: SignalLifecycle{Prepare: []SignalLifecycleStep{
		{Command: "cp .env.example .env", Verdict: "pass"},
	}}}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("passed step must not match")
	}
}
