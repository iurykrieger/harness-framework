// Package heal hosts the deterministic core of the sensor self-heal
// mechanism: classifier registry, allowlisted action applier, .env
// writer, version transformer, and the Setup Plan model that flows
// from diagnose to apply.
package heal

// Shape names a setup-shaped failure category. Closed enum — adding a
// shape is a code change paired with a rule that produces it.
type Shape string

const (
	ShapeMissingEnv         Shape = "missing-env"
	ShapeBinaryNotFound     Shape = "binary-not-found"
	ShapeEnvFileAbsent      Shape = "env-file-absent"
	ShapeServiceUnavailable Shape = "service-unavailable"
)

// IsKnown reports whether s is one of the registered shapes.
func (s Shape) IsKnown() bool {
	switch s {
	case ShapeMissingEnv, ShapeBinaryNotFound, ShapeEnvFileAbsent, ShapeServiceUnavailable:
		return true
	}
	return false
}

// Signal is a thin view of the aggregate Signal a rule may need to
// inspect. Only fields rules actually read are exposed.
type Signal struct {
	Verdict  string
	Severity string
	Evidence []SignalEvidence
	Metadata SignalMetadata
}

type SignalEvidence struct {
	Rationale string
	Excerpt   string
}

type SignalMetadata struct {
	HealHint  string
	ExitCode  *int
	Lifecycle SignalLifecycle
	Counts    map[string]int
}

type SignalLifecycle struct {
	Prepare []SignalLifecycleStep
}

type SignalLifecycleStep struct {
	Command string
	Verdict string
}

// FailedSensor exposes the parts of the failing sensor's declaration
// rules need to inspect: the env vars, tools, and context paths it
// declared.
type FailedSensor struct {
	ID       string
	EnvNames []string
	Tools    []string
	Context  []string
}

// Rule classifies a Signal as setup-shape. Implementations live in
// lib/heal/rule_*.go files; the registrar (rules.go) holds the
// canonical ordered list.
type Rule interface {
	Name() string
	Match(signal Signal, failed FailedSensor) (matched bool, shape Shape, detail string)
}

// Result is what Classify returns when a rule matches.
type Result struct {
	Rule   string
	Shape  Shape
	Detail string
}

// Classify walks the registered rules in order and returns the first
// match. Empty result + ok=false means "not setup-shape".
func Classify(signal Signal, failed FailedSensor) (Result, bool) {
	return ClassifyWith(registeredRules(), signal, failed)
}

// ClassifyWith is Classify with explicit rules — used by tests. The
// production caller uses Classify.
func ClassifyWith(rules []Rule, signal Signal, failed FailedSensor) (Result, bool) {
	for _, r := range rules {
		matched, shape, detail := r.Match(signal, failed)
		if matched {
			return Result{Rule: r.Name(), Shape: shape, Detail: detail}, true
		}
	}
	return Result{}, false
}
