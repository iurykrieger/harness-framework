package layer

import (
	"fmt"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type contractTest struct{}

func (contractTest) Name() Layer { return sensor.LayerContractTest }

func (contractTest) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "http-server") && !hasRole(s, "rpc") {
		return false, "no role=http-server or role=rpc component on stack"
	}
	for _, c := range s.Components {
		for _, e := range c.Evidence {
			if endsWith(e.File, ".proto") || endsWith(e.File, "openapi.yaml") || endsWith(e.File, "openapi.json") {
				return true, ""
			}
		}
	}
	return false, "no OpenAPI / proto contract file in any component evidence"
}

func (contractTest) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("contract-test-%s", uc.ID)
	timeoutMS := 30000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("contract-test filtered to %s", uc.ID),
		Layer:       sensor.LayerContractTest,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Validates contract for %s via OpenAPI or protobuf schema.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 100, P95MS: 1000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: wire a real contract validator (oas-validator / buf lint) appropriate to the project' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

// endsWith reports whether s ends with the given suffix.
func endsWith(s, suffix string) bool {
	return strings.HasSuffix(s, suffix)
}

func init() { Register(sensor.LayerContractTest, contractTest{}) }
