package sensortest_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

func schemasDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// .../lib/sensor/sensortest/canonical_test.go -> 3 levels up to repo root.
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "schemas"))
}

func TestCanonicalLoadersReturnSensor(t *testing.T) {
	for _, tc := range []struct {
		name string
		load func(*testing.T) any
	}{
		{"computational", func(t *testing.T) any { return sensortest.LoadComputational(t) }},
		{"inferential", func(t *testing.T) any { return sensortest.LoadInferential(t) }},
		{"setup", func(t *testing.T) any { return sensortest.LoadSetup(t) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.load(t)
			if s == nil {
				t.Fatalf("loader returned nil")
			}
		})
	}
}

func TestCanonicalJSONValidatesAgainstSchema(t *testing.T) {
	v, err := schema.NewValidator(schemasDir(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		load func(*testing.T) any
	}{
		{"computational", func(t *testing.T) any { return sensortest.LoadComputational(t).AsMap() }},
		{"inferential", func(t *testing.T) any { return sensortest.LoadInferential(t).AsMap() }},
		{"setup", func(t *testing.T) any { return sensortest.LoadSetup(t).AsMap() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.Validate(schema.TargetSensor, tc.load(t)); err != nil {
				t.Fatalf("schema validate: %v", err)
			}
		})
	}
}
