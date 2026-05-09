package subprocess_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

func TestSpawnDetached_StartsAndWritesToLog(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "raw.log")

	res, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: `echo HELLO; sleep 0.05`,
		LogFile: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-res.PGID, syscall.SIGKILL) // belt-and-suspenders

	// Wait briefly for the process to flush.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log)
		if len(data) > 0 {
			if string(data) != "HELLO\n" {
				t.Fatalf("got %q, want %q", string(data), "HELLO\n")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("log never populated")
}

func TestSpawnDetached_PIDAndPGIDPopulated(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "raw.log")

	res, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: `sleep 0.05`,
		LogFile: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-res.PGID, syscall.SIGKILL)

	if res.PID <= 0 {
		t.Fatalf("PID: got %d", res.PID)
	}
	if res.PGID <= 0 {
		t.Fatalf("PGID: got %d", res.PGID)
	}
}
