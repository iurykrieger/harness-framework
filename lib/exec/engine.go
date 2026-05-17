// Package exec is the typed-step pipeline engine. Run is called by the
// orchestrator after PreflightGate and prepare phases, with the loaded
// *Sensor and a sealed env snapshot. It dispatches each
// execution.steps[] entry to the appropriate step builder (shell/http/
// assert/sensor), folds verdicts with worst-of semantics, and stops on
// the first fail/error. The returned slice is the full signal stream in
// canonical order: individuals from each completed step in declaration
// order, followed by the final aggregate signal as the LAST entry.
//
// The engine does not import lib/orchestrator; the type: sensor step
// re-enters the orchestrator through the SubrunFunc indirection to keep
// the dependency graph acyclic.
package exec

import (
	"context"
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
	"github.com/iurykrieger/harness-framework/lib/step/assert"
	httpstep "github.com/iurykrieger/harness-framework/lib/step/http"
	sensorstep "github.com/iurykrieger/harness-framework/lib/step/sensor"
	"github.com/iurykrieger/harness-framework/lib/step/shell"
)

// Run executes the sensor's steps sequentially with fail-fast semantics
// and returns the full signal stream (individuals from each step in
// declaration order, then the aggregate as the LAST element).
//
// subrun is consulted only by the type: sensor step; callers that do
// not exercise sensor steps may pass nil. buildStep forwards subrun to
// sensorstep.New, which rejects a nil callback at construction time so
// the misconfiguration is attributable to the parent sensor.
//
// env is the sealed environment snapshot for the run; the engine does
// not read os.Environ. Fixtures are read from s.Fixtures (populated by
// the caller after Load).
func Run(ctx context.Context, s *sensor.Sensor, subrun step.SubrunFunc, env map[string]string) ([]map[string]interface{}, error) {
	ec := &step.ExecContext{
		Fixtures: s.Fixtures,
		Env:      env,
		Steps:    map[string]*step.StepResult{},
		Cwd:      s.Cwd,
		RunDir:   s.RunDir,
		Envelope: s.Envelope,
	}
	var out []map[string]interface{}
	perStepDetails := []map[string]interface{}{}
	runningVerdict := signal.VerdictPass

	for _, cfg := range s.Execution.Steps {
		instance, err := buildStep(cfg, subrun)
		if err != nil {
			return nil, fmt.Errorf("buildStep %q: %w", cfg.ID, err)
		}
		res := instance.Execute(ctx, ec)
		ec.Steps[cfg.ID] = res
		out = append(out, res.Signals...)
		detail := map[string]interface{}{
			"id":      cfg.ID,
			"type":    cfg.Type,
			"verdict": string(res.Verdict),
		}
		// Surface captured stderr for shell steps so consumers (the
		// orchestrator's wrapper aggregate) can derive heal_hint and
		// stderr-tail evidence the same way the legacy single-command
		// pipeline did. Other step types do not produce stderr.
		if res.Stderr != "" {
			detail["stderr_excerpt"] = res.Stderr
		}
		perStepDetails = append(perStepDetails, detail)
		runningVerdict = worst(runningVerdict, res.Verdict)
		if res.Verdict == signal.VerdictFail || res.Verdict == signal.VerdictError {
			break
		}
	}

	aggregate := buildAggregate(s, runningVerdict, perStepDetails)
	out = append(out, aggregate)
	return out, nil
}

// buildStep dispatches on the step's declared Type to the appropriate
// step constructor. The type=sensor branch wires the engine-supplied
// SubrunFunc into sensorstep so the child sensor's pipeline can be
// re-entered without lib/step importing lib/orchestrator.
func buildStep(cfg sensor.StepConfig, subrun step.SubrunFunc) (step.Step, error) {
	switch cfg.Type {
	case "shell":
		return shell.New(cfg)
	case "http":
		return httpstep.New(cfg)
	case "assert":
		return assert.New(cfg)
	case "sensor":
		return sensorstep.New(cfg, subrun)
	default:
		return nil, fmt.Errorf("unknown step type %q", cfg.Type)
	}
}

// worst returns the higher-rank verdict per the pass < warn < fail <
// error ordering used everywhere else in the framework. Kept private
// to the package; lib/signal.VerdictRank is the canonical source of
// truth for the ordering itself.
func worst(a, b signal.Verdict) signal.Verdict {
	if signal.VerdictRank[string(b)] > signal.VerdictRank[string(a)] {
		return b
	}
	return a
}
