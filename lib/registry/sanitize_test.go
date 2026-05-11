package registry_test

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
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

func TestSanitizeAll_RewritesWatcherPID(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "x", PID: 10, PGID: 10, WatcherPID: -1, StartedAt: "t", Command: "c", LogDir: "d"},
		},
	}
	reports := registry.SanitizeAll(rs)
	if len(reports) != 1 {
		t.Fatalf("reports: got %d, want 1", len(reports))
	}
	r := reports[0]
	if r.SensorID != "x" || r.Field != "watcher_pid" || r.OldValue != -1 || r.Dropped {
		t.Errorf("unexpected report: %+v", r)
	}
	if rs.Entries[0].WatcherPID != 0 {
		t.Errorf("WatcherPID: got %d, want 0", rs.Entries[0].WatcherPID)
	}
}

func TestSanitizeAll_DropsHolderWithBadSensorPID(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID: "x", PID: 10, PGID: 10, WatcherPID: 11,
				StartedAt: "t", Command: "c", LogDir: "d",
				HeldBy: []registry.HeldByEntry{
					{Kind: "sensor", ID: "dep", PID: -1, AttachedAt: "t"},
					{Kind: "manual", AttachedAt: "t"},
				},
			},
		},
	}
	reports := registry.SanitizeAll(rs)
	if len(reports) != 1 {
		t.Fatalf("reports: got %d, want 1", len(reports))
	}
	r := reports[0]
	if r.Field != "held_by[0].pid" || !r.Dropped || r.OldValue != -1 {
		t.Errorf("unexpected report: %+v", r)
	}
	if len(rs.Entries[0].HeldBy) != 1 || rs.Entries[0].HeldBy[0].Kind != "manual" {
		t.Errorf("HeldBy after drop: %+v", rs.Entries[0].HeldBy)
	}
}

func TestSanitizeAll_DropsEntryWithBadPID(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "bad", PID: -1, PGID: 10, WatcherPID: 11, StartedAt: "t", Command: "c", LogDir: "d"},
			{SensorID: "good", PID: 10, PGID: 10, WatcherPID: 11, StartedAt: "t", Command: "c", LogDir: "d"},
		},
	}
	reports := registry.SanitizeAll(rs)
	if len(reports) != 1 {
		t.Fatalf("reports: got %d, want 1", len(reports))
	}
	r := reports[0]
	if r.SensorID != "bad" || r.Field != "pid" || !r.Dropped || r.OldValue != -1 {
		t.Errorf("unexpected report: %+v", r)
	}
	if len(rs.Entries) != 1 || rs.Entries[0].SensorID != "good" {
		t.Errorf("Entries after drop: %+v", rs.Entries)
	}
}

func TestSanitizeAll_Idempotent(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "x", PID: 10, PGID: 10, WatcherPID: -1, StartedAt: "t", Command: "c", LogDir: "d"},
		},
	}
	if got := registry.SanitizeAll(rs); len(got) != 1 {
		t.Fatalf("first call: got %d reports, want 1", len(got))
	}
	if got := registry.SanitizeAll(rs); len(got) != 0 {
		t.Fatalf("second call: got %d reports, want 0", len(got))
	}
}

func TestSanitizeAll_NoOpOnHealthy(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "x", PID: 10, PGID: 10, WatcherPID: 11, StartedAt: "t", Command: "c", LogDir: "d"},
		},
	}
	if got := registry.SanitizeAll(rs); len(got) != 0 {
		t.Fatalf("got %d reports, want 0", len(got))
	}
}

func TestRegistryMigratedSignal_Shape(t *testing.T) {
	res := registry.Result{
		Root:        registry.NewRoot("/tmp/proj"),
		ProjectRoot: "/tmp/proj",
		Source:      registry.SourceWalkUp,
		Exists:      true,
	}
	reports := []registry.SanitizeReport{
		{SensorID: "run-api-local", Field: "watcher_pid", OldValue: -1, Dropped: false},
	}
	sig := registry.RegistryMigratedSignal(res, reports, "list-sensors")
	if got, _ := sig["verdict"].(string); got != "warn" {
		t.Errorf("verdict: got %q, want warn", got)
	}
	if got, _ := sig["severity"].(string); got != "low" {
		t.Errorf("severity: got %q, want low", got)
	}
	if got, _ := sig["sensor_id"].(string); got != "list-sensors" {
		t.Errorf("sensor_id: got %q, want list-sensors", got)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if md == nil {
		t.Fatal("metadata: nil")
	}
	if got, _ := md["kind"].(string); got != "registry_migrated" {
		t.Errorf("metadata.kind: got %q, want registry_migrated", got)
	}
	if got, _ := md["registry_path"].(string); got == "" {
		t.Errorf("metadata.registry_path missing")
	}
	rpts, ok := md["reports"].([]registry.SanitizeReport)
	if !ok || len(rpts) != 1 || rpts[0].Field != "watcher_pid" {
		t.Errorf("metadata.reports: got %v", md["reports"])
	}

	// Round-trip via JSON to confirm the envelope marshals and the
	// declared "passes signal.json validation" contract holds.
	encoded, err := json.Marshal(sig)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	v, vCode := schema.LoadValidator(testfixtures.RepoSchemasDir(t), io.Discard)
	if vCode != 0 {
		t.Fatalf("schema validator init failed (code %d)", vCode)
	}
	if err := v.Validate(schema.TargetSignal, decoded); err != nil {
		t.Fatalf("signal failed schema validation: %v", err)
	}
}

func TestRegistryMigratedSignal_DroppedReport(t *testing.T) {
	res := registry.Result{
		Root:        registry.NewRoot("/tmp/proj"),
		ProjectRoot: "/tmp/proj",
		Source:      registry.SourceWalkUp,
		Exists:      true,
	}
	reports := []registry.SanitizeReport{
		{SensorID: "bad", Field: "pid", OldValue: -1, Dropped: true},
		{SensorID: "ok", Field: "watcher_pid", OldValue: -1, Dropped: false},
	}
	sig := registry.RegistryMigratedSignal(res, reports, "start-sensor")

	ev, _ := sig["evidence"].([]interface{})
	if len(ev) != 1 {
		t.Fatalf("evidence: got %d, want 1", len(ev))
	}
	first, _ := ev[0].(map[string]interface{})
	rationale, _ := first["rationale"].(string)
	if !strings.Contains(rationale, "rewrote 1") {
		t.Errorf("rationale missing rewrite count: %q", rationale)
	}
	if !strings.Contains(rationale, "dropped 1") {
		t.Errorf("rationale missing drop count: %q", rationale)
	}

	md, _ := sig["metadata"].(map[string]interface{})
	rpts, _ := md["reports"].([]registry.SanitizeReport)
	if len(rpts) != 2 {
		t.Fatalf("reports: got %d, want 2", len(rpts))
	}
	if !rpts[0].Dropped || rpts[1].Dropped {
		t.Errorf("dropped flags lost in passthrough: %+v", rpts)
	}
}
