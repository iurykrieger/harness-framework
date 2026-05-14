package stack

import "fmt"

// CrossCheckError signals that a stack payload failed a post-schema
// semantic check (e.g. an orphan produced_by entry, or a journey whose
// archetype is not declared at the top level). Callers can use
// errors.As to distinguish these from I/O or schema-load failures and
// map them to a "validation failure" exit code.
type CrossCheckError struct {
	Kind    string // e.g. "produced_by_orphan", "journey_archetype_orphan"
	Message string
}

func (e *CrossCheckError) Error() string { return e.Message }

// CheckProducedBy verifies every log_shapes[].produced_by[] entry matches
// some components[].name. Replaces the per-script implementation that
// previously lived in skills/detect-sensors/scripts/write-stack.go.
func CheckProducedBy(s *Stack) error {
	names := map[string]struct{}{}
	for _, c := range s.Components {
		names[c.Name] = struct{}{}
	}
	for _, sh := range s.LogShapes {
		for _, pb := range sh.ProducedBy {
			if _, ok := names[pb]; !ok {
				return &CrossCheckError{
					Kind:    "produced_by_orphan",
					Message: fmt.Sprintf("log_shape %q references unknown component %q", sh.ID, pb),
				}
			}
		}
	}
	return nil
}

// CheckJourneyArchetypes verifies every journeys[].archetype is a value
// present in archetypes[]. Returns nil when both arrays are empty.
func CheckJourneyArchetypes(s *Stack) error {
	if len(s.Journeys) == 0 {
		return nil
	}
	known := map[Archetype]struct{}{}
	for _, a := range s.Archetypes {
		known[a] = struct{}{}
	}
	for _, j := range s.Journeys {
		if _, ok := known[j.Archetype]; !ok {
			return &CrossCheckError{
				Kind:    "journey_archetype_orphan",
				Message: fmt.Sprintf("journey %q declares archetype %q not listed in archetypes[]", j.ID, j.Archetype),
			}
		}
	}
	return nil
}
