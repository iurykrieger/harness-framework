package usecase

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

// CheckJourneyReference verifies UseCase.JourneyID matches some
// stack.Journeys[].ID. Strict by design: an empty stack.Journeys with
// any non-empty journey_id is rejected (stale UseCase pointing at a
// deleted journey).
func CheckJourneyReference(uc *UseCase, s *stack.Stack) error {
	for _, j := range s.Journeys {
		if j.ID == uc.JourneyID {
			return nil
		}
	}
	return &stack.CrossCheckError{
		Kind:    "journey_reference_orphan",
		Message: fmt.Sprintf("usecase %q references journey_id %q absent from stack.journeys[]", uc.ID, uc.JourneyID),
	}
}
