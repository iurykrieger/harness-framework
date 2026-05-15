// Package orchestratortest exposes test-only helpers for packages that
// exercise blocking-dep code paths in lib/orchestrator from outside that
// package. The helpers call into orchestrator via package-level setters
// kept unexported in the production package.
package orchestratortest

import (
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
)

// SetTunables shortens the health-gate, watcher-drain, and stop-graceful
// timeouts used by orchestrator's blocking-dep paths. Returns a restore
// function the caller must defer. Pass 0 for any field to leave it
// unchanged.
func SetTunables(t *testing.T, gateTimeout, gatePoll, drainTimeout time.Duration, gracefulMS int) func() {
	t.Helper()
	return orchestrator.SetTunables(gateTimeout, gatePoll, drainTimeout, gracefulMS)
}
