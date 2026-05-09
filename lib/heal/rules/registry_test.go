// lib/heal/rules/registry_test.go
package rules

import "testing"

func TestRegisteredRules_OrderIsStable(t *testing.T) {
	expected := []string{
		"missing-env",
		"heal-hint",
		"exit-code-127",
		"prepare-template-copy",
		"stderr-pattern",
	}
	got := Registered()
	if len(got) != len(expected) {
		t.Fatalf("len = %d, want %d", len(got), len(expected))
	}
	for i, r := range got {
		if r.Name() != expected[i] {
			t.Errorf("rules[%d].Name() = %q, want %q", i, r.Name(), expected[i])
		}
	}
}
