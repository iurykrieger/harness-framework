// Package schema internal tests for the legacy-shape sniff helpers.
// These tests live in package schema (not schema_test) so they can call
// the unexported detectLegacyShape and detectUnknownKind directly.
package schema

import "testing"

func TestDetectLegacyShape_DependsOn(t *testing.T) {
	fields, ok := detectLegacyShape([]byte(`{"id":"x","depends_on":["a"]}`))
	if !ok || len(fields) == 0 || fields[0] != "depends_on" {
		t.Fatalf("expected depends_on detected, got %v / ok=%v", fields, ok)
	}
}

func TestDetectLegacyShape_RequiresObject(t *testing.T) {
	fields, ok := detectLegacyShape([]byte(`{"id":"x","requires":{"tools":["docker"]}}`))
	if !ok || len(fields) == 0 || fields[0] != "requires (object)" {
		t.Fatalf("expected requires object detected, got %v / ok=%v", fields, ok)
	}
}

func TestDetectLegacyShape_ExecutionPrepare(t *testing.T) {
	fields, ok := detectLegacyShape([]byte(`{"id":"x","execution":{"prepare":[{"command":"true"}]}}`))
	if !ok || len(fields) == 0 || fields[0] != "execution.prepare" {
		t.Fatalf("expected execution.prepare detected, got %v / ok=%v", fields, ok)
	}
}

func TestDetectLegacyShape_V2Clean(t *testing.T) {
	fields, ok := detectLegacyShape([]byte(`{"id":"x","requires":[{"kind":"sensor","id":"a"}]}`))
	if ok || len(fields) > 0 {
		t.Fatalf("expected no legacy detection, got %v / ok=%v", fields, ok)
	}
}

func TestDetectUnknownKind(t *testing.T) {
	idx, kind, ok := detectUnknownKind([]byte(`{"requires":[{"kind":"foobar"}]}`))
	if !ok || idx != 0 || kind != "foobar" {
		t.Fatalf("expected unknown kind=foobar at index 0, got idx=%d kind=%q ok=%v", idx, kind, ok)
	}
}

func TestDetectUnknownKind_AllKnown(t *testing.T) {
	_, _, ok := detectUnknownKind([]byte(`{"requires":[{"kind":"sensor","id":"a"}]}`))
	if ok {
		t.Fatal("expected no unknown kind for valid input")
	}
}
