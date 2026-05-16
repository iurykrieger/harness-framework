package exec_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/exec"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

// TestRun_HappyPath_ShellHttpAssert drives the three currently supported
// step types in a single chain. The shell step is a no-op success; the
// http step POSTs to a local httptest server and extracts an output from
// the JSON response body; the assert step gates on the extracted value
// matching a regex (a stand-in for "the previous step exposed a usable
// observation"). The aggregate signal is verified to be the last element
// of the returned stream with verdict=pass.
func TestRun_HappyPath_ShellHttpAssert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		fmt.Fprint(w, `{"id":"abc-123"}`)
	}))
	defer srv.Close()

	s := &sensor.Sensor{
		ID:      "happy-pipeline",
		Version: "1.0.0",
		Execution: sensor.Execution{
			Steps: []sensor.StepConfig{
				{
					ID: "noop", Type: "shell", Run: "echo ok",
					ExitCodeMap: map[string]sensor.Verdict{"0": sensor.Verdict(signal.VerdictPass)},
				},
				{
					ID: "create", Type: "http", Method: "POST", URL: srv.URL,
					Outputs: map[string]sensor.OutputSpec{
						"order_id": {From: "response.body", JSONPath: "$.id"},
					},
				},
				{
					ID: "gate", Type: "assert",
					Expect: map[string]interface{}{
						"value":   "${{ steps.create.outputs.order_id }}",
						"matches": `^[a-z0-9-]+$`,
					},
				},
			},
		},
	}
	signals, err := exec.Run(context.Background(), s, nil, map[string]string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(signals) == 0 {
		t.Fatal("Run returned no signals")
	}
	agg := signals[len(signals)-1]
	if agg["verdict"] != string(signal.VerdictPass) {
		t.Fatalf("aggregate verdict = %v (want pass); signals = %+v", agg["verdict"], signals)
	}
	meta, _ := agg["metadata"].(map[string]interface{})
	if meta["kind"] != "aggregate" {
		t.Fatalf("aggregate metadata.kind = %v", meta["kind"])
	}
	steps, _ := meta["steps"].([]map[string]interface{})
	if len(steps) != 3 {
		t.Fatalf("metadata.steps length = %d (want 3)", len(steps))
	}
	wantTypes := []string{"shell", "http", "assert"}
	wantIDs := []string{"noop", "create", "gate"}
	for i, want := range wantTypes {
		if steps[i]["type"] != want {
			t.Errorf("metadata.steps[%d].type = %v (want %q)", i, steps[i]["type"], want)
		}
		if steps[i]["id"] != wantIDs[i] {
			t.Errorf("metadata.steps[%d].id = %v (want %q)", i, steps[i]["id"], wantIDs[i])
		}
		if steps[i]["verdict"] != string(signal.VerdictPass) {
			t.Errorf("metadata.steps[%d].verdict = %v (want pass)", i, steps[i]["verdict"])
		}
	}
	if agg["sensor_id"] != "happy-pipeline" {
		t.Errorf("aggregate sensor_id = %v", agg["sensor_id"])
	}
	if agg["version"] != "1.0.0" {
		t.Errorf("aggregate version = %v", agg["version"])
	}
	if agg["severity"] != string(signal.SeverityInfo) {
		t.Errorf("aggregate severity = %v (want info)", agg["severity"])
	}
}

// TestRun_FailFast_AbortsRest stops the chain on the first fail/error
// verdict. The second step is never built or executed, so no signal from
// it ever reaches the stream. The aggregate carries verdict=fail and
// includes only the executed step in metadata.steps.
func TestRun_FailFast_AbortsRest(t *testing.T) {
	s := &sensor.Sensor{
		ID:      "fail-fast",
		Version: "1.0.0",
		Execution: sensor.Execution{
			Steps: []sensor.StepConfig{
				{
					ID: "fail", Type: "shell", Run: "exit 2",
					ExitCodeMap: map[string]sensor.Verdict{"2": sensor.Verdict(signal.VerdictFail)},
				},
				{
					ID: "skip", Type: "shell", Run: "echo should-not-run",
					ExitCodeMap: map[string]sensor.Verdict{"0": sensor.Verdict(signal.VerdictPass)},
				},
			},
		},
	}
	signals, err := exec.Run(context.Background(), s, nil, map[string]string{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(signals) == 0 {
		t.Fatal("Run returned no signals")
	}
	agg := signals[len(signals)-1]
	if agg["verdict"] != string(signal.VerdictFail) {
		t.Fatalf("aggregate verdict = %v (want fail)", agg["verdict"])
	}
	if agg["severity"] != string(signal.SeverityHigh) {
		t.Errorf("aggregate severity = %v (want high)", agg["severity"])
	}
	// The 'skip' step should not have emitted any signal.
	for _, sig := range signals[:len(signals)-1] {
		meta, _ := sig["metadata"].(map[string]interface{})
		if meta != nil && meta["step_id"] == "skip" {
			t.Fatalf("aborted step emitted a signal: %+v", sig)
		}
	}
	meta, _ := agg["metadata"].(map[string]interface{})
	steps, _ := meta["steps"].([]map[string]interface{})
	if len(steps) != 1 {
		t.Fatalf("metadata.steps length = %d (want 1; skip must not be recorded)", len(steps))
	}
	if steps[0]["id"] != "fail" || steps[0]["verdict"] != string(signal.VerdictFail) {
		t.Errorf("metadata.steps[0] = %+v", steps[0])
	}
}

// TestRun_SensorStepNotYetSupported guards against accidental
// enablement of the type: sensor step before it is wired into the
// engine. Run must return an error so the orchestrator surfaces a
// clear, attributable failure instead of silently dropping the step.
func TestRun_SensorStepNotYetSupported(t *testing.T) {
	s := &sensor.Sensor{
		ID:      "premature",
		Version: "1.0.0",
		Execution: sensor.Execution{
			Steps: []sensor.StepConfig{
				{ID: "child", Type: "sensor", Ref: "other"},
			},
		},
	}
	_, err := exec.Run(context.Background(), s, nil, map[string]string{})
	if err == nil {
		t.Fatal("Run with type=sensor should error when no implementation is wired")
	}
}

// TestRun_UnknownStepType keeps the dispatcher honest: any unknown type
// must be surfaced as a Run-level error so the orchestrator can attribute
// the failure to a misconfigured sensor rather than crashing or silently
// skipping the step.
func TestRun_UnknownStepType(t *testing.T) {
	s := &sensor.Sensor{
		ID:      "bogus",
		Version: "1.0.0",
		Execution: sensor.Execution{
			Steps: []sensor.StepConfig{
				{ID: "x", Type: "telepathy"},
			},
		},
	}
	_, err := exec.Run(context.Background(), s, nil, map[string]string{})
	if err == nil {
		t.Fatal("Run with unknown step type should error")
	}
}
