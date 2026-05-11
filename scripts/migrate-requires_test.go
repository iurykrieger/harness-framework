package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestConvert_FullV1Sensor(t *testing.T) {
	v1, _ := json.Marshal(map[string]interface{}{
		"id":         "ex",
		"version":    "1.0.0",
		"depends_on": []string{"dep-a", "dep-b"},
		"requires": map[string]interface{}{
			"tools":       []string{"docker"},
			"permissions": []string{"repo:read"},
			"context":     []string{"docs/"},
			"env": []map[string]interface{}{
				{"name": "GH_TOKEN", "description": "PAT", "optional": false},
			},
		},
		"execution": map[string]interface{}{
			"command": "true",
			"prepare": []map[string]interface{}{
				{"command": "cp .env.example .env", "timeout_ms": 1000},
			},
		},
	})
	out, changed, err := convert(v1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true for full v1 input")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["depends_on"]; present {
		t.Fatal("depends_on should be removed")
	}
	if exec, _ := got["execution"].(map[string]interface{}); exec != nil {
		if _, present := exec["prepare"]; present {
			t.Fatal("execution.prepare should be removed")
		}
	}
	requires, ok := got["requires"].([]interface{})
	if !ok {
		t.Fatalf("requires should be an array, got %T", got["requires"])
	}
	wantKinds := []string{"sensor", "sensor", "tool", "env", "context", "permission", "step"}
	if len(requires) != len(wantKinds) {
		t.Fatalf("expected %d entries, got %d", len(wantKinds), len(requires))
	}
	for i, kind := range wantKinds {
		entry := requires[i].(map[string]interface{})
		if entry["kind"] != kind {
			t.Fatalf("entry %d: expected kind=%s, got %s", i, kind, entry["kind"])
		}
	}
	if got["version"] != "1.0.1" {
		t.Fatalf("expected version bump to 1.0.1, got %v", got["version"])
	}
}

func TestConvert_AlreadyV2_NoChange(t *testing.T) {
	v2, _ := json.Marshal(map[string]interface{}{
		"id":      "ex",
		"version": "1.0.0",
		"requires": []map[string]interface{}{
			{"kind": "sensor", "id": "dep-a"},
		},
	})
	out, changed, err := convert(v2)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected changed=false for already-v2 input")
	}
	if !bytes.Equal(out, v2) {
		t.Fatal("v2 input should be returned bit-identical")
	}
}

func TestConvert_PartiallyMigrated_Fails(t *testing.T) {
	mixed, _ := json.Marshal(map[string]interface{}{
		"id":         "ex",
		"version":    "1.0.0",
		"depends_on": []string{"a"},
		"requires":   []map[string]interface{}{{"kind": "sensor", "id": "b"}},
	})
	_, _, err := convert(mixed)
	if err == nil {
		t.Fatal("expected error for sensor with both depends_on and v2 requires[]")
	}
}

func TestConvert_StepDeduplicationForbidden(t *testing.T) {
	v1, _ := json.Marshal(map[string]interface{}{
		"id":      "ex",
		"version": "1.0.0",
		"execution": map[string]interface{}{
			"prepare": []map[string]interface{}{
				{"command": "echo same"},
				{"command": "echo same"},
			},
		},
	})
	out, _, err := convert(v1)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(out, &got)
	requires := got["requires"].([]interface{})
	if len(requires) != 2 {
		t.Fatalf("step entries must not dedupe, got %d", len(requires))
	}
}

func TestConvert_EmptyV1_ProducesNoRequiresOrEmptyArray(t *testing.T) {
	v1, _ := json.Marshal(map[string]interface{}{
		"id":      "ex",
		"version": "1.0.0",
	})
	out, changed, err := convert(v1)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected changed=false when there is nothing to migrate")
	}
	var got map[string]interface{}
	_ = json.Unmarshal(out, &got)
	if r, ok := got["requires"]; ok {
		if arr, ok := r.([]interface{}); !ok || len(arr) != 0 {
			t.Fatalf("if requires is present it must be empty array, got %v", r)
		}
	}
}

func TestConvert_EmptyDependsOnAndPrepare_ProducesEmptyArray(t *testing.T) {
	v1, _ := json.Marshal(map[string]interface{}{
		"id":         "ex",
		"version":    "1.0.0",
		"depends_on": []string{},
		"execution": map[string]interface{}{
			"prepare": []map[string]interface{}{},
		},
	})
	out, changed, err := convert(v1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true (we stripped depends_on and prepare)")
	}
	var got map[string]interface{}
	_ = json.Unmarshal(out, &got)
	arr, ok := got["requires"].([]interface{})
	if !ok || len(arr) != 0 {
		t.Fatalf("expected requires: [], got %v", got["requires"])
	}
}

func TestBumpPatch(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"1.0.0", "1.0.1"},
		{"0.6.3", "0.6.4"},
		{"2.10.99", "2.10.100"},
	}
	for _, tc := range tests {
		got := bumpPatch(tc.in)
		if got != tc.want {
			t.Fatalf("bumpPatch(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
