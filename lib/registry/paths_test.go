package registry_test

import (
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestPaths_RootSensorsDir(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	got := r.SensorsDir()
	want := filepath.Join("/tmp/proj", ".runtime", "sensors")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPaths_RegistryFile(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	got := r.RegistryFile()
	want := filepath.Join("/tmp/proj", ".runtime", "sensors", "running_sensors.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPaths_LockFile(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	got := r.LockFile()
	want := filepath.Join("/tmp/proj", ".runtime", "sensors", "running_sensors.lock")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPaths_PerSensor(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	if got, want := r.SensorDir("watch-logs"), filepath.Join("/tmp/proj", ".runtime", "sensors", "watch-logs"); got != want {
		t.Errorf("SensorDir: got %q, want %q", got, want)
	}
	if got, want := r.RawLog("watch-logs"), filepath.Join("/tmp/proj", ".runtime", "sensors", "watch-logs", "raw.log"); got != want {
		t.Errorf("RawLog: got %q, want %q", got, want)
	}
	if got, want := r.SignalsLog("watch-logs"), filepath.Join("/tmp/proj", ".runtime", "sensors", "watch-logs", "signals.log"); got != want {
		t.Errorf("SignalsLog: got %q, want %q", got, want)
	}
}
