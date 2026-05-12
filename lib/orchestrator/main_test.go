package orchestrator_test

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	// Install a stub watcher binary (copy of /usr/bin/true) in the test
	// binary's directory so tests that reach the spawn-watcher step do not
	// fail with "no such file". The watcher exits immediately (pass), which
	// is fine because watcher behaviour is covered by its own tests.
	exe, err := os.Executable()
	if err != nil {
		panic("TestMain: os.Executable failed: " + err.Error())
	}
	watcherPath := filepath.Join(filepath.Dir(exe), "watcher")
	if _, serr := os.Stat(watcherPath); os.IsNotExist(serr) {
		stub, rerr := os.ReadFile("/usr/bin/true")
		if rerr != nil {
			panic("TestMain: read /usr/bin/true: " + rerr.Error())
		}
		if werr := os.WriteFile(watcherPath, stub, 0o755); werr != nil {
			panic("TestMain: write watcher stub: " + werr.Error())
		}
	}
	os.Exit(m.Run())
}
