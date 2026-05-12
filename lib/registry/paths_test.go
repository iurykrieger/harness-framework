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

func TestRunDir(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	got := r.RunDir("alpha", "12345-abc12345")
	want := "/tmp/proj/.runtime/sensors/alpha/12345-abc12345"
	if got != want {
		t.Errorf("RunDir = %q, want %q", got, want)
	}
}

func TestRawLogRun_SignalsLogRun(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	if got := r.RawLogRun("alpha", "1-aa"); got != "/tmp/proj/.runtime/sensors/alpha/1-aa/raw.log" {
		t.Errorf("RawLogRun = %q", got)
	}
	if got := r.SignalsLogRun("alpha", "1-aa"); got != "/tmp/proj/.runtime/sensors/alpha/1-aa/signals.log" {
		t.Errorf("SignalsLogRun = %q", got)
	}
}

func TestLegacyRawLog_LegacySignalsLog(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	if got := r.LegacyRawLog("alpha"); got != "/tmp/proj/.runtime/sensors/alpha/raw.log" {
		t.Errorf("LegacyRawLog = %q", got)
	}
	if got := r.LegacySignalsLog("alpha"); got != "/tmp/proj/.runtime/sensors/alpha/signals.log" {
		t.Errorf("LegacySignalsLog = %q", got)
	}
}
