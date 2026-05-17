package assert_test

import (
	"context"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
	"github.com/iurykrieger/harness-framework/lib/step/assert"
)

func TestAssert_EqualsHit(t *testing.T) {
	cfg := sensor.StepConfig{
		ID: "g", Type: "assert",
		Expect: map[string]interface{}{
			"value":  "${{ steps.prev.outputs.x }}",
			"equals": "ok",
		},
	}
	s, err := assert.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ec := &step.ExecContext{
		Env: map[string]string{},
		Steps: map[string]*step.StepResult{
			"prev": {Outputs: map[string]string{"x": "ok"}, Verdict: signal.VerdictPass},
		},
	}
	res := s.Execute(context.Background(), ec)
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
	if len(res.Signals) != 1 {
		t.Fatalf("expected 1 signal, got %d", len(res.Signals))
	}
	md, _ := res.Signals[0]["metadata"].(map[string]interface{})
	if md == nil || md["kind"] != "assertion" {
		t.Fatalf("metadata.kind not assertion: %+v", res.Signals[0])
	}
}

func TestAssert_EqualsMiss(t *testing.T) {
	cfg := sensor.StepConfig{
		ID: "g", Type: "assert",
		Expect: map[string]interface{}{
			"value":  "actual",
			"equals": "expected",
		},
	}
	s, _ := assert.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictFail {
		t.Fatalf("verdict = %v", res.Verdict)
	}
}

func TestAssert_GteMiss(t *testing.T) {
	cfg := sensor.StepConfig{
		ID: "g", Type: "assert",
		Expect: map[string]interface{}{
			"value": "1200",
			"lte":   500,
		},
	}
	s, _ := assert.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictFail {
		t.Fatalf("verdict = %v", res.Verdict)
	}
}

func TestAssert_GteHit(t *testing.T) {
	cfg := sensor.StepConfig{
		ID: "g", Type: "assert",
		Expect: map[string]interface{}{
			"value": "600",
			"gte":   500,
		},
	}
	s, _ := assert.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
}

func TestAssert_MatchesRegex(t *testing.T) {
	cfg := sensor.StepConfig{
		ID: "g", Type: "assert",
		Expect: map[string]interface{}{
			"value":   "abc-123",
			"matches": `^[a-z0-9-]+$`,
		},
	}
	s, _ := assert.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v", res.Verdict)
	}
}

func TestAssert_New_RejectsNonAssertType(t *testing.T) {
	if _, err := assert.New(sensor.StepConfig{ID: "x", Type: "shell"}); err == nil {
		t.Fatal("expected error for non-assert type")
	}
}

func TestAssert_New_RejectsMissingExpect(t *testing.T) {
	if _, err := assert.New(sensor.StepConfig{ID: "x", Type: "assert"}); err == nil {
		t.Fatal("expected error for missing expect")
	}
}

func TestAssert_New_RejectsWith(t *testing.T) {
	cfg := sensor.StepConfig{
		ID: "x", Type: "assert",
		With:   map[string]interface{}{"k": "v"},
		Expect: map[string]interface{}{"value": "a", "equals": "a"},
	}
	if _, err := assert.New(cfg); err == nil {
		t.Fatal("expected error: with: is not valid on assert step")
	}
}

func TestAssert_ExpectMalformed(t *testing.T) {
	cfg := sensor.StepConfig{
		ID: "g", Type: "assert",
		Expect: "not-a-map",
	}
	s, _ := assert.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictError {
		t.Fatalf("verdict = %v (want error)", res.Verdict)
	}
}

func TestAssert_MissingValue(t *testing.T) {
	cfg := sensor.StepConfig{
		ID: "g", Type: "assert",
		Expect: map[string]interface{}{"equals": "anything"},
	}
	s, _ := assert.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictError {
		t.Fatalf("verdict = %v (want error)", res.Verdict)
	}
}
