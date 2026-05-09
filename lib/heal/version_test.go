// lib/heal/version_test.go
package heal_test

import (
	"encoding/json"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestBumpPatch_Simple(t *testing.T) {
	in := map[string]interface{}{"id": "x", "version": "0.1.0"}
	body, _ := json.Marshal(in)
	out, err := heal.BumpPatch(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	json.Unmarshal(out, &got)
	if got["version"] != "0.1.1" {
		t.Fatalf("version = %v", got["version"])
	}
}

func TestBumpPatch_DoubleDigit(t *testing.T) {
	in := map[string]interface{}{"id": "x", "version": "1.10.99"}
	body, _ := json.Marshal(in)
	out, err := heal.BumpPatch(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	json.Unmarshal(out, &got)
	if got["version"] != "1.10.100" {
		t.Fatalf("version = %v", got["version"])
	}
}

func TestBumpPatch_Malformed(t *testing.T) {
	for _, in := range []map[string]interface{}{
		{"id": "x", "version": "0.1"},
		{"id": "x", "version": "alpha"},
		{"id": "x"},
	} {
		body, _ := json.Marshal(in)
		if _, err := heal.BumpPatch(body); err == nil {
			t.Errorf("expected error for %v", in)
		}
	}
}
