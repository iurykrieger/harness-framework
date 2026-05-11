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
