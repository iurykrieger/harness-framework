package lib

import "testing"

func TestValidator_InferentialRequiresCommand(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorInferential()
	delete(s["execution"].(map[string]interface{}), "command")
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected inferential without command to fail")
	}
}

func TestValidator_ComputationalRequiresCommand(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	delete(s["execution"].(map[string]interface{}), "command")
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected computational without command to fail")
	}
}

func TestValidator_InferentialAllowsMissingExitCodeMap(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorInferential()
	// Inferential sensors typically don't declare exit_code_map.
	if _, has := s["execution"].(map[string]interface{})["exit_code_map"]; has {
		t.Fatal("fixture should not have exit_code_map")
	}
	if err := v.Validate(TargetSensor, s); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidator_ComputationalForbidsLLMFields(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
	s := validSensorComputational()
	s["execution"].(map[string]interface{})["model"] = "anthropic/claude-sonnet-4-6"
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected computational with model to fail")
	}
}
