package rules

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestMissingContextRule_Match(t *testing.T) {
	cases := []struct {
		name       string
		sig        heal.Signal
		wantOK     bool
		wantShape  heal.Shape
		wantDetail string
	}{
		{
			name: "matches formatted rationale",
			sig: heal.Signal{
				Verdict: "error",
				Evidence: []heal.SignalEvidence{
					{Rationale: `Required context path "./.env" does not exist`},
				},
			},
			wantOK:     true,
			wantShape:  heal.ShapeMissingContext,
			wantDetail: "./.env",
		},
		{
			name: "matches with surrounding text",
			sig: heal.Signal{
				Verdict: "error",
				Evidence: []heal.SignalEvidence{
					{Rationale: `something Required context path "/abs/path/file.yaml" does not exist trailing`},
				},
			},
			wantOK:     true,
			wantShape:  heal.ShapeMissingContext,
			wantDetail: "/abs/path/file.yaml",
		},
		{
			name: "no match when verdict not error",
			sig: heal.Signal{
				Verdict: "pass",
				Evidence: []heal.SignalEvidence{
					{Rationale: `Required context path "./x" does not exist`},
				},
			},
			wantOK: false,
		},
		{
			name: "no match when rationale shape different",
			sig: heal.Signal{
				Verdict: "error",
				Evidence: []heal.SignalEvidence{
					{Rationale: "some other failure"},
				},
			},
			wantOK: false,
		},
		{
			name: "scans all evidence entries",
			sig: heal.Signal{
				Verdict: "error",
				Evidence: []heal.SignalEvidence{
					{Rationale: "unrelated"},
					{Rationale: `Required context path "second" does not exist`},
				},
			},
			wantOK:     true,
			wantShape:  heal.ShapeMissingContext,
			wantDetail: "second",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, shape, detail := missingContext{}.Match(tc.sig, heal.FailedSensor{})
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if shape != tc.wantShape {
				t.Errorf("shape = %v, want %v", shape, tc.wantShape)
			}
			if detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", detail, tc.wantDetail)
			}
		})
	}
}

func TestMissingContextRule_Name(t *testing.T) {
	if got := (missingContext{}).Name(); got != "missing-context" {
		t.Fatalf("Name() = %q, want %q", got, "missing-context")
	}
}
