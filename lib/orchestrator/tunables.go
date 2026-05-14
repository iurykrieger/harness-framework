package orchestrator

import "time"

// SetTunables overrides the package-internal timeouts used by the
// health-gate and stop-blocking-dep paths. Intended ONLY for the
// orchestratortest helper package and the orchestrator's own _test.go
// files — production code never calls this. Tests outside this package
// should go through orchestratortest.SetTunables for a *testing.T-typed
// helper signature; this raw setter is the underlying machinery.
//
// Returns a restore function the caller must defer to put the production
// values back. Pass 0 for any field to leave it unchanged.
func SetTunables(gateTimeout, gatePoll, drainTimeout time.Duration, gracefulMS int) func() {
	prevGate, prevPoll, prevDrain, prevGraceful := healthGateTimeout, healthGatePollInterval, watcherDrainTimeout, stopGracefulMS
	if gateTimeout > 0 {
		healthGateTimeout = gateTimeout
	}
	if gatePoll > 0 {
		healthGatePollInterval = gatePoll
	}
	if drainTimeout > 0 {
		watcherDrainTimeout = drainTimeout
	}
	if gracefulMS > 0 {
		stopGracefulMS = gracefulMS
	}
	return func() {
		healthGateTimeout = prevGate
		healthGatePollInterval = prevPoll
		watcherDrainTimeout = prevDrain
		stopGracefulMS = prevGraceful
	}
}
