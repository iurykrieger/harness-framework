// Package schema compiles the harness sensor, signal, stack, and usecase
// JSON Schemas (Draft 2020-12) and exposes a Validator that checks
// instances against any of them. Schemas are loaded from a directory
// containing sensor.yaml, signal.yaml, stack.yaml, and usecase.yaml;
// cross-file $ref is resolved at compile time so callers do not need to
// know about the underlying compiler.
package schema

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	schemaBaseURL = "https://harness-framework/schemas/"
	sensorURL     = schemaBaseURL + "sensor.yaml"
	signalURL     = schemaBaseURL + "signal.yaml"
	stackURL      = schemaBaseURL + "stack.yaml"
	usecaseURL    = schemaBaseURL + "usecase.yaml"
)

// Target identifies which schema an instance is checked against.
type Target string

const (
	TargetSensor  Target = "sensor"
	TargetSignal  Target = "signal"
	TargetStack   Target = "stack"
	TargetUseCase Target = "usecase"
)

// Validator holds the compiled sensor, signal, stack, and usecase
// schemas with cross-file $ref already resolved.
type Validator struct {
	sensor  *jsonschema.Schema
	signal  *jsonschema.Schema
	stack   *jsonschema.Schema
	usecase *jsonschema.Schema
}

// NewValidator loads sensor.yaml, signal.yaml, stack.yaml, and usecase.yaml
// from schemasDir.
func NewValidator(schemasDir string) (*Validator, error) {
	sensorBytes, err := ReadAsJSON(filepath.Join(schemasDir, "sensor.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read sensor.yaml: %w", err)
	}
	signalBytes, err := ReadAsJSON(filepath.Join(schemasDir, "signal.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read signal.yaml: %w", err)
	}
	stackBytes, err := ReadAsJSON(filepath.Join(schemasDir, "stack.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read stack.yaml: %w", err)
	}
	usecaseBytes, err := ReadAsJSON(filepath.Join(schemasDir, "usecase.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read usecase.yaml: %w", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(signalURL, strings.NewReader(string(signalBytes))); err != nil {
		return nil, fmt.Errorf("register signal schema: %w", err)
	}
	if err := c.AddResource(sensorURL, strings.NewReader(string(sensorBytes))); err != nil {
		return nil, fmt.Errorf("register sensor schema: %w", err)
	}
	if err := c.AddResource(stackURL, strings.NewReader(string(stackBytes))); err != nil {
		return nil, fmt.Errorf("register stack schema: %w", err)
	}
	if err := c.AddResource(usecaseURL, strings.NewReader(string(usecaseBytes))); err != nil {
		return nil, fmt.Errorf("register usecase schema: %w", err)
	}
	sensor, err := c.Compile(sensorURL)
	if err != nil {
		return nil, fmt.Errorf("compile sensor schema: %w", err)
	}
	signal, err := c.Compile(signalURL)
	if err != nil {
		return nil, fmt.Errorf("compile signal schema: %w", err)
	}
	stack, err := c.Compile(stackURL)
	if err != nil {
		return nil, fmt.Errorf("compile stack schema: %w", err)
	}
	usecase, err := c.Compile(usecaseURL)
	if err != nil {
		return nil, fmt.Errorf("compile usecase schema: %w", err)
	}
	return &Validator{sensor: sensor, signal: signal, stack: stack, usecase: usecase}, nil
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
				"sensor %s uses v1 schema fields (%s). Upgrade to v2 schema manually (v1 migration is complete; no migration script remains).",
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
	case TargetStack:
		return v.stack.Validate(instance)
	case TargetUseCase:
		return v.usecase.Validate(instance)
	default:
		return fmt.Errorf("unknown target %q", target)
	}
}

// detectLegacyShape returns the names of v1 schema fields present in the
// raw sensor JSON. ok is true when at least one is found. Used by Validate
// to short-circuit with an actionable migration message even after the v1
// fields are removed from the schema (since the JSON Schema rejection
// message for an unknown top-level property is opaque).
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
