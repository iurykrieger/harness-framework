package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestLogTraceApplicable(t *testing.T) {
	r := Get(sensor.LayerLogTrace)
	if r == nil {
		t.Fatal("log-trace not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatalf("expected applicable, got reason %q", reason)
	}
}

func TestLogTracePlanEmitsOneNarrow(t *testing.T) {
	r := Get(sensor.LayerLogTrace)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.SensorID != "observe-log-create-user" {
		t.Fatalf("SensorID = %s, want observe-log-create-user", d.SensorID)
	}
	if d.Kind != sensor.KindObservation {
		t.Fatalf("kind = %s, want observation", d.Kind)
	}
}
