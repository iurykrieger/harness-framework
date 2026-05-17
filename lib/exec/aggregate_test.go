package exec

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

// TestSeverityFromVerdict pins the verdict → severity mapping the
// aggregate signal uses. The mapping is the project-wide convention
// echoed by lib/step/http and lib/step/assert: pass→info, warn→medium,
// fail→high, error→critical.
func TestSeverityFromVerdict(t *testing.T) {
	cases := []struct {
		in   signal.Verdict
		want signal.Severity
	}{
		{signal.VerdictPass, signal.SeverityInfo},
		{signal.VerdictWarn, signal.SeverityMedium},
		{signal.VerdictFail, signal.SeverityHigh},
		{signal.VerdictError, signal.SeverityCritical},
	}
	for _, c := range cases {
		got := severityFromVerdict(c.in)
		if got != c.want {
			t.Errorf("severityFromVerdict(%q) = %q (want %q)", c.in, got, c.want)
		}
	}
}

// TestBuildAggregate_Shape covers the canonical envelope of the
// aggregate signal: sensor_id/version from the *Sensor, verdict +
// severity from the running verdict, and metadata.{kind=aggregate,
// steps=passthrough}.
func TestBuildAggregate_Shape(t *testing.T) {
	s := &sensor.Sensor{ID: "x", Version: "1.2.3"}
	perStep := []map[string]interface{}{
		{"id": "a", "type": "shell", "verdict": "pass"},
		{"id": "b", "type": "http", "verdict": "fail"},
	}
	agg := buildAggregate(s, signal.VerdictFail, perStep)

	if agg["sensor_id"] != "x" {
		t.Errorf("sensor_id = %v", agg["sensor_id"])
	}
	if agg["version"] != "1.2.3" {
		t.Errorf("version = %v", agg["version"])
	}
	if agg["verdict"] != string(signal.VerdictFail) {
		t.Errorf("verdict = %v", agg["verdict"])
	}
	if agg["severity"] != string(signal.SeverityHigh) {
		t.Errorf("severity = %v", agg["severity"])
	}
	meta, ok := agg["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata is not a map: %T", agg["metadata"])
	}
	if meta["kind"] != "aggregate" {
		t.Errorf("metadata.kind = %v", meta["kind"])
	}
	got, _ := meta["steps"].([]map[string]interface{})
	if len(got) != 2 {
		t.Fatalf("metadata.steps length = %d (want 2)", len(got))
	}
	if got[1]["verdict"] != "fail" {
		t.Errorf("metadata.steps[1].verdict = %v", got[1]["verdict"])
	}
}

// TestWorst pins the verdict folding so callers can rely on the
// rank pass < warn < fail < error.
func TestWorst(t *testing.T) {
	cases := []struct {
		a, b, want signal.Verdict
	}{
		{signal.VerdictPass, signal.VerdictPass, signal.VerdictPass},
		{signal.VerdictPass, signal.VerdictWarn, signal.VerdictWarn},
		{signal.VerdictWarn, signal.VerdictPass, signal.VerdictWarn},
		{signal.VerdictWarn, signal.VerdictFail, signal.VerdictFail},
		{signal.VerdictFail, signal.VerdictError, signal.VerdictError},
		{signal.VerdictError, signal.VerdictPass, signal.VerdictError},
	}
	for _, c := range cases {
		got := worst(c.a, c.b)
		if got != c.want {
			t.Errorf("worst(%q, %q) = %q (want %q)", c.a, c.b, got, c.want)
		}
	}
}
