package orchestrator

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// PreflightGate evaluates the requires[kind ∈ {tool, context, env}] gate for s
// and returns the canonical preflight-failed Signal when any precondition is
// unmet. It is the single entry point that every sensor-spawn call site MUST
// use before invoking subprocess.{StreamSubprocess, Start, SpawnDetached} with
// the sensor's execution.command.
//
// Returns:
//
//	sig=nil,  failed=false → gate passed; caller may spawn.
//	sig!=nil, failed=true  → caller emits sig and aborts spawn. The signal
//	                         carries verdict=error, metadata.kind="failed",
//	                         metadata.cause="preflight_failed", and machine-
//	                         readable missing_envs / missing_tools /
//	                         missing_contexts lists (omitted when empty).
//
// The Envelope is supplied by the caller (not constructed here): for a runtime
// sensor execution it is the same envelope used for the eventual aggregate
// signal; for /start-sensor it is built from the target sensor before the
// detach spawn; for AttachLiveDep's spawn-fresh branch it is built from the
// dep sensor immediately before startBlockingDep is called.
func PreflightGate(s Sensor, env sensor.Envelope, outputMode string) (sig map[string]interface{}, failed bool) {
	gate := sensor.CheckRequiresGate(s.JSON, sensor.GateOpts{LookupEnv: sensor.LookupEnvFn})
	if !gate.Failed() {
		return nil, false
	}
	return sensor.BuildRequiresGateSignal(env, outputMode, gate), true
}
