package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type dependencyHealth struct{}

func (dependencyHealth) Name() Layer { return sensor.LayerDependencyHealth }

func (dependencyHealth) Applicable(_ stack.Stack, _ usecase.UseCase, _ []sensor.Sensor) (bool, string) {
	return true, ""
}

func (dependencyHealth) Plan(_ stack.Stack, uc usecase.UseCase, _ []sensor.Sensor) []Draft {
	id := fmt.Sprintf("dependency-health-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("dependency-health audit for %s", uc.ID),
		Layer:       sensor.LayerDependencyHealth,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeInferential,
		Regulation:  sensor.RegulationMaintainability,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismMedium,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Audits dependency health (outdated versions, known vulnerabilities, license incompatibilities) relevant to %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassExpensive,
			Latency: sensor.Latency{P50MS: 3000, P95MS: 20000, TimeoutMS: &timeoutMS},
			Tokens: &sensor.Tokens{
				Model:     "anthropic/claude-sonnet-4-6",
				InputAvg:  4000,
				OutputAvg: 1000,
				MaxOutput: 4096,
			},
			Compute: nil,
		},
		Execution: sensor.Execution{
			Model:              "anthropic/claude-sonnet-4-6",
			SystemPrompt:       "You audit dependencies: outdated versions, known vulnerabilities, license incompatibilities.",
			UserPromptTemplate: fmt.Sprintf("Audit dependency health relevant to usecase %s. Emit a Signal JSON object.", uc.ID),
			Decoding:           &sensor.Decoding{Temperature: 0.2, MaxTokens: 4096},
			Command:            "echo 'TODO: route to the inferential runner (run-inferential)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "error", Severity: "high"},
			},
		},
		Calibration: defaultCalibration(),
	}}
}

func init() { Register(sensor.LayerDependencyHealth, dependencyHealth{}) }
