package sensorstep_test

import (
	"context"
	"errors"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
	sensorstep "github.com/iurykrieger/harness-framework/lib/step/sensor"
)

// TestSensorStep_SubrunPropagatesVerdict drives the happy path: the stub
// subrun returns Pass; the sensorstep mirrors that verdict as its own.
func TestSensorStep_SubrunPropagatesVerdict(t *testing.T) {
	called := false
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		called = true
		if ref != "child" {
			t.Fatalf("ref = %q (want child)", ref)
		}
		return &step.StepResult{
			Verdict: signal.VerdictPass,
			Outputs: map[string]string{},
			Status:  step.StatusCompleted,
		}, nil
	}
	s, err := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor", Ref: "child"}, sub)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if !called {
		t.Fatal("subrun was never invoked")
	}
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v (want pass)", res.Verdict)
	}
	if res.Status != step.StatusCompleted {
		t.Fatalf("status = %q (want completed)", res.Status)
	}
}

// TestSensorStep_SubrunFailureAborts shows that a subrun returning Fail
// propagates as the parent step's verdict (and the step is still considered
// completed: the sub-run produced an observable signal).
func TestSensorStep_SubrunFailureAborts(t *testing.T) {
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return &step.StepResult{Verdict: signal.VerdictFail, Status: step.StatusCompleted}, nil
	}
	s, _ := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor", Ref: "child"}, sub)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictFail {
		t.Fatalf("verdict = %v (want fail)", res.Verdict)
	}
}

// TestSensorStep_SubrunErrorAborts: a subrun that returns an error makes
// the step verdict=error with status=aborted.
func TestSensorStep_SubrunErrorAborts(t *testing.T) {
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return nil, errors.New("boom")
	}
	s, _ := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor", Ref: "child"}, sub)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictError {
		t.Fatalf("verdict = %v (want error)", res.Verdict)
	}
	if res.Status != step.StatusAborted {
		t.Fatalf("status = %q (want aborted)", res.Status)
	}
	if res.Err == nil {
		t.Fatal("err is nil")
	}
}

// TestSensorStep_New_RejectsNonSensorType: dispatch is by Type; calling
// New with a non-sensor type is a programmer error and must fail loudly.
func TestSensorStep_New_RejectsNonSensorType(t *testing.T) {
	_, err := sensorstep.New(sensor.StepConfig{ID: "x", Type: "shell", Ref: "child"}, func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("New with type=shell should error")
	}
}

// TestSensorStep_New_RejectsEmptyRef: ref: identifies which sensor to
// sub-run; an empty value is meaningless and must be rejected.
func TestSensorStep_New_RejectsEmptyRef(t *testing.T) {
	_, err := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor"}, func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("New with empty ref should error")
	}
}

// TestSensorStep_New_RequiresSubrun: a nil subrun callback means the
// engine forgot to wire the indirection — a programmer error, not a
// runtime condition.
func TestSensorStep_New_RequiresSubrun(t *testing.T) {
	_, err := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor", Ref: "child"}, nil)
	if err == nil {
		t.Fatal("New with nil subrun should error")
	}
}

// TestSensorStep_OutputsPassthrough_KeepsSignals: when outputs_passthrough
// is true, the sub-run's signals appear in the parent step's stream so the
// engine can forward them upstream.
func TestSensorStep_OutputsPassthrough_KeepsSignals(t *testing.T) {
	subSignals := []map[string]interface{}{
		{"verdict": "pass", "metadata": map[string]interface{}{"kind": "individual"}},
		{"verdict": "pass", "metadata": map[string]interface{}{"kind": "aggregate"}},
	}
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return &step.StepResult{
			Verdict: signal.VerdictPass,
			Status:  step.StatusCompleted,
			Signals: subSignals,
		}, nil
	}
	s, _ := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor", Ref: "child", OutputsPassthrough: true}, sub)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if len(res.Signals) != 2 {
		t.Fatalf("signals length = %d (want 2)", len(res.Signals))
	}
}

// TestSensorStep_DefaultConsumesSignals: when outputs_passthrough is false
// (default), the sub-run's signals are consumed internally — the parent
// step exposes only its own verdict to the engine.
func TestSensorStep_DefaultConsumesSignals(t *testing.T) {
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return &step.StepResult{
			Verdict: signal.VerdictPass,
			Status:  step.StatusCompleted,
			Signals: []map[string]interface{}{
				{"verdict": "pass", "metadata": map[string]interface{}{"kind": "individual"}},
			},
		}, nil
	}
	s, _ := sensorstep.New(sensor.StepConfig{ID: "x", Type: "sensor", Ref: "child"}, sub)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Signals != nil {
		t.Fatalf("signals = %v (want nil)", res.Signals)
	}
}

// TestSensorStep_Outputs_AggregateVerdictBuiltin: the only built-in
// extraction this PR ships is from: aggregate.verdict, which copies the
// sub-run's verdict into the named output.
func TestSensorStep_Outputs_AggregateVerdictBuiltin(t *testing.T) {
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return &step.StepResult{Verdict: signal.VerdictWarn, Status: step.StatusCompleted}, nil
	}
	cfg := sensor.StepConfig{
		ID: "x", Type: "sensor", Ref: "child",
		Outputs: map[string]sensor.OutputSpec{
			"v": {From: "aggregate.verdict"},
		},
	}
	s, _ := sensorstep.New(cfg, sub)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if got := res.Outputs["v"]; got != string(signal.VerdictWarn) {
		t.Fatalf("outputs[v] = %q (want warn)", got)
	}
}

// TestSensorStep_Outputs_UnsupportedFromIsEmpty: any From other than the
// built-ins is deferred — the output is declared but populated with the
// empty string. This is the documented common-case follow-up behavior.
func TestSensorStep_Outputs_UnsupportedFromIsEmpty(t *testing.T) {
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return &step.StepResult{Verdict: signal.VerdictPass, Status: step.StatusCompleted}, nil
	}
	cfg := sensor.StepConfig{
		ID: "x", Type: "sensor", Ref: "child",
		Outputs: map[string]sensor.OutputSpec{
			"order": {From: "aggregate.evidence[0].rationale"},
		},
	}
	s, _ := sensorstep.New(cfg, sub)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	got, ok := res.Outputs["order"]
	if !ok {
		t.Fatal("outputs[order] missing")
	}
	if got != "" {
		t.Fatalf("outputs[order] = %q (want empty)", got)
	}
}

// TestSensorStep_With_FixtureOverride: a with: entry that names a fixture
// is resolved against ec.Fixtures and threaded into the sub-run's fixture
// override map. Env overrides are untouched by fixture-typed entries.
func TestSensorStep_With_FixtureOverride(t *testing.T) {
	var gotFx, gotEnv map[string]string
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		gotFx = fx
		gotEnv = env
		return &step.StepResult{Verdict: signal.VerdictPass, Status: step.StatusCompleted}, nil
	}
	cfg := sensor.StepConfig{
		ID: "x", Type: "sensor", Ref: "child",
		With: map[string]interface{}{
			"body": map[string]interface{}{"fixture": "card.json"},
		},
	}
	s, _ := sensorstep.New(cfg, sub)
	ec := &step.ExecContext{
		Fixtures: map[string]string{"card.json": "/abs/path/card.json"},
		Env:      map[string]string{},
	}
	res := s.Execute(context.Background(), ec)
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v", res.Verdict)
	}
	if gotFx["card.json"] != "/abs/path/card.json" {
		t.Fatalf("fixture override = %+v", gotFx)
	}
	if len(gotEnv) != 0 {
		t.Fatalf("env override = %+v (want empty)", gotEnv)
	}
}

// TestSensorStep_With_FixtureMissingErrors: a fixture name that does not
// exist in ec.Fixtures must surface a verdict=error so misconfiguration
// is attributable to the parent step rather than the sub-run.
func TestSensorStep_With_FixtureMissingErrors(t *testing.T) {
	called := false
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		called = true
		return &step.StepResult{Verdict: signal.VerdictPass, Status: step.StatusCompleted}, nil
	}
	cfg := sensor.StepConfig{
		ID: "x", Type: "sensor", Ref: "child",
		With: map[string]interface{}{
			"body": map[string]interface{}{"fixture": "missing.json"},
		},
	}
	s, _ := sensorstep.New(cfg, sub)
	ec := &step.ExecContext{Fixtures: map[string]string{}, Env: map[string]string{}}
	res := s.Execute(context.Background(), ec)
	if called {
		t.Fatal("subrun must not be called when fixture is missing")
	}
	if res.Verdict != signal.VerdictError {
		t.Fatalf("verdict = %v (want error)", res.Verdict)
	}
	if res.Status != step.StatusAborted {
		t.Fatalf("status = %q (want aborted)", res.Status)
	}
}

// TestSensorStep_With_EnvOverride: scalar (string / number / bool) with:
// entries map to env overrides keyed by the with: key as-is.
func TestSensorStep_With_EnvOverride(t *testing.T) {
	var gotEnv map[string]string
	sub := func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		gotEnv = env
		return &step.StepResult{Verdict: signal.VerdictPass, Status: step.StatusCompleted}, nil
	}
	cfg := sensor.StepConfig{
		ID: "x", Type: "sensor", Ref: "child",
		With: map[string]interface{}{
			"FEATURE_FLAG": "${{ env.FF }}",
			"COUNT":        42,
		},
	}
	s, _ := sensorstep.New(cfg, sub)
	ec := &step.ExecContext{Fixtures: map[string]string{}, Env: map[string]string{}}
	res := s.Execute(context.Background(), ec)
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v", res.Verdict)
	}
	if gotEnv["FEATURE_FLAG"] != "${{ env.FF }}" {
		t.Fatalf("env[FEATURE_FLAG] = %q", gotEnv["FEATURE_FLAG"])
	}
	if gotEnv["COUNT"] != "42" {
		t.Fatalf("env[COUNT] = %q (want 42)", gotEnv["COUNT"])
	}
}

// TestSensorStep_IDAndType returns the configured discriminators, so the
// engine can attribute signals back to the declared step.
func TestSensorStep_IDAndType(t *testing.T) {
	s, _ := sensorstep.New(sensor.StepConfig{ID: "child-step", Type: "sensor", Ref: "child"}, func(ctx context.Context, ref string, fx, env map[string]string) (*step.StepResult, error) {
		return &step.StepResult{Verdict: signal.VerdictPass}, nil
	})
	if s.ID() != "child-step" {
		t.Errorf("ID = %q", s.ID())
	}
	if s.Type() != "sensor" {
		t.Errorf("Type = %q", s.Type())
	}
}
