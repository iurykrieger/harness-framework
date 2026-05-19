package layer

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

// hasRole reports whether any component in the stack carries the given role.
func hasRole(s stack.Stack, role string) bool {
	for _, c := range s.Components {
		if string(c.Role) == role {
			return true
		}
	}
	return false
}

// hasArchetype reports whether the stack declares the given archetype.
func hasArchetype(s stack.Stack, a string) bool {
	for _, x := range s.Archetypes {
		if string(x) == a {
			return true
		}
	}
	return false
}

// hasLogShape reports whether the stack declares at least one log_shape.
func hasLogShape(s stack.Stack) bool { return len(s.LogShapes) > 0 }

// hasCoreSensor reports whether the given sensor id is present in the
// catalog (root-tier platform primitives).
func hasCoreSensor(cat []sensor.Sensor, id string) bool {
	for _, s := range cat {
		if s.ID == id {
			return true
		}
	}
	return false
}

// hasJourneyEntryPoints reports whether the usecase's parent journey on
// the stack has at least one declared entry_point.
func hasJourneyEntryPoints(s stack.Stack, journeyID string) bool {
	for _, j := range s.Journeys {
		if j.ID == journeyID {
			return len(j.EntryPoints) > 0
		}
	}
	return false
}
