package stack

import (
	"strings"
	"testing"
)

func TestCheckJourneyArchetypes_OK(t *testing.T) {
	s := &Stack{
		Archetypes: []Archetype{ArchetypeHTTPAPI},
		Journeys: []Journey{
			{ID: "j1", Archetype: ArchetypeHTTPAPI},
		},
	}
	if err := CheckJourneyArchetypes(s); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestCheckJourneyArchetypes_Orphan(t *testing.T) {
	s := &Stack{
		Archetypes: []Archetype{ArchetypeHTTPAPI},
		Journeys: []Journey{
			{ID: "j1", Archetype: ArchetypeQueueConsumer},
		},
	}
	err := CheckJourneyArchetypes(s)
	if err == nil {
		t.Fatal("expected error for orphan archetype, got nil")
	}
	if !strings.Contains(err.Error(), "queue-consumer") || !strings.Contains(err.Error(), "j1") {
		t.Errorf("error %q must name both the journey id and the archetype", err)
	}
}

func TestCheckProducedBy_Orphan(t *testing.T) {
	s := &Stack{
		Components: []Component{{Name: "real"}},
		LogShapes:  []LogShape{{ID: "lost", ProducedBy: []string{"ghost"}}},
	}
	err := CheckProducedBy(s)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "lost") || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q must name both the log_shape id and the orphan component", err)
	}
}
