package registry_test

import (
	"os"
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
			{Kind: "sensor", ID: "B", PID: 3_999_999, AttachedAt: "t2"},          // dead
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

func TestSummarizeHolders_All(t *testing.T) {
	holders := []registry.HeldByEntry{
		{Kind: "manual", AttachedAt: "2026-05-12T00:00:00Z"},
		{Kind: "sensor", ID: "foo", PID: 1, AttachedAt: "2026-05-12T00:00:01Z"},
	}
	out := registry.SummarizeHolders(holders, registry.SummarizeOpts{})
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(out))
	}
	m0 := out[0].(map[string]interface{})
	if m0["kind"] != "manual" || m0["attached_at"] != "2026-05-12T00:00:00Z" {
		t.Fatalf("entry 0 mismatch: %v", m0)
	}
	m1 := out[1].(map[string]interface{})
	if m1["kind"] != "sensor" || m1["id"] != "foo" || m1["pid"] != 1 {
		t.Fatalf("entry 1 mismatch: %v", m1)
	}
	if _, ok := m1["pid_alive"]; !ok {
		t.Fatal("sensor entry missing pid_alive")
	}
}

func TestSummarizeHolders_DeadOnly(t *testing.T) {
	holders := []registry.HeldByEntry{
		{Kind: "manual", AttachedAt: "2026-05-12T00:00:00Z"},
		{Kind: "sensor", ID: "live", PID: os.Getpid(), AttachedAt: "2026-05-12T00:00:01Z"},
		{Kind: "sensor", ID: "dead", PID: 3_999_999, AttachedAt: "2026-05-12T00:00:02Z"},
	}
	out := registry.SummarizeHolders(holders, registry.SummarizeOpts{DeadOnly: true})
	if len(out) != 1 {
		t.Fatalf("expected 1 dead entry, got %d", len(out))
	}
	m := out[0].(map[string]interface{})
	if m["id"] != "dead" {
		t.Fatalf("expected dead sensor, got %v", m)
	}
}

func TestSummarizeHolders_EmptyReturnsNonNil(t *testing.T) {
	out := registry.SummarizeHolders(nil, registry.SummarizeOpts{})
	if out == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(out))
	}
}
