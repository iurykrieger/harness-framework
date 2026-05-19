package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type performance struct{}

func (performance) Name() Layer { return sensor.LayerPerformance }

func (performance) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if hasArchetype(s, "http-api") || hasArchetype(s, "queue-consumer") || hasArchetype(s, "queue-producer") {
		return true, ""
	}
	return false, "archetype is not http-api, queue-consumer, or queue-producer"
}

func (performance) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("performance-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("performance check for %s", uc.ID),
		Layer:       sensor.LayerPerformance,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Checks performance characteristics of %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 30000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPUMedium, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: run load test and assert on latency/throughput targets' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerPerformance, performance{}) }
