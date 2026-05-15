package orchestrator_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

// defaultFakeWatcherSpawn simulates a healthy watcher: writes a single
// individual signal with verdict=pass to signals.log so the orchestrator's
// health gate observes Ready immediately, then spawns a short-lived `sleep`
// subprocess and returns its pid. That pid is alive (so IsPIDAlive is true
// while the test runs), and stopBlockingDep's SIGTERM successfully kills it
// — never the test process itself.
func defaultFakeWatcherSpawn(opts watcher.SpawnOpts) (int, error) {
	sig := map[string]interface{}{
		"sensor_id":   opts.SensorID,
		"version":     "0.0.0",
		"run_id":      opts.RunID,
		"started_at":  "2026-05-14T00:00:00Z",
		"finished_at": "2026-05-14T00:00:00Z",
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "fake watcher pass"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "individual"},
	}
	f, err := os.OpenFile(opts.SignalsLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		_ = json.NewEncoder(f).Encode(sig)
		_ = f.Close()
	}
	return spawnFakeWatcherProc()
}

// spawnFakeWatcherProc starts a `sleep 30` subprocess and returns its pid.
// The subprocess is detached (Release) so the test process need not Wait;
// it dies via SIGTERM in stopBlockingDep or via SIGKILL if the test ends
// before. Tests can also call recordFakeWatcher for direct cleanup.
func spawnFakeWatcherProc() (int, error) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func TestMain(m *testing.M) {
	// Override the watcher spawner with a fake that emits a synthetic
	// "ready" signal. Orchestrator tests never exercise real watcher
	// behavior — they only require that Spawn succeeds and that the
	// health gate sees a healthy signal so AttachLiveDep proceeds.
	// Individual tests that need other outcomes (failed, died_silently,
	// timed_out) override watcher.SpawnFn for the duration of the test.
	os.Setenv("CLAUDE_PLUGIN_ROOT", os.TempDir())
	prev := watcher.SpawnFn
	watcher.SpawnFn = defaultFakeWatcherSpawn

	// Shrink health-gate timeouts so any test path that hits the timeout
	// branch finishes in milliseconds instead of 5 seconds.
	restoreTunables := orchestrator.SetTunables(
		200*time.Millisecond, // healthGateTimeout
		10*time.Millisecond,  // healthGatePollInterval
		100*time.Millisecond, // watcherDrainTimeout
		500,                  // stopGracefulMS
	)

	code := m.Run()

	restoreTunables()
	watcher.SpawnFn = prev
	os.Unsetenv("CLAUDE_PLUGIN_ROOT")
	os.Exit(code)
}
