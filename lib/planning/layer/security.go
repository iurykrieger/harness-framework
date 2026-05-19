package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type securityRecipe struct{}

func (securityRecipe) Name() Layer { return sensor.LayerSecurity }

func (securityRecipe) Applicable(_ stack.Stack, _ usecase.UseCase, _ []sensor.Sensor) (bool, string) {
	return true, ""
}

func (securityRecipe) Plan(_ stack.Stack, uc usecase.UseCase, _ []sensor.Sensor) []Draft {
	id := fmt.Sprintf("security-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Version:     "0.1.0",
		Name:        fmt.Sprintf("security audit for %s", uc.ID),
		Layer:       sensor.LayerSecurity,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeInferential,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismMedium,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Audits security (injection, secrets, authz, OWASP-class vulnerabilities) for %s.", uc.ID),
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
			SystemPrompt:       "You audit security: injection, secrets, authz, OWASP-class vulnerabilities.",
			UserPromptTemplate: fmt.Sprintf("Audit security of usecase %s. Emit a Signal JSON object.", uc.ID),
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

func init() { Register(sensor.LayerSecurity, securityRecipe{}) }
