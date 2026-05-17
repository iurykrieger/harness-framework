// Package planning owns the deterministic /create-sensor grouping +
// inference pipeline. Given a slice of canonical usecase.UseCase
// records and an optional catalog of existing sensors, Plan emits one
// or more proposed Plan entries that the create-sensor skill turns
// into persisted sensor YAML files.
//
// All types in this package describe NEW planner outputs (Plan,
// StepOutline, Aggregate). They are not projections of usecase.UseCase
// or sensor.Sensor — those canonical shapes are consumed directly.
package planning

import "github.com/iurykrieger/harness-framework/lib/stack"

// Plan is one proposed sensor. plan-sensors.go emits one JSONL line
// per Plan plus a final Aggregate line.
type Plan struct {
	SensorID    string        `json:"sensor_id"`
	Kind        string        `json:"kind"`
	Type        string        `json:"type"`
	Output      string        `json:"output"`
	UseCases    []string      `json:"use_cases"`
	StepOutline []StepOutline `json:"step_outline"`
	Rationale   string        `json:"rationale"`
}

// StepOutline describes one step a planned sensor will need to
// assert. Evidence is the canonical stack.Evidence shape consumed
// elsewhere — no projection.
type StepOutline struct {
	StepID            string           `json:"step_id"`
	SourceUsecase     string           `json:"source_usecase"`
	SourceRule        string           `json:"source_rule"`
	SuggestedStepType string           `json:"suggested_step_type"`
	MockStrategy      string           `json:"mock_strategy"`
	Evidence          []stack.Evidence `json:"evidence,omitempty"`
}

// Aggregate is the closing JSONL line plan-sensors.go emits. The
// "aggregate":true discriminator distinguishes it from Plan lines.
type Aggregate struct {
	Aggregate        bool   `json:"aggregate"`
	Verdict          string `json:"verdict"`
	Severity         string `json:"severity"`
	SensorsPlanned   int    `json:"sensors_planned"`
	UsecasesConsumed int    `json:"usecases_consumed"`
}

// BucketLimit caps the number of usecases a single planned sensor
// covers; buckets that exceed it are split into id-sorted chunks
// labelled "-part-1", "-part-2", … in sensor_id.
const BucketLimit = 8
