package registry_test

import (
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestAddHolder_AppendsRecord(t *testing.T) {
	e := &registry.RunningSensorEntry{}
	registry.AddHolder(e, registry.HeldByEntry{Kind: "manual", AttachedAt: "t1"})
	if len(e.HeldBy) != 1 || e.HeldBy[0].Kind != "manual" {
		t.Fatalf("got %+v", e.HeldBy)
	}
}

func TestRemoveHolder_Manual(t *testing.T) {
	e := &registry.RunningSensorEntry{
		HeldBy: []registry.HeldByEntry{
			{Kind: "manual", AttachedAt: "t1"},
			{Kind: "sensor", ID: "B", PID: 100, AttachedAt: "t2"},
		},
	}
	removed := registry.RemoveHolder(e, registry.HeldByEntry{Kind: "manual"})
	if !removed {
		t.Fatal("expected RemoveHolder to return true")
	}
	if len(e.HeldBy) != 1 || e.HeldBy[0].Kind != "sensor" {
		t.Fatalf("after manual removal: %+v", e.HeldBy)
	}
}

func TestRemoveHolder_SensorMatchesIDAndPID(t *testing.T) {
	e := &registry.RunningSensorEntry{
		HeldBy: []registry.HeldByEntry{
			{Kind: "sensor", ID: "B", PID: 100, AttachedAt: "t1"},
			{Kind: "sensor", ID: "B", PID: 200, AttachedAt: "t2"},
		},
	}
	removed := registry.RemoveHolder(e, registry.HeldByEntry{Kind: "sensor", ID: "B", PID: 100})
	if !removed {
		t.Fatal("expected RemoveHolder to return true")
	}
	if len(e.HeldBy) != 1 || e.HeldBy[0].PID != 200 {
		t.Fatalf("after sensor removal: %+v", e.HeldBy)
	}
}

func TestIsHeld(t *testing.T) {
	e := &registry.RunningSensorEntry{}
	if registry.IsHeld(e) {
		t.Fatal("empty held_by must report not held")
	}
	registry.AddHolder(e, registry.HeldByEntry{Kind: "manual"})
	if !registry.IsHeld(e) {
		t.Fatal("entry with manual hold must report held")
	}
}

func TestReapDead_DropsDeadHolders(t *testing.T) {
	e := &registry.RunningSensorEntry{
		HeldBy: []registry.HeldByEntry{
			{Kind: "manual", AttachedAt: "t1"},
			{Kind: "sensor", ID: "B", PID: 3_999_999, AttachedAt: "t2"}, // dead
			{Kind: "sensor", ID: "C", PID: registry.SelfPID(), AttachedAt: "t3"}, // alive
		},
	}
	reaped := registry.ReapDead(e)
	if len(reaped) != 1 || reaped[0].PID != 3_999_999 {
		t.Fatalf("reaped: %+v", reaped)
	}
	want := []registry.HeldByEntry{
		{Kind: "manual", AttachedAt: "t1"},
		{Kind: "sensor", ID: "C", PID: registry.SelfPID(), AttachedAt: "t3"},
	}
	if !reflect.DeepEqual(e.HeldBy, want) {
		t.Fatalf("after reap: %+v", e.HeldBy)
	}
}
