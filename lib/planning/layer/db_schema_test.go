package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// TestDBSchemaNotApplicableWithoutMigrationsEvidence uses the http-api-postgres
// stack which has a db-client component but no evidence file path containing
// "migration" — so the second applicability gate must fire.
func TestDBSchemaNotApplicableWithoutMigrationsEvidence(t *testing.T) {
	r := Get(sensor.LayerDBSchema)
	if r == nil {
		t.Fatal("db-schema not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable when no migration evidence present")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}
