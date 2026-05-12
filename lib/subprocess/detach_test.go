package subprocess_test

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSpawnDetached_RespectsDir(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "out.log")
	if err := os.WriteFile(logFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: "pwd > " + filepath.Join(tmp, "pwd.out"),
		LogFile: logFile,
		Dir:     tmp,
	})
	if err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-res.PGID, syscall.SIGKILL)
	})

	// Wait for the pwd command to flush.
	deadline := time.Now().Add(1 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		got, _ = os.ReadFile(filepath.Join(tmp, "pwd.out"))
		if len(got) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	want, _ := filepath.EvalSymlinks(tmp)
	gotResolved, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if gotResolved != want {
		t.Errorf("subprocess cwd = %q, want %q", gotResolved, want)
	}
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
