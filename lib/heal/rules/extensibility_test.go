// lib/heal/rules/extensibility_test.go
//
// This test exists to lock the spec property: adding a new rule must
// require zero changes to existing rule files or to classify.go. The
// test mints a fake rule, walks heal.ClassifyWith with a slice that
// has it appended, and asserts dispatch.
//
// If a future refactor moves rule logic into classify.go (a hardcoded
// chain), this test will keep passing — but a follow-up reviewer
// should still flag the violation. The companion safety net is
// registry_test.go which locks the registered slice.
package rules

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

type fakeNewRule struct{ shape heal.Shape }

func (f fakeNewRule) Name() string { return "fake-new-rule" }
func (fakeNewRule) Match(_ heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	return true, heal.ShapeMissingEnv, "fake-detail"
}

func TestExtensibility_AddingRule_DoesNotTouchExistingFiles(t *testing.T) {
	// A new rule plugged into the walker via ClassifyWith picks up
	// without any modification to classify.go or the production rules
	// slice. This is the single property the registry buys us.
	rs := append([]heal.Rule{}, Registered()...)
	rs = append(rs, fakeNewRule{})

	res, ok := heal.ClassifyWith(rs, heal.Signal{Verdict: "ok"}, heal.FailedSensor{})
	if !ok || res.Rule != "fake-new-rule" {
		t.Fatalf("expected fake-new-rule to dispatch; got rule=%q ok=%v", res.Rule, ok)
	}
}

func TestExtensibility_NewRuleIgnoredWhenEarlierMatches(t *testing.T) {
	rs := []heal.Rule{healHint{}, fakeNewRule{}}
	sig := heal.Signal{Metadata: heal.SignalMetadata{HealHint: "missing-env:FOO"}}
	res, ok := heal.ClassifyWith(rs, sig, heal.FailedSensor{})
	if !ok || res.Rule != "heal-hint" {
		t.Fatalf("first match must win; got rule=%q ok=%v", res.Rule, ok)
	}
}
