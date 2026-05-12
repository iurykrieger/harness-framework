package watcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSpawn_PropagatesRunID(t *testing.T) {
	// We can't run real Spawn here because there's no watcher binary.
	// Validate via a code-shape check: SpawnOpts has RunID, and the env
	// block in realSpawn includes HARNESS_WATCHER_RUN_ID. Done by reading
	// the source — but a better integration check is to install a fake
	// `watcher` next to the test binary that echoes its env to a file,
	// then assert the file contains HARNESS_WATCHER_RUN_ID=opts.RunID.

	tmp := t.TempDir()
	exe, _ := os.Executable()
	watcher := filepath.Join(filepath.Dir(exe), "watcher")
	// Bash script that writes env to $HARNESS_WATCHER_RUN_ID-out
	stub := []byte("#!/bin/sh\nenv | grep HARNESS_WATCHER_RUN_ID > " + tmp + "/env.out\n")
	if err := os.WriteFile(watcher, stub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(watcher) })

	rawLog := filepath.Join(tmp, "raw.log")
	sigLog := filepath.Join(tmp, "signals.log")
	if err := os.WriteFile(rawLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Spawn(SpawnOpts{
		ProjectRoot:    tmp,
		SensorID:       "x",
		RunID:          "run-abc-123",
		RawLogPath:     rawLog,
		SignalsLogPath: sigLog,
		EnvelopeJSON:   []byte(`{}`),
		PatternsJSON:   []byte(`[]`),
		SubprocessPID:  os.Getpid(),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Give the stub a moment to flush
	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(filepath.Join(tmp, "env.out"))
		if len(got) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(string(got), "HARNESS_WATCHER_RUN_ID=run-abc-123") {
		t.Errorf("env.out = %q, want it to contain HARNESS_WATCHER_RUN_ID=run-abc-123", got)
	}
}
