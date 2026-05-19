package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestArchitectureAlwaysApplicable(t *testing.T) {
	r := Get(sensor.LayerArchitecture)
	if r == nil {
		t.Fatal("architecture not registered")
	}
	uc := loadUsecase(t, "usecase-create-user.yaml")
	s := loadStack(t, "stack-library-no-deps.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatalf("expected applicable on library stack, got reason %q", reason)
	}
}
