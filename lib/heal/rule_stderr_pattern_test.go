// lib/heal/rule_stderr_pattern_test.go
package heal

import "testing"

func TestRuleStderrPattern_DotEnvENOENT(t *testing.T) {
	r := ruleStderrPattern{}
	sig := Signal{Evidence: []SignalEvidence{{Rationale: "open .env: ENOENT no such file"}}}
	matched, shape, _ := r.Match(sig, FailedSensor{})
	if !matched || shape != ShapeEnvFileAbsent {
		t.Fatalf("matched=%v shape=%q", matched, shape)
	}
}

func TestRuleStderrPattern_ServiceUnavailable(t *testing.T) {
	r := ruleStderrPattern{}
	sig := Signal{Evidence: []SignalEvidence{{Rationale: "connection refused: postgres at 127.0.0.1:5432"}}}
	matched, shape, _ := r.Match(sig, FailedSensor{})
	if !matched || shape != ShapeServiceUnavailable {
		t.Fatalf("matched=%v shape=%q", matched, shape)
	}
}

func TestRuleStderrPattern_NoMatch(t *testing.T) {
	r := ruleStderrPattern{}
	sig := Signal{Evidence: []SignalEvidence{{Rationale: "all green"}}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("benign rationale must not match")
	}
}
