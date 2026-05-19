package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type accessibility struct{}

func (accessibility) Name() Layer { return sensor.LayerAccessibility }

func (accessibility) Applicable(s stack.Stack, _ usecase.UseCase, _ []sensor.Sensor) (bool, string) {
	if hasArchetype(s, "http-spa") || hasArchetype(s, "http-ssr") {
		return true, ""
	}
	return false, "archetype is not http-spa or http-ssr"
}

func (accessibility) Plan(_ stack.Stack, uc usecase.UseCase, _ []sensor.Sensor) []Draft {
	id := fmt.Sprintf("accessibility-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("accessibility check for %s", uc.ID),
		Layer:       sensor.LayerAccessibility,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Checks accessibility (WCAG compliance via axe-core/pa11y) for %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 30000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: run axe-core / pa11y against the page' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerAccessibility, accessibility{}) }
