package lib

import (
	"strings"
	"testing"
)

func TestValidator_AcceptsOutputParsing(t *testing.T) {
	v, err := NewValidator(repoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := validSensorComputational()
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
	if err := v.Validate(TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidator_RejectsEmptyPatterns(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{}, // empty — must be rejected
	}
	err := v.Validate(TargetSensor, s)
	if err == nil {
		t.Fatal("expected error for empty patterns")
	}
}

func TestValidator_RejectsBadVerdictInPattern(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "x", "verdict": "broken", "severity": "info"},
		},
	}
	err := v.Validate(TargetSensor, s)
	if err == nil || !strings.Contains(flattenValidationError(err), "$defs/Verdict") {
		t.Fatalf("expected Verdict ref violation, got %v", err)
	}
}

func TestValidator_AcceptsOutputParsingOnInferential(t *testing.T) {
	v, err := NewValidator(repoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	s := validSensorInferential()
	s["output"] = "stream"
	s["execution"].(map[string]interface{})["output_parsing"] = map[string]interface{}{
		"patterns": []interface{}{
			map[string]interface{}{"regex": "^PASS", "verdict": "pass", "severity": "info"},
		},
	}
	if err := v.Validate(TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}
