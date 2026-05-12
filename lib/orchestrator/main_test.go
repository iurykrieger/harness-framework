package orchestrator_test

import (
	"os"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/watcher"
)

func TestMain(m *testing.M) {
	// Override the watcher spawner with a noop returning a fake PID.
	// Orchestrator tests never exercise watcher behavior — they only
	// require that Spawn succeeds and returns a positive PID for the
	// registry entry.
	prev := watcher.SpawnFn
	watcher.SpawnFn = func(opts watcher.SpawnOpts) (int, error) {
		return 99999, nil
	}
	code := m.Run()
	watcher.SpawnFn = prev
	os.Exit(code)
}
