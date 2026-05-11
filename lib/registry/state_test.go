package registry_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestLoad_Empty(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Version != 1 {
		t.Errorf("Version: got %d, want 1", rs.Version)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("Entries: got %d, want 0", len(rs.Entries))
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)

	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID:   "watch-logs",
				PID:        1234,
				PGID:       1234,
				WatcherPID: 1235,
				StartedAt:  "2026-05-09T15:30:00Z",
				Command:    "tail -f /var/log/syslog",
				LogDir:     ".runtime/sensors/watch-logs",
				HeldBy: []registry.HeldByEntry{
					{Kind: "manual", AttachedAt: "2026-05-09T15:30:00Z"},
				},
			},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	got, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rs, got) {
		t.Fatalf("round trip mismatch\nwant %+v\ngot  %+v", rs, got)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)

	rs := registry.RunningSensors{Version: 1}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(r.SensorsDir(), "running_sensors.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file not cleaned up after Save")
	}
}

func TestLoad_RejectsCorrupt(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.RegistryFile(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Load(r); err == nil {
		t.Fatal("expected error on corrupt file")
	}
}

func TestLoadOrEmpty_FileAbsent(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	rs, exists, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Errorf("exists: got true, want false")
	}
	if rs.Version != 1 {
		t.Errorf("Version: got %d, want 1", rs.Version)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("Entries: got %d, want 0", len(rs.Entries))
	}
}

func TestLoadOrEmpty_FilePresentEmpty(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	rs, exists, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Errorf("exists: got false, want true")
	}
	if rs.Version != 1 {
		t.Errorf("Version: got %d, want 1", rs.Version)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("Entries: got %d, want 0", len(rs.Entries))
	}
}

func TestLoadOrEmpty_FilePresentWithEntries(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	want := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "loop", PID: 1234, PGID: 1234, StartedAt: "2026-05-10T00:00:00Z"},
		},
	}
	if err := registry.Save(r, want); err != nil {
		t.Fatal(err)
	}
	rs, exists, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Errorf("exists: got false, want true")
	}
	if !reflect.DeepEqual(want, rs) {
		t.Fatalf("state mismatch\nwant %+v\ngot  %+v", want, rs)
	}
}

func TestSave_RejectsInvalidEntry(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)

	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID:   "bad",
				PID:        1234,
				PGID:       1234,
				WatcherPID: -1, // invalid
				StartedAt:  "t",
				Command:    "c",
				LogDir:     "d",
				HeldBy:     []registry.HeldByEntry{{Kind: "manual", AttachedAt: "t"}},
			},
		},
	}
	err := registry.Save(r, rs)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ie *registry.InvalidEntryError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidEntryError, got %T: %v", err, err)
	}
	if ie.Field != "watcher_pid" || ie.SensorID != "bad" {
		t.Errorf("got field=%q sensor=%q, want watcher_pid/bad", ie.Field, ie.SensorID)
	}
	if _, statErr := os.Stat(r.RegistryFile()); statErr == nil {
		t.Errorf("registry file should NOT exist after rejected Save")
	}
}

func TestLoadOrEmpty_FileMalformed(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.RegistryFile(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, exists, err := registry.LoadOrEmpty(r)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if exists {
		t.Errorf("exists: got true on parse error, want false")
	}
}

func TestLoadSanitized_MigratesLegacy(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "entries": [
    {
      "sensor_id": "run-api-local",
      "pid": 90006,
      "pgid": 90006,
      "watcher_pid": -1,
      "started_at": "2026-05-09T13:51:38Z",
      "command": "docker compose up",
      "log_dir": ".runtime/sensors/run-api-local",
      "held_by": [{"kind": "manual", "attached_at": "2026-05-09T13:51:38Z"}]
    }
  ]
}`)
	if err := os.WriteFile(r.RegistryFile(), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	rs, reports, err := registry.LoadSanitized(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Field != "watcher_pid" {
		t.Errorf("reports: %+v", reports)
	}
	if rs.Entries[0].WatcherPID != 0 {
		t.Errorf("WatcherPID in memory: got %d, want 0", rs.Entries[0].WatcherPID)
	}
	// Re-Save persisted on disk:
	rs2, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs2.Entries[0].WatcherPID != 0 {
		t.Errorf("WatcherPID on disk: got %d, want 0", rs2.Entries[0].WatcherPID)
	}
}

func TestLoadSanitized_NoOpOnHealthy(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	healthy := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "ok", PID: 100, PGID: 100, WatcherPID: 101,
				StartedAt: "t", Command: "c", LogDir: "d",
				HeldBy: []registry.HeldByEntry{{Kind: "manual", AttachedAt: "t"}}},
		},
	}
	if err := registry.Save(r, healthy); err != nil {
		t.Fatal(err)
	}
	statBefore, _ := os.Stat(r.RegistryFile())

	rs, reports, err := registry.LoadSanitized(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Errorf("reports: got %d, want 0", len(reports))
	}
	if len(rs.Entries) != 1 || rs.Entries[0].WatcherPID != 101 {
		t.Errorf("Entries: %+v", rs.Entries)
	}
	statAfter, _ := os.Stat(r.RegistryFile())
	if statBefore != nil && statAfter != nil && !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("mtime changed on no-op LoadSanitized")
	}
}

func TestLoadSanitized_ReturnsEmptyOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	rs, reports, err := registry.LoadSanitized(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Version != 1 || len(rs.Entries) != 0 {
		t.Errorf("rs: got %+v", rs)
	}
	if len(reports) != 0 {
		t.Errorf("reports: got %d, want 0", len(reports))
	}
}

func TestFindBlockingEntry(t *testing.T) {
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
		{SensorID: "alpha", RunID: "1-aa", Blocking: false, PID: 1, PGID: 1},
		{SensorID: "alpha", RunID: "2-bb", Blocking: true, PID: 2, PGID: 2},
		{SensorID: "beta", RunID: "3-cc", Blocking: false, PID: 3, PGID: 3},
	}}
	e := rs.FindBlockingEntry("alpha")
	if e == nil || e.RunID != "2-bb" {
		t.Fatalf("expected RunID=2-bb, got %+v", e)
	}
	if rs.FindBlockingEntry("beta") != nil {
		t.Error("expected nil for beta (no blocking entry)")
	}
	if rs.FindBlockingEntry("gamma") != nil {
		t.Error("expected nil for missing id")
	}
}

func TestFindEntries(t *testing.T) {
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
		{SensorID: "alpha", RunID: "1-aa", PID: 1, PGID: 1},
		{SensorID: "alpha", RunID: "2-bb", PID: 2, PGID: 2},
		{SensorID: "beta", RunID: "3-cc", PID: 3, PGID: 3},
	}}
	es := rs.FindEntries("alpha")
	if len(es) != 2 {
		t.Fatalf("expected 2 entries for alpha, got %d", len(es))
	}
	if rs.FindEntries("missing") != nil && len(rs.FindEntries("missing")) != 0 {
		t.Error("expected empty/nil for missing id")
	}
}

func TestFindEntryByRunID(t *testing.T) {
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
		{SensorID: "alpha", RunID: "1-aa", PID: 1, PGID: 1},
		{SensorID: "alpha", RunID: "2-bb", PID: 2, PGID: 2},
	}}
	e := rs.FindEntryByRunID("2-bb")
	if e == nil || e.SensorID != "alpha" || e.PID != 2 {
		t.Fatalf("got %+v", e)
	}
	if rs.FindEntryByRunID("missing") != nil {
		t.Error("expected nil for missing run_id")
	}
}

func TestRemoveEntryByRunID(t *testing.T) {
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
		{SensorID: "alpha", RunID: "1-aa", PID: 1, PGID: 1},
		{SensorID: "alpha", RunID: "2-bb", PID: 2, PGID: 2},
	}}
	rs.RemoveEntryByRunID("1-aa")
	if len(rs.Entries) != 1 || rs.Entries[0].RunID != "2-bb" {
		t.Fatalf("after remove: %+v", rs.Entries)
	}
	rs.RemoveEntryByRunID("missing") // no-op
	if len(rs.Entries) != 1 {
		t.Fatalf("no-op removed entries: %+v", rs.Entries)
	}
}

func TestRunningSensorEntry_RunIDBlockingRoundtrip(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{{
		SensorID: "alpha", RunID: "12345-abc12345", Blocking: true,
		PID: 100, PGID: 100, WatcherPID: 101,
		StartedAt: "2026-05-11T00:00:00Z", Command: "echo hi",
		LogDir: ".runtime/sensors/alpha/12345-abc12345",
		HeldBy: []registry.HeldByEntry{},
	}}}
	if err := registry.Save(r, rs); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := registry.Load(r)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(got.Entries))
	}
	e := got.Entries[0]
	if e.RunID != "12345-abc12345" {
		t.Errorf("RunID = %q, want %q", e.RunID, "12345-abc12345")
	}
	if !e.Blocking {
		t.Errorf("Blocking = false, want true")
	}
}
