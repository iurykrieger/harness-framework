package heal

import (
	"encoding/json"
	"fmt"
)

// Plan is the structured handoff between diagnose (LLM-flavored) and
// the deterministic appliers. Marshalled JSON conforms to the contract
// documented in docs/superpowers/specs/2026-05-09-sensor-self-heal-design.md.
type Plan struct {
	Diagnosis       Diagnosis     `json:"diagnosis"`
	AutoApply       []Action      `json:"auto_apply,omitempty"`
	ProposeOnly     []Proposal    `json:"propose_only,omitempty"`
	SensorPatches   []SensorPatch `json:"sensor_patches,omitempty"`
	NewSetupSensors []NewSensor   `json:"new_setup_sensors,omitempty"`
}

type Diagnosis struct {
	FailedSensorID  string `json:"failed_sensor_id"`
	Shape           Shape  `json:"shape"`
	EvidenceExcerpt string `json:"evidence_excerpt,omitempty"`
	RootCauseHint   string `json:"root_cause_hint,omitempty"`
}

// Action is an item in auto_apply[]. Only the kind-specific fields are
// populated; the rest are zero. apply.go's allowlist gate enforces the
// required combination per kind.
type Action struct {
	Kind        string `json:"kind"`
	Src         string `json:"src,omitempty"`
	Dst         string `json:"dst,omitempty"`
	Dir         string `json:"dir,omitempty"`
	File        string `json:"file,omitempty"`
	Name        string `json:"name,omitempty"`
	Value       string `json:"value,omitempty"`
	ValueSource string `json:"value_source,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
}

// Proposal is an item in propose_only[] — anything heal cannot or will
// not auto-apply, surfaced to the user via the final Signal's
// remediation.
type Proposal struct {
	Kind      string `json:"kind"`
	Command   string `json:"command,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

// SensorPatch describes an in-place edit to an existing sensor.
type SensorPatch struct {
	ID    string                 `json:"id"`
	Patch map[string]interface{} `json:"patch"`
}

// NewSensor describes a brand-new sensor that heal wants to create.
type NewSensor struct {
	ID   string                 `json:"id"`
	JSON map[string]interface{} `json:"json"`
}

// ParsePlan unmarshals JSON into a Plan, validating the Shape enum.
func ParsePlan(body []byte) (Plan, error) {
	var p Plan
	if err := json.Unmarshal(body, &p); err != nil {
		return Plan{}, fmt.Errorf("parse plan: %w", err)
	}
	if !p.Diagnosis.Shape.IsKnown() {
		return Plan{}, fmt.Errorf("unknown shape %q", p.Diagnosis.Shape)
	}
	return p, nil
}
