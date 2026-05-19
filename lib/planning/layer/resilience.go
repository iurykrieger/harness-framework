package layer

import (
	"fmt"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

var faultInjectionTools = []string{"toxiproxy", "chaos-monkey", "chaos-mesh", "gremlin", "pumba"}

type resilience struct{}

func (resilience) Name() Layer { return sensor.LayerResilience }

func (resilience) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	for _, c := range s.Components {
		name := strings.ToLower(c.Name)
		for _, tool := range faultInjectionTools {
			if strings.Contains(name, tool) {
				return true, ""
			}
		}
	}
	return false, "no fault-injection tooling component (toxiproxy/chaos-*/gremlin/pumba) on stack"
}

func (resilience) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("resilience-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("resilience check for %s", uc.ID),
		Layer:       sensor.LayerResilience,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Checks resilience under fault injection for %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 10000, P95MS: 30000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPUMedium, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: inject fault via tooling and assert system recovers within SLO' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerResilience, resilience{}) }
