package signal_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/signal"
)

func canonicalSignalMap() map[string]interface{} {
	return map[string]interface{}{
		"sensor_id":   "demo",
		"version":     "0.1.0",
		"run_id":      "00000000-0000-4000-8000-000000000000",
		"started_at":  "2026-05-12T12:00:00Z",
		"finished_at": "2026-05-12T12:00:01Z",
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence": []interface{}{
			map[string]interface{}{"rationale": "smoke fixture"},
		},
		"cost_actual": map[string]interface{}{"latency_ms": 12},
		"metadata":    map[string]interface{}{"kind": "aggregate"},
	}
}

func canonicalize(t *testing.T, m map[string]interface{}) map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestSignalShape_RoundTrip(t *testing.T) {
	orig := canonicalSignalMap()
	body, _ := json.Marshal(orig)
	var typed signal.Signal
	if err := json.Unmarshal(body, &typed); err != nil {
		t.Fatalf("unmarshal -> Signal: %v", err)
	}
	got := typed.AsMap()
	want := canonicalize(t, orig)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round-trip diff\nwant=%#v\n got=%#v", want, got)
	}
}

func TestSignalShape_BuilderParity(t *testing.T) {
	b := signal.NewBuilder("demo", "0.1.0").
		WithVerdict("pass", "info").
		WithKind("aggregate").
		WithRationale("smoke fixture").
		WithLatencyMS(12).
		WithRunID(
			"00000000-0000-4000-8000-000000000000",
			"2026-05-12T12:00:00Z",
			"2026-05-12T12:00:01Z",
		)
	asMap := b.Build()
	asTypedMap := b.BuildTyped().AsMap()
	if !reflect.DeepEqual(canonicalize(t, asMap), canonicalize(t, asTypedMap)) {
		t.Fatalf("Build vs BuildTyped diff\nmap=%#v\n typed=%#v", asMap, asTypedMap)
	}
}
