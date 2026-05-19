package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldLint(s stack.Stack) *Draft {
	timeoutMS := 60000
	return &Draft{
		Version:     "0.1.0",
		Name:        "lint",
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputStream,
		Description: "Runs the language linter (go vet / eslint / ruff).",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 10000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate linter (go vet / eslint / ruff)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "medium"},
			},
			OutputParsing: &sensor.OutputParsing{
				Patterns: []sensor.Pattern{{Regex: ".*", Verdict: "warn", Severity: "low"}},
			},
		},
	}
}

func init() { Register("lint", scaffoldLint) }
