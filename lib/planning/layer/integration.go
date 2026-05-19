package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type integrationTest struct{}

func (integrationTest) Name() Layer { return sensor.LayerIntegrationTest }

func (integrationTest) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "test-runner") {
		return false, "no role=test-runner component on stack"
	}
	if !hasRole(s, "db-client") && !hasRole(s, "queue-consumer") &&
		!hasRole(s, "queue-producer") && !hasRole(s, "external-integration") {
		return false, "no boundary role (db-client / queue-* / external-integration) on stack"
	}
	return true, ""
}

func (integrationTest) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("integration-test-%s", uc.ID)
	timeoutMS := 120000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("integration-test filtered to %s", uc.ID),
		Layer:       sensor.LayerIntegrationTest,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Runs integration tests filtered to %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 30000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: fmt.Sprintf("go test -tags=integration -run '%s' ./...", testRunPattern(uc)),
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerIntegrationTest, integrationTest{}) }
