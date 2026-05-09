// lib/heal/rules/missing_env_test.go
package rules

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestRuleMissingEnv_PositiveDeclared(t *testing.T) {
	r := missingEnv{}
	sig := heal.Signal{
		Verdict: "error",
		Evidence: []heal.SignalEvidence{
			{Rationale: "Required environment variable RSA_PRIVATE_KEY not set"},
		},
	}
	failed := heal.FailedSensor{EnvNames: []string{"RSA_PRIVATE_KEY"}}
	matched, shape, detail := r.Match(sig, failed)
	if !matched {
		t.Fatal("expected match")
	}
	if shape != heal.ShapeMissingEnv {
		t.Errorf("shape=%q", shape)
	}
	if detail != "RSA_PRIVATE_KEY" {
		t.Errorf("detail=%q", detail)
	}
}

func TestRuleMissingEnv_NegativeNotDeclared(t *testing.T) {
	r := missingEnv{}
	sig := heal.Signal{
		Verdict: "error",
		Evidence: []heal.SignalEvidence{
			{Rationale: "Required environment variable BOGUS not set"},
		},
	}
	failed := heal.FailedSensor{EnvNames: []string{"RSA_PRIVATE_KEY"}}
	matched, _, _ := r.Match(sig, failed)
	if matched {
		t.Fatal("expected no match — var not in requires.env")
	}
}

func TestRuleMissingEnv_NegativeWrongVerdict(t *testing.T) {
	r := missingEnv{}
	sig := heal.Signal{
		Verdict:  "fail",
		Evidence: []heal.SignalEvidence{{Rationale: "Required environment variable FOO not set"}},
	}
	matched, _, _ := r.Match(sig, heal.FailedSensor{EnvNames: []string{"FOO"}})
	if matched {
		t.Fatal("rule should require verdict=error")
	}
}

// TestRuleMissingEnv_MatchesRunnerOutput locks the contract that the
// runner's per-var rationale (built by sensor.BuildMissingEnvSignal)
// is matched by missingEnvRegex. The previous block-style rationale
// ("Sensor cannot run: N required env var(s) missing ...") did not
// match the regex, leaving rule_missing_env dead in production.
func TestRuleMissingEnv_MatchesRunnerOutput(t *testing.T) {
	envelope := sensor.Envelope{
		SensorID: "x", Version: "0.1.0", RunID: "abc",
		StartedAt: "2026-05-08T00:00:00Z", SensorType: "computational",
	}
	missing := []sensor.MissingEnv{
		{Name: "RSA_PRIVATE_KEY", Description: "PEM contents for JWT signing"},
		{Name: "GCP_PROJECT"},
	}
	sig := sensor.BuildMissingEnvSignal(envelope, "single", missing)

	// Convert the runner-emitted Signal map into the heal.Signal view.
	rawEv, _ := sig["evidence"].([]interface{})
	if len(rawEv) != len(missing) {
		t.Fatalf("expected one evidence entry per missing var, got %d", len(rawEv))
	}
	healSig := heal.Signal{
		Verdict:  "error",
		Severity: "high",
	}
	for _, raw := range rawEv {
		ev, _ := raw.(map[string]interface{})
		rat, _ := ev["rationale"].(string)
		healSig.Evidence = append(healSig.Evidence, heal.SignalEvidence{Rationale: rat})
	}

	failed := heal.FailedSensor{EnvNames: []string{"RSA_PRIVATE_KEY", "GCP_PROJECT"}}
	res, ok := heal.ClassifyWith(Registered(), healSig, failed)
	if !ok {
		t.Fatal("classifier should fire on runner output")
	}
	if res.Rule != "missing-env" {
		t.Fatalf("rule=%q, expected missing-env", res.Rule)
	}
	// Detail should be the first matching var (RSA_PRIVATE_KEY by emission order).
	if res.Detail != "RSA_PRIVATE_KEY" {
		t.Fatalf("detail=%q, expected RSA_PRIVATE_KEY", res.Detail)
	}
}
