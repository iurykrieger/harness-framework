package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldBuild(s stack.Stack) *Draft {
	timeoutMS := 120000
	return &Draft{
		Version:     "0.1.0",
		Name:        "build",
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: "Compiles the project end-to-end (go build / tsc --noEmit / cargo build).",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 60000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPUMedium, MemoryMB: 512},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate build (go build / tsc --noEmit / cargo build)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}

func init() { Register("build", scaffoldBuild) }
