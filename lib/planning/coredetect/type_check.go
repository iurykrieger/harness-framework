package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldTypeCheck(s stack.Stack) *Draft {
	timeoutMS := 60000
	return &Draft{
		Version:     "0.1.0",
		Name:        "type-check",
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: "Runs the language type checker.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 10000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate type check' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}

func init() { Register("type-check", scaffoldTypeCheck) }
