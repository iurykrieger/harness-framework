//go:build run_computational || run_inferential

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

// fakeWatcherSpawn mirrors the helper in lib/orchestrator's main_test:
// writes a synthetic pass signal to signals.log so the orchestrator's health
// gate observes Ready, then spawns a short-lived `sleep` subprocess and
// returns its pid (so stopBlockingDep's SIGTERM has a real target other than
// the test process itself).
func fakeWatcherSpawn(opts watcher.SpawnOpts) (int, error) {
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
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func TestMain(m *testing.M) {
	os.Setenv("CLAUDE_PLUGIN_ROOT", os.TempDir())
	prev := watcher.SpawnFn
	watcher.SpawnFn = fakeWatcherSpawn

	restore := orchestrator.SetTunables(
		200*time.Millisecond,
		10*time.Millisecond,
		100*time.Millisecond,
		500,
	)

	code := m.Run()

	restore()
	watcher.SpawnFn = prev
	os.Unsetenv("CLAUDE_PLUGIN_ROOT")
	os.Exit(code)
}
