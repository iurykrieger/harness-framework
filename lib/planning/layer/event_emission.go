package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type eventEmission struct{}

func (eventEmission) Name() Layer { return sensor.LayerEventEmission }

func (eventEmission) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "queue-producer") {
		return false, "no role=queue-producer component on stack"
	}
	return true, ""
}

func (eventEmission) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-event-emission-%s", uc.ID)
	timeoutMS := 15000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("observe event emission for %s", uc.ID),
		Layer:       sensor.LayerEventEmission,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Observes event emission after %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 200, P95MS: 2000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: assert event was emitted to queue for usecase' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerEventEmission, eventEmission{}) }
