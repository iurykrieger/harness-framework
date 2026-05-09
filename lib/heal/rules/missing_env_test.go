// lib/heal/rules/missing_env_test.go
package rules

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
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
