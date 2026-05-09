// lib/heal/rule_heal_hint_test.go
package heal

import "testing"

func TestRuleHealHint_KnownShape(t *testing.T) {
	r := ruleHealHint{}
	sig := Signal{Metadata: SignalMetadata{HealHint: "missing-env:RSA_PRIVATE_KEY"}}
	matched, shape, detail := r.Match(sig, FailedSensor{})
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

func TestRuleHealHint_UnknownShape(t *testing.T) {
	r := ruleHealHint{}
	sig := Signal{Metadata: SignalMetadata{HealHint: "bogus-shape:detail"}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("unknown prefix must not match")
	}
}

func TestRuleHealHint_NoColon(t *testing.T) {
	r := ruleHealHint{}
	sig := Signal{Metadata: SignalMetadata{HealHint: "missing-env"}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("hint without colon must not match")
	}
}

func TestRuleHealHint_Empty(t *testing.T) {
	r := ruleHealHint{}
	matched, _, _ := r.Match(Signal{}, FailedSensor{})
	if matched {
		t.Fatal("empty hint must not match")
	}
}
