// Package ledger defines the JSON shapes exchanged by the create-sensor
// pipeline (read-usecases.go -> plan-sensors.go) and consumed by the
// orchestrating SKILL.md. The shapes are mirrors of schemas/usecase.yaml
// trimmed to the fields the planning heuristic needs.
package ledger

// Ledger is the top-level document read-usecases emits on stdout when
// successful. ProjectRoot is always populated; Stack and Catalog are
// present only when --include-stack / --include-catalog were passed.
type Ledger struct {
	Usecases    []Usecase      `json:"usecases"`
	Stack       map[string]any `json:"stack,omitempty"`
	Catalog     []CatalogEntry `json:"catalog,omitempty"`
	ProjectRoot string         `json:"project_root"`
}

// Usecase is the loader-side projection of a usecase.yaml file, carrying
// only the fields the planner and downstream LLM synthesis consume.
type Usecase struct {
	ID                 string         `json:"id"`
	JourneyID          string         `json:"journey_id"`
	Name               string         `json:"name"`
	RegressionPriority string         `json:"regression_priority"`
	Tags               []string       `json:"tags,omitempty"`
	Trigger            Trigger        `json:"trigger"`
	Behavior           Behavior       `json:"behavior"`
	ExpectedOutcome    Expected       `json:"expected_outcome"`
	Evidence           []EvidenceItem `json:"evidence"`
	SourcePath         string         `json:"source_path"`
}

// Trigger mirrors usecase.trigger.
type Trigger struct {
	Shape   string         `json:"shape"`
	Summary string         `json:"summary,omitempty"`
	Fixture map[string]any `json:"fixture,omitempty"`
}

// Behavior mirrors usecase.behavior.
type Behavior struct {
	Summary       string   `json:"summary"`
	BusinessRules []string `json:"business_rules,omitempty"`
}

// Expected mirrors usecase.expected_outcome.
type Expected struct {
	Shape       string         `json:"shape"`
	Summary     string         `json:"summary"`
	Fixture     map[string]any `json:"fixture"`
	Invariants  []string       `json:"invariants,omitempty"`
	SideEffects []string       `json:"side_effects,omitempty"`
}

// EvidenceItem mirrors a single usecase.evidence[] entry.
type EvidenceItem struct {
	File      string `json:"file"`
	LineStart *int   `json:"line_start,omitempty"`
	Rationale string `json:"rationale"`
}

// CatalogEntry is the loader-side projection of an existing sensor file,
// embedded into the ledger when --include-catalog is passed.
type CatalogEntry struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	Output   string `json:"output"`
	Blocking bool   `json:"blocking"`
	Path     string `json:"path"`
}

// ListEntry is the thin form used by --list-only.
type ListEntry struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags,omitempty"`
}

// IndexLedger is the thin shape --list-only emits.
type IndexLedger struct {
	Usecases []ListEntry `json:"usecases"`
}
