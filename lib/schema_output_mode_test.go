package lib

import "testing"

func TestValidator_RequiresOutputField(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	delete(s, "output")
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected error when output is missing")
	}
}

func TestValidator_RejectsBadOutputValue(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["output"] = "broken"
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected enum violation for output=broken")
	}
}

func TestValidator_SingleForbidsOutputParsing(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["output"] = "single"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "^x", "verdict": "pass", "severity": "info"},
		},
	}
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected single sensor with output_parsing to fail")
	}
}

func TestValidator_StreamRequiresOutputParsing(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["output"] = "stream"
	// no output_parsing
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected stream sensor without output_parsing to fail")
	}
}

func TestValidator_StreamWithEmptyPatternsRejected(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{}, // minItems: 1 violation
	}
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected stream sensor with empty patterns to fail")
	}
}

func TestValidator_AcceptsSingleAndStream(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))

	// single (default fixture is already single-shaped)
	if err := v.Validate(TargetSensor, validSensorComputational()); err != nil {
		t.Fatalf("default fixture (single) should validate: %v", err)
	}

	// stream
	s := validSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "^FAIL", "verdict": "fail", "severity": "high"},
		},
	}
	if err := v.Validate(TargetSensor, s); err != nil {
		t.Fatalf("stream variant should validate: %v", err)
	}
}
