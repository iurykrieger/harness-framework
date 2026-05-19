package layer

import (
	"fmt"
	"time"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type codeQuality struct{}

func (codeQuality) Name() Layer { return sensor.LayerCodeQuality }

func (codeQuality) Applicable(_ stack.Stack, _ usecase.UseCase, _ []sensor.Sensor) (bool, string) {
	return true, ""
}

func (codeQuality) Plan(_ stack.Stack, uc usecase.UseCase, _ []sensor.Sensor) []Draft {
	id := fmt.Sprintf("code-quality-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("code-quality audit for %s", uc.ID),
		Layer:       sensor.LayerCodeQuality,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeInferential,
		Regulation:  sensor.RegulationMaintainability,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismMedium,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Audits code quality (duplication, complexity, idiomatic patterns) for %s.", uc.ID),
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
			SystemPrompt:       "You audit code for duplication, complexity, and idiomatic patterns. Emit a single JSON Signal.",
			UserPromptTemplate: fmt.Sprintf("Audit the implementation of usecase %s. Emit a Signal JSON object.", uc.ID),
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

// defaultCalibration returns the calibration block /create-sensors emits
// on every inferential draft. Operators tune via the future /update-sensor
// skill once they have a labelled set.
func defaultCalibration() *sensor.Calibration {
	return &sensor.Calibration{
		ConfidenceThreshold: 0.7,
		CalibrationSet:      "",
		CalibrationSize:     1,
		CalibrationDate:     time.Now().UTC().Format("2006-01-02"),
	}
}

func init() { Register(sensor.LayerCodeQuality, codeQuality{}) }
