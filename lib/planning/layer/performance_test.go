package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestPerformanceApplicableHttpApi(t *testing.T) {
	r := Get(sensor.LayerPerformance)
	if r == nil {
		t.Fatal("performance not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatalf("expected applicable for http-api archetype, got reason %q", reason)
	}
}

func TestPerformanceNotApplicableLibrary(t *testing.T) {
	r := Get(sensor.LayerPerformance)
	s := loadStack(t, "stack-library-no-deps.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable for library archetype")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}
