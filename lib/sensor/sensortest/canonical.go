// Package sensortest exposes test helpers that load canonical sensor
// fixtures from lib/sensor/testdata/. The package depends only on
// lib/sensor; production code MUST NOT import it.
package sensortest

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// LoadComputational returns the canonical computational sensor.
func LoadComputational(t *testing.T) *sensor.Sensor { return load(t, "canonical-computational.yaml") }

// LoadInferential returns the canonical inferential sensor.
func LoadInferential(t *testing.T) *sensor.Sensor { return load(t, "canonical-inferential.yaml") }

// LoadSetup returns the canonical setup sensor.
func LoadSetup(t *testing.T) *sensor.Sensor { return load(t, "canonical-setup.yaml") }

func load(t *testing.T, name string) *sensor.Sensor {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	// .../lib/sensor/sensortest/canonical.go -> ../testdata
	p := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "testdata", name))
	body, err := schema.ReadAsJSON(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	var s sensor.Sensor
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("unmarshal %s: %v", p, err)
	}
	return &s
}
