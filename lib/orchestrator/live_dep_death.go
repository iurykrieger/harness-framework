package orchestrator

import (
	"context"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/watcher"
)

// DepDeath identifies which live blocking dep died and why the
// orchestrator observed the death. Emitted on the channel returned by
// WatchLiveDepsForDeath at most once per call (the channel is closed
// after the first observation or after ctx is cancelled with no death).
type DepDeath struct {
	DepID string
	// Reason is the diagnostic the orchestrator surfaces on the root's
	// aggregate via metadata.cancellation_reason. One of:
	//
	//   - "subprocess_exit_recorded" — the watcher's reaper stamped
	//     entry.SubprocessExit in the registry. The dep's subprocess is
	//     known to have terminated; the reaper has the authoritative
	//     "when".
	//   - "subprocess_died"          — entry.PID is no longer alive but
	//     entry.SubprocessExit is not yet set. Either the watcher is
	//     lagging or it crashed before recording. Cancellation should
	//     still fire; the watcher's drain on detach catches the rest.
	//   - "registry_entry_missing"   — the registry entry for the dep
	//     vanished while the root ran (defensive: another process tore
	//     it down out from under us). Treat as death.
	Reason string
}

// WatchLiveDepsForDeath polls the registry at pollInterval for the live
// blocking deps in liveDeps. The first observed death is emitted on the
// returned channel and the channel is closed. If ctx is cancelled before
// any death, the channel is closed with no value.
//
// projectRoot is the user's project tree (the directory containing
// .harness/); the registry root is derived from it. The poller honors
// ctx between ticks so callers may cancel cleanly when the root sensor
// exits on its own (no dep death).
//
// Production callers use depDeathPollInterval (100ms); tests may shorten
// it via SetDepDeathPollInterval. A pollInterval ≤ 0 falls back to the
// production tunable.
func WatchLiveDepsForDeath(ctx context.Context, projectRoot string, liveDeps []LiveDep, pollInterval time.Duration) <-chan DepDeath {
	out := make(chan DepDeath, 1)
	if len(liveDeps) == 0 {
		close(out)
		return out
	}
	if pollInterval <= 0 {
		pollInterval = depDeathPollInterval
	}
	go func() {
		defer close(out)
		tick := time.NewTicker(pollInterval)
		defer tick.Stop()
		r := registry.NewRoot(projectRoot)
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				rs, err := registry.Load(r)
				if err != nil {
					// Transient I/O errors should not abort the watch;
					// the next tick will retry. A persistently broken
					// registry will eventually surface elsewhere.
					continue
				}
				for _, d := range liveDeps {
					entry := rs.FindEntryByRunID(d.RunID)
					if entry == nil {
						out <- DepDeath{DepID: d.ID, Reason: "registry_entry_missing"}
						return
					}
					if entry.SubprocessExit != nil {
						out <- DepDeath{DepID: d.ID, Reason: "subprocess_exit_recorded"}
						return
					}
					if !watcher.IsSubprocessAlive(entry.PID) {
						out <- DepDeath{DepID: d.ID, Reason: "subprocess_died"}
						return
					}
				}
			}
		}
	}()
	return out
}
