package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/exec"
	"github.com/iurykrieger/harness-framework/lib/fixture"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
)

// execPhaseResult is what runViaEngine returns to the orchestrator: the
// aggregate verdict/severity computed by exec.Run plus the individual
// signals that flowed through the engine. The orchestrator wraps these
// into its own aggregate Signal, folding lifecycle metadata over the top.
type execPhaseResult struct {
	Verdict     string
	Severity    string
	Individuals []map[string]interface{}
	// Steps is metadata.steps[] from the engine's aggregate: one entry
	// per executed step with {id, type, verdict}. Used by the
	// orchestrator to enrich its aggregate Signal so /heal-sensor can
	// attribute failures to specific steps.
	Steps []map[string]interface{}
	// EngineError is non-nil when exec.Run itself returned an error
	// (misconfigured step, unknown type). Surfaced separately from a
	// fail/error verdict so the orchestrator can distinguish a sensor-
	// observed failure from a framework misconfiguration.
	EngineError error
}

// loadTypedSensor returns a *sensor.Sensor for the orchestrator's
// Sensor. The canonical path is sensor.Load via the on-disk file at
// s.Path; this gives schema validation and command-shortcut
// normalization in one shot. When the on-disk file is missing or
// unreadable (e.g. tests that synthesize Sensor.JSON in-process and
// only stage s.Path so projectRoot can be derived without ever
// writing the YAML), we fall back to a JSON round-trip and apply the
// same shape normalization manually. The fallback projects only the
// fields the engine actually needs (id, version, output, execution) so
// loosely-typed test fixtures with legacy shapes for unrelated fields
// (e.g. cost.compute as a string) do not derail the typed unmarshal.
func loadTypedSensor(s Sensor, v *schema.Validator) (*sensor.Sensor, error) {
	if v != nil && s.Path != "" {
		if typed, err := sensor.Load(s.Path, v); err == nil {
			return typed, nil
		}
		// Fall through to JSON round-trip on Load failure (missing file,
		// schema gap that the in-memory JSON tolerates, etc.).
	}
	if s.JSON == nil {
		return nil, fmt.Errorf("loadTypedSensor: sensor JSON is nil and Path %q is unloadable", s.Path)
	}
	return projectTypedSensor(s.JSON)
}

// projectTypedSensor builds a *sensor.Sensor populated only with the
// fields the engine reads: ID, Version, Output, and Execution
// (Command, Env, Steps, ExitCodeMap, OutputParsing, etc.). Fields not
// consumed by exec.Run (Cost, Triggers, Verification, …) are skipped to
// keep the in-memory shape decoupled from on-disk schema strictness.
// Returns an error only on truly fatal misshapes (Execution not an
// object). Callers that need a fully-typed sensor should go through
// sensor.Load instead.
func projectTypedSensor(j map[string]interface{}) (*sensor.Sensor, error) {
	typed := &sensor.Sensor{}
	if id, ok := j["id"].(string); ok {
		typed.ID = id
	}
	if version, ok := j["version"].(string); ok {
		typed.Version = version
	}
	if name, ok := j["name"].(string); ok {
		typed.Name = name
	}
	if t, ok := j["type"].(string); ok {
		typed.Type = sensor.Type(t)
	}
	if o, ok := j["output"].(string); ok {
		typed.Output = sensor.Output(o)
	}
	execAny, ok := j["execution"]
	if !ok {
		return typed, nil
	}
	execMap, ok := execAny.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("execution is not an object: %T", execAny)
	}
	// Marshal just the execution block back to JSON and decode into
	// the typed Execution. This isolates strict unmarshal to the one
	// sub-tree the engine actually consumes.
	execBytes, err := json.Marshal(execMap)
	if err != nil {
		return nil, fmt.Errorf("encode execution: %w", err)
	}
	if err := json.Unmarshal(execBytes, &typed.Execution); err != nil {
		return nil, fmt.Errorf("decode execution: %w", err)
	}
	sensor.NormalizeCommandShortcut(typed)
	return typed, nil
}

// runViaEngine drives exec.Run with a fixture pool discovered under
// projectRoot and a sealed env snapshot composed of os.Environ() plus
// the sensor's execution.env. Individuals flowing through the engine
// are streamed to stdout as JSONL; the engine's own aggregate is held
// back so the orchestrator's wrapper aggregate remains the LAST line.
//
// projectRoot is used for both fixture discovery and as the typed
// sensor's runtime Cwd, which the shell step inherits as the
// subprocess working directory (step.ExecContext.Cwd → StreamConfig.Dir).
//
// runDir is the persistent .harness/runtime/<id>/<run-id>/ directory the
// orchestrator created for this invocation; the engine threads it into
// ExecContext so subprocess-spawning steps append verbatim output to
// <runDir>/raw.log and matched individuals to <runDir>/signals.log.
// Empty when called from the non-persistence path (RunOne without a root).
//
// envelope is the run-scoped Signal scaffold the orchestrator built for
// this invocation; the engine threads it into ExecContext so subprocess-
// spawning steps stamp it onto each matched individual signal.
//
// fxOverride and envOverride carry per-sub-run overrides resolved from a
// parent sensor step's with: block. Both are merged after the
// project-discovered pool and the sealed env snapshot, so caller-supplied
// entries win on key collision. Either map may be nil for top-level runs.
func runViaEngine(
	ctx context.Context,
	typed *sensor.Sensor,
	projectRoot, schemasDir, runDir string,
	envelope sensor.Envelope,
	v *schema.Validator,
	root *registry.Root,
	stdout io.Writer,
	fxOverride, envOverride map[string]string,
) execPhaseResult {
	// Fixture pool. A missing .harness/fixtures/ directory yields an
	// empty pool with no error; an oversized fixture returns an error
	// the orchestrator surfaces as a sensor-level error verdict.
	pool, ferr := fixture.Discover(projectRoot)
	if ferr != nil {
		return execPhaseResult{
			Verdict:     "error",
			Severity:    "high",
			EngineError: fmt.Errorf("fixture.Discover: %w", ferr),
		}
	}
	// Merge fixture override on top of the discovered pool: caller-supplied
	// entries (i.e. parent step's with: { foo: { fixture: name } }) win when
	// the same fixture name is present in both. nil/empty override leaves
	// the pool untouched.
	if len(fxOverride) > 0 {
		if pool == nil {
			pool = map[string]string{}
		}
		for k, v := range fxOverride {
			pool[k] = v
		}
	}
	typed.Fixtures = pool
	typed.Cwd = projectRoot
	typed.RunDir = runDir
	typed.Envelope = envelope

	envMap := buildSealedEnv(typed)
	// Merge env override on top of the sealed snapshot. Parent step's
	// with: { foo: "value" } entries appear under env.foo in the sub-run.
	for k, v := range envOverride {
		envMap[k] = v
	}
	subrun := newSubrunFunc(projectRoot, schemasDir, v, root)

	signals, runErr := exec.Run(ctx, typed, subrun, envMap)
	if runErr != nil {
		return execPhaseResult{
			Verdict:     "error",
			Severity:    "high",
			EngineError: runErr,
		}
	}
	if len(signals) == 0 {
		return execPhaseResult{Verdict: "pass", Severity: "info"}
	}
	// The engine guarantees the aggregate is the LAST entry; everything
	// before it is an individual signal. Write individuals to stdout;
	// hold the aggregate back for the orchestrator's wrapper.
	individuals := signals[:len(signals)-1]
	engineAgg := signals[len(signals)-1]
	for _, s := range individuals {
		_ = json.NewEncoder(stdout).Encode(s)
	}

	res := execPhaseResult{
		Individuals: individuals,
	}
	if v, ok := engineAgg["verdict"].(string); ok {
		res.Verdict = v
	}
	if s, ok := engineAgg["severity"].(string); ok {
		res.Severity = s
	}
	if md, ok := engineAgg["metadata"].(map[string]interface{}); ok {
		if steps, ok := md["steps"].([]map[string]interface{}); ok {
			res.Steps = steps
		} else if stepsAny, ok := md["steps"].([]interface{}); ok {
			converted := make([]map[string]interface{}, 0, len(stepsAny))
			for _, raw := range stepsAny {
				if m, ok := raw.(map[string]interface{}); ok {
					converted = append(converted, m)
				}
			}
			res.Steps = converted
		}
	}
	return res
}

// buildSealedEnv composes the env snapshot the engine threads into
// step.ExecContext.Env. The base is the orchestrator's own os.Environ
// (so steps see the project's shell env), overlaid with the sensor's
// execution.env. Sensor-declared values win on collision.
func buildSealedEnv(typed *sensor.Sensor) map[string]string {
	out := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	for k, val := range typed.Execution.Env {
		out[k] = val
	}
	return out
}

// newSubrunFunc returns the engine's re-entry callback for type: sensor
// steps. The callback re-enters the sub-run via RunOneWithRootCaptureOverride
// so fixture/env overrides resolved from the parent step's with: block
// are merged into the child's fixture pool and sealed env snapshot
// before the child's engine runs. Caller-supplied overrides win on
// collision with the project-discovered pool / shell environment.
func newSubrunFunc(projectRoot, schemasDir string, v *schema.Validator, root *registry.Root) step.SubrunFunc {
	return func(ctx context.Context, ref string, fxOverride, envOverride map[string]string) (*step.StepResult, error) {
		// Resolve the referenced sensor by id (or path). sensor.Resolve
		// handles both forms and validates the id shape.
		path, err := sensor.Resolve(ref, projectRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve sub-sensor %q: %w", ref, err)
		}
		// Load the JSON form for the orchestrator's Sensor wrapper.
		// Schema validation runs upstream when v != nil via sensor.Load
		// (the orchestrator wrapper does its own typed load too).
		body, rerr := schema.ReadAsJSON(path)
		if rerr != nil {
			return nil, fmt.Errorf("read sub-sensor %q: %w", ref, rerr)
		}
		var asMap map[string]interface{}
		if err := json.Unmarshal(body, &asMap); err != nil {
			return nil, fmt.Errorf("parse sub-sensor %q: %w", ref, err)
		}
		id, _ := asMap["id"].(string)
		if id == "" {
			id = StripSensorExt(ref)
		}
		child := Sensor{ID: id, Path: path, JSON: asMap}

		// Discard the sub-run's stdout/stderr emission so it doesn't
		// pollute the parent's stream; the sub-run's aggregate
		// (returned as sig) is folded into the parent step's result
		// instead, so the parent can decide whether to surface it.
		sig, exit := RunOneWithRootCaptureOverride(
			ctx, child, projectRoot, schemasDir, v, root,
			io.Discard, io.Discard, fxOverride, envOverride,
		)
		if exit != 0 || sig == nil {
			return &step.StepResult{
				Verdict: signal.VerdictError,
				Status:  step.StatusCompleted,
				Err:     fmt.Errorf("sub-sensor %q exited %d", ref, exit),
			}, nil
		}
		verdict, _ := sig["verdict"].(string)
		return &step.StepResult{
			Verdict: signal.Verdict(verdict),
			Status:  step.StatusCompleted,
			Outputs: map[string]string{},
			Signals: []map[string]interface{}{sig},
		}, nil
	}
}
