package orchestrator

import "time"

// depDeathPollInterval is how often runWithDepsImpl polls each live
// blocking dep for subprocess death while the root sensor runs. Declared
// here as a var so SetDepDeathPollInterval can shorten it from tests.
// Production default mirrors healthGatePollInterval so the mid-run
// observer matches the boot-time gate's responsiveness.
var depDeathPollInterval = 100 * time.Millisecond

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

// SetDepDeathPollInterval overrides depDeathPollInterval and returns a
// restore func. Test-only knob (the orchestrator's own _test.go files);
// production code keeps the default.
func SetDepDeathPollInterval(d time.Duration) func() {
	prev := depDeathPollInterval
	if d > 0 {
		depDeathPollInterval = d
	}
	return func() { depDeathPollInterval = prev }
}
