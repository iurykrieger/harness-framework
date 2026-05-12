package signal_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/signal"
)

func TestBuilder_DefaultsAreSignalValid(t *testing.T) {
	sig := signal.NewBuilder("my-sensor", "1.0.0").
		WithVerdict("pass", "info").
		WithKind("started").
		WithRationale("ok").
		Build()

	if sig["sensor_id"] != "my-sensor" {
		t.Fatalf("sensor_id: %v", sig["sensor_id"])
	}
	if sig["version"] != "1.0.0" {
		t.Fatalf("version: %v", sig["version"])
	}
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict: %v", sig["verdict"])
	}
	if sig["severity"] != "info" {
		t.Fatalf("severity: %v", sig["severity"])
	}
	if sig["confidence"] != 1.0 {
		t.Fatalf("confidence: %v", sig["confidence"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "started" {
		t.Fatalf("metadata.kind: %v", md["kind"])
	}
	if sig["run_id"] == "" {
		t.Fatal("run_id empty")
	}
	if sig["started_at"] == "" || sig["finished_at"] == "" {
		t.Fatal("timestamps empty")
	}
	cost := sig["cost_actual"].(map[string]interface{})
	if cost["latency_ms"] != 0 {
		t.Fatalf("latency: %v", cost["latency_ms"])
	}
	ev := sig["evidence"].([]interface{})
	if len(ev) != 1 {
		t.Fatalf("expected single evidence, got %d", len(ev))
	}
	if ev[0].(map[string]interface{})["rationale"] != "ok" {
		t.Fatalf("rationale: %v", ev[0])
	}
}

func TestBuilder_MissingVersionFallsBackToZero(t *testing.T) {
	sig := signal.NewBuilder("x", "").
		WithVerdict("error", "high").
		WithKind("failed").
		Build()
	if sig["version"] != "0.0.0" {
		t.Fatalf("expected 0.0.0 fallback, got %v", sig["version"])
	}
}

func TestBuilder_WithMetadataMergesAndKindWins(t *testing.T) {
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("aggregate").
		WithMetadata(map[string]interface{}{
			"counts":    map[string]interface{}{"pass": 3.0},
			"exit_code": 0,
		}).
		Build()
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "aggregate" {
		t.Fatalf("kind: %v", md["kind"])
	}
	if md["exit_code"] != 0 {
		t.Fatalf("exit_code: %v", md["exit_code"])
	}
}

func TestBuilder_WithDiagnoseMergesAfter(t *testing.T) {
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("list").
		WithDiagnose(map[string]interface{}{
			"registry_path": "/x/.runtime/sensors/running_sensors.json",
		}).
		Build()
	md := sig["metadata"].(map[string]interface{})
	if md["registry_path"] != "/x/.runtime/sensors/running_sensors.json" {
		t.Fatalf("diagnose not merged: %v", md)
	}
}

func TestBuilder_WithRunIDOverridesDefaults(t *testing.T) {
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("started").
		WithRunID("RUN-1", "2026-05-12T10:00:00Z", "2026-05-12T10:00:01Z").
		Build()
	if sig["run_id"] != "RUN-1" {
		t.Fatalf("run_id: %v", sig["run_id"])
	}
	if sig["started_at"] != "2026-05-12T10:00:00Z" {
		t.Fatalf("started_at: %v", sig["started_at"])
	}
	if sig["finished_at"] != "2026-05-12T10:00:01Z" {
		t.Fatalf("finished_at: %v", sig["finished_at"])
	}
}

func TestBuilder_WithEvidenceWinsOverRationale(t *testing.T) {
	custom := []interface{}{
		map[string]interface{}{"rationale": "A"},
		map[string]interface{}{"rationale": "B"},
	}
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("k").
		WithRationale("ignored").
		WithEvidence(custom).
		Build()
	ev := sig["evidence"].([]interface{})
	if len(ev) != 2 {
		t.Fatalf("expected 2 evidence entries, got %d", len(ev))
	}
}

func TestBuilder_WithLatencyMS(t *testing.T) {
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("k").
		WithLatencyMS(123).
		Build()
	cost := sig["cost_actual"].(map[string]interface{})
	if cost["latency_ms"] != 123 {
		t.Fatalf("latency: %v", cost["latency_ms"])
	}
}
