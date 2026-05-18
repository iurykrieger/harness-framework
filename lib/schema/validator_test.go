package schema_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

// yamlInstance decodes a YAML literal into the loosely-typed shape the
// validator accepts (map[string]interface{}). Mirrors the conversion path
// used by lib/sensor/persist.go and lib/schema.ReadAsJSON so the test sees
// the same instance shape as production code.
func yamlInstance(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	jsonBytes, err := yaml.YAMLToJSON(body)
	if err != nil {
		t.Fatalf("yamlInstance: yaml→json: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &out); err != nil {
		t.Fatalf("yamlInstance: unmarshal: %v", err)
	}
	return out
}

// ──────────────────────────────────────────────────────────────────────
// Validator (covers cross-file $ref to signal.json#/$defs/{Verdict,Severity})
// ──────────────────────────────────────────────────────────────────────

func TestValidator_AcceptsValidSensors(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]map[string]interface{}{
		"computational": sensortest.LoadComputational(t).AsMap(),
		"inferential":   sensortest.LoadInferential(t).AsMap(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := v.Validate(schema.TargetSensor, s); err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

func TestValidator_RejectsMutations(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
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
				s := clone(sensortest.LoadComputational(t).AsMap())
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
				s := clone(sensortest.LoadComputational(t).AsMap())
				ec := s["execution"].(map[string]interface{})["exit_code_map"].([]interface{})
				ec[0].(map[string]interface{})["verdict"] = "broken"
				return s
			},
			wantSub: "$defs/Verdict",
		},
		{
			name: "inferential missing calibration",
			mutator: func() map[string]interface{} {
				s := clone(sensortest.LoadInferential(t).AsMap())
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
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadInferential(t).AsMap()
	delete(s["execution"].(map[string]interface{}), "command")
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected inferential without command to fail")
	}
}

func TestValidator_ComputationalRequiresCommand(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
	delete(s["execution"].(map[string]interface{}), "command")
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected computational without command to fail")
	}
}

func TestValidator_InferentialAllowsMissingExitCodeMap(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadInferential(t).AsMap()
	// Inferential sensors typically don't declare exit_code_map.
	if _, has := s["execution"].(map[string]interface{})["exit_code_map"]; has {
		t.Fatal("fixture should not have exit_code_map")
	}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidator_ComputationalForbidsLLMFields(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
	s["execution"].(map[string]interface{})["model"] = "anthropic/claude-sonnet-4-6"
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected computational with model to fail")
	}
}

// ──────────────────────────────────────────────────────────────────────
// Output-mode tests (single|stream)
// ──────────────────────────────────────────────────────────────────────

func TestValidator_RequiresOutputField(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
	delete(s, "output")
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected error when output is missing")
	}
}

func TestValidator_RejectsBadOutputValue(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
	s["output"] = "broken"
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected enum violation for output=broken")
	}
}

func TestValidator_SingleForbidsOutputParsing(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
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
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
	s["output"] = "stream"
	// no output_parsing
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected stream sensor without output_parsing to fail")
	}
}

func TestValidator_StreamWithEmptyPatternsRejected(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{}, // minItems: 1 violation
	}
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected stream sensor with empty patterns to fail")
	}
}

func TestValidator_AcceptsSingleAndStream(t *testing.T) {
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))

	// single (default fixture is already single-shaped)
	if err := v.Validate(schema.TargetSensor, sensortest.LoadComputational(t).AsMap()); err != nil {
		t.Fatalf("default fixture (single) should validate: %v", err)
	}

	// stream
	s := sensortest.LoadComputational(t).AsMap()
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
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
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
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
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
	v, _ := schema.NewValidator(schematest.RepoSchemasDir(t))
	s := sensortest.LoadComputational(t).AsMap()
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
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadInferential(t).AsMap()
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
// UseCase target
// ──────────────────────────────────────────────────────────────────────

func TestValidator_UseCaseTarget(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, err := schema.NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string]interface{}{"id": "x"} // missing every other required field
	if err := v.Validate(schema.TargetUseCase, bad); err == nil {
		t.Fatal("expected schema rejection for empty usecase, got nil")
	}
}

func TestValidator_UseCaseTarget_AcceptsValidInstance(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, err := schema.NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	valid := map[string]interface{}{
		"id":          "create-user",
		"version":     "0.1.0",
		"name":        "Create user",
		"description": "POST /users creates an account.",
		"journey_id":  "user-registration",
		"trigger": map[string]interface{}{
			"summary": "POST /users",
			"shape":   "HTTP request",
			"fixture": map[string]interface{}{
				"inline": map[string]interface{}{"method": "POST", "path": "/users"},
			},
		},
		"behavior": map[string]interface{}{"summary": "validates and persists"},
		"expected_outcome": map[string]interface{}{
			"summary": "201 created",
			"shape":   "HTTP response",
			"fixture": map[string]interface{}{
				"inline": map[string]interface{}{"status": 201},
			},
		},
		"evidence": []interface{}{
			map[string]interface{}{"file": "src/users/controller.ts", "rationale": "handler"},
		},
	}
	if err := v.Validate(schema.TargetUseCase, valid); err != nil {
		t.Fatalf("expected valid usecase to pass, got: %v", err)
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
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
	delete(s, "kind")
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected validation to fail without kind")
	}
}

func TestValidator_KindEnumRejectsUnknown(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
	s["kind"] = "diagnostic"
	if err := v.Validate(schema.TargetSensor, s); err == nil {
		t.Fatal("expected validation to fail for kind='diagnostic'")
	}
}

func TestValidator_DependsOnRejectedAsV1Field(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
	s["depends_on"] = []interface{}{"start-postgres", "setup-env"}
	err = v.Validate(schema.TargetSensor, s)
	if err == nil {
		t.Fatal("expected depends_on to be rejected as v1 field after hard-cut")
	}
	if !strings.Contains(err.Error(), "depends_on") {
		t.Fatalf("expected error to mention depends_on, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Upgrade to v2") {
		t.Fatalf("expected error to reference v2 upgrade, got: %v", err)
	}
}

func TestValidator_ExecutionPrepareRejectedAsV1Field(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
	exec := s["execution"].(map[string]interface{})
	exec["prepare"] = []interface{}{
		map[string]interface{}{"command": "echo prep", "timeout_ms": 1000},
	}
	err = v.Validate(schema.TargetSensor, s)
	if err == nil {
		t.Fatal("expected execution.prepare to be rejected as v1 field after hard-cut")
	}
	if !strings.Contains(err.Error(), "execution.prepare") {
		t.Fatalf("expected error to mention execution.prepare, got: %v", err)
	}
}

func TestValidator_TeardownAccepted(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
	exec := s["execution"].(map[string]interface{})
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "echo down"},
	}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		t.Fatalf("expected teardown to validate: %v", err)
	}
}

func TestValidator_UpstreamSensorsRemoved(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := sensortest.LoadComputational(t).AsMap()
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
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
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
		"use_cases": ["fake-uc"]
	}`)
	var instance map[string]interface{}
	if err := json.Unmarshal(body, &instance); err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(schema.TargetSensor, instance); err != nil {
		t.Fatalf("expected v2 requires[] to validate, got: %v", err)
	}
}

func TestValidator_Sensor_RequiresArrayV2_Rejections(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}

	// Minimal valid sensor skeleton with requires replaced per case.
	const skeleton = `{
		"id": "ex-v2-reject",
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
		"requires": %s,
		"execution": {"command":"true","exit_code_map":[{"exit_code":0,"verdict":"pass","severity":"info"}]},
		"use_cases": ["fake-uc"]
	}`

	cases := []struct {
		name string
		body string
	}{
		{
			name: "unknown kind in v2 array item",
			body: `[{"kind":"port","addr":"5432"}]`,
		},
		{
			name: "RequireSensor item missing required id field",
			body: `[{"kind":"sensor"}]`,
		},
		{
			name: "RequireEnv item with lowercase name violates pattern",
			body: `[{"kind":"env","name":"lowercase"}]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(skeleton, tc.body))
			var instance map[string]interface{}
			if err := json.Unmarshal(raw, &instance); err != nil {
				t.Fatalf("fixture JSON is invalid: %v", err)
			}
			if err := v.Validate(schema.TargetSensor, instance); err == nil {
				t.Fatalf("expected validation to fail for case %q, but it passed", tc.name)
			}
		})
	}
}

// ──────────────────────────────────────────────────────────────────────
// steps[] shape (typed-step execution) — added by complex-commands plan
// ──────────────────────────────────────────────────────────────────────

func TestValidateSensor_StepsShape(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`
id: example-steps
version: 0.1.0
name: smoke
description: smoke
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute:
    cpu: low
    memory_mb: 64
  latency:
    p50_ms: 10
    p95_ms: 100
    timeout_ms: 5000
triggers:
  - "on": manual
use_cases:
  - fake-uc
execution:
  steps:
    - id: ping
      type: shell
      run: echo hi
      exit_code_map:
        "0": pass
`)
	if err := v.Validate(schema.TargetSensor, yamlInstance(t, body)); err != nil {
		t.Fatalf("steps shape should validate, got: %v", err)
	}
}

func TestValidateSensor_StepsAndCommandReject(t *testing.T) {
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`
id: bad
version: 0.1.0
name: smoke
description: x
kind: assertion
type: computational
regulation: maintainability
phase: on-demand
determinism: high
output: single
cost:
  class: cheap
  compute:
    cpu: low
    memory_mb: 64
  latency:
    p50_ms: 10
    p95_ms: 100
    timeout_ms: 5000
triggers:
  - "on": manual
use_cases:
  - fake-uc
execution:
  command: echo hi
  exit_code_map:
    - exit_code: 0
      verdict: pass
      severity: info
  steps:
    - id: dup
      type: shell
      run: echo also
`)
	if err := v.Validate(schema.TargetSensor, yamlInstance(t, body)); err == nil {
		t.Fatalf("declaring both command and steps must reject")
	}
}
