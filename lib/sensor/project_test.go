package sensor

import "testing"

func TestProject_V2Array_AllKinds(t *testing.T) {
	s := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "sensor", "id": "a"},
			map[string]interface{}{"kind": "tool", "name": "docker"},
			map[string]interface{}{"kind": "env", "name": "X"},
			map[string]interface{}{"kind": "context", "path": "docs/"},
			map[string]interface{}{"kind": "permission", "scope": "repo:read"},
			map[string]interface{}{"kind": "step", "command": "true"},
		},
	}
	for _, kind := range []string{"sensor", "tool", "env", "context", "permission", "step"} {
		got := Project(s, kind)
		if len(got) != 1 {
			t.Fatalf("kind=%s: expected 1 item, got %d (%#v)", kind, len(got), got)
		}
		if got[0]["kind"] != kind {
			t.Fatalf("kind=%s: got kind=%v", kind, got[0]["kind"])
		}
	}
}

func TestProject_V2Array_PreservesOrder(t *testing.T) {
	s := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "step", "command": "first"},
			map[string]interface{}{"kind": "step", "command": "second"},
		},
	}
	got := Project(s, "step")
	if len(got) != 2 || got[0]["command"] != "first" || got[1]["command"] != "second" {
		t.Fatalf("order not preserved: %#v", got)
	}
}

func TestProject_V2Array_SkipsMalformed(t *testing.T) {
	s := map[string]interface{}{
		"requires": []interface{}{
			"not-an-object",
			map[string]interface{}{},
			map[string]interface{}{"kind": 123},
			map[string]interface{}{"kind": "sensor", "id": "a"},
		},
	}
	got := Project(s, "sensor")
	if len(got) != 1 || got[0]["id"] != "a" {
		t.Fatalf("expected only the well-formed sensor entry, got: %#v", got)
	}
}

func TestProject_EmptyAndMissing(t *testing.T) {
	t.Run("missing requires key", func(t *testing.T) {
		if got := Project(map[string]interface{}{}, "sensor"); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})
	t.Run("empty requires array", func(t *testing.T) {
		if got := Project(map[string]interface{}{"requires": []interface{}{}}, "sensor"); got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})
}

func TestProject_V1Fallback_DependsOn(t *testing.T) {
	s := map[string]interface{}{
		"depends_on": []interface{}{"a", "b"},
	}
	got := Project(s, "sensor")
	if len(got) != 2 || got[0]["id"] != "a" || got[1]["id"] != "b" {
		t.Fatalf("v1 depends_on fallback failed: %#v", got)
	}
}

func TestProject_V1Fallback_RequiresObject(t *testing.T) {
	s := map[string]interface{}{
		"requires": map[string]interface{}{
			"env": []interface{}{
				map[string]interface{}{"name": "GH_TOKEN", "description": "PAT", "optional": false},
			},
			"tools":       []interface{}{"docker"},
			"context":     []interface{}{"docs/"},
			"permissions": []interface{}{"repo:read"},
		},
	}
	if got := Project(s, "env"); len(got) != 1 || got[0]["name"] != "GH_TOKEN" {
		t.Fatalf("env fallback: %#v", got)
	}
	if got := Project(s, "tool"); len(got) != 1 || got[0]["name"] != "docker" {
		t.Fatalf("tool fallback: %#v", got)
	}
	if got := Project(s, "context"); len(got) != 1 || got[0]["path"] != "docs/" {
		t.Fatalf("context fallback: %#v", got)
	}
	if got := Project(s, "permission"); len(got) != 1 || got[0]["scope"] != "repo:read" {
		t.Fatalf("permission fallback: %#v", got)
	}
}

func TestProject_V1Fallback_ExecutionPrepare(t *testing.T) {
	s := map[string]interface{}{
		"execution": map[string]interface{}{
			"prepare": []interface{}{
				map[string]interface{}{"command": "cp .env.example .env", "timeout_ms": float64(1000)},
			},
		},
	}
	got := Project(s, "step")
	if len(got) != 1 || got[0]["command"] != "cp .env.example .env" {
		t.Fatalf("prepare fallback: %#v", got)
	}
}

func TestProject_V1Fallback_SkipsMalformedEnv(t *testing.T) {
	s := map[string]interface{}{
		"requires": map[string]interface{}{
			"env": []interface{}{
				"not-a-map",
				map[string]interface{}{"name": "GH_TOKEN"},
				42,
				map[string]interface{}{"name": "GCP_PROJECT", "description": "id"},
			},
		},
	}
	got := Project(s, "env")
	if len(got) != 2 {
		t.Fatalf("expected 2 well-formed env entries, got %d: %#v", len(got), got)
	}
	if got[0]["name"] != "GH_TOKEN" || got[1]["name"] != "GCP_PROJECT" {
		t.Fatalf("env entries not preserved: %#v", got)
	}
}

func TestProject_V1Fallback_CombinedDoesNotDuplicate(t *testing.T) {
	s := map[string]interface{}{
		"depends_on": []interface{}{"a"},
		"requires": map[string]interface{}{
			"tools": []interface{}{"docker"},
		},
		"execution": map[string]interface{}{
			"prepare": []interface{}{
				map[string]interface{}{"command": "true"},
			},
		},
	}
	if got := Project(s, "sensor"); len(got) != 1 {
		t.Fatalf("sensor: %#v", got)
	}
	if got := Project(s, "tool"); len(got) != 1 {
		t.Fatalf("tool: %#v", got)
	}
	if got := Project(s, "step"); len(got) != 1 {
		t.Fatalf("step: %#v", got)
	}
	if got := Project(s, "env"); got != nil {
		t.Fatalf("env: expected nil, got %#v", got)
	}
}

func TestProject_UnknownKindReturnsNil(t *testing.T) {
	s := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "sensor", "id": "a"},
		},
	}
	if got := Project(s, "tool"); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}
