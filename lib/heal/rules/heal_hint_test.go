// lib/heal/rules/heal_hint_test.go
package rules

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestRuleHealHint_KnownShape(t *testing.T) {
	r := healHint{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{HealHint: "missing-env:RSA_PRIVATE_KEY"}}
	matched, shape, detail := r.Match(sig, heal.FailedSensor{})
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

func TestRuleHealHint_UnknownShape(t *testing.T) {
	r := healHint{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{HealHint: "bogus-shape:detail"}}
	matched, _, _ := r.Match(sig, heal.FailedSensor{})
	if matched {
		t.Fatal("unknown prefix must not match")
	}
}

func TestRuleHealHint_NoColon(t *testing.T) {
	r := healHint{}
	sig := heal.Signal{Metadata: heal.SignalMetadata{HealHint: "missing-env"}}
	matched, _, _ := r.Match(sig, heal.FailedSensor{})
	if matched {
		t.Fatal("hint without colon must not match")
	}
}

func TestRuleHealHint_Empty(t *testing.T) {
	r := healHint{}
	matched, _, _ := r.Match(heal.Signal{}, heal.FailedSensor{})
	if matched {
		t.Fatal("empty hint must not match")
	}
}
