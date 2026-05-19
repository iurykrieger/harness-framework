package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type dbState struct{}

func (dbState) Name() Layer { return sensor.LayerDBState }

func (dbState) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "db-client") {
		return false, "no role=db-client component on stack"
	}
	return true, ""
}

func (dbState) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-db-%s", uc.ID)
	timeoutMS := 10000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("observe db state for %s", uc.ID),
		Layer:       sensor.LayerDBState,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Observes database state after %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Requires: []sensor.Requirement{
			{Kind: sensor.RequireSensor, ID: "setup-postgres"},
		},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 50, P95MS: 500, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: psql -c \"SELECT ... FROM <table> WHERE <fixture>\"' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerDBState, dbState{}) }
