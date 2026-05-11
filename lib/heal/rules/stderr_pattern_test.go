// lib/heal/rules/stderr_pattern_test.go
package rules

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestRuleStderrPattern_DotEnvENOENT(t *testing.T) {
	r := stderrPatternRule{}
	sig := heal.Signal{Evidence: []heal.SignalEvidence{{Rationale: "open .env: ENOENT no such file"}}}
	matched, shape, _ := r.Match(sig, heal.FailedSensor{})
	if !matched || shape != heal.ShapeEnvFileAbsent {
		t.Fatalf("matched=%v shape=%q", matched, shape)
	}
}

func TestRuleStderrPattern_ServiceUnavailable(t *testing.T) {
	r := stderrPatternRule{}
	sig := heal.Signal{Evidence: []heal.SignalEvidence{{Rationale: "connection refused: postgres at 127.0.0.1:5432"}}}
	matched, shape, _ := r.Match(sig, heal.FailedSensor{})
	if !matched || shape != heal.ShapeServiceUnavailable {
		t.Fatalf("matched=%v shape=%q", matched, shape)
	}
}

func TestRuleStderrPattern_NoMatch(t *testing.T) {
	r := stderrPatternRule{}
	sig := heal.Signal{Evidence: []heal.SignalEvidence{{Rationale: "all green"}}}
	matched, _, _ := r.Match(sig, heal.FailedSensor{})
	if matched {
		t.Fatal("benign rationale must not match")
	}
}

func TestStderrPatternRule_MatchesRequiredToolNotOnPath(t *testing.T) {
	rule := stderrPatternRule{}
	sig := heal.Signal{
		Verdict: "error",
		Evidence: []heal.SignalEvidence{
			{Rationale: `Required tool "docker" is not on PATH`},
		},
	}
	ok, shape, detail := rule.Match(sig, heal.FailedSensor{Tools: []string{"docker"}})
	if !ok {
		t.Fatal("expected match")
	}
	if shape != heal.ShapeBinaryNotFound {
		t.Errorf("shape = %v, want %v", shape, heal.ShapeBinaryNotFound)
	}
	if detail != "docker" {
		t.Errorf("detail = %q, want %q", detail, "docker")
	}
}
