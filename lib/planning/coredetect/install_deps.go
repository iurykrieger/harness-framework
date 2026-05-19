package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldInstallDeps(s stack.Stack) *Draft {
	timeoutMS := 120000
	return &Draft{
		Version:     "0.1.0",
		Name:        "install-deps",
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: "Installs the project's dependency manifest (go mod download / pnpm install / pip install).",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 60000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate install (go mod download / pnpm install / pip install)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}

func init() { Register("install-deps", scaffoldInstallDeps) }
