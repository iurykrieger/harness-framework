package planning_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/planning"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// ledger mirrors the wire-format read-usecases.go emits and the
// planner consumes. Loading the same JSON shape keeps test fixtures
// in lib/planning/testdata/ledger-*.json portable: the wrapper script
// produces these files at runtime.
type ledger struct {
	Usecases []usecase.UseCase `json:"usecases"`
}

func loadLedger(t *testing.T, name string) []usecase.UseCase {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var lg ledger
	if err := json.Unmarshal(body, &lg); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return lg.Usecases
}

func TestBuildSingleUsecase(t *testing.T) {
	plans := planning.Build(loadLedger(t, "ledger-single-usecase.json"))
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan; got %d", len(plans))
	}
	p := plans[0]
	if p.SensorID != "assert-tail-sensor" {
		t.Fatalf("sensor_id: got %q", p.SensorID)
	}
	if p.Kind != "assertion" || p.Type != "computational" || p.Output != "stream" {
		t.Fatalf("kind/type/output: %s / %s / %s", p.Kind, p.Type, p.Output)
	}
	if len(p.StepOutline) != 2 {
		t.Fatalf("expected 2 steps; got %d", len(p.StepOutline))
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	ucs := loadLedger(t, "ledger-two-grouped.json")
	first, err := json.Marshal(planning.Build(ucs))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		again, err := json.Marshal(planning.Build(ucs))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("iter %d diverged:\nfirst:\n%s\nthis:\n%s", i, string(first), string(again))
		}
	}
}

func TestBuildShapeSplitProducesDistinctIDs(t *testing.T) {
	ucs := loadLedger(t, "ledger-two-split-trigger-shape.json")
	plans := planning.Build(ucs)
	if len(plans) < 2 {
		t.Fatalf("want at least 2 plans; got %d", len(plans))
	}
	seen := map[string]struct{}{}
	for _, p := range plans {
		if p.SensorID == "" {
			t.Fatalf("plan with empty sensor_id: %+v", p)
		}
		if _, dup := seen[p.SensorID]; dup {
			t.Fatalf("duplicate sensor_id %q", p.SensorID)
		}
		seen[p.SensorID] = struct{}{}
	}
}

func TestBuildFissionPartsShareInferredShape(t *testing.T) {
	plans := planning.Build(loadLedger(t, "ledger-bucket-too-large.json"))
	if len(plans) < 2 {
		t.Fatalf("expected ≥2 plans from fission; got %d", len(plans))
	}
	first := plans[0]
	for i, p := range plans[1:] {
		if p.Kind != first.Kind || p.Type != first.Type || p.Output != first.Output {
			t.Fatalf("plans disagree on kind/type/output: plans[0]=%s/%s/%s plans[%d]=%s/%s/%s",
				first.Kind, first.Type, first.Output, i+1, p.Kind, p.Type, p.Output)
		}
	}
}

func TestBuildGroupsByJourneyAndShape(t *testing.T) {
	plans := planning.Build(loadLedger(t, "ledger-two-grouped.json"))
	if len(plans) != 1 {
		t.Fatalf("want 1 sensor planned; got %d", len(plans))
	}
	agg := planning.MakeAggregate(plans)
	if agg.SensorsPlanned != 1 {
		t.Fatalf("aggregate.sensors_planned: %d", agg.SensorsPlanned)
	}
	if agg.UsecasesConsumed != 2 {
		t.Fatalf("aggregate.usecases_consumed: %d", agg.UsecasesConsumed)
	}
}

func TestBuildInferentialEmitsCalibrationWarn(t *testing.T) {
	plans := planning.Build(loadLedger(t, "ledger-inferential.json"))
	if len(plans) != 1 {
		t.Fatalf("want 1 plan; got %d", len(plans))
	}
	if plans[0].Type != "inferential" {
		t.Fatalf("type: %s", plans[0].Type)
	}
	if !strings.Contains(plans[0].Rationale, "WARN: inferential") {
		t.Fatalf("expected calibration warn in rationale; got %q", plans[0].Rationale)
	}
}

func TestBuildObservationKind(t *testing.T) {
	plans := planning.Build(loadLedger(t, "ledger-observation.json"))
	if len(plans) != 1 {
		t.Fatalf("want 1 plan; got %d", len(plans))
	}
	if plans[0].Kind != "observation" || plans[0].Output != "stream" {
		t.Fatalf("kind/output: %s / %s", plans[0].Kind, plans[0].Output)
	}
}

func TestMakeAggregateCountsUniqueUsecaseIDs(t *testing.T) {
	plans := []planning.Plan{
		{SensorID: "a", UseCases: []string{"uc-1", "uc-2"}},
		{SensorID: "b", UseCases: []string{"uc-2", "uc-3"}},
	}
	agg := planning.MakeAggregate(plans)
	if agg.SensorsPlanned != 2 || agg.UsecasesConsumed != 3 {
		t.Fatalf("aggregate counts wrong: %+v", agg)
	}
	if agg.Verdict != "pass" || agg.Severity != "info" || !agg.Aggregate {
		t.Fatalf("aggregate envelope wrong: %+v", agg)
	}
}
