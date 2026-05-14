package rules_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/heal/rules"
)

func TestSubprocessFailed_Match(t *testing.T) {
	intP := func(i int) *int { return &i }

	cases := []struct {
		name      string
		signal    heal.Signal
		wantMatch bool
		wantShape heal.Shape
	}{
		{
			name: "exit_code=0 — never matches",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(0),
				},
				Evidence: []heal.SignalEvidence{{Excerpt: "failed to solve: oops"}},
			},
			wantMatch: false,
		},
		{
			name: "exit_code=nil — never matches",
			signal: heal.Signal{
				Verdict:  "fail",
				Metadata: heal.SignalMetadata{},
				Evidence: []heal.SignalEvidence{{Excerpt: "failed to solve: oops"}},
			},
			wantMatch: false,
		},
		{
			name: "heal_hint with subprocess-failed prefix — match",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(1),
					HealHint: "subprocess-failed:failed to solve: oops",
				},
			},
			wantMatch: true,
			wantShape: heal.ShapeSubprocessFailed,
		},
		{
			name: "evidence excerpt matches curated pattern — match",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(1),
				},
				Evidence: []heal.SignalEvidence{
					{Excerpt: "failed to solve: process did not complete successfully: exit code: 1"},
				},
			},
			wantMatch: true,
			wantShape: heal.ShapeSubprocessFailed,
		},
		{
			name: "evidence rationale matches curated pattern — match",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(2),
				},
				Evidence: []heal.SignalEvidence{
					{Rationale: "cannot load module myservice listed in go.work file: open myservice/go.mod"},
				},
			},
			wantMatch: true,
			wantShape: heal.ShapeSubprocessFailed,
		},
		{
			name: "heal_hint with other shape prefix — no match (other rule handles)",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(1),
					HealHint: "service-unavailable:redis",
				},
			},
			wantMatch: false,
		},
	}

	registered := rules.Registered()
	var subprocFailedRule heal.Rule
	for _, r := range registered {
		if r.Name() == "subprocess-failed" {
			subprocFailedRule = r
			break
		}
	}
	if subprocFailedRule == nil {
		t.Fatal("subprocess-failed rule not registered")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched, shape, _ := subprocFailedRule.Match(tc.signal, heal.FailedSensor{})
			if matched != tc.wantMatch {
				t.Fatalf("matched=%v, want %v", matched, tc.wantMatch)
			}
			if matched && shape != tc.wantShape {
				t.Fatalf("shape=%q, want %q", shape, tc.wantShape)
			}
		})
	}
}
