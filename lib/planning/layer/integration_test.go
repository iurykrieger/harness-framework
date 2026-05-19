package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestIntegrationApplicableRequiresBoundaryRole(t *testing.T) {
	r := Get(sensor.LayerIntegrationTest)
	if r == nil {
		t.Fatal("integration-test not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatalf("expected applicable, got reason %q", reason)
	}
}

func TestIntegrationNotApplicableLibraryOnly(t *testing.T) {
	r := Get(sensor.LayerIntegrationTest)
	s := loadStack(t, "stack-library-no-deps.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable for library-only stack")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestIntegrationPlanEmitsOneDraft(t *testing.T) {
	r := Get(sensor.LayerIntegrationTest)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.SensorID != "integration-test-create-user" {
		t.Fatalf("SensorID = %s, want integration-test-create-user", d.SensorID)
	}
	if d.Layer != sensor.LayerIntegrationTest {
		t.Fatalf("layer = %s", d.Layer)
	}
	if d.Kind != sensor.KindAssertion {
		t.Fatalf("kind = %s", d.Kind)
	}
}
