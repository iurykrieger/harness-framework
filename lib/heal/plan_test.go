package heal_test

import (
	"encoding/json"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestParsePlan_Minimal(t *testing.T) {
	body := []byte(`{
		"diagnosis": {
			"failed_sensor_id": "run-project-nest",
			"shape": "missing-env",
			"evidence_excerpt": "RSA_PRIVATE_KEY required",
			"root_cause_hint": "var declared in requires.env but unset"
		},
		"auto_apply": [
			{"kind": "copy-template", "src": ".env.example", "dst": ".env"},
			{"kind": "set-env-in-file", "file": ".env", "name": "RSA_PRIVATE_KEY", "value_source": "ask-user"}
		],
		"propose_only": [
			{"kind": "shell", "command": "pnpm install", "rationale": "deps missing"}
		],
		"sensor_patches": [
			{"id": "run-project-nest", "patch": {"requires": {"env": [{"name": "RSA_PRIVATE_KEY"}]}}}
		],
		"new_setup_sensors": []
	}`)

	p, err := heal.ParsePlan(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Diagnosis.FailedSensorID != "run-project-nest" {
		t.Errorf("failed_sensor_id = %q", p.Diagnosis.FailedSensorID)
	}
	if p.Diagnosis.Shape != heal.ShapeMissingEnv {
		t.Errorf("shape = %v", p.Diagnosis.Shape)
	}
	if len(p.AutoApply) != 2 {
		t.Errorf("auto_apply len = %d", len(p.AutoApply))
	}
	if p.AutoApply[0].Kind != "copy-template" {
		t.Errorf("auto_apply[0].kind = %q", p.AutoApply[0].Kind)
	}
	if p.AutoApply[1].ValueSource != "ask-user" {
		t.Errorf("value_source = %q", p.AutoApply[1].ValueSource)
	}
}

func TestParsePlan_UnknownShape(t *testing.T) {
	body := []byte(`{"diagnosis": {"failed_sensor_id": "x", "shape": "bogus-shape"}}`)
	_, err := heal.ParsePlan(body)
	if err == nil {
		t.Fatal("expected error for unknown shape")
	}
}

func TestParsePlan_MarshalRoundTrip(t *testing.T) {
	p := heal.Plan{
		Diagnosis: heal.Diagnosis{
			FailedSensorID: "x",
			Shape:          heal.ShapeBinaryNotFound,
		},
		AutoApply:   []heal.Action{{Kind: "mkdir", Dir: "/tmp/foo"}},
		ProposeOnly: []heal.Proposal{{Kind: "shell", Command: "make build"}},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	round, err := heal.ParsePlan(b)
	if err != nil {
		t.Fatal(err)
	}
	if round.Diagnosis.Shape != heal.ShapeBinaryNotFound {
		t.Fatalf("round-trip shape lost")
	}
}
