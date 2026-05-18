package fixture_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestDeriveFromContract_JSONSchema(t *testing.T) {
	abs, err := filepath.Abs("testdata/contract/json_schema/order.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := fixture.DeriveFromContract(fixture.Hint{Role: "trigger"}, fixture.SourceJSONSchema, abs)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if s == nil {
		t.Fatal("nil sample")
	}
	if s.Source != "contract" {
		t.Errorf("Source = %q, want contract", s.Source)
	}
	if s.Ext != "json" {
		t.Errorf("Ext = %q, want json", s.Ext)
	}
	var payload map[string]any
	if err := json.Unmarshal(s.Payload, &payload); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	// Required fields present with zero values typed by schema.
	if got, ok := payload["sku"].(string); !ok || got != "" {
		t.Errorf("sku = %v, want \"\"", payload["sku"])
	}
	if got, ok := payload["qty"].(float64); !ok || got != 0 {
		t.Errorf("qty = %v, want 0", payload["qty"])
	}
	// Optional fields omitted.
	if _, has := payload["tags"]; has {
		t.Errorf("optional field tags should be omitted, got %v", payload["tags"])
	}
	if len(s.BlindSpots) == 0 {
		t.Error("expected at least one BlindSpot entry")
	}
}

func TestDeriveFromContract_OpenAPI(t *testing.T) {
	abs, err := filepath.Abs("testdata/contract/openapi/api.yaml")
	if err != nil {
		t.Fatal(err)
	}
	decl := abs + "#/components/schemas/Order"
	s, err := fixture.DeriveFromContract(fixture.Hint{Role: "trigger"}, fixture.SourceOpenAPI, decl)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(s.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["sku"].(string); !ok {
		t.Errorf("sku missing/wrong type: %v", payload["sku"])
	}
	if _, has := payload["tags"]; has {
		t.Error("optional field tags should be omitted")
	}
}

func TestDeriveFromContract_UnsupportedSource(t *testing.T) {
	_, err := fixture.DeriveFromContract(fixture.Hint{Role: "trigger"}, fixture.SourceKind("go-struct"), "/dev/null")
	if !errors.Is(err, fixture.ErrUnsupportedContractSource) {
		t.Fatalf("err = %v, want ErrUnsupportedContractSource", err)
	}
}

func TestDeriveFromContract_Avro(t *testing.T) {
	abs, _ := filepath.Abs("testdata/contract/avro/order.avsc")
	s, err := fixture.DeriveFromContract(fixture.Hint{Role: "event"}, fixture.SourceAvro, abs)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(s.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if got, ok := payload["sku"].(string); !ok || got != "" {
		t.Errorf("sku = %v want \"\"", payload["sku"])
	}
	if got, ok := payload["qty"].(float64); !ok || got != 0 {
		t.Errorf("qty = %v want 0", payload["qty"])
	}
	if got, ok := payload["channel"].(string); !ok || got != "WEB" {
		t.Errorf("channel = %v want WEB (first declared symbol)", payload["channel"])
	}
}

func TestDeriveFromContract_Protobuf(t *testing.T) {
	abs, _ := filepath.Abs("testdata/contract/protobuf/order.proto")
	decl := abs + ":shop.Order"
	s, err := fixture.DeriveFromContract(fixture.Hint{Role: "event"}, fixture.SourceProtobuf, decl)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(s.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["sku"].(string); !ok {
		t.Errorf("sku missing/wrong type: %v", payload["sku"])
	}
	// proto3 scalars: all zero. Repeated: empty list.
	if got, ok := payload["tags"].([]any); !ok || len(got) != 0 {
		t.Errorf("tags = %v want []", payload["tags"])
	}
	// Enum: first declared symbol.
	if got, ok := payload["channel"].(string); !ok || got != "CHANNEL_UNSPECIFIED" {
		t.Errorf("channel = %v want CHANNEL_UNSPECIFIED", payload["channel"])
	}
}
