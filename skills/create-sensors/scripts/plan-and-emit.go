//go:build plan_and_emit

// Command plan-and-emit reads a ledger from stdin (the wire format
// emitted by read-usecases.go), applies the closed-enum layer matrix
// in lib/planning/layer/, auto-creates missing platform primitives
// via lib/planning/coredetect/, and emits a JSONL plan on stdout — one
// Plan line per draft + one Aggregate envelope at the end.
//
// The script is the deterministic adapter between the in-memory layer
// recipes and the wire format consumed by write-sensor.go.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/iurykrieger/harness-framework/lib/planning/coredetect"
	"github.com/iurykrieger/harness-framework/lib/planning/layer"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

func sortStrings(s []string) { sort.Strings(s) }

type ledger struct {
	Usecases []usecase.UseCase `json:"usecases"`
	Stack    map[string]any    `json:"stack,omitempty"`
	Catalog  []sensor.Sensor   `json:"catalog,omitempty"`
}

type wirePlan struct {
	Type     string            `json:"type"`
	Draft    layer.Draft       `json:"draft,omitempty"`
	UseCase  string            `json:"use_case,omitempty"`
	Layer    sensor.Layer      `json:"layer,omitempty"`
	Skipped  bool              `json:"skipped,omitempty"`
	Reason   string            `json:"reason,omitempty"`
	CoreScaf *coredetect.Draft `json:"core_scaffold,omitempty"`
}

func main() { os.Exit(run(os.Stdin, os.Stdout, os.Stderr)) }

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		emit(stdout, errSig("usage", "read stdin: "+err.Error()))
		return 2
	}
	if len(body) == 0 {
		emit(stdout, errSig("usage", "stdin is empty"))
		return 2
	}
	var lg ledger
	if err := json.Unmarshal(body, &lg); err != nil {
		emit(stdout, errSig("usage", "parse ledger: "+err.Error()))
		return 2
	}

	st := decodeStack(lg.Stack)

	// Pass 1: figure out which core sensors are needed by any applicable layer,
	// and track which usecases triggered each so the scaffold's use_cases[] is
	// non-empty (the sensor.yaml schema requires minItems: 1).
	triggeredBy := map[string]map[string]struct{}{}
	for _, uc := range lg.Usecases {
		for _, l := range layer.AllLayers() {
			r := layer.Get(l)
			ok, reason := r.Applicable(st, uc, lg.Catalog)
			if !ok && reasonNamesMissingCore(reason) {
				if id := extractCoreID(reason); id != "" {
					if triggeredBy[id] == nil {
						triggeredBy[id] = map[string]struct{}{}
					}
					triggeredBy[id][uc.ID] = struct{}{}
				}
			}
		}
	}

	// Auto-create missing core scaffolds (emit them first).
	coreIDs := make([]string, 0, len(triggeredBy))
	for id := range triggeredBy {
		coreIDs = append(coreIDs, id)
	}
	scaffolds, err := coredetect.EnsureMissing(st, coreIDs)
	if err != nil {
		emit(stdout, errSig("coredetect_failed", err.Error()))
		return 1
	}
	syntheticCatalog := append([]sensor.Sensor{}, lg.Catalog...)
	for _, sc := range scaffolds {
		scCopy := sc
		ucIDs := make([]string, 0, len(triggeredBy[sc.SensorID]))
		for id := range triggeredBy[sc.SensorID] {
			ucIDs = append(ucIDs, id)
		}
		sortStrings(ucIDs)
		scCopy.UseCases = ucIDs
		emit(stdout, wirePlan{Type: "core_scaffold", CoreScaf: &scCopy})
		// Add to in-memory catalog so layer.Applicable now sees the primitive.
		syntheticCatalog = append(syntheticCatalog, sensor.Sensor{ID: sc.SensorID})
	}

	// Pass 2: emit layer drafts.
	total := 0
	for _, uc := range lg.Usecases {
		for _, l := range layer.AllLayers() {
			r := layer.Get(l)
			ok, reason := r.Applicable(st, uc, syntheticCatalog)
			if !ok {
				emit(stdout, wirePlan{Type: "layer_skipped", UseCase: uc.ID, Layer: l, Skipped: true, Reason: reason})
				continue
			}
			for _, d := range r.Plan(st, uc, syntheticCatalog) {
				emit(stdout, wirePlan{Type: "draft", UseCase: uc.ID, Draft: d})
				total++
			}
		}
	}

	emit(stdout, map[string]any{
		"aggregate":        true,
		"verdict":          "pass",
		"severity":         "info",
		"drafts_emitted":   total,
		"core_scaffolds":   len(scaffolds),
		"usecases_planned": len(lg.Usecases),
	})
	return 0
}

// decodeStack normalises the map[string]any ledger field into the
// typed stack.Stack consumed by recipes. JSON round-trip is the
// cheapest path; the validator already ran upstream.
func decodeStack(m map[string]any) stack.Stack {
	if m == nil {
		return stack.Stack{}
	}
	body, _ := json.Marshal(m)
	var out stack.Stack
	_ = json.Unmarshal(body, &out)
	return out
}

func reasonNamesMissingCore(reason string) bool {
	return len(reason) > 0 && (containsAll(reason, "core sensor", "missing"))
}

func extractCoreID(reason string) string {
	// reason format from layer.e2eRecipe.Applicable:
	//   "core sensor run-project missing from catalog (will be auto-created)"
	const prefix = "core sensor "
	const suffix = " missing"
	i := indexOf(reason, prefix)
	if i < 0 {
		return ""
	}
	rest := reason[i+len(prefix):]
	j := indexOf(rest, suffix)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func containsAll(s string, needles ...string) bool {
	for _, n := range needles {
		if indexOf(s, n) < 0 {
			return false
		}
	}
	return true
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func emit(w io.Writer, v interface{}) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func errSig(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("plan-and-emit", "0.1.0").
		WithVerdict("error", "high").WithKind(kind).WithRationale(rationale).Build()
}
