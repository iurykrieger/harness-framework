package orchestrator_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestPreflightGate_PassReturnsNilSignal(t *testing.T) {
	s := orchestrator.Sensor{
		ID: "ok",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{"command": "true"},
		},
	}
	env := sensor.Envelope{SensorID: "ok", Version: "1.0.0", RunID: "r-1", StartedAt: "2026-05-12T00:00:00Z"}

	sig, failed := orchestrator.PreflightGate(s, env, "single")
	if failed {
		t.Errorf("failed: got true, want false (no requires[])")
	}
	if sig != nil {
		t.Errorf("sig: got %v, want nil", sig)
	}
}

func TestPreflightGate_FailReturnsCanonicalSignal(t *testing.T) {
	const envName = "__HARNESS_PREFLIGHT_GATE_NEVER_SET__"
	s := orchestrator.Sensor{
		ID: "needs-env",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{"command": "true"},
			"requires": []interface{}{
				map[string]interface{}{"kind": "env", "name": envName},
			},
		},
	}
	env := sensor.Envelope{SensorID: "needs-env", Version: "1.0.0", RunID: "r-2", StartedAt: "2026-05-12T00:00:00Z"}

	sig, failed := orchestrator.PreflightGate(s, env, "single")
	if !failed {
		t.Fatalf("failed: got false, want true (env %s is unset)", envName)
	}
	if sig == nil {
		t.Fatal("sig: got nil, want non-nil")
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict: got %v, want error", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind: got %v, want failed", md["kind"])
	}
	if md["cause"] != "preflight_failed" {
		t.Errorf("metadata.cause: got %v, want preflight_failed", md["cause"])
	}
	envs := md["missing_envs"].([]interface{})
	if len(envs) != 1 || envs[0] != envName {
		t.Errorf("metadata.missing_envs: got %v, want [%s]", envs, envName)
	}
	if md["heal_hint"] != "missing-env:"+envName {
		t.Errorf("metadata.heal_hint: got %v, want missing-env:%s", md["heal_hint"], envName)
	}
}

func TestPreflightGate_UsesProvidedEnvelope(t *testing.T) {
	const envName = "__HARNESS_PREFLIGHT_GATE_NEVER_SET_2__"
	s := orchestrator.Sensor{
		ID: "ignored-in-envelope",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{"command": "true"},
			"requires": []interface{}{
				map[string]interface{}{"kind": "env", "name": envName},
			},
		},
	}
	env := sensor.Envelope{
		SensorID:  "caller-chose-this",
		Version:   "9.9.9",
		RunID:     "r-from-caller",
		StartedAt: "2099-12-31T23:59:59Z",
	}

	sig, _ := orchestrator.PreflightGate(s, env, "stream")
	if sig["sensor_id"] != "caller-chose-this" {
		t.Errorf("sensor_id: got %v, want caller-chose-this (helper must not re-derive)", sig["sensor_id"])
	}
	if sig["version"] != "9.9.9" {
		t.Errorf("version: got %v, want 9.9.9", sig["version"])
	}
	if sig["run_id"] != "r-from-caller" {
		t.Errorf("run_id: got %v, want r-from-caller", sig["run_id"])
	}
	if sig["started_at"] != "2099-12-31T23:59:59Z" {
		t.Errorf("started_at: got %v, want 2099-12-31T23:59:59Z", sig["started_at"])
	}
}
