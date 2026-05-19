package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldSetupPostgres(s stack.Stack) *Draft {
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
		Name:        "setup-postgres",
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: "Idempotently brings a Postgres instance up (docker / system service) and waits for ready.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 10000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 128},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: docker-compose up -d postgres && pg_isready' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}

func init() { Register("setup-postgres", scaffoldSetupPostgres) }
