package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestDBStateApplicable(t *testing.T) {
	r := Get(sensor.LayerDBState)
	if r == nil {
		t.Fatal("db-state not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatalf("expected applicable, got reason %q", reason)
	}
}

func TestDBStateNotApplicableWithoutDBClient(t *testing.T) {
	r := Get(sensor.LayerDBState)
	s := loadStack(t, "stack-library-no-deps.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable when db-client missing")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestDBStatePlanEmitsOneNarrow(t *testing.T) {
	r := Get(sensor.LayerDBState)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.SensorID != "observe-db-create-user" {
		t.Fatalf("SensorID = %s, want observe-db-create-user", d.SensorID)
	}
	if d.Kind != sensor.KindObservation {
		t.Fatalf("kind = %s, want observation", d.Kind)
	}
}
