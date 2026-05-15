package sensor

import "encoding/json"

// Sensor is the typed view of a sensor.yaml file. Mirrors the schema
// shape one-to-one with JSON tags. Optional fields use pointers or
// omitempty so absence is distinguishable from zero.
type Sensor struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Kind           Kind            `json:"kind"`
	Type           Type            `json:"type"`
	Regulation     Regulation      `json:"regulation"`
	Phase          Phase           `json:"phase"`
	Determinism    Determinism     `json:"determinism"`
	Output         Output          `json:"output"`
	Cost           Cost            `json:"cost"`
	Triggers       []Trigger       `json:"triggers"`
	Requires       []Requirement   `json:"requires,omitempty"`
	Execution      Execution       `json:"execution"`
	SelfCorrection *SelfCorrection `json:"self_correction,omitempty"`
	Verification   Verification    `json:"verification"`
	BlindSpots     []string        `json:"blind_spots,omitempty"`
	Calibration    *Calibration    `json:"calibration,omitempty"`
	References     []string        `json:"references,omitempty"`
}

type Cost struct {
	Class                     CostClass   `json:"class"`
	Latency                   Latency     `json:"latency"`
	Tokens                    *Tokens     `json:"tokens,omitempty"`
	Compute                   *Compute    `json:"compute,omitempty"`
	MonetaryEstimateUSDPerRun *float64    `json:"monetary_estimate_usd_per_run,omitempty"`
	Guardrails                *Guardrails `json:"guardrails,omitempty"`
}

type Latency struct {
	P50MS     int  `json:"p50_ms"`
	P95MS     int  `json:"p95_ms"`
	TimeoutMS *int `json:"timeout_ms,omitempty"`
}

type Tokens struct {
	Model     string `json:"model"`
	InputAvg  int    `json:"input_avg"`
	OutputAvg int    `json:"output_avg"`
	MaxOutput int    `json:"max_output"`
}

type Compute struct {
	CPU      CPUClass `json:"cpu"`
	MemoryMB int      `json:"memory_mb"`
}

type Guardrails struct {
	OnTimeout       string  `json:"on_timeout,omitempty"`
	OnTokenOverrun  string  `json:"on_token_overrun,omitempty"`
	OnPhaseMismatch string  `json:"on_phase_mismatch,omitempty"`
	FallbackSensor  *string `json:"fallback_sensor,omitempty"`
}

type Trigger struct {
	On      TriggerOn `json:"on"`
	When    string    `json:"when,omitempty"`
	Cadence string    `json:"cadence,omitempty"`
}

// Requirement is a flat discriminated union keyed by Kind. Fields not
// applicable to a given kind are zero-valued and omitted from JSON via
// omitempty. This mirrors how lib/sensor/requires.go and Project()
// already read the array.
type Requirement struct {
	Kind        RequirementKind    `json:"kind"`
	ID          string             `json:"id,omitempty"`
	Name        string             `json:"name,omitempty"`
	Description string             `json:"description,omitempty"`
	Optional    *bool              `json:"optional,omitempty"`
	Path        string             `json:"path,omitempty"`
	Scope       string             `json:"scope,omitempty"`
	Command     string             `json:"command,omitempty"`
	TimeoutMS   *int               `json:"timeout_ms,omitempty"`
	ExitCodeMap []ExitCodeMapEntry `json:"exit_code_map,omitempty"`
}

type Execution struct {
	Command            string             `json:"command"`
	Env                map[string]string  `json:"env,omitempty"`
	Blocking           bool               `json:"blocking,omitempty"`
	GracefulTimeoutMS  *int               `json:"graceful_timeout_ms,omitempty"`
	Teardown           []LifecycleStep    `json:"teardown,omitempty"`
	ExitCodeMap        []ExitCodeMapEntry `json:"exit_code_map,omitempty"`
	OutputParsing      *OutputParsing     `json:"output_parsing,omitempty"`
	Model              string             `json:"model,omitempty"`
	SystemPrompt       string             `json:"system_prompt,omitempty"`
	UserPromptTemplate string             `json:"user_prompt_template,omitempty"`
	Decoding           *Decoding          `json:"decoding,omitempty"`
}

type LifecycleStep struct {
	Command     string             `json:"command"`
	TimeoutMS   *int               `json:"timeout_ms,omitempty"`
	ExitCodeMap []ExitCodeMapEntry `json:"exit_code_map,omitempty"`
}

// ExitCodeMapEntry's ExitCode field accepts either an integer or the
// literal "*" wildcard. Using interface{} preserves the schema's oneOf;
// callers inspect the runtime type.
type ExitCodeMapEntry struct {
	ExitCode interface{} `json:"exit_code"`
	Verdict  string      `json:"verdict"`
	Severity string      `json:"severity"`
}

type OutputParsing struct {
	Patterns []Pattern `json:"patterns"`
}

type Pattern struct {
	Regex    string    `json:"regex"`
	Verdict  string    `json:"verdict"`
	Severity string    `json:"severity"`
	Captures *Captures `json:"captures,omitempty"`
}

type Captures struct {
	File      *int `json:"file,omitempty"`
	LineStart *int `json:"line_start,omitempty"`
	LineEnd   *int `json:"line_end,omitempty"`
	Excerpt   *int `json:"excerpt,omitempty"`
	Rationale *int `json:"rationale,omitempty"`
}

type Decoding struct {
	Temperature float64  `json:"temperature"`
	TopP        *float64 `json:"top_p,omitempty"`
	MaxTokens   int      `json:"max_tokens"`
	Seed        *int     `json:"seed,omitempty"`
}

type SelfCorrection struct {
	MaxRetries *int   `json:"max_retries,omitempty"`
	OnWarn     string `json:"on_warn,omitempty"`
	OnFail     string `json:"on_fail,omitempty"`
	OnError    string `json:"on_error,omitempty"`
	Escalation string `json:"escalation,omitempty"`
}

type Verification struct {
	GoldenCases     []GoldenCase `json:"golden_cases"`
	SelfTestCommand string       `json:"self_test_command,omitempty"`
}

type GoldenCase struct {
	Fixture          string `json:"fixture"`
	ExpectedVerdict  string `json:"expected_verdict"`
	ExpectedSeverity string `json:"expected_severity"`
	Notes            string `json:"notes,omitempty"`
}

type Calibration struct {
	ConfidenceThreshold float64 `json:"confidence_threshold"`
	DriftCheckCadence   string  `json:"drift_check_cadence,omitempty"`
	CalibrationSet      string  `json:"calibration_set"`
	CalibrationSize     int     `json:"calibration_size"`
	CalibrationDate     string  `json:"calibration_date"`
}

// Kind is the enum from sensor.yaml::properties.kind.
type Kind string

const (
	KindObservation Kind = "observation"
	KindAssertion   Kind = "assertion"
	KindSetup       Kind = "setup"
)

// Type is the enum from sensor.yaml::properties.type.
type Type string

const (
	TypeComputational Type = "computational"
	TypeInferential   Type = "inferential"
)

type Regulation string

const (
	RegulationMaintainability     Regulation = "maintainability"
	RegulationArchitectureFitness Regulation = "architecture-fitness"
	RegulationBehaviour           Regulation = "behaviour"
)

type Phase string

const (
	PhasePreCommit       Phase = "pre-commit"
	PhasePreMerge        Phase = "pre-merge"
	PhasePostIntegration Phase = "post-integration"
	PhaseContinuous      Phase = "continuous"
	PhaseOnDemand        Phase = "on-demand"
)

type Determinism string

const (
	DeterminismHigh   Determinism = "high"
	DeterminismMedium Determinism = "medium"
	DeterminismLow    Determinism = "low"
)

type Output string

const (
	OutputSingle Output = "single"
	OutputStream Output = "stream"
)

type CostClass string

const (
	CostClassCheap     CostClass = "cheap"
	CostClassMedium    CostClass = "medium"
	CostClassExpensive CostClass = "expensive"
)

type CPUClass string

const (
	CPULow    CPUClass = "low"
	CPUMedium CPUClass = "medium"
	CPUHigh   CPUClass = "high"
)

type TriggerOn string

const (
	TriggerPullRequest   TriggerOn = "pull-request"
	TriggerFileChange    TriggerOn = "file-change"
	TriggerCron          TriggerOn = "cron"
	TriggerMetricAnomaly TriggerOn = "metric-anomaly"
	TriggerManual        TriggerOn = "manual"
	TriggerAgentRequest  TriggerOn = "agent-request"
)

type RequirementKind string

const (
	RequireSensor     RequirementKind = "sensor"
	RequireTool       RequirementKind = "tool"
	RequireEnv        RequirementKind = "env"
	RequireContext    RequirementKind = "context"
	RequirePermission RequirementKind = "permission"
	RequireStep       RequirementKind = "step"
)

// AsMap returns the map[string]interface{} representation by JSON
// round-trip. Used by call sites that still consume sensor data as
// loosely-typed maps (validator input, signal metadata payloads).
func (s *Sensor) AsMap() map[string]interface{} {
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
