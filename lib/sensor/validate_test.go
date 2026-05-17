package sensor_test

import (
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// minimalStepsSensor constructs a baseline-valid Sensor with one shell step
// and no other knobs set. Tests mutate the returned pointer to exercise the
// rule under test in isolation.
func minimalStepsSensor(t *testing.T) *sensor.Sensor {
	t.Helper()
	return &sensor.Sensor{
		ID:          "test-sensor",
		Version:     "0.1.0",
		Name:        "test",
		Description: "fixture",
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationMaintainability,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 10, P95MS: 100},
		},
		Triggers: []sensor.Trigger{{On: sensor.TriggerManual}},
		Execution: sensor.Execution{
			Steps: []sensor.StepConfig{
				{
					ID:   "main",
					Type: "shell",
					Run:  "true",
				},
			},
		},
		UseCases: []string{"fake-uc"},
	}
}

// ----------------------------------------------------------------------------
// Rule 1: output: single + step parse: → error
// ----------------------------------------------------------------------------

func TestValidate_Rule1_OutputSingleNoParse_PositiveNoParse(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Output = sensor.OutputSingle
	s.Execution.Steps[0].Parse = nil
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 1 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule1_OutputSingleNoParse_NegativeWithParse(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Output = sensor.OutputSingle
	s.Execution.Steps[0].Parse = &sensor.ParseConfig{
		Patterns: []sensor.Pattern{{Regex: "ERROR:", Verdict: "fail", Severity: "high"}},
	}
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 1 negative: expected error for single+parse")
	}
	if !strings.Contains(err.Error(), "output: single") || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("rule 1: error should mention output: single and parse; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 2: output: stream + steps: but no shell parse: → error
// ----------------------------------------------------------------------------

func TestValidate_Rule2_OutputStreamWithParse_Positive(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Output = sensor.OutputStream
	s.Execution.Steps[0].Parse = &sensor.ParseConfig{
		Patterns: []sensor.Pattern{{Regex: "OK", Verdict: "pass", Severity: "info"}},
	}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 2 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule2_OutputStreamWithParse_NegativeNoParse(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Output = sensor.OutputStream
	s.Execution.Steps[0].Parse = nil
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 2 negative: expected error for stream+no-parse")
	}
	if !strings.Contains(err.Error(), "stream") {
		t.Fatalf("rule 2: error should mention stream; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 3: execution.blocking: true + steps: → error
// ----------------------------------------------------------------------------

func TestValidate_Rule3_BlockingAndStepsReject_Positive(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Blocking = false
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 3 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule3_BlockingAndStepsReject_Negative(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Blocking = true
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 3 negative: expected error for blocking+steps")
	}
	if !strings.Contains(err.Error(), "blocking") {
		t.Fatalf("rule 3: error should mention blocking; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 4: duplicate step ids → error
// ----------------------------------------------------------------------------

func TestValidate_Rule4_DuplicateStepIDs_Positive(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{ID: "a", Type: "shell", Run: "true"},
		{ID: "b", Type: "shell", Run: "true"},
	}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 4 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule4_DuplicateStepIDs_Negative(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{ID: "dup", Type: "shell", Run: "true"},
		{ID: "dup", Type: "shell", Run: "true"},
	}
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 4 negative: expected error for duplicate step ids")
	}
	if !strings.Contains(err.Error(), "duplicate") || !strings.Contains(err.Error(), "dup") {
		t.Fatalf("rule 4: error should mention duplicate id; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 5: with: { fixture: X } for unknown X → error citing .harness/fixtures/
// ----------------------------------------------------------------------------

func TestValidate_Rule5_WithFixtureExists_Positive(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Fixtures = map[string]string{"order.json": "/abs/.harness/fixtures/order.json"}
	s.Execution.Steps[0].With = map[string]interface{}{"fixture": "order.json"}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 5 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule5_WithFixtureExists_Negative(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Fixtures = map[string]string{"other.json": "/abs/.harness/fixtures/other.json"}
	s.Execution.Steps[0].With = map[string]interface{}{"fixture": "missing.json"}
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 5 negative: expected error for missing fixture")
	}
	if !strings.Contains(err.Error(), ".harness/fixtures/") || !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("rule 5: error should cite .harness/fixtures/ and the missing name; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 6: ${{ steps.<id>.outputs.<key> }} referencing unknown/later step → error
// ----------------------------------------------------------------------------

func TestValidate_Rule6_InterpolationOrder_Positive(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{
			ID:   "first",
			Type: "shell",
			Run:  "echo hello",
			Outputs: map[string]sensor.OutputSpec{
				"greeting": {From: "stdout"},
			},
		},
		{
			ID:   "second",
			Type: "shell",
			Run:  "echo ${{ steps.first.outputs.greeting }}",
		},
	}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 6 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule6_InterpolationOrder_NegativeMissing(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{
			ID:   "only",
			Type: "shell",
			Run:  "echo ${{ steps.nonexistent.outputs.x }}",
		},
	}
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 6 negative (missing): expected error")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("rule 6: error should mention missing step id; got %v", err)
	}
}

func TestValidate_Rule6_InterpolationOrder_NegativeLater(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{
			ID:   "first",
			Type: "shell",
			Run:  "echo ${{ steps.second.outputs.x }}",
		},
		{
			ID:   "second",
			Type: "shell",
			Run:  "echo hello",
			Outputs: map[string]sensor.OutputSpec{
				"x": {From: "stdout"},
			},
		},
	}
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 6 negative (later): expected error for forward reference")
	}
	if !strings.Contains(err.Error(), "second") {
		t.Fatalf("rule 6: error should mention later step id; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 7: ${{ steps.<id>.outputs.<key> }} where <key> not declared → error
// ----------------------------------------------------------------------------

func TestValidate_Rule7_InterpolationDeclared_Positive(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{
			ID:   "src",
			Type: "shell",
			Run:  "echo hi",
			Outputs: map[string]sensor.OutputSpec{
				"greeting": {From: "stdout"},
			},
		},
		{
			ID:   "use",
			Type: "shell",
			Run:  "echo ${{ steps.src.outputs.greeting }}",
		},
	}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 7 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule7_InterpolationDeclared_Negative(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{
			ID:   "src",
			Type: "shell",
			Run:  "echo hi",
			Outputs: map[string]sensor.OutputSpec{
				"greeting": {From: "stdout"},
			},
		},
		{
			ID:   "use",
			Type: "shell",
			Run:  "echo ${{ steps.src.outputs.unknown }}",
		},
	}
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 7 negative: expected error for undeclared output")
	}
	if !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "src") {
		t.Fatalf("rule 7: error should mention undeclared key and step id; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 8: type: sensor cycle detection (DFS, depth ≤ 5)
// ----------------------------------------------------------------------------

func TestValidate_Rule8_SensorCycleDetected_PositiveNoPeers(t *testing.T) {
	// When peers is nil/empty, the cycle rule cannot detect cycles it can't
	// see, so a sensor referencing an unknown peer is treated as no-cycle.
	s := minimalStepsSensor(t)
	s.ID = "a"
	s.Execution.Steps = []sensor.StepConfig{
		{ID: "main", Type: "sensor", Ref: "b"},
	}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 8 positive (no peers): expected nil, got %v", err)
	}
}

func TestValidate_Rule8_SensorCycleDetected_NegativeDirectCycle(t *testing.T) {
	// A → B, B → A.
	a := minimalStepsSensor(t)
	a.ID = "a"
	a.Execution.Steps = []sensor.StepConfig{
		{ID: "main", Type: "sensor", Ref: "b"},
	}
	b := minimalStepsSensor(t)
	b.ID = "b"
	b.Execution.Steps = []sensor.StepConfig{
		{ID: "main", Type: "sensor", Ref: "a"},
	}
	peers := map[string]*sensor.Sensor{"a": a, "b": b}

	err := sensor.Validate(a, peers)
	if err == nil {
		t.Fatalf("rule 8 negative: expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("rule 8: error should mention cycle; got %v", err)
	}
}

func TestValidate_Rule8_SensorCycleDetected_NegativeViaRequires(t *testing.T) {
	// A → B via type:sensor step; B → A via requires[kind=sensor].
	a := minimalStepsSensor(t)
	a.ID = "a"
	a.Execution.Steps = []sensor.StepConfig{
		{ID: "main", Type: "sensor", Ref: "b"},
	}
	b := minimalStepsSensor(t)
	b.ID = "b"
	b.Requires = []sensor.Requirement{
		{Kind: sensor.RequireSensor, ID: "a"},
	}
	peers := map[string]*sensor.Sensor{"a": a, "b": b}

	err := sensor.Validate(a, peers)
	if err == nil {
		t.Fatalf("rule 8 negative (combined graph): expected cycle error")
	}
}

func TestValidate_Rule8_SensorCycleDetected_NegativeDepthExceeded(t *testing.T) {
	// Chain a → b → c → d → e → f → g; depth from a is 6 (>5) → error.
	peers := map[string]*sensor.Sensor{}
	chain := []string{"a", "b", "c", "d", "e", "f", "g"}
	for i, id := range chain {
		s := minimalStepsSensor(t)
		s.ID = id
		if i < len(chain)-1 {
			s.Execution.Steps = []sensor.StepConfig{
				{ID: "main", Type: "sensor", Ref: chain[i+1]},
			}
		} else {
			s.Execution.Steps = []sensor.StepConfig{
				{ID: "main", Type: "shell", Run: "true"},
			}
		}
		peers[id] = s
	}
	err := sensor.Validate(peers["a"], peers)
	if err == nil {
		t.Fatalf("rule 8 negative (depth): expected error for chain deeper than 5")
	}
	if !strings.Contains(err.Error(), "depth") && !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("rule 8: depth error should mention depth or cycle; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 9: type: sensor ref pointing at a blocking child → error
// ----------------------------------------------------------------------------

func TestValidate_Rule9_SensorRefNotBlocking_Positive(t *testing.T) {
	a := minimalStepsSensor(t)
	a.ID = "a"
	a.Execution.Steps = []sensor.StepConfig{
		{ID: "main", Type: "sensor", Ref: "b"},
	}
	b := minimalStepsSensor(t)
	b.ID = "b"
	b.Execution.Blocking = false
	peers := map[string]*sensor.Sensor{"a": a, "b": b}
	if err := sensor.Validate(a, peers); err != nil {
		t.Fatalf("rule 9 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule9_SensorRefNotBlocking_Negative(t *testing.T) {
	a := minimalStepsSensor(t)
	a.ID = "a"
	a.Execution.Steps = []sensor.StepConfig{
		{ID: "main", Type: "sensor", Ref: "b"},
	}
	b := minimalStepsSensor(t)
	b.ID = "b"
	// b is a "command" shape sensor with blocking=true; the normalized
	// Steps don't matter for the rule — it checks Execution.Blocking on
	// the peer directly. Clear Steps to avoid rule 3 contamination.
	b.Execution.Blocking = true
	b.Execution.Steps = nil
	b.Execution.Command = "sleep 999"
	peers := map[string]*sensor.Sensor{"a": a, "b": b}

	err := sensor.Validate(a, peers)
	if err == nil {
		t.Fatalf("rule 9 negative: expected error for sensor-ref to blocking child")
	}
	if !strings.Contains(err.Error(), "blocking") || !strings.Contains(err.Error(), "b") {
		t.Fatalf("rule 9: error should mention blocking and peer id; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 10: type: assert step with `with:` declared → error
// ----------------------------------------------------------------------------

func TestValidate_Rule10_AssertNoWith_Positive(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{
			ID:     "check",
			Type:   "assert",
			Expect: map[string]interface{}{"equals": "ok"},
		},
	}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 10 positive: expected nil, got %v", err)
	}
}

func TestValidate_Rule10_AssertNoWith_Negative(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Execution.Steps = []sensor.StepConfig{
		{
			ID:     "check",
			Type:   "assert",
			Expect: map[string]interface{}{"equals": "ok"},
			With:   map[string]interface{}{"something": "value"},
		},
	}
	err := sensor.Validate(s, nil)
	if err == nil {
		t.Fatalf("rule 10 negative: expected error for assert+with")
	}
	if !strings.Contains(err.Error(), "assert") || !strings.Contains(err.Error(), "with") {
		t.Fatalf("rule 10: error should mention assert and with; got %v", err)
	}
}

// ----------------------------------------------------------------------------
// Rule 11: requires[kind=sensor] AND type:sensor ref overlap → warning
// ----------------------------------------------------------------------------

func TestValidate_Rule11_OverlapWarning(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Requires = []sensor.Requirement{
		{Kind: sensor.RequireSensor, ID: "shared"},
	}
	s.Execution.Steps = []sensor.StepConfig{
		{ID: "main", Type: "sensor", Ref: "shared"},
	}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 11: expected nil error (overlap is a warning), got %v", err)
	}
	if len(s.Warnings) == 0 {
		t.Fatalf("rule 11: expected at least one warning")
	}
	joined := strings.Join(s.Warnings, "\n")
	if !strings.Contains(joined, "shared") {
		t.Fatalf("rule 11: warning should name the overlapping id; got %v", s.Warnings)
	}
}

func TestValidate_Rule11_OverlapWarning_NoOverlap(t *testing.T) {
	s := minimalStepsSensor(t)
	s.Requires = []sensor.Requirement{
		{Kind: sensor.RequireSensor, ID: "alpha"},
	}
	s.Execution.Steps = []sensor.StepConfig{
		{ID: "main", Type: "sensor", Ref: "beta"},
	}
	if err := sensor.Validate(s, nil); err != nil {
		t.Fatalf("rule 11 (no overlap): expected nil, got %v", err)
	}
	if len(s.Warnings) != 0 {
		t.Fatalf("rule 11 (no overlap): expected no warnings, got %v", s.Warnings)
	}
}
