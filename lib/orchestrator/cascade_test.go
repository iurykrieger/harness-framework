package orchestrator

import (
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestBuildCascadeSignal_Envelope(t *testing.T) {
	skipped := Sensor{
		ID: "e2e-tests",
		JSON: map[string]interface{}{
			"id":      "e2e-tests",
			"version": "0.1.0",
			"execution": map[string]interface{}{
				"command": "pnpm playwright test",
			},
		},
	}
	failedDepSignal := map[string]interface{}{
		"sensor_id": "start-postgres",
		"run_id":    "run-pg-1",
		"verdict":   "fail",
		"severity":  "high",
	}

	prevNow := sensor.NowFn
	prevID := sensor.NewRunIDFn
	defer func() { sensor.NowFn = prevNow; sensor.NewRunIDFn = prevID }()
	sensor.NowFn = func() time.Time { return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC) }
	sensor.NewRunIDFn = func() string { return "run-cascade-1" }

	sig := BuildCascadeSignal(skipped, failedDepSignal)

	checks := map[string]interface{}{
		"sensor_id":  "e2e-tests",
		"version":    "0.1.0",
		"run_id":     "run-cascade-1",
		"verdict":    "error",
		"severity":   "high",
		"confidence": 1.0,
	}
	for k, want := range checks {
		if got := sig[k]; got != want {
			t.Errorf("sig[%q] = %v, want %v", k, got, want)
		}
	}
	if sig["started_at"] != sig["finished_at"] {
		t.Errorf("started_at != finished_at for cascade signal")
	}
	cost := sig["cost_actual"].(map[string]interface{})
	if cost["latency_ms"] != 0 {
		t.Errorf("cost_actual.latency_ms = %v, want 0", cost["latency_ms"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "cascade" {
		t.Errorf("metadata.kind = %v, want cascade", md["kind"])
	}
	if md["failed_dep_id"] != "start-postgres" {
		t.Errorf("metadata.failed_dep_id = %v", md["failed_dep_id"])
	}
	if md["failed_dep_run_id"] != "run-pg-1" {
		t.Errorf("metadata.failed_dep_run_id = %v", md["failed_dep_run_id"])
	}
	ev := sig["evidence"].([]interface{})
	if len(ev) != 1 {
		t.Fatalf("evidence len = %d", len(ev))
	}
	first := ev[0].(map[string]interface{})
	rationale, _ := first["rationale"].(string)
	if rationale == "" {
		t.Error("expected non-empty evidence[0].rationale")
	}
}

func TestBuildCascadeSignal_ValidatesAgainstSchema(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	v, err := schema.NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	skipped := Sensor{
		ID: "e2e-tests",
		JSON: map[string]interface{}{
			"id":        "e2e-tests",
			"version":   "0.1.0",
			"execution": map[string]interface{}{"command": "pnpm test"},
		},
	}
	failed := map[string]interface{}{
		"sensor_id": "start-postgres", "run_id": "r1",
		"verdict": "fail", "severity": "high",
	}
	sig := BuildCascadeSignal(skipped, failed)
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		t.Fatalf("cascade Signal failed signal.json validation: %v", err)
	}
}
