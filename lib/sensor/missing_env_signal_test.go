package sensor_test

import (
	"strings"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func stableNowMissingEnv() time.Time {
	return time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
}

func TestBuildMissingEnvSignal_ShapeWrappedToGate(t *testing.T) {
	prev := sensor.NowFn
	defer func() { sensor.NowFn = prev }()
	sensor.NowFn = stableNowMissingEnv

	env := sensor.Envelope{SensorID: "x", Version: "0.1.0", RunID: "r1", StartedAt: "2026-05-08T00:00:00Z"}
	missing := []sensor.MissingEnv{
		{Name: "GH_TOKEN", Description: "PAT"},
		{Name: "REGION"},
	}
	sig := sensor.BuildMissingEnvSignal(env, "stream", missing)

	if sig["verdict"] != "error" {
		t.Fatalf("verdict = %v", sig["verdict"])
	}
	ev := sig["evidence"].([]interface{})
	if len(ev) != 2 {
		t.Fatalf("evidence length = %d, want 2", len(ev))
	}
	md := sig["metadata"].(map[string]interface{})
	if md["heal_hint"] != "missing-env:GH_TOKEN" {
		t.Errorf("heal_hint = %v, want %q", md["heal_hint"], "missing-env:GH_TOKEN")
	}
	rem := sig["remediation"].(map[string]interface{})
	if !strings.Contains(rem["instructions"].(string), "GH_TOKEN") {
		t.Errorf("remediation missing GH_TOKEN: %v", rem["instructions"])
	}
}
