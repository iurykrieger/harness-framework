package layer

import (
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestE2EApplicableRequiresEntryPointsAndRunProject(t *testing.T) {
	r := Get(sensor.LayerE2E)
	if r == nil {
		t.Fatal("e2e not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	// No catalog supplied — expect NOT applicable with "core sensor missing" reason.
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable without run-project in catalog")
	}
	if !strings.Contains(reason, "run-project") {
		t.Fatalf("reason should name run-project; got %q", reason)
	}

	// With run-project in catalog, expect APPLICABLE.
	cat := []sensor.Sensor{{ID: "run-project"}}
	ok, _ = r.Applicable(s, uc, cat)
	if !ok {
		t.Fatal("expected applicable with run-project in catalog")
	}
}

func TestE2EPlanEmitsCompositePlusScenarios(t *testing.T) {
	r := Get(sensor.LayerE2E)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	cat := []sensor.Sensor{{ID: "run-project"}}
	drafts := r.Plan(s, uc, cat)

	// usecase has 3 business_rules ⇒ 4 scenario narrows + 1 composite = 5 drafts.
	if len(drafts) != 5 {
		t.Fatalf("expected 5 drafts (1 happy + 3 rule scenarios + 1 composite), got %d", len(drafts))
	}

	// Last draft MUST be the composite.
	last := drafts[len(drafts)-1]
	if last.SensorID != "e2e-create-user" {
		t.Fatalf("composite id = %s", last.SensorID)
	}
	if last.Layer != sensor.LayerE2E {
		t.Fatalf("composite layer = %s", last.Layer)
	}
	// Composite references every scenario by SensorStep ref.
	if len(last.Execution.Steps) != 4 {
		t.Fatalf("composite expected 4 SensorSteps, got %d", len(last.Execution.Steps))
	}
	for _, st := range last.Execution.Steps {
		if st.Type != "sensor" {
			t.Fatalf("composite step %s is not type=sensor", st.ID)
		}
	}

	// Every draft has layer=e2e.
	for i, d := range drafts {
		if d.Layer != sensor.LayerE2E {
			t.Fatalf("draft %d layer = %s", i, d.Layer)
		}
	}

	// Happy scenario id.
	if drafts[0].SensorID != "e2e-happy-path-create-user" {
		t.Fatalf("first draft id = %s", drafts[0].SensorID)
	}
}
