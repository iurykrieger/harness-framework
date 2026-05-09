// lib/heal/rule_missing_env_test.go
package heal

import "testing"

func TestRuleMissingEnv_PositiveDeclared(t *testing.T) {
	r := ruleMissingEnv{}
	sig := Signal{
		Verdict: "error",
		Evidence: []SignalEvidence{
			{Rationale: "Required environment variable RSA_PRIVATE_KEY not set"},
		},
	}
	failed := FailedSensor{EnvNames: []string{"RSA_PRIVATE_KEY"}}
	matched, shape, detail := r.Match(sig, failed)
	if !matched {
		t.Fatal("expected match")
	}
	if shape != ShapeMissingEnv {
		t.Errorf("shape=%q", shape)
	}
	if detail != "RSA_PRIVATE_KEY" {
		t.Errorf("detail=%q", detail)
	}
}

func TestRuleMissingEnv_NegativeNotDeclared(t *testing.T) {
	r := ruleMissingEnv{}
	sig := Signal{
		Verdict: "error",
		Evidence: []SignalEvidence{
			{Rationale: "Required environment variable BOGUS not set"},
		},
	}
	failed := FailedSensor{EnvNames: []string{"RSA_PRIVATE_KEY"}}
	matched, _, _ := r.Match(sig, failed)
	if matched {
		t.Fatal("expected no match — var not in requires.env")
	}
}

func TestRuleMissingEnv_NegativeWrongVerdict(t *testing.T) {
	r := ruleMissingEnv{}
	sig := Signal{
		Verdict:  "fail",
		Evidence: []SignalEvidence{{Rationale: "Required environment variable FOO not set"}},
	}
	matched, _, _ := r.Match(sig, FailedSensor{EnvNames: []string{"FOO"}})
	if matched {
		t.Fatal("rule should require verdict=error")
	}
}
