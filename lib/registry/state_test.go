package registry_test

import (
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
			{SensorID: "loop", PID: 1234, StartedAt: "2026-05-10T00:00:00Z"},
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
