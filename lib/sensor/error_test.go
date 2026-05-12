package sensor

import "testing"

func TestBuildErrorSignal_ShapeAndRemediation(t *testing.T) {
	prev := NowFn
	defer func() { NowFn = prev }()
	NowFn = stableNow

	env := Envelope{
		SensorID: "x", Version: "0.1.0", RunID: "abc",
		StartedAt: "2026-05-08T00:00:00Z", SensorType: "computational",
	}
	sig := BuildErrorSignal(env, "single", "missing required env var GITHUB_TOKEN", "export GITHUB_TOKEN and re-run")

	if sig["verdict"] != "error" || sig["severity"] != "high" {
		t.Fatalf("verdict/severity mismatch: %v %v", sig["verdict"], sig["severity"])
	}
	rem, ok := sig["remediation"].(map[string]interface{})
	if !ok {
		t.Fatalf("remediation missing")
	}
	if rem["instructions"] != "export GITHUB_TOKEN and re-run" {
		t.Fatalf("remediation.instructions=%v", rem["instructions"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "aggregate" || md["output_mode"] != "single" {
		t.Fatalf("metadata wrong: %+v", md)
	}
}

func TestBuildErrorSignal_OmitsRemediationWhenEmpty(t *testing.T) {
	env := Envelope{SensorID: "x", Version: "0.1.0", RunID: "r", StartedAt: "2026-05-08T00:00:00Z"}
	sig := BuildErrorSignal(env, "stream", "rationale", "")
	if _, ok := sig["remediation"]; ok {
		t.Fatalf("remediation should be omitted when empty")
	}
}
