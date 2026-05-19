package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type e2eRecipe struct{}

func (e2eRecipe) Name() Layer { return sensor.LayerE2E }

func (e2eRecipe) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasJourneyEntryPoints(s, uc.JourneyID) {
		return false, fmt.Sprintf("journey %s has no entry_points", uc.JourneyID)
	}
	if !hasCoreSensor(cat, "run-project") {
		return false, "core sensor run-project missing from catalog (will be auto-created)"
	}
	return true, ""
}

func (e2eRecipe) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	scenarios := deriveScenarios(uc)
	drafts := make([]Draft, 0, len(scenarios)+1)

	// One narrow per scenario (leaves first; composite at the end).
	timeoutMS := 30000
	for _, sc := range scenarios {
		drafts = append(drafts, Draft{
			SensorID:    fmt.Sprintf("e2e-%s-%s", sc.Slug, uc.ID),
			Version:     "0.1.0",
			Name:        fmt.Sprintf("e2e %s scenario for %s", sc.Slug, uc.ID),
			Layer:       sensor.LayerE2E,
			Kind:        sensor.KindAssertion,
			Type:        sensor.TypeComputational,
			Regulation:  sensor.RegulationBehaviour,
			Phase:       sensor.PhaseOnDemand,
			Determinism: sensor.DeterminismHigh,
			Output:      sensor.OutputStream,
			Description: sc.Description,
			UseCases:    []string{uc.ID},
			Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
			Requires: []sensor.Requirement{
				{Kind: sensor.RequireSensor, ID: "run-project"},
			},
			Cost: sensor.Cost{
				Class:   sensor.CostClassMedium,
				Latency: sensor.Latency{P50MS: 500, P95MS: 5000, TimeoutMS: &timeoutMS},
				Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 128},
			},
			Execution: sensor.Execution{
				Steps: []sensor.StepConfig{{
					ID:          "replay",
					Type:        "shell",
					Run:         fmt.Sprintf("echo 'TODO: replay %s against %s and assert %s'; false", sc.Slug, uc.ID, sc.ExpectedAssertion),
					ExitCodeMap: map[string]sensor.Verdict{"0": "pass", "*": "fail"},
				}},
			},
		})
	}

	// One composite referencing every scenario via SensorStep.
	steps := make([]sensor.StepConfig, 0, len(scenarios))
	for _, sc := range scenarios {
		steps = append(steps, sensor.StepConfig{
			ID:   fmt.Sprintf("scenario-%s", sc.Slug),
			Type: "sensor",
			Ref:  fmt.Sprintf("e2e-%s-%s", sc.Slug, uc.ID),
		})
	}
	compositeTimeout := 60000
	drafts = append(drafts, Draft{
		SensorID:    fmt.Sprintf("e2e-%s", uc.ID),
		Version:     "0.1.0",
		Name:        fmt.Sprintf("e2e composite for %s", uc.ID),
		Layer:       sensor.LayerE2E,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputStream,
		Description: fmt.Sprintf("Orchestrates every e2e scenario for %s (happy + rule violations).", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Requires: []sensor.Requirement{
			{Kind: sensor.RequireSensor, ID: "run-project"},
		},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 2000, P95MS: 15000, TimeoutMS: &compositeTimeout},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 128},
		},
		Execution: sensor.Execution{Steps: steps},
	})

	return drafts
}

// e2eScenario is the internal planner-level representation of one e2e
// scenario to materialize. Slug feeds the sensor id; Description and
// ExpectedAssertion seed the shell step placeholder body the operator
// fills in via /update-sensor.
type e2eScenario struct {
	Slug              string
	Description       string
	ExpectedAssertion string
}

// deriveScenarios extracts one happy-path scenario + one per business
// rule. Rules are slugged via Slugify.
func deriveScenarios(uc usecase.UseCase) []e2eScenario {
	out := []e2eScenario{{
		Slug:              "happy-path",
		Description:       fmt.Sprintf("Replays the canonical fixture for %s and asserts the canonical response.", uc.ID),
		ExpectedAssertion: "canonical expected_outcome.fixture",
	}}
	for _, rule := range uc.Behavior.BusinessRules {
		out = append(out, e2eScenario{
			Slug:              Slugify(rule),
			Description:       fmt.Sprintf("Exercises violation of rule %q on %s.", rule, uc.ID),
			ExpectedAssertion: fmt.Sprintf("the API rejects with the documented error for %q", rule),
		})
	}
	return out
}

// Slugify lowercases s, drops non-alphanumeric runes (preserving
// hyphens and converting spaces to hyphens), collapses runs of "-",
// trims leading/trailing "-", and truncates to 32 chars.
func Slugify(s string) string {
	lower := []byte{}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			lower = append(lower, c+32)
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			lower = append(lower, c)
		case c == ' ', c == '-', c == '_':
			lower = append(lower, '-')
		}
	}
	// collapse runs of '-' and trim.
	out := []byte{}
	prev := byte(0)
	for _, c := range lower {
		if c == '-' && prev == '-' {
			continue
		}
		out = append(out, c)
		prev = c
	}
	for len(out) > 0 && out[0] == '-' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return string(out)
}

func init() { Register(sensor.LayerE2E, e2eRecipe{}) }
