package watcher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBinaryPath_NeighbourOfExecutable(t *testing.T) {
	got, err := BinaryPath()
	if err != nil {
		t.Fatalf("BinaryPath: %v", err)
	}
	exe, _ := os.Executable()
	want := filepath.Join(filepath.Dir(exe), "watcher")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSpawn_ErrorWhenBinaryAbsent(t *testing.T) {
	tmp := t.TempDir()
	rawLog := filepath.Join(tmp, "raw.log")
	sigLog := filepath.Join(tmp, "signals.log")
	if err := os.WriteFile(rawLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	pid, err := Spawn(SpawnOpts{
		ProjectRoot:    tmp,
		SensorID:       "x",
		RunID:          "r1",
		RawLogPath:     rawLog,
		SignalsLogPath: sigLog,
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
	})
	if err == nil {
		t.Fatalf("expected error when watcher binary missing, got pid=%d", pid)
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0", pid)
	}
}
