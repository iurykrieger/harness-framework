// lib/heal/rules/prepare_template_copy_test.go
package rules

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestRulePrepareTemplate_CopyExampleFailed(t *testing.T) {
	r := prepareTemplateCopy{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{Lifecycle: heal.SignalLifecycle{Prepare: []heal.SignalLifecycleStep{
		{Command: "cp config/.env.example config/.env", Verdict: "fail"},
	}}}}
	matched, shape, detail := r.Match(sig, heal.FailedSensor{})
	if !matched {
		t.Fatal("expected match")
	}
	if shape != heal.ShapeEnvFileAbsent {
		t.Errorf("shape=%q", shape)
	}
	if detail == "" {
		t.Errorf("detail empty")
	}
}

func TestRulePrepareTemplate_OtherCommand(t *testing.T) {
	r := prepareTemplateCopy{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{Lifecycle: heal.SignalLifecycle{Prepare: []heal.SignalLifecycleStep{
		{Command: "make protos", Verdict: "fail"},
	}}}}
	matched, _, _ := r.Match(sig, heal.FailedSensor{})
	if matched {
		t.Fatal("non-cp command must not match")
	}
}

func TestRulePrepareTemplate_PassedStep(t *testing.T) {
	r := prepareTemplateCopy{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{Lifecycle: heal.SignalLifecycle{Prepare: []heal.SignalLifecycleStep{
		{Command: "cp .env.example .env", Verdict: "pass"},
	}}}}
	matched, _, _ := r.Match(sig, heal.FailedSensor{})
	if matched {
		t.Fatal("passed step must not match")
	}
}
