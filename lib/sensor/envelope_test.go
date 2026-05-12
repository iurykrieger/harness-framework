package sensor_test

import (
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestBuildEnvelope(t *testing.T) {
	defer testfixtures.FreezeClock(t)()
	env, err := sensor.BuildEnvelope(testfixtures.ValidSensorComputational())
	if err != nil {
		t.Fatal(err)
	}
	if env.SensorID != "smoke-comp" || env.Version != "0.1.0" || env.SensorType != "computational" {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	if env.RunID != "00000000-0000-4000-8000-000000000000" {
		t.Fatalf("run_id not from frozen NewRunIDFn: %s", env.RunID)
	}
	if env.StartedAt != "2026-05-06T12:00:00Z" {
		t.Fatalf("started_at not from frozen NowFn: %s", env.StartedAt)
	}
}

func TestBuildEnvelope_MissingFields(t *testing.T) {
	for _, missing := range []string{"id", "version", "type"} {
		t.Run(missing, func(t *testing.T) {
			s := testfixtures.ValidSensorComputational()
			delete(s, missing)
			if _, err := sensor.BuildEnvelope(s); err == nil {
				t.Fatalf("expected error when %q missing", missing)
			}
		})
	}
}

func TestBuildEnvelopeTyped(t *testing.T) {
	prev := sensor.NowFn
	defer func() { sensor.NowFn = prev }()
	sensor.NowFn = func() time.Time {
		return time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	}
	s := &sensor.Sensor{ID: "demo", Version: "0.1.0", Type: sensor.TypeComputational}
	env := sensor.BuildEnvelopeTyped(s)
	if env.SensorID != "demo" || env.Version != "0.1.0" || env.SensorType != "computational" {
		t.Fatalf("envelope mismatch: %+v", env)
	}
	if env.RunID == "" {
		t.Fatalf("run id was empty")
	}
}
