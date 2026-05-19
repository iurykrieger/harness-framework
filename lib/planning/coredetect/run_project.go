package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldRunProject(s stack.Stack) *Draft {
	return &Draft{
		Version:     "0.1.0",
		Name:        "run-project",
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputStream,
		Description: "Brings the project up locally and streams its stdout to the runtime log.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 5000},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Blocking: true,
			Command:  "echo 'TODO: project-specific run command (go run / pnpm dev / make dev). Auto-generated; tune via /update-sensor.' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
			OutputParsing: &sensor.OutputParsing{
				Patterns: []sensor.Pattern{{Regex: ".*", Verdict: "pass", Severity: "info"}},
			},
		},
	}
}

func init() { Register("run-project", scaffoldRunProject) }
