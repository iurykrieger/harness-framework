package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
)

// seedRegistryEntry writes a single RunningSensorEntry to the project's
// running_sensors.json. mutate is called with the entry before save so
// the test can stamp SubprocessExit, vary PID, etc. Returns the
// corresponding LiveDep handle the caller can hand to
// WatchLiveDepsForDeath.
func seedRegistryEntry(t *testing.T, root, sensorID, runID string, pid int, mutate func(*registry.RunningSensorEntry)) orchestrator.LiveDep {
	t.Helper()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatalf("mkdir sensors dir: %v", err)
	}
	entry := registry.RunningSensorEntry{
		SensorID:  sensorID,
		RunID:     runID,
		Blocking:  true,
		PID:       pid,
		PGID:      pid,
		StartedAt: "2026-05-15T00:00:00Z",
		Command:   "true",
		LogDir:    filepath.Join(".harness", "runtime", sensorID, runID),
		HeldBy: []registry.HeldByEntry{
			{Kind: "sensor", ID: "holder", PID: os.Getpid(), AttachedAt: "2026-05-15T00:00:00Z"},
		},
	}
	if mutate != nil {
		mutate(&entry)
	}
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{entry}}
	if err := registry.WithFileLock(r.LockFile(), func() error {
		return registry.Save(r, rs)
	}); err != nil {
		t.Fatalf("save registry: %v", err)
	}
	return orchestrator.LiveDep{ID: sensorID, RunID: runID}
}

// recvDepDeath waits up to timeout for a DepDeath on ch and returns it.
// Fails the test if nothing arrives in time or the channel closes empty.
func recvDepDeath(t *testing.T, ch <-chan orchestrator.DepDeath, timeout time.Duration) orchestrator.DepDeath {
	t.Helper()
	select {
	case d, ok := <-ch:
		if !ok {
			t.Fatalf("death channel closed with no value")
		}
		return d
	case <-time.After(timeout):
		t.Fatalf("no DepDeath after %v", timeout)
	}
	return orchestrator.DepDeath{}
}

// TestWatchLiveDepsForDeath_SubprocessExitRecorded covers the path where
// the watcher's reaper has stamped entry.SubprocessExit before the
// orchestrator's poller observes the death. This is the canonical
// happy-path branch — the registry holds the authoritative "when".
func TestWatchLiveDepsForDeath_SubprocessExitRecorded(t *testing.T) {
	root := t.TempDir()
	// Use the test process's own PID so IsSubprocessAlive returns true;
	// the reason must therefore come from SubprocessExit being set, not
	// from a dead PID check.
	dep := seedRegistryEntry(t, root, "blocking-tick", "1-aa", os.Getpid(), func(e *registry.RunningSensorEntry) {
		e.SubprocessExit = &registry.SubprocessExit{Code: 1, ExitedAt: "2026-05-15T00:00:01Z"}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := orchestrator.WatchLiveDepsForDeath(ctx, root, []orchestrator.LiveDep{dep}, 20*time.Millisecond)
	got := recvDepDeath(t, ch, 2*time.Second)
	if got.DepID != "blocking-tick" {
		t.Errorf("DepID = %q, want blocking-tick", got.DepID)
	}
	if got.Reason != "subprocess_exit_recorded" {
		t.Errorf("Reason = %q, want subprocess_exit_recorded", got.Reason)
	}
}

// TestWatchLiveDepsForDeath_SubprocessDied covers the path where
// entry.PID is no longer alive but entry.SubprocessExit has not yet
// been stamped (the watcher is lagging or crashed). The poller should
// still surface the death rather than wait forever.
func TestWatchLiveDepsForDeath_SubprocessDied(t *testing.T) {
	root := t.TempDir()
	// PID 999999 is virtually guaranteed not to exist on a fresh test
	// machine. IsSubprocessAlive(999999) returns false, so the reason
	// must be "subprocess_died".
	dep := seedRegistryEntry(t, root, "blocking-tick", "1-aa", 999999, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := orchestrator.WatchLiveDepsForDeath(ctx, root, []orchestrator.LiveDep{dep}, 20*time.Millisecond)
	got := recvDepDeath(t, ch, 2*time.Second)
	if got.Reason != "subprocess_died" {
		t.Errorf("Reason = %q, want subprocess_died", got.Reason)
	}
}

// TestWatchLiveDepsForDeath_RegistryEntryMissing covers the defensive
// path where the registry no longer contains the entry the LiveDep
// references (e.g. another process tore it down). Treat as death so
// the orchestrator cancels the dependent.
func TestWatchLiveDepsForDeath_RegistryEntryMissing(t *testing.T) {
	root := t.TempDir()
	// Seed the registry with a different run_id than the LiveDep handle
	// the watch reads — FindEntryByRunID returns nil.
	_ = seedRegistryEntry(t, root, "blocking-tick", "1-aa", os.Getpid(), nil)
	dep := orchestrator.LiveDep{ID: "blocking-tick", RunID: "2-bb"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := orchestrator.WatchLiveDepsForDeath(ctx, root, []orchestrator.LiveDep{dep}, 20*time.Millisecond)
	got := recvDepDeath(t, ch, 2*time.Second)
	if got.Reason != "registry_entry_missing" {
		t.Errorf("Reason = %q, want registry_entry_missing", got.Reason)
	}
}

// TestWatchLiveDepsForDeath_CtxCancelNoDeath covers the clean-exit
// path: the dep stays alive throughout the root's run, the caller
// cancels ctx, and the channel closes with no value. This is the
// expected outcome for every healthy run; absent it, the goroutine
// leaks.
func TestWatchLiveDepsForDeath_CtxCancelNoDeath(t *testing.T) {
	root := t.TempDir()
	dep := seedRegistryEntry(t, root, "blocking-tick", "1-aa", os.Getpid(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	ch := orchestrator.WatchLiveDepsForDeath(ctx, root, []orchestrator.LiveDep{dep}, 20*time.Millisecond)
	// Give the goroutine one tick to confirm it sees the dep as alive,
	// then cancel. The channel must close without emitting any DepDeath.
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case d, ok := <-ch:
		if ok {
			t.Fatalf("expected channel close with no value, got DepDeath{%+v}", d)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("watcher goroutine did not exit after ctx cancel — possible leak")
	}
}

// TestWatchLiveDepsForDeath_EmptyLiveDeps covers the trivial case
// where there are no live deps to watch. The channel should close
// immediately so the orchestrator's select returns quickly when no
// blocking dep was attached.
func TestWatchLiveDepsForDeath_EmptyLiveDeps(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := orchestrator.WatchLiveDepsForDeath(ctx, root, nil, 20*time.Millisecond)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel for empty live-deps slice, got a value")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("empty live-deps channel did not close immediately")
	}
}
