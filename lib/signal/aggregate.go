// Package signal implements the deterministic helpers that turn a
// sensor's run-time observations into Signal-shaped data: aggregate
// verdict computation (worst-of-two), per-line pattern matching, and
// exit-code mapping.
package signal

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
	Blocking       bool // true for execution.blocking=true sensors
}

// AggregateResult is what goes into the aggregate Signal.
type AggregateResult struct {
	Verdict  string
	Severity string
}

// Aggregate applies the worst-of-two rule.
//
// For one-shot sensors (Blocking=false), timeout always forces
// verdict=error regardless of the inputs: the run is incomplete and the
// tool's own notion of success cannot be trusted.
//
// For blocking sensors (Blocking=true), timeout is the *intended*
// lifecycle — the runner deliberately terminates the process when its
// observation window ends. In that case the exit side is treated as
// pass/info and the aggregate is driven entirely by what the stream
// observed: if any pattern surfaced a problem the worst-of-stream wins,
// otherwise the verdict is pass.
//
// On rank ties the exit side wins: the run-level result (exit code mapping)
// is the more authoritative summary because it reflects the tool's own
// notion of success, while the stream verdict is reconstructed from
// per-item parsing.
func Aggregate(in AggregateInput) AggregateResult {
	if in.TimedOut && !in.Blocking {
		return AggregateResult{Verdict: "error", Severity: "high"}
	}
	exitV, exitS := in.ExitVerdict, in.ExitSeverity
	if in.TimedOut && in.Blocking {
		exitV, exitS = "pass", "info"
	}
	if VerdictRank[in.StreamVerdict] > VerdictRank[exitV] {
		return AggregateResult{Verdict: in.StreamVerdict, Severity: in.StreamSeverity}
	}
	return AggregateResult{Verdict: exitV, Severity: exitS}
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
