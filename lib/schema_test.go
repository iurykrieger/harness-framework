package lib

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestFindSchemasDir(t *testing.T) {
	expected := repoSchemasDir(t)
	_, thisFile, _, _ := runtime.Caller(0)
	got, err := FindSchemasDir(filepath.Dir(thisFile))
	if err != nil {
		t.Fatal(err)
	}
	if got != expected {
		t.Fatalf("got %s, want %s", got, expected)
	}
}

func TestFindSchemasDir_Missing(t *testing.T) {
	if _, err := FindSchemasDir(t.TempDir()); err == nil {
		t.Fatal("expected error when no schemas dir exists")
	}
}

// ──────────────────────────────────────────────────────────────────────
// Validator (covers cross-file $ref to signal.json#/$defs/{Verdict,Severity})
// ──────────────────────────────────────────────────────────────────────

func TestValidator_AcceptsValidSensors(t *testing.T) {
	v, err := NewValidator(repoSchemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]map[string]interface{}{
		"computational": validSensorComputational(),
		"inferential":   validSensorInferential(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := v.Validate(TargetSensor, s); err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
		})
	}
}

func TestValidator_RejectsMutations(t *testing.T) {
	v, _ := NewValidator(repoSchemasDir(t))
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
				s := clone(validSensorComputational())
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
				s := clone(validSensorComputational())
				ec := s["execution"].(map[string]interface{})["exit_code_map"].([]interface{})
				ec[0].(map[string]interface{})["verdict"] = "broken"
				return s
			},
			wantSub: "$defs/Verdict",
		},
		{
			name: "inferential missing calibration",
			mutator: func() map[string]interface{} {
				s := clone(validSensorInferential())
				delete(s, "calibration")
				return s
			},
			wantSub: "calibration",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(TargetSensor, tc.mutator())
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
// helpers
// ──────────────────────────────────────────────────────────────────────

func flattenValidationError(err error) string {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		var b strings.Builder
		PrintValidationError(&b, ve, "")
		return b.String()
	}
	return err.Error()
}
