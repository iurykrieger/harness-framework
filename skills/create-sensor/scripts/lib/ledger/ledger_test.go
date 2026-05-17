package ledger

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestLedgerRoundTrip(t *testing.T) {
	five := 5
	in := Ledger{
		Usecases: []Usecase{{
			ID:        "x",
			JourneyID: "j",
			Name:      "n",
			Tags:      []string{"a"},
			Trigger:   Trigger{Shape: "CLI", Summary: "s"},
			Behavior:  Behavior{Summary: "b", BusinessRules: []string{"r"}},
			ExpectedOutcome: Expected{
				Shape:   "shape",
				Summary: "esum",
				Fixture: map[string]any{"exit_code": float64(1)},
			},
			Evidence:   []EvidenceItem{{File: "f", LineStart: &five, Rationale: "r"}},
			SourcePath: "p",
		}},
		ProjectRoot: "/x",
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Ledger
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round-trip diff:\n  in : %#v\n  out: %#v", in, out)
	}
}
