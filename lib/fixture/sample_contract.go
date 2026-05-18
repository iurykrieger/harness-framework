package fixture

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

// SourceKind identifies a tier-2 contract source. The set is closed by
// design — cross-language source-code AST parsing is out of scope.
type SourceKind string

const (
	SourceJSONSchema SourceKind = "json-schema"
	SourceOpenAPI    SourceKind = "openapi-component"
	SourceAvro       SourceKind = "avro"
	SourceProtobuf   SourceKind = "protobuf"
)

// ErrUnsupportedContractSource is returned by DeriveFromContract when
// src is not one of the four supported SourceKind values.
var ErrUnsupportedContractSource = errors.New("fixture: unsupported contract source")

// DeriveFromContract reads the contract declaration at declPath and
// emits the minimum valid JSON payload for it. Required fields are
// populated with zero values typed by the declared kind; optional
// fields are omitted; enums pick the first declared value.
//
// declPath shape per source:
//
//	json-schema       : absolute path to the .json/.schema.json file
//	openapi-component : "<absolute-openapi-file>#/components/schemas/<Name>"
//	avro              : absolute path to the .avsc file
//	protobuf          : "<absolute-proto-file>:<MessageName>"
func DeriveFromContract(h Hint, src SourceKind, declPath string) (*Sample, error) {
	switch src {
	case SourceJSONSchema:
		return deriveFromJSONSchema(declPath)
	case SourceOpenAPI:
		return deriveFromOpenAPI(declPath)
	case SourceAvro:
		return deriveFromAvro(declPath)
	case SourceProtobuf:
		return deriveFromProtobuf(declPath)
	default:
		return nil, ErrUnsupportedContractSource
	}
}

func deriveFromJSONSchema(path string) (*Sample, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read json-schema: %w", err)
	}
	var schema jsonSchemaNode
	if err := json.Unmarshal(body, &schema); err != nil {
		return nil, fmt.Errorf("parse json-schema: %w", err)
	}
	payload := emitFromJSONSchema(&schema)
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Sample{
		Payload: out,
		Ext:     "json",
		Source:  "contract",
		BlindSpots: []string{
			fmt.Sprintf("Derived from json-schema contract at %s; no real sample on disk near the entry point.", path),
		},
	}, nil
}

func deriveFromOpenAPI(declPath string) (*Sample, error) {
	file, frag, ok := strings.Cut(declPath, "#")
	if !ok || !strings.HasPrefix(frag, "/components/schemas/") {
		return nil, fmt.Errorf("openapi declPath must be '<file>#/components/schemas/<Name>', got %q", declPath)
	}
	name := strings.TrimPrefix(frag, "/components/schemas/")
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read openapi file: %w", err)
	}
	asJSON, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("convert openapi yaml: %w", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(asJSON, &doc); err != nil {
		return nil, fmt.Errorf("parse openapi: %w", err)
	}
	schemaBody, ok := doc.Components.Schemas[name]
	if !ok {
		return nil, fmt.Errorf("openapi component %q not found in %s", name, file)
	}
	var schema jsonSchemaNode
	if err := json.Unmarshal(schemaBody, &schema); err != nil {
		return nil, fmt.Errorf("parse openapi component %q: %w", name, err)
	}
	payload := emitFromJSONSchema(&schema)
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Sample{
		Payload: out,
		Ext:     "json",
		Source:  "contract",
		BlindSpots: []string{
			fmt.Sprintf("Derived from openapi-component contract at %s; no real sample on disk near the entry point.", declPath),
		},
	}, nil
}
type avroField struct {
	Name string          `json:"name"`
	Type json.RawMessage `json:"type"`
}

type avroRecord struct {
	Type   string      `json:"type"`
	Name   string      `json:"name"`
	Fields []avroField `json:"fields"`
}

func deriveFromAvro(path string) (*Sample, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read avsc: %w", err)
	}
	var rec avroRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("parse avsc: %w", err)
	}
	if rec.Type != "record" {
		return nil, fmt.Errorf("avro: only top-level record types are supported (got %q)", rec.Type)
	}
	out := map[string]any{}
	for _, f := range rec.Fields {
		out[f.Name] = avroZeroValue(f.Type)
	}
	body, _ := json.Marshal(out)
	return &Sample{
		Payload: body,
		Ext:     "json",
		Source:  "contract",
		BlindSpots: []string{
			fmt.Sprintf("Derived from avro contract at %s; no real sample on disk near the entry point.", path),
		},
	}, nil
}

// avroZeroValue returns the zero value for an Avro type expression.
// Type expressions are one of:
//   - a string ("string", "int", "long", "float", "double", "boolean", "bytes", "null")
//   - an object: {"type":"array", "items": ...} / {"type":"enum", "symbols":[...]}
//   - a union: ["null", "string"] — picks the first non-null branch
func avroZeroValue(raw json.RawMessage) any {
	// Try string form first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return avroPrimitiveZero(s)
	}
	// Try array (union).
	var union []json.RawMessage
	if err := json.Unmarshal(raw, &union); err == nil {
		for _, branch := range union {
			var bs string
			if json.Unmarshal(branch, &bs) == nil && bs == "null" && len(union) > 1 {
				continue
			}
			return avroZeroValue(branch)
		}
		return nil
	}
	// Try object form.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	var typ string
	_ = json.Unmarshal(obj["type"], &typ)
	switch typ {
	case "array":
		return []any{}
	case "enum":
		var symbols []string
		_ = json.Unmarshal(obj["symbols"], &symbols)
		if len(symbols) > 0 {
			return symbols[0]
		}
		return ""
	case "record":
		var fields []avroField
		_ = json.Unmarshal(obj["fields"], &fields)
		inner := map[string]any{}
		for _, f := range fields {
			inner[f.Name] = avroZeroValue(f.Type)
		}
		return inner
	case "map":
		return map[string]any{}
	default:
		return nil
	}
}

func avroPrimitiveZero(t string) any {
	switch t {
	case "string", "bytes":
		return ""
	case "int", "long":
		return 0
	case "float", "double":
		return 0.0
	case "boolean":
		return false
	case "null":
		return nil
	}
	return nil
}
func deriveFromProtobuf(declPath string) (*Sample, error) {
	return nil, errors.New("protobuf: not implemented yet")
}

// jsonSchemaNode is the subset of Draft 2020-12 / Draft 7 we need:
// `type`, `required`, `properties`, `items`, `enum`.
type jsonSchemaNode struct {
	Type       any                       `json:"type"` // string or []string
	Required   []string                  `json:"required"`
	Properties map[string]jsonSchemaNode `json:"properties"`
	Items      *jsonSchemaNode           `json:"items"`
	Enum       []any                     `json:"enum"`
}

func emitFromJSONSchema(s *jsonSchemaNode) any {
	if len(s.Enum) > 0 {
		return s.Enum[0]
	}
	switch firstType(s.Type) {
	case "object":
		out := map[string]any{}
		for _, name := range s.Required {
			child, ok := s.Properties[name]
			if !ok {
				out[name] = nil
				continue
			}
			out[name] = emitFromJSONSchema(&child)
		}
		return out
	case "array":
		if s.Items == nil {
			return []any{}
		}
		return []any{emitFromJSONSchema(s.Items)}
	case "string":
		return ""
	case "integer", "number":
		return 0
	case "boolean":
		return false
	case "null":
		return nil
	default:
		return nil
	}
}

func firstType(t any) string {
	switch x := t.(type) {
	case string:
		return x
	case []any:
		if len(x) > 0 {
			if s, ok := x[0].(string); ok {
				return s
			}
		}
	}
	return ""
}
