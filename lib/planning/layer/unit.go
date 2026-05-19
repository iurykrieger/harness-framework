package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type unitTest struct{}

func (unitTest) Name() Layer { return sensor.LayerUnitTest }

func (unitTest) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "test-runner") {
		return false, "no role=test-runner component on stack"
	}
	return true, ""
}

func (unitTest) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("unit-test-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("unit-test filtered to %s", uc.ID),
		Layer:       sensor.LayerUnitTest,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Runs unit tests filtered to %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 200, P95MS: 2000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: fmt.Sprintf("go test -run '%s' ./...", testRunPattern(uc)),
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

// testRunPattern derives a Go test -run regex from the usecase id. For
// non-Go stacks this would be replaced with the language-appropriate
// invocation; phase 1 covers Go only via the test-runner role check.
func testRunPattern(uc usecase.UseCase) string {
	return "Test.*" + camelize(uc.ID)
}

func camelize(s string) string {
	out := []byte{}
	upper := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' || c == '_' {
			upper = true
			continue
		}
		if upper && c >= 'a' && c <= 'z' {
			c = c - 32
		}
		out = append(out, c)
		upper = false
	}
	return string(out)
}

func init() { Register(sensor.LayerUnitTest, unitTest{}) }
