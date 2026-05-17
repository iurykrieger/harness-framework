// Package usecase owns the project-level UseCase artifact: a descriptive
// snapshot of one observable journey variation (input, behavior, expected
// outcome) used by /create-sensor to synthesize a regression sensor.
package usecase

import "github.com/iurykrieger/harness-framework/lib/stack"

// UseCase is the typed view of a usecase.yaml file.
type UseCase struct {
	ID                 string             `json:"id"`
	Version            string             `json:"version"`
	Name               string             `json:"name,omitempty"`
	Description        string             `json:"description,omitempty"`
	JourneyID          string             `json:"journey_id"`
	Trigger            Trigger            `json:"trigger,omitempty"`
	Behavior           Behavior           `json:"behavior,omitempty"`
	ExpectedOutcome    ExpectedOutcome    `json:"expected_outcome,omitempty"`
	Evidence           []stack.Evidence   `json:"evidence,omitempty"`
	RegressionPriority RegressionPriority `json:"regression_priority,omitempty"`
	BlindSpots         []string           `json:"blind_spots,omitempty"`
	Tags               []string           `json:"tags,omitempty"`
	References         []string           `json:"references,omitempty"`
}

type Trigger struct {
	Summary       string   `json:"summary,omitempty"`
	Shape         string   `json:"shape,omitempty"`
	Fixture       any      `json:"fixture,omitempty"`
	Preconditions []string `json:"preconditions,omitempty"`
}

type Behavior struct {
	Summary       string   `json:"summary,omitempty"`
	BusinessRules []string `json:"business_rules,omitempty"`
}

type ExpectedOutcome struct {
	Summary     string   `json:"summary,omitempty"`
	Shape       string   `json:"shape,omitempty"`
	Fixture     any      `json:"fixture,omitempty"`
	Invariants  []string `json:"invariants,omitempty"`
	SideEffects []string `json:"side_effects,omitempty"`
}

type RegressionPriority string

const (
	PriorityCritical RegressionPriority = "critical"
	PriorityHigh     RegressionPriority = "high"
	PriorityMedium   RegressionPriority = "medium"
	PriorityLow      RegressionPriority = "low"
)

// EvidenceKind values mirror the enum on schemas/usecase.yaml's Evidence.
// An empty string is treated as EvidenceKindImplementation by callers.
const (
	EvidenceKindImplementation = "implementation"
	EvidenceKindContract       = "contract"
)
