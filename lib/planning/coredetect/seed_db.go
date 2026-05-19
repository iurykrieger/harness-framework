package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldSeedDB(s stack.Stack) *Draft {
	hasDB := false
	for _, c := range s.Components {
		if string(c.Role) == "db-client" {
			hasDB = true
			break
		}
	}
	if !hasDB {
		return nil
	}
	timeoutMS := 30000
	return &Draft{
		Version:     "0.1.0",
		Name:        "seed-db",
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: "Idempotently seeds the database with the fixtures e2e scenarios depend on.",
		Requires: []sensor.Requirement{
			{Kind: sensor.RequireSensor, ID: "setup-postgres"},
		},
		Triggers: []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 500, P95MS: 5000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: apply seed SQL or fixture loader' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}

func init() { Register("seed-db", scaffoldSeedDB) }
