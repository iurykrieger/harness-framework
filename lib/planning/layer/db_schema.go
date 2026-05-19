package layer

import (
	"fmt"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type dbSchema struct{}

func (dbSchema) Name() Layer { return sensor.LayerDBSchema }

func (dbSchema) Applicable(s stack.Stack, _ usecase.UseCase, _ []sensor.Sensor) (bool, string) {
	if !hasRole(s, "db-client") {
		return false, "no role=db-client component on stack"
	}
	for _, c := range s.Components {
		for _, ev := range c.Evidence {
			if strings.Contains(ev.File, "migration") || strings.Contains(ev.File, "migrations/") {
				return true, ""
			}
		}
	}
	return false, "no migrations evidence in any component"
}

func (dbSchema) Plan(_ stack.Stack, uc usecase.UseCase, _ []sensor.Sensor) []Draft {
	id := fmt.Sprintf("db-schema-%s", uc.ID)
	timeoutMS := 30000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("db-schema check for %s", uc.ID),
		Layer:       sensor.LayerDBSchema,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationArchitectureFitness,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Validates database schema migrations (up/down idempotency) for %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 10000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: replay migrations up then down then up; assert no error' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerDBSchema, dbSchema{}) }
