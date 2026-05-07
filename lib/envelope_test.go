package lib

import "testing"

func TestBuildEnvelope(t *testing.T) {
	defer freezeClock(t)()
	env, err := BuildEnvelope(validSensorComputational())
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
			s := validSensorComputational()
			delete(s, missing)
			if _, err := BuildEnvelope(s); err == nil {
				t.Fatalf("expected error when %q missing", missing)
			}
		})
	}
}
