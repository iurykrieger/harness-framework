package orchestrator_test

import (
	"context"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
)

func TestRunPreparePhase_NoSteps(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "no-prep",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, 1000)
	if failed {
		t.Errorf("failed: got true, want false (no prepare steps)")
	}
	if len(results) != 0 {
		t.Errorf("results: got %d entries, want 0", len(results))
	}
}

func TestRunPreparePhase_AllPass(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "all-pass",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
				"prepare": []interface{}{
					map[string]interface{}{"command": "true"},
					map[string]interface{}{"command": "true"},
				},
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, 1000)
	if failed {
		t.Errorf("failed: got true, want false (all steps pass)")
	}
	if len(results) != 2 {
		t.Fatalf("results length: got %d, want 2", len(results))
	}
	for i, raw := range results {
		step, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("results[%d] not a map", i)
		}
		if step["verdict"] != "pass" {
			t.Errorf("results[%d].verdict = %v, want pass", i, step["verdict"])
		}
	}
}

func TestRunPreparePhase_NoExecution(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "no-execution",
		JSON: map[string]interface{}{
			"id": "no-execution",
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, 1000)
	if failed {
		t.Errorf("failed: got true, want false (no execution map)")
	}
	if results != nil {
		t.Errorf("results: got %v, want nil", results)
	}
}

func TestRunPreparePhase_FirstFails_FailFast(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "first-fails",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
				"prepare": []interface{}{
					map[string]interface{}{"command": "false"},
					map[string]interface{}{"command": "true"},
				},
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, 1000)
	if !failed {
		t.Errorf("failed: got false, want true (first step is `false`)")
	}
	if len(results) != 1 {
		t.Fatalf("results length: got %d, want 1 (fail-fast should abort)", len(results))
	}
	step := results[0].(map[string]interface{})
	if step["verdict"] != "fail" {
		t.Errorf("results[0].verdict = %v, want fail", step["verdict"])
	}
}
