package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type metric struct{}

func (metric) Name() Layer { return sensor.LayerMetric }

func (metric) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "metrics") {
		return false, "no role=metrics component on stack"
	}
	return true, ""
}

func (metric) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-metric-%s", uc.ID)
	timeoutMS := 10000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("observe metric for %s", uc.ID),
		Layer:       sensor.LayerMetric,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Observes metrics after %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 100, P95MS: 1000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: curl the metrics endpoint and assert on the relevant counter' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerMetric, metric{}) }
