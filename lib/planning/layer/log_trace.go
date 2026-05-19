package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type logTrace struct{}

func (logTrace) Name() Layer { return sensor.LayerLogTrace }

func (logTrace) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasLogShape(s) {
		return false, "stack declares no log_shapes"
	}
	return true, ""
}

func (logTrace) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-log-%s", uc.ID)
	timeoutMS := 10000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("observe log trace for %s", uc.ID),
		Layer:       sensor.LayerLogTrace,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputStream,
		Description: fmt.Sprintf("Observes log trace for %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 50, P95MS: 500, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: grep the project log_shape sample for the usecase entry-point pattern' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
			OutputParsing: &sensor.OutputParsing{
				Patterns: []sensor.Pattern{
					{Regex: ".*", Verdict: "pass", Severity: "info"},
				},
			},
		},
	}}
}

func init() { Register(sensor.LayerLogTrace, logTrace{}) }
