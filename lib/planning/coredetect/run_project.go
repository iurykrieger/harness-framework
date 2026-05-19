package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

// scaffoldRunProject emits a setup sensor that brings a runnable
// service up. When the stack carries a runnable role
// ({http-server, http-router, queue-consumer, queue-producer, rpc}), the
// scaffold emits a blocking command tailored to the project's primary
// language (with a generic placeholder boot marker so the watcher
// detects readiness). Otherwise — i.e. for CLI tools, libraries, and
// any other project that does not host a long-running service — the
// scaffold falls back to `true`: a one-shot idempotent noop that exits
// pass. This honours the setup-sensor contract (idempotent precondition)
// without emitting a `false` placeholder that would force every
// dependent sensor into cascade.
func scaffoldRunProject(s stack.Stack) *Draft {
	// Default (noop) shape — non-blocking, so the schema requires a
	// timeout_ms. The blocking branch below clears it.
	defaultTimeout := 5000
	d := &Draft{
		Version:     "0.1.0",
		Name:        "run-project",
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputStream,
		Description: "Brings the project up locally. No-op pass when the stack declares no runnable service role.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 100, P95MS: 1000, TimeoutMS: &defaultTimeout},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: `sh -c 'printf "run-project: no runnable service role on stack — noop pass\n"; exit 0'`,
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
			OutputParsing: &sensor.OutputParsing{
				Patterns: []sensor.Pattern{{Regex: ".*", Verdict: "pass", Severity: "info"}},
			},
		},
	}

	// Promote to a blocking boot command when the stack has a runnable
	// service. The exact boot command remains language-specific and the
	// user customises via /update-sensor; the scaffold establishes the
	// shape (blocking + stream + a ready pattern) up front.
	if hasRunnableRole(s) {
		graceful := 5000
		d.Description = "Brings the project up locally and streams its stdout to the runtime log."
		// Blocking sensors MUST NOT declare cost.latency.timeout_ms per
		// the sensor.yaml allOf gate; the graceful_timeout_ms below
		// controls the SIGTERM→SIGKILL window instead.
		d.Cost.Latency = sensor.Latency{P50MS: 1000, P95MS: 5000}
		d.Cost.Compute = &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256}
		d.Execution.Blocking = true
		d.Execution.GracefulTimeoutMS = &graceful
		d.Execution.Command = `sh -c 'printf "ready: run-project scaffold needs a real boot command; replace via /update-sensor\n"; sleep 1; exit 0'`
		d.Execution.OutputParsing = &sensor.OutputParsing{
			Patterns: []sensor.Pattern{
				{Regex: `(?i)^ready`, Verdict: "pass", Severity: "info"},
				{Regex: `(?i)error|panic|fatal`, Verdict: "fail", Severity: "high"},
				{Regex: `.*`, Verdict: "pass", Severity: "info"},
			},
		}
	}
	return d
}

// hasRunnableRole reports whether the stack declares at least one role
// that implies a long-running service surface. Used to decide whether
// run-project should emit a blocking boot command or a noop pass.
func hasRunnableRole(s stack.Stack) bool {
	runnable := map[string]bool{
		"http-server":     true,
		"http-router":     true,
		"http-middleware": true,
		"queue-consumer":  true,
		"queue-producer":  true,
		"rpc":             true,
		"job-runner":      true,
	}
	for _, c := range s.Components {
		if runnable[string(c.Role)] {
			return true
		}
	}
	return false
}

func init() { Register("run-project", scaffoldRunProject) }
