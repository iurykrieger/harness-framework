// Package assert implements the type: assert step. The assertion is
// in-memory only: render expect.value through the actions template, apply
// one Matcher from the same expect: map, emit one signal with
// metadata.kind=assertion. The schema rejects with: on assert steps
// (Validation rule 10, Task 9) so we also reject it here defensively.
//
// matcherFrom and buildActionsContext are duplicated from lib/step/http
// (rule of three; abstract only if a fourth caller appears).
package assert

import (
	"context"
	"fmt"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
	"github.com/iurykrieger/harness-framework/lib/template"
)

// Step is the assert-type Step implementation.
type Step struct {
	cfg sensor.StepConfig
}

// New constructs a Step from a YAML-decoded StepConfig. The config's Type
// must be "assert" and Expect must be non-nil. With: is rejected.
func New(cfg sensor.StepConfig) (step.Step, error) {
	if cfg.Type != "assert" {
		return nil, fmt.Errorf("assert.New: type=%q (want assert)", cfg.Type)
	}
	if cfg.Expect == nil {
		return nil, fmt.Errorf("assert.New: expect is required")
	}
	if len(cfg.With) > 0 {
		return nil, fmt.Errorf("assert.New: with: is not valid on assert step")
	}
	return &Step{cfg: cfg}, nil
}

// ID returns the configured step id.
func (s *Step) ID() string { return s.cfg.ID }

// Type returns the discriminator string for the assert step.
func (s *Step) Type() string { return "assert" }

// Execute renders expect.value through the actions template, applies the
// single Matcher derived from the same expect map (equals/matches/contains/
// gte/lte), and emits one signal with metadata.kind=assertion. The verdict
// is pass when the matcher hits, fail when it misses, and error when the
// expect block itself is malformed (non-map, no value).
func (s *Step) Execute(ctx context.Context, ec *step.ExecContext) *step.StepResult {
	res := &step.StepResult{
		Status:  step.StatusCompleted,
		Outputs: map[string]string{},
	}

	m, ok := s.cfg.Expect.(map[string]interface{})
	if !ok {
		res.Verdict = signal.VerdictError
		res.Err = fmt.Errorf("assert.Expect must be an object")
		res.Signals = []map[string]interface{}{buildAssertSignal(s.cfg.ID, "", step.Matcher{}, signal.VerdictError, "expect must be an object")}
		return res
	}

	raw, ok := m["value"]
	if !ok {
		res.Verdict = signal.VerdictError
		res.Err = fmt.Errorf("assert.Expect missing value")
		res.Signals = []map[string]interface{}{buildAssertSignal(s.cfg.ID, "", step.Matcher{}, signal.VerdictError, "expect.value is required")}
		return res
	}

	val := raw
	if s, ok := raw.(string); ok {
		rendered, err := template.RenderActions(s, buildActionsContext(ec))
		if err != nil {
			res.Verdict = signal.VerdictError
			res.Err = err
			res.Signals = []map[string]interface{}{buildAssertSignal("", "", step.Matcher{}, signal.VerdictError, err.Error())}
			return res
		}
		val = rendered
	}

	matcher := matcherFrom(m)
	if step.Match(matcher, val) {
		res.Verdict = signal.VerdictPass
	} else {
		res.Verdict = signal.VerdictFail
	}
	res.Signals = []map[string]interface{}{buildAssertSignal(s.cfg.ID, fmt.Sprint(val), matcher, res.Verdict, assertEvidence(val, matcher, res.Verdict))}
	return res
}

// assertEvidence is a one-liner rationale string for the emitted signal's
// evidence[]. The wording lists the rendered value and the constraint that
// either passed or failed.
func assertEvidence(val interface{}, m step.Matcher, v signal.Verdict) string {
	verb := "matched"
	if v != signal.VerdictPass {
		verb = "did not match"
	}
	return fmt.Sprintf("value=%v %s constraint", val, verb)
}

// buildAssertSignal returns the single signal the assert step emits. Shape
// matches signal.yaml; metadata.kind=assertion identifies the producer.
// The matcher is included as a (best-effort) string so the signal carries
// enough context to be read on its own.
func buildAssertSignal(stepID, value string, m step.Matcher, verdict signal.Verdict, evidence string) map[string]interface{} {
	return map[string]interface{}{
		"verdict":  string(verdict),
		"severity": string(severityFor(verdict)),
		"evidence": []interface{}{
			map[string]interface{}{"rationale": evidence},
		},
		"metadata": map[string]interface{}{
			"kind":    "assertion",
			"step_id": stepID,
			"value":   value,
		},
	}
}

func severityFor(v signal.Verdict) signal.Severity {
	switch v {
	case signal.VerdictPass:
		return signal.SeverityInfo
	case signal.VerdictWarn:
		return signal.SeverityMedium
	case signal.VerdictFail:
		return signal.SeverityHigh
	case signal.VerdictError:
		return signal.SeverityCritical
	}
	return signal.SeverityInfo
}

// matcherFrom converts a YAML-decoded matcher (typically a
// map[string]interface{}) into a step.Matcher. Per CLAUDE.md rule 4 (rule
// of three), this helper is duplicated from lib/step/http/expect.go; do
// not extract until a fourth caller appears. Keep the two copies in sync.
func matcherFrom(in interface{}) step.Matcher {
	m, ok := in.(map[string]interface{})
	if !ok {
		return step.Matcher{Equals: in}
	}
	out := step.Matcher{}
	if v, ok := m["equals"]; ok {
		out.Equals = v
	}
	if v, ok := m["matches"].(string); ok {
		out.Matches = v
	}
	if v, ok := m["contains"].(string); ok {
		out.Contains = v
	}
	if f, ok := numericField(m["gte"]); ok {
		out.Gte = &f
	}
	if f, ok := numericField(m["lte"]); ok {
		out.Lte = &f
	}
	if v, ok := m["jsonpath"].(string); ok {
		out.JSONPath = v
	}
	if v, ok := m["type"].(string); ok {
		out.Type = v
	}
	if n, ok := intField(m["min_length"]); ok {
		out.MinLength = &n
	}
	if n, ok := intField(m["max_length"]); ok {
		out.MaxLength = &n
	}
	if v, ok := m["value"]; ok {
		out.Value = v
	}
	return out
}

func numericField(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func intField(v interface{}) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}

// buildActionsContext converts an ExecContext into the read-only context
// the actions template uses for ${{ … }} resolution. Per CLAUDE.md rule 4
// (rule of three), this helper is duplicated from lib/step/shell and
// lib/step/http. Do not extract until a fourth caller appears.
func buildActionsContext(ec *step.ExecContext) template.ActionContext {
	out := template.ActionContext{
		Fixtures: ec.Fixtures,
		Env:      ec.Env,
		Steps:    map[string]template.ActionStep{},
	}
	for id, sr := range ec.Steps {
		as := template.ActionStep{
			Verdict: string(sr.Verdict),
			Outputs: sr.Outputs,
		}
		if sr.Response != nil {
			headers := map[string]string{}
			for k, vs := range sr.Response.Headers {
				if len(vs) > 0 {
					headers[strings.ToLower(k)] = vs[0]
				}
			}
			as.Response = &template.ActionResponse{
				Status:  sr.Response.Status,
				Headers: headers,
			}
		}
		out.Steps[id] = as
	}
	return out
}
