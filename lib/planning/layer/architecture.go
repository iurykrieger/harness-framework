package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type architecture struct{}

func (architecture) Name() Layer { return sensor.LayerArchitecture }

func (architecture) Applicable(_ stack.Stack, _ usecase.UseCase, _ []sensor.Sensor) (bool, string) {
	return true, ""
}

func (architecture) Plan(_ stack.Stack, uc usecase.UseCase, _ []sensor.Sensor) []Draft {
	id := fmt.Sprintf("architecture-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("architecture audit for %s", uc.ID),
		Layer:       sensor.LayerArchitecture,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeInferential,
		Regulation:  sensor.RegulationArchitectureFitness,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismMedium,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Audits architecture (layering, dependency direction, boundary discipline) for %s.", uc.ID),
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
			SystemPrompt:       "You audit architecture: layering, dependency direction, boundary discipline.",
			UserPromptTemplate: fmt.Sprintf("Audit the architectural shape of usecase %s. Emit a Signal JSON object.", uc.ID),
			Decoding:           &sensor.Decoding{Temperature: 0.2, MaxTokens: 4096},
			Command:            inferentialCommand(),
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "error", Severity: "high"},
			},
		},
		Calibration: defaultCalibration(),
	}}
}

func init() { Register(sensor.LayerArchitecture, architecture{}) }
