// lib/heal/patterns_test.go
package heal_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestStderrPatterns_PositiveCases(t *testing.T) {
	cases := []struct {
		text  string
		shape heal.Shape
	}{
		{"open .env: ENOENT no such file", heal.ShapeEnvFileAbsent},
		{"permission denied: .env", heal.ShapeEnvFileAbsent},
		{"connection refused: postgres at 127.0.0.1:5432", heal.ShapeServiceUnavailable},
		{"connection refused: redis://localhost:6379", heal.ShapeServiceUnavailable},
		{"sh: pnpm: command not found", heal.ShapeBinaryNotFound},
	}
	for _, c := range cases {
		shape, ok := heal.MatchStderrPattern(c.text)
		if !ok {
			t.Errorf("expected match for %q", c.text)
			continue
		}
		if shape != c.shape {
			t.Errorf("text=%q got shape %q want %q", c.text, shape, c.shape)
		}
	}
}

func TestStderrPatterns_NegativeCases(t *testing.T) {
	cases := []string{
		"all green",
		"--- FAIL: TestFoo",
		"PASS",
		"npm WARN deprecated lodash@4.17.0",
	}
	for _, c := range cases {
		if _, ok := heal.MatchStderrPattern(c); ok {
			t.Errorf("expected no match for %q", c)
		}
	}
}

func TestHealHintGrammar_Documented(t *testing.T) {
	// Sanity check that the documented prefixes are exactly the known
	// shapes — the grammar is a stable contract.
	for _, s := range []heal.Shape{heal.ShapeMissingEnv, heal.ShapeBinaryNotFound, heal.ShapeEnvFileAbsent, heal.ShapeServiceUnavailable} {
		if !s.IsKnown() {
			t.Errorf("shape %q not registered", s)
		}
	}
}
