package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestSecurityAlwaysApplicable(t *testing.T) {
	r := Get(sensor.LayerSecurity)
	if r == nil {
		t.Fatal("security not registered")
	}
	uc := loadUsecase(t, "usecase-create-user.yaml")

	for _, fixture := range []string{"stack-http-api-postgres.yaml", "stack-library-no-deps.yaml"} {
		s := loadStack(t, fixture)
		ok, reason := r.Applicable(s, uc, nil)
		if !ok {
			t.Fatalf("%s: expected applicable, got reason %q", fixture, reason)
		}
	}
}
