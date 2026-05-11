package registry_test

import (
	"errors"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func validEntry() registry.RunningSensorEntry {
	return registry.RunningSensorEntry{
		SensorID:   "ok",
		PID:        100,
		PGID:       100,
		WatcherPID: 101,
		StartedAt:  "2026-05-11T00:00:00Z",
		Command:    "true",
		LogDir:     ".runtime/sensors/ok",
		HeldBy: []registry.HeldByEntry{
			{Kind: "manual", AttachedAt: "2026-05-11T00:00:00Z"},
		},
	}
}

func TestValidateEntry_AcceptsValid(t *testing.T) {
	cases := []registry.RunningSensorEntry{
		validEntry(),
		func() registry.RunningSensorEntry {
			e := validEntry()
			e.WatcherPID = 0 // orchestrator path
			return e
		}(),
		func() registry.RunningSensorEntry {
			e := validEntry()
			e.HeldBy = []registry.HeldByEntry{
				{Kind: "sensor", ID: "dep", PID: 99, AttachedAt: "2026-05-11T00:00:00Z"},
			}
			return e
		}(),
	}
	for i, c := range cases {
		if err := registry.ValidateEntry(c); err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
}

func TestValidateEntry_RejectsNegative(t *testing.T) {
	type tc struct {
		name  string
		mut   func(*registry.RunningSensorEntry)
		field string
		value int
	}
	cases := []tc{
		{"pid zero", func(e *registry.RunningSensorEntry) { e.PID = 0 }, "pid", 0},
		{"pid negative", func(e *registry.RunningSensorEntry) { e.PID = -1 }, "pid", -1},
		{"pgid zero", func(e *registry.RunningSensorEntry) { e.PGID = 0 }, "pgid", 0},
		{"pgid negative", func(e *registry.RunningSensorEntry) { e.PGID = -7 }, "pgid", -7},
		{"watcher_pid negative", func(e *registry.RunningSensorEntry) { e.WatcherPID = -1 }, "watcher_pid", -1},
		{"sensor holder pid zero", func(e *registry.RunningSensorEntry) {
			e.HeldBy = []registry.HeldByEntry{{Kind: "sensor", ID: "x", PID: 0, AttachedAt: "t"}}
		}, "held_by[0].pid", 0},
		{"manual holder pid negative", func(e *registry.RunningSensorEntry) {
			e.HeldBy = []registry.HeldByEntry{{Kind: "manual", PID: -1, AttachedAt: "t"}}
		}, "held_by[0].pid", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validEntry()
			c.mut(&e)
			err := registry.ValidateEntry(e)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			var ie *registry.InvalidEntryError
			if !errors.As(err, &ie) {
				t.Fatalf("expected *InvalidEntryError, got %T: %v", err, err)
			}
			if ie.Field != c.field || ie.Value != c.value {
				t.Errorf("got field=%q value=%d, want field=%q value=%d", ie.Field, ie.Value, c.field, c.value)
			}
			if ie.SensorID != "ok" {
				t.Errorf("SensorID: got %q, want %q", ie.SensorID, "ok")
			}
		})
	}
}
