package usecase

import (
	"errors"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestCheckJourneyReference_OK(t *testing.T) {
	s := &stack.Stack{Journeys: []stack.Journey{{ID: "user-registration"}}}
	uc := &UseCase{JourneyID: "user-registration"}
	if err := CheckJourneyReference(uc, s); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCheckJourneyReference_Missing(t *testing.T) {
	s := &stack.Stack{Journeys: []stack.Journey{{ID: "user-registration"}}}
	uc := &UseCase{JourneyID: "ghost"}
	err := CheckJourneyReference(uc, s)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error must name the bad id; got %q", err)
	}
}

func TestCheckJourneyReference_EmptyStackJourneys(t *testing.T) {
	s := &stack.Stack{}
	uc := &UseCase{JourneyID: "anything"}
	err := CheckJourneyReference(uc, s)
	if err == nil {
		t.Fatal("expected error: stack has no journeys, so any journey_id is unresolved")
	}
}

func TestCheckJourneyReference_ReturnsTypedError(t *testing.T) {
	s := &stack.Stack{Journeys: []stack.Journey{{ID: "u"}}}
	uc := &UseCase{JourneyID: "ghost"}
	err := CheckJourneyReference(uc, s)
	var cce *stack.CrossCheckError
	if !errors.As(err, &cce) {
		t.Fatalf("expected *stack.CrossCheckError, got %T", err)
	}
	if cce.Kind != "journey_reference_orphan" {
		t.Errorf("kind = %q", cce.Kind)
	}
}
