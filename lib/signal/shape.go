package signal

import "encoding/json"

// Signal is the typed view of a signal.yaml instance. Mirrors the schema
// shape one-to-one with JSON tags. Optional fields use pointers or
// omitempty so absence is distinguishable from zero.
type Signal struct {
	SensorID    string                 `json:"sensor_id"`
	Version     string                 `json:"version"`
	RunID       string                 `json:"run_id"`
	StartedAt   string                 `json:"started_at"`
	FinishedAt  string                 `json:"finished_at"`
	Verdict     Verdict                `json:"verdict"`
	Severity    Severity               `json:"severity"`
	Score       *float64               `json:"score,omitempty"`
	Confidence  float64                `json:"confidence"`
	Evidence    []Evidence             `json:"evidence"`
	Remediation *Remediation           `json:"remediation,omitempty"`
	CostActual  CostActual             `json:"cost_actual"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type Evidence struct {
	File      string `json:"file,omitempty"`
	LineStart *int   `json:"line_start,omitempty"`
	LineEnd   *int   `json:"line_end,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
	Rationale string `json:"rationale"`
}

type Remediation struct {
	Instructions   string          `json:"instructions,omitempty"`
	SuggestedEdits []SuggestedEdit `json:"suggested_edits,omitempty"`
	References     []string        `json:"references,omitempty"`
}

type SuggestedEdit struct {
	File  string `json:"file"`
	Patch string `json:"patch"`
}

type CostActual struct {
	LatencyMS    int     `json:"latency_ms"`
	InputTokens  *int    `json:"input_tokens,omitempty"`
	OutputTokens *int    `json:"output_tokens,omitempty"`
	Model        *string `json:"model,omitempty"`
}

// Verdict is the enum from signal.yaml::$defs/Verdict.
type Verdict string

const (
	VerdictPass  Verdict = "pass"
	VerdictWarn  Verdict = "warn"
	VerdictFail  Verdict = "fail"
	VerdictError Verdict = "error"
)

// Severity is the enum from signal.yaml::$defs/Severity.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// AsMap returns the map[string]interface{} representation by JSON
// round-trip. Used by call sites that still consume signals as
// loosely-typed maps.
func (s *Signal) AsMap() map[string]interface{} {
	body, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}
