//go:build plan_sensors

// Command plan-sensors reads a ledger from stdin (the wire format
// emitted by read-usecases.go) and emits a JSONL plan on stdout — one
// Plan line per proposed sensor, ending with one Aggregate envelope.
//
// All planning logic lives in lib/planning. This script is the JSON-on-
// stdin → JSONL-on-stdout adapter; it owns the wire format and exit
// codes only.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/planning"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// ledger mirrors the wire format read-usecases.go emits. The full
// catalog projection sits inside read-usecases.go (the wire-format
// owner); here we only need to consume usecases for planning. The
// catalog parameter to planning.Build is currently unused but kept on
// the API, so we pass nil.
type ledger struct {
	Usecases []usecase.UseCase `json:"usecases"`
}

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		emit(stdout, errSignal("usage", "read stdin: "+err.Error()))
		return 2
	}
	var lg ledger
	if err := json.Unmarshal(body, &lg); err != nil {
		emit(stdout, errSignal("usage", "parse ledger: "+err.Error()))
		return 2
	}

	plans := planning.Build(lg.Usecases, nil)
	for _, p := range plans {
		emit(stdout, p)
	}
	emit(stdout, planning.MakeAggregate(plans))
	return 0
}

func emit(w io.Writer, v any) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func errSignal(kind, rationale string) map[string]any {
	return signal.NewBuilder("plan-sensors", "0.1.0").
		WithVerdict("error", "high").
		WithKind(kind).
		WithRationale(rationale).
		Build()
}
