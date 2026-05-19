package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// TestAccessibilityNotApplicableForHttpApi uses the http-api-postgres stack
// whose archetype is "http-api" — NOT "http-spa" or "http-ssr".
func TestAccessibilityNotApplicableForHttpApi(t *testing.T) {
	r := Get(sensor.LayerAccessibility)
	if r == nil {
		t.Fatal("accessibility not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable for http-api archetype")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}
