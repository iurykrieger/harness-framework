// lib/heal/rules_test.go
package heal

import "testing"

func TestRegisteredRules_OrderIsStable(t *testing.T) {
	expected := []string{
		"missing-env",
		"heal-hint",
		"exit-code-127",
		"prepare-template-copy",
		"stderr-pattern",
	}
	got := registeredRules()
	if len(got) != len(expected) {
		t.Fatalf("len = %d, want %d", len(got), len(expected))
	}
	for i, r := range got {
		if r.Name() != expected[i] {
			t.Errorf("rules[%d].Name() = %q, want %q", i, r.Name(), expected[i])
		}
	}
}
