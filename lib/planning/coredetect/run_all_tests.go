package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldRunAllTests(s stack.Stack) *Draft {
	timeoutMS := 300000
	return &Draft{
		Version:     "0.1.0",
		Name:        "run-all-tests",
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: "Runs the full test suite.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 10000, P95MS: 120000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPUMedium, MemoryMB: 512},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate full test command' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}

func init() { Register("run-all-tests", scaffoldRunAllTests) }
