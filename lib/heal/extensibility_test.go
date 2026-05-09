// lib/heal/extensibility_test.go
//
// This test exists to lock the spec property: adding a new rule must
// require zero changes to existing rule files or to classify.go. The
// test mints a fake rule, walks ClassifyWith with a slice that has
// it appended, and asserts dispatch.
//
// If a future refactor moves rule logic into classify.go (a hardcoded
// chain), this test will keep passing — but a follow-up reviewer
// should still flag the violation. The companion safety net is
// rules_test.go which locks the registered slice.
package heal

import "testing"

type fakeNewRule struct{ shape Shape }

func (f fakeNewRule) Name() string { return "fake-new-rule" }
func (fakeNewRule) Match(_ Signal, _ FailedSensor) (bool, Shape, string) {
	return true, ShapeMissingEnv, "fake-detail"
}

func TestExtensibility_AddingRule_DoesNotTouchExistingFiles(t *testing.T) {
	// A new rule plugged into the walker via ClassifyWith picks up
	// without any modification to classify.go or the production rules
	// slice. This is the single property the registry buys us.
	rules := append([]Rule{}, registeredRules()...)
	rules = append(rules, fakeNewRule{})

	res, ok := ClassifyWith(rules, Signal{Verdict: "ok"}, FailedSensor{})
	if !ok || res.Rule != "fake-new-rule" {
		t.Fatalf("expected fake-new-rule to dispatch; got rule=%q ok=%v", res.Rule, ok)
	}
}

func TestExtensibility_NewRuleIgnoredWhenEarlierMatches(t *testing.T) {
	rules := []Rule{ruleHealHint{}, fakeNewRule{}}
	sig := Signal{Metadata: SignalMetadata{HealHint: "missing-env:FOO"}}
	res, ok := ClassifyWith(rules, sig, FailedSensor{})
	if !ok || res.Rule != "heal-hint" {
		t.Fatalf("first match must win; got rule=%q ok=%v", res.Rule, ok)
	}
}
