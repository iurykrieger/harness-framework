package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestContractApplicableRequiresContract(t *testing.T) {
	r := Get(sensor.LayerContractTest)
	if r == nil {
		t.Fatal("contract-test not registered")
	}
	// stack-http-api-postgres.yaml has role=http-server but no .proto/openapi evidence.
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable: no OpenAPI / proto contract file in evidence")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}
