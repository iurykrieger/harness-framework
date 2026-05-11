package schema_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

// ──────────────────────────────────────────────────────────────────────
// Validator (covers cross-file $ref to signal.json#/$defs/{Verdict,Severity})
// ──────────────────────────────────────────────────────────────────────

func TestValidator_AcceptsValidSensors(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]map[string]interface{}{
		"computational": testfixtures.ValidSensorComputational(),
		"inferential":   testfixtures.ValidSensorInferential(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := v.Validate(schema.TargetSensor, s); err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

func TestValidator_RejectsMutations(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	clone := func(m map[string]interface{}) map[string]interface{} {
		raw, _ := json.Marshal(m)
		var c map[string]interface{}
		_ = json.Unmarshal(raw, &c)
		return c
	}
	cases := []struct {
		name    string
		mutator func() map[string]interface{}
		wantSub string
	}{
		{
			name: "computational + tokens (mutual exclusion)",
			mutator: func() map[string]interface{} {
				s := clone(testfixtures.ValidSensorComputational())
				s["cost"].(map[string]interface{})["tokens"] = map[string]interface{}{
					"model": "x", "input_avg": 1, "output_avg": 1, "max_output": 1,
				}
				return s
			},
			wantSub: "not failed",
		},
		{
			name: "$ref to signal.json#/$defs/Verdict bites in exit_code_map",
			mutator: func() map[string]interface{} {
				s := clone(testfixtures.ValidSensorComputational())
				ec := s["execution"].(map[string]interface{})["exit_code_map"].([]interface{})
				ec[0].(map[string]interface{})["verdict"] = "broken"
				return s
			},
			wantSub: "$defs/Verdict",
		},
		{
			name: "inferential missing calibration",
			mutator: func() map[string]interface{} {
				s := clone(testfixtures.ValidSensorInferential())
				delete(s, "calibration")
				return s
			},
			wantSub: "calibration",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(schema.TargetSensor, tc.mutator())
			if err == nil {
				t.Fatal("expected error")
			}
			rendered := flattenValidationError(err)
			if !strings.Contains(rendered, tc.wantSub) {
				t.Fatalf("missing %q: %s", tc.wantSub, rendered)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────
// Command-required tests
// ──────────────────────────────────────────────────────────────────────

func TestValidator_InferentialRequiresCommand(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorInferential()
	delete(s["execution"].(map[string]interface{}), "command")
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected inferential without command to fail")
	}
}

func TestValidator_ComputationalRequiresCommand(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	delete(s["execution"].(map[string]interface{}), "command")
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected computational without command to fail")
	}
}

func TestValidator_InferentialAllowsMissingExitCodeMap(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorInferential()
	// Inferential sensors typically don't declare exit_code_map.
	if _, has := s["execution"].(map[string]interface{})["exit_code_map"]; has {
		t.Fatal("fixture should not have exit_code_map")
	}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidator_ComputationalForbidsLLMFields(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	s["execution"].(map[string]interface{})["model"] = "anthropic/claude-sonnet-4-6"
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected computational with model to fail")
	}
}

// ──────────────────────────────────────────────────────────────────────
// Output-mode tests (single|stream)
// ──────────────────────────────────────────────────────────────────────

func TestValidator_RequiresOutputField(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	delete(s, "output")
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected error when output is missing")
	}
}

func TestValidator_RejectsBadOutputValue(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	s["output"] = "broken"
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected enum violation for output=broken")
	}
}

func TestValidator_SingleForbidsOutputParsing(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	s["output"] = "single"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "^x", "verdict": "pass", "severity": "info"},
		},
	}
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected single sensor with output_parsing to fail")
	}
}

func TestValidator_StreamRequiresOutputParsing(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	s["output"] = "stream"
	// no output_parsing
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected stream sensor without output_parsing to fail")
	}
}

func TestValidator_StreamWithEmptyPatternsRejected(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{}, // minItems: 1 violation
	}
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected stream sensor with empty patterns to fail")
	}
}

func TestValidator_AcceptsSingleAndStream(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))

	// single (default fixture is already single-shaped)
	if err := v.Validate(schema.TargetSensor, testfixtures.ValidSensorComputational()); err != nil {
		t.Fatalf("default fixture (single) should validate: %v", err)
	}

	// stream
	s := testfixtures.ValidSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
		},
	}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		t.Fatalf("stream variant should validate: %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────
// Output-parsing tests
// ──────────────────────────────────────────────────────────────────────

func TestValidator_AcceptsOutputParsing(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{
				"regex":    `^FAIL\s+(\S+)`,
				"verdict":  "fail",
				"severity": "high",
				"captures": map[string]interface{}{"file": 1},
			},
			map[string]interface{}{
				"regex":    `^PASS\s+(\S+)`,
				"verdict":  "pass",
				"severity": "info",
			},
		},
	}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidator_RejectsEmptyPatterns(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{}, // empty — must be rejected
	}
	err := v.Validate(schema.TargetSensor, s)
	if err == nil {
		t.Fatal("expected error for empty patterns")
	}
}

func TestValidator_RejectsBadVerdictInPattern(t *testing.T) {
	v, _ := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	s := testfixtures.ValidSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "x", "verdict": "broken", "severity": "info"},
		},
	}
	err := v.Validate(schema.TargetSensor, s)
	if err == nil || !strings.Contains(flattenValidationError(err), "$defs/Verdict") {
		t.Fatalf("expected Verdict ref violation, got %v", err)
	}
}

func TestValidator_AcceptsOutputParsingOnInferential(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorInferential()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
		},
	}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

// ──────────────────────────────────────────────────────────────────────
// helpers
// ──────────────────────────────────────────────────────────────────────

func flattenValidationError(err error) string {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		var b strings.Builder
		schema.PrintValidationError(&b, ve, "")
		return b.String()
	}
	return err.Error()
}

// ──────────────────────────────────────────────────────────────────────
// kind / depends_on / lifecycle (prepare, teardown) tests
// ──────────────────────────────────────────────────────────────────────

func TestValidator_KindRequired(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	delete(s, "kind")
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected validation to fail without kind")
	}
}

func TestValidator_KindEnumRejectsUnknown(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	s["kind"] = "diagnostic"
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected validation to fail for kind='diagnostic'")
	}
}

func TestValidator_DependsOnAcceptsIDArray(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	s["depends_on"] = []interface{}{"start-postgres", "setup-env"}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		t.Fatalf("expected valid depends_on to validate: %v", err)
	}
}

func TestValidator_DependsOnRejectsBadID(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	s["depends_on"] = []interface{}{"Bad-ID"} // uppercase rejected
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected validation to fail for uppercase id in depends_on")
	}
}

func TestValidator_PrepareTeardownAccepted(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	exec := s["execution"].(map[string]interface{})
	exec["prepare"] = []interface{}{
		map[string]interface{}{"command": "echo prep", "timeout_ms": 1000},
	}
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "echo down"},
	}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		t.Fatalf("expected prepare+teardown to validate: %v", err)
	}
}

func TestValidator_UpstreamSensorsRemoved(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	requires := map[string]interface{}{
		"upstream_sensors": []interface{}{"x"},
	}
	s["requires"] = requires
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected requires.upstream_sensors to be rejected (additionalProperties false)")
	}
}

// ──────────────────────────────────────────────────────────────────────
// requires v2 array (discriminated union)
// ──────────────────────────────────────────────────────────────────────

func TestValidator_Sensor_RequiresArrayV2(t *testing.T) {
	v, err := schema.NewValidator(testfixtures.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
		"id": "ex-v2",
		"version": "1.0.0",
		"name": "ex",
		"description": "desc",
		"kind": "observation",
		"type": "computational",
		"regulation": "behaviour",
		"phase": "on-demand",
		"determinism": "high",
		"output": "single",
		"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":64},"latency":{"p50_ms":1,"p95_ms":1,"timeout_ms":1000}},
		"triggers": [{"on":"manual"}],
		"requires": [
			{"kind":"sensor","id":"setup-touch-file"},
			{"kind":"tool","name":"docker"},
			{"kind":"env","name":"GH_TOKEN","optional":false},
			{"kind":"context","path":"docs/"},
			{"kind":"permission","scope":"repo:read"},
			{"kind":"step","command":"true"}
		],
		"execution": {"command":"true","exit_code_map":[{"exit_code":0,"verdict":"pass","severity":"info"}]},
		"verification": {"golden_cases":[{"fixture":"f","expected_verdict":"pass","expected_severity":"info"}]}
	}`)
	var instance map[string]interface{}
	if err := json.Unmarshal(body, &instance); err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(schema.TargetSensor, instance); err != nil {
		t.Fatalf("expected v2 requires[] to validate, got: %v", err)
	}
}
