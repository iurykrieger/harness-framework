// Package sensorstep implements the type: sensor step. A sensor step
// composes another sensor by ref as an inline sub-run of the parent
// pipeline. To avoid an exec → orchestrator → exec import cycle, the
// re-entry point is injected as a step.SubrunFunc supplied by the engine
// at construction time; this package never imports lib/orchestrator.
//
// The sub-run's aggregate verdict becomes the sensor step's verdict. When
// outputs_passthrough is true on the StepConfig, the sub-run's signals
// are surfaced through the parent step's StepResult so the engine can
// fold them into the parent's stream; otherwise the signals are consumed
// internally and only the verdict crosses the boundary.
//
// Output extraction is limited to the verdict/severity built-ins in this
// iteration. Declaring outputs.<name>.from = "aggregate.verdict" copies
// sub.Verdict into the named output. Any other From value is accepted by
// the config but yields the empty string at runtime; richer extraction
// from aggregate.evidence and aggregate.metadata.* is a follow-up.
package sensorstep

import (
	"context"
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
)

// Step is the sensor-type Step implementation.
type Step struct {
	cfg    sensor.StepConfig
	subrun step.SubrunFunc
}

// New constructs a Step from a YAML-decoded StepConfig and the engine-
// supplied SubrunFunc. Type must be "sensor", Ref must be non-empty,
// and subrun must be non-nil — each is a programmer-side invariant
// surfaced as a constructor error rather than a runtime signal.
func New(cfg sensor.StepConfig, subrun step.SubrunFunc) (step.Step, error) {
	if cfg.Type != "sensor" {
		return nil, fmt.Errorf("sensorstep.New: type=%q (want sensor)", cfg.Type)
	}
	if cfg.Ref == "" {
		return nil, fmt.Errorf("sensorstep.New: ref is required")
	}
	if subrun == nil {
		return nil, fmt.Errorf("sensorstep.New: subrun func required")
	}
	return &Step{cfg: cfg, subrun: subrun}, nil
}

// ID returns the configured step id.
func (s *Step) ID() string { return s.cfg.ID }

// Type returns the discriminator string for the sensor step.
func (s *Step) Type() string { return "sensor" }

// Execute resolves cfg.With into per-sub-run fixture/env override maps,
// invokes the subrun for cfg.Ref, then translates the sub-run's
// StepResult into the parent step's StepResult.
//
// Failures fall into three buckets:
//   - resolveWith error (e.g. missing fixture): verdict=error,
//     status=aborted, subrun not invoked.
//   - subrun returns error: verdict=error, status=aborted; the error is
//     preserved on res.Err for attribution upstream.
//   - subrun returns a StepResult: its Verdict is mirrored as-is; Status
//     is StatusCompleted because the sub-run produced observable
//     behavior even when its verdict is fail/error.
func (s *Step) Execute(ctx context.Context, ec *step.ExecContext) *step.StepResult {
	fxOverride, envOverride, err := resolveWith(s.cfg.With, ec)
	if err != nil {
		return &step.StepResult{
			Verdict: signal.VerdictError,
			Err:     err,
			Status:  step.StatusAborted,
			Outputs: map[string]string{},
		}
	}
	sub, err := s.subrun(ctx, s.cfg.Ref, fxOverride, envOverride)
	if err != nil {
		return &step.StepResult{
			Verdict: signal.VerdictError,
			Err:     err,
			Status:  step.StatusAborted,
			Outputs: map[string]string{},
		}
	}
	res := &step.StepResult{
		Verdict: sub.Verdict,
		Status:  step.StatusCompleted,
		Outputs: map[string]string{},
		Signals: sub.Signals,
	}
	for name, spec := range s.cfg.Outputs {
		switch spec.From {
		case "aggregate.verdict":
			res.Outputs[name] = string(sub.Verdict)
		default:
			// Built-in extraction beyond verdict/severity is the
			// follow-up; the schema accepts the declaration but
			// runtime leaves the value empty for now.
			res.Outputs[name] = ""
		}
	}
	if !s.cfg.OutputsPassthrough {
		res.Signals = nil
	}
	return res
}

// resolveWith maps a YAML with: block into two override maps the
// SubrunFunc consumes: fixtures (name → absolute path, looked up in
// ec.Fixtures) and env (key → string, formatted from the raw value).
//
//   - { foo: { fixture: <name> } } → fx[<name>] = ec.Fixtures[<name>].
//     Missing fixtures abort the step with a deterministic error so the
//     parent step (not the sub-run) is blamed for the misconfiguration.
//   - any other value type (string, number, bool, …) → env[<key>] = the
//     fmt.Sprint of the value. Templating of "${{ … }}" inside env
//     values is the SubrunFunc's responsibility, not this resolver's.
func resolveWith(with map[string]interface{}, ec *step.ExecContext) (map[string]string, map[string]string, error) {
	fx := map[string]string{}
	env := map[string]string{}
	for k, v := range with {
		switch x := v.(type) {
		case map[string]interface{}:
			if name, ok := x["fixture"].(string); ok && name != "" {
				abs, found := ec.Fixtures[name]
				if !found {
					return nil, nil, fmt.Errorf("with[%q]: fixture %q not in pool", k, name)
				}
				fx[name] = abs
				continue
			}
			// Non-fixture maps fall through to env-as-stringified.
			env[k] = fmt.Sprint(v)
		case string:
			env[k] = x
		default:
			env[k] = fmt.Sprint(v)
		}
	}
	return fx, env, nil
}
