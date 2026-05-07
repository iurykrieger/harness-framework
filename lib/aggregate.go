// Package lib — aggregate.go computes the aggregate verdict for a sensor run
// from two independent sources (the exit-code mapping and the stream of
// individual Signals) and provides deterministic helpers to summarise the
// stream into the aggregate Signal: top-N evidence selection and a
// verdict-count histogram.
package lib

import "sort"

// VerdictRank gives an ordinal for verdicts: pass < warn < fail < error.
var VerdictRank = map[string]int{
	"pass":  0,
	"warn":  1,
	"fail":  2,
	"error": 3,
}

// AggregateInput collects everything needed to compute the aggregate verdict.
type AggregateInput struct {
	ExitVerdict    string // from MapExitCode
	ExitSeverity   string
	StreamVerdict  string // from MaxStreamVerdict over individuals
	StreamSeverity string
	TimedOut       bool
}

// AggregateResult is what goes into the aggregate Signal.
type AggregateResult struct {
	Verdict  string
	Severity string
}

// Aggregate applies the worst-of-two rule. Timeout always forces verdict=error
// regardless of the inputs (the run is incomplete; trust nothing else).
//
// On rank ties the exit side wins: the run-level result (exit code mapping)
// is the more authoritative summary because it reflects the tool's own
// notion of success, while the stream verdict is reconstructed from
// per-item parsing.
func Aggregate(in AggregateInput) AggregateResult {
	if in.TimedOut {
		return AggregateResult{Verdict: "error", Severity: "high"}
	}
	if VerdictRank[in.StreamVerdict] > VerdictRank[in.ExitVerdict] {
		return AggregateResult{Verdict: in.StreamVerdict, Severity: in.StreamSeverity}
	}
	return AggregateResult{Verdict: in.ExitVerdict, Severity: in.ExitSeverity}
}

// MaxStreamVerdict scans individuals and returns the highest-rank verdict and
// the severity of the first individual that hit that rank. Empty list →
// ("pass", "info"). Unknown verdict strings rank as 0 (treated as pass).
func MaxStreamVerdict(individuals []map[string]interface{}) (string, string) {
	best := "pass"
	bestSev := "info"
	bestRank := 0
	for _, s := range individuals {
		v, _ := s["verdict"].(string)
		if VerdictRank[v] > bestRank {
			bestRank = VerdictRank[v]
			best = v
			bestSev, _ = s["severity"].(string)
		}
	}
	return best, bestSev
}

// SelectTopEvidence returns up to n evidence items, prioritising the
// most-severe individuals. Stable order: verdict rank desc, then original
// order. Each individual may contribute multiple evidence items; the cap is
// applied across all contributors.
func SelectTopEvidence(individuals []map[string]interface{}, n int) []interface{} {
	type tagged struct {
		idx  int
		rank int
		s    map[string]interface{}
	}
	tagged_ := make([]tagged, len(individuals))
	for i, s := range individuals {
		v, _ := s["verdict"].(string)
		tagged_[i] = tagged{i, VerdictRank[v], s}
	}
	sort.SliceStable(tagged_, func(i, j int) bool {
		if tagged_[i].rank != tagged_[j].rank {
			return tagged_[i].rank > tagged_[j].rank
		}
		return tagged_[i].idx < tagged_[j].idx
	})
	out := []interface{}{}
	for _, t := range tagged_ {
		if len(out) >= n {
			break
		}
		ev, _ := t.s["evidence"].([]interface{})
		for _, item := range ev {
			if len(out) >= n {
				break
			}
			out = append(out, item)
		}
	}
	return out
}

// CountVerdicts returns a 4-key histogram (pass/warn/fail/error) over
// individuals. Keys missing from the input are present with value 0.
// Unknown verdict strings are ignored.
func CountVerdicts(individuals []map[string]interface{}) map[string]int {
	counts := map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 0}
	for _, s := range individuals {
		v, _ := s["verdict"].(string)
		if _, ok := counts[v]; ok {
			counts[v]++
		}
	}
	return counts
}
