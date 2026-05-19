package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestResilienceNotApplicableWithoutFaultInjection(t *testing.T) {
	r := Get(sensor.LayerResilience)
	if r == nil {
		t.Fatal("resilience not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable for http stack without fault-injection tooling")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}
