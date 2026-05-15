package signal_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func loadTestValidator(t *testing.T) *schema.Validator {
	t.Helper()
	v, err := schema.NewValidator(schematest.RepoSchemasDir(t))
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	return v
}

func TestValidateOrEmergency_PassThroughWhenValid(t *testing.T) {
	v := loadTestValidator(t)
	good := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("started").
		WithRationale("ok").
		Build()
	var buf bytes.Buffer
	out := signal.ValidateOrEmergency(v, good, "x", &buf)
	if out["verdict"] != "pass" {
		t.Fatalf("expected original sig, got %v", out)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", buf.String())
	}
}

func TestValidateOrEmergency_EmitsEmergencyOnInvalid(t *testing.T) {
	v := loadTestValidator(t)
	bad := map[string]interface{}{"sensor_id": "x"} // missing required fields
	var buf bytes.Buffer
	out := signal.ValidateOrEmergency(v, bad, "x", &buf)
	if out["verdict"] != "error" {
		t.Fatalf("expected emergency verdict=error, got %v", out["verdict"])
	}
	md := out["metadata"].(map[string]interface{})
	if md["kind"] != "signal_validation_failed" {
		t.Fatalf("kind: %v", md["kind"])
	}
	if !strings.Contains(buf.String(), "BUG: emitted signal failed signal.yaml validation") {
		t.Fatalf("stderr should contain BUG message, got %q", buf.String())
	}
}
