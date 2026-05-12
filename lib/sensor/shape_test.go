package sensor_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

// canonicalize re-serializes a map[string]interface{} through json.Marshal
// + json.Unmarshal so all numeric values are float64 (matching how
// json.Unmarshal decodes numbers into interface{}). Without this step
// reflect.DeepEqual compares int vs float64 and always returns false.
func canonicalize(t *testing.T, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("canonicalize marshal: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("canonicalize unmarshal: %v", err)
	}
	return out
}

func roundTripSensor(t *testing.T, orig map[string]interface{}) {
	t.Helper()
	body, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal orig: %v", err)
	}
	var typed sensor.Sensor
	if err := json.Unmarshal(body, &typed); err != nil {
		t.Fatalf("unmarshal -> Sensor: %v", err)
	}
	got := typed.AsMap()
	want := canonicalize(t, orig)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip diff\nwant=%#v\n got=%#v", want, got)
	}
}

func TestSensorShape_RoundTrip_Computational(t *testing.T) {
	roundTripSensor(t, sensortest.LoadComputational(t).AsMap())
}

func TestSensorShape_RoundTrip_Inferential(t *testing.T) {
	roundTripSensor(t, sensortest.LoadInferential(t).AsMap())
}

func TestSensorShape_RoundTrip_Setup(t *testing.T) {
	roundTripSensor(t, sensortest.LoadSetup(t).AsMap())
}
