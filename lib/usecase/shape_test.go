package usecase

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUseCase_RoundTrip(t *testing.T) {
	body := readTestdata(t, "canonical-usecase.json")
	var uc UseCase
	if err := json.Unmarshal(body, &uc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if uc.ID != "create-user-with-email" {
		t.Errorf("id = %q", uc.ID)
	}
	if uc.JourneyID != "user-registration" {
		t.Errorf("journey_id = %q", uc.JourneyID)
	}
	if uc.Trigger.Summary == "" || uc.Trigger.Shape == "" || uc.Trigger.Fixture == nil {
		t.Errorf("trigger decoded incomplete: %+v", uc.Trigger)
	}
	if uc.Behavior.Summary == "" {
		t.Errorf("behavior summary empty")
	}
	if len(uc.ExpectedOutcome.Invariants) == 0 {
		t.Errorf("invariants lost")
	}
	if len(uc.Evidence) != 1 || uc.Evidence[0].File == "" {
		t.Errorf("evidence lost: %+v", uc.Evidence)
	}
	out, err := json.Marshal(uc)
	if err != nil {
		t.Fatal(err)
	}
	var back UseCase
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if back.ID != uc.ID {
		t.Errorf("round-trip id mismatch")
	}
}

func TestUseCase_OptionalFieldsOmitted(t *testing.T) {
	uc := UseCase{ID: "x", Version: "0.1.0", JourneyID: "j"}
	body, err := json.Marshal(uc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, k := range []string{`"regression_priority"`, `"blind_spots"`, `"tags"`, `"references"`} {
		if strings.Contains(s, k) {
			t.Errorf("expected %s omitted, got %s", k, s)
		}
	}
}

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	_, this, _, _ := runtime.Caller(0)
	p := filepath.Clean(filepath.Join(filepath.Dir(this), "testdata", name))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
