package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestCodeQualityAlwaysApplicable(t *testing.T) {
	r := Get(sensor.LayerCodeQuality)
	if r == nil {
		t.Fatal("code-quality not registered")
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

func TestCodeQualityPlanEmitsInferentialDraftWithCalibration(t *testing.T) {
	r := Get(sensor.LayerCodeQuality)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.Type != sensor.TypeInferential {
		t.Fatalf("type = %s, want inferential", d.Type)
	}
	if d.Calibration == nil {
		t.Fatal("expected non-nil Calibration")
	}
	if d.Calibration.ConfidenceThreshold == 0 {
		t.Fatal("expected non-zero ConfidenceThreshold")
	}
}
