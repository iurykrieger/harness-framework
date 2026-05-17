package step_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
)

// TestStatusConstants pins the wire-level string values of the status
// constants — they appear in templates and external consumers.
func TestStatusConstants(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"completed", step.StatusCompleted, "completed"},
		{"aborted", step.StatusAborted, "aborted"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s: got=%q want=%q", tc.name, tc.got, tc.want)
		}
	}
}

// TestStepResultZeroValue confirms the zero StepResult has a usable shape.
// In particular, an empty StepResult has no verdict, no status, and no
// allocated maps — fields are populated by step implementations as needed.
func TestStepResultZeroValue(t *testing.T) {
	r := step.StepResult{}
	if r.Verdict != signal.Verdict("") {
		t.Errorf("zero Verdict: got=%q", r.Verdict)
	}
	if r.Status != "" {
		t.Errorf("zero Status: got=%q", r.Status)
	}
	if r.Outputs != nil {
		t.Errorf("zero Outputs: got=%v", r.Outputs)
	}
	if r.Signals != nil {
		t.Errorf("zero Signals: got=%v", r.Signals)
	}
}
