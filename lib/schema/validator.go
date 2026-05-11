// Package schema compiles the harness sensor and signal JSON Schemas
// (Draft 2020-12) and exposes a Validator that checks instances against
// either schema. Schemas are loaded from a directory containing
// sensor.json and signal.json; cross-file $ref is resolved at compile
// time so callers do not need to know about the underlying compiler.
package schema

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	schemaBaseURL = "https://harness-framework/schemas/"
	sensorURL     = schemaBaseURL + "sensor.json"
	signalURL     = schemaBaseURL + "signal.json"
)

// Target identifies which schema an instance is checked against.
type Target string

const (
	TargetSensor Target = "sensor"
	TargetSignal Target = "signal"
)

// Validator holds the compiled sensor and signal schemas with cross-file
// $ref already resolved.
type Validator struct {
	sensor *jsonschema.Schema
	signal *jsonschema.Schema
}

// NewValidator loads sensor.json and signal.json from schemasDir.
func NewValidator(schemasDir string) (*Validator, error) {
	sensorBytes, err := os.ReadFile(filepath.Join(schemasDir, "sensor.json"))
	if err != nil {
		return nil, fmt.Errorf("read sensor.json: %w", err)
	}
	signalBytes, err := os.ReadFile(filepath.Join(schemasDir, "signal.json"))
	if err != nil {
		return nil, fmt.Errorf("read signal.json: %w", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(signalURL, strings.NewReader(string(signalBytes))); err != nil {
		return nil, fmt.Errorf("register signal schema: %w", err)
	}
	if err := c.AddResource(sensorURL, strings.NewReader(string(sensorBytes))); err != nil {
		return nil, fmt.Errorf("register sensor schema: %w", err)
	}
	sensor, err := c.Compile(sensorURL)
	if err != nil {
		return nil, fmt.Errorf("compile sensor schema: %w", err)
	}
	signal, err := c.Compile(signalURL)
	if err != nil {
		return nil, fmt.Errorf("compile signal schema: %w", err)
	}
	return &Validator{sensor: sensor, signal: signal}, nil
}

// Validate runs the schema for target against instance.
func (v *Validator) Validate(target Target, instance interface{}) error {
	switch target {
	case TargetSensor:
		raw, _ := json.Marshal(instance)
		if fields, ok := detectLegacyShape(raw); ok {
			id := "<unknown>"
			if m, mok := instance.(map[string]interface{}); mok {
				if s, sok := m["id"].(string); sok {
					id = s
				}
			}
			return fmt.Errorf(
				"sensor %s uses v1 schema fields (%s).\nRun `go run ./scripts/migrate-requires.go <path>` to upgrade to v2.",
				id, strings.Join(fields, ", "),
			)
		}
		if idx, k, ok := detectUnknownKind(raw); ok {
			id := "<unknown>"
			if m, mok := instance.(map[string]interface{}); mok {
				if s, sok := m["id"].(string); sok {
					id = s
				}
			}
			return fmt.Errorf(
				"sensor %s requires[%d] has unknown kind %q. Valid kinds: sensor, tool, env, context, permission, step.",
				id, idx, k,
			)
		}
		return v.sensor.Validate(instance)
	case TargetSignal:
		return v.signal.Validate(instance)
	default:
		return fmt.Errorf("unknown target %q", target)
	}
}

// detectLegacyShape returns the names of v1 schema fields present in the
// raw sensor JSON. ok is true when at least one is found. Used by Validate
// to short-circuit with an actionable migration message that points at
// scripts/migrate-requires.go even after the v1 fields are removed from
// the schema (since the JSON Schema rejection message for an unknown
// top-level property is opaque).
func detectLegacyShape(raw []byte) ([]string, bool) {
	var s map[string]interface{}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	var found []string
	if _, ok := s["depends_on"]; ok {
		found = append(found, "depends_on")
	}
	if _, ok := s["requires"].(map[string]interface{}); ok {
		found = append(found, "requires (object)")
	}
	if exec, ok := s["execution"].(map[string]interface{}); ok {
		if _, ok := exec["prepare"]; ok {
			found = append(found, "execution.prepare")
		}
	}
	return found, len(found) > 0
}

// detectUnknownKind returns the index and value of the first requires[]
// entry whose kind is not one of the six known kinds. ok is true when
// found. Translates the opaque "oneOf failed: 0 of 6 schemas matched"
// rejection into an actionable error message.
func detectUnknownKind(raw []byte) (int, string, bool) {
	var s map[string]interface{}
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, "", false
	}
	arr, ok := s["requires"].([]interface{})
	if !ok {
		return 0, "", false
	}
	known := map[string]bool{
		"sensor": true, "tool": true, "env": true,
		"context": true, "permission": true, "step": true,
	}
	for i, raw := range arr {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		k, ok := item["kind"].(string)
		if !ok {
			continue
		}
		if !known[k] {
			return i, k, true
		}
	}
	return 0, "", false
}
