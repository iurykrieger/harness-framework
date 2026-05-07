package signal_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/signal"
)

func TestAggregate_WorstOfTwo(t *testing.T) {
	cases := []struct {
		name       string
		exitVerd   string
		exitSev    string
		streamVerd string
		streamSev  string
		wantVerd   string
		wantSev    string
	}{
		{"both pass", "pass", "info", "pass", "info", "pass", "info"},
		{"exit pass, stream fail", "pass", "info", "fail", "high", "fail", "high"},
		{"exit fail, stream pass", "fail", "high", "pass", "info", "fail", "high"},
		{"exit warn, stream fail", "warn", "low", "fail", "high", "fail", "high"},
		{"exit error, stream pass", "error", "high", "pass", "info", "error", "high"},
		{"both fail", "fail", "medium", "fail", "high", "fail", "medium"}, // ties → exit side
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := signal.Aggregate(signal.AggregateInput{
				ExitVerdict: c.exitVerd, ExitSeverity: c.exitSev,
				StreamVerdict: c.streamVerd, StreamSeverity: c.streamSev,
			})
			if r.Verdict != c.wantVerd || r.Severity != c.wantSev {
				t.Errorf("got %s/%s, want %s/%s", r.Verdict, r.Severity, c.wantVerd, c.wantSev)
			}
		})
	}
}

func TestAggregate_TimeoutForcesError(t *testing.T) {
	r := signal.Aggregate(signal.AggregateInput{
		ExitVerdict: "pass", ExitSeverity: "info",
		StreamVerdict: "pass", StreamSeverity: "info",
		TimedOut: true,
	})
	if r.Verdict != "error" || r.Severity != "high" {
		t.Fatalf("got %+v", r)
	}
}

func TestMaxStreamVerdict_Empty(t *testing.T) {
	v, s := signal.MaxStreamVerdict(nil)
	if v != "pass" || s != "info" {
		t.Fatalf("got %s/%s", v, s)
	}
}

func TestMaxStreamVerdict_Mixed(t *testing.T) {
	individuals := []map[string]interface{}{
		{"verdict": "pass", "severity": "info"},
		{"verdict": "warn", "severity": "low"},
		{"verdict": "fail", "severity": "medium"},
		{"verdict": "warn", "severity": "low"},
	}
	v, s := signal.MaxStreamVerdict(individuals)
	if v != "fail" || s != "medium" {
		t.Fatalf("got %s/%s", v, s)
	}
}

func TestSelectTopEvidence_PrefersWorseVerdict(t *testing.T) {
	individuals := []map[string]interface{}{
		{"verdict": "pass", "severity": "info", "evidence": []interface{}{
			map[string]interface{}{"rationale": "ok 1"},
		}},
		{"verdict": "fail", "severity": "high", "evidence": []interface{}{
			map[string]interface{}{"rationale": "bad 1"},
		}},
		{"verdict": "warn", "severity": "low", "evidence": []interface{}{
			map[string]interface{}{"rationale": "warn 1"},
		}},
	}
	ev := signal.SelectTopEvidence(individuals, 2)
	if len(ev) != 2 {
		t.Fatalf("len=%d", len(ev))
	}
	first := ev[0].(map[string]interface{})["rationale"].(string)
	if first != "bad 1" {
		t.Fatalf("expected fail evidence first, got %q", first)
	}
}

func TestCountVerdicts(t *testing.T) {
	individuals := []map[string]interface{}{
		{"verdict": "pass"}, {"verdict": "pass"}, {"verdict": "fail"}, {"verdict": "warn"},
	}
	got := signal.CountVerdicts(individuals)
	want := map[string]int{"pass": 2, "warn": 1, "fail": 1, "error": 0}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d want %d", k, got[k], v)
		}
	}
}
