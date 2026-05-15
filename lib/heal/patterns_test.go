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

func TestMatchStderrPattern_SubprocessFailed_Patterns(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"docker buildx", `failed to solve: process "/bin/sh -c go work sync" did not complete successfully: exit code: 1`},
		{"go work module missing", `go: cannot load module charge-worker-conciliation listed in go.work file: open charge-worker-conciliation/go.mod: no such file or directory`},
		{"docker COPY", `COPY failed: file not found in build context or excluded by .dockerignore: stat src/missing: file does not exist`},
		{"plain did-not-complete", `process "/bin/sh -c npm run build" did not complete successfully: exit code: 2`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shape, ok := heal.MatchStderrPattern(tc.text)
			if !ok {
				t.Fatalf("MatchStderrPattern returned ok=false for %q", tc.text)
			}
			if shape != heal.ShapeSubprocessFailed {
				t.Fatalf("shape = %q, want %q", shape, heal.ShapeSubprocessFailed)
			}
		})
	}
}
