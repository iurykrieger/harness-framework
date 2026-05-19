package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestUnitTestApplicable(t *testing.T) {
	r := Get(sensor.LayerUnitTest)
	if r == nil {
		t.Fatal("unit-test not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatalf("expected applicable, got reason %q", reason)
	}
}

func TestUnitTestNotApplicableWithoutTestRunner(t *testing.T) {
	r := Get(sensor.LayerUnitTest)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	s.Components = filterOutRole(s.Components, "test-runner")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable when test-runner missing")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestUnitTestPlanEmitsOneNarrow(t *testing.T) {
	r := Get(sensor.LayerUnitTest)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.Layer != sensor.LayerUnitTest {
		t.Fatalf("layer = %s", d.Layer)
	}
	if d.SensorID != "unit-test-create-user" {
		t.Fatalf("id = %s", d.SensorID)
	}
	if d.Kind != sensor.KindAssertion {
		t.Fatalf("kind = %s", d.Kind)
	}
}
