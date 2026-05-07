// Package schema compiles the harness sensor and signal JSON Schemas
// (Draft 2020-12) and exposes a Validator that checks instances against
// either schema. Schemas are loaded from a directory containing
// sensor.json and signal.json; cross-file $ref is resolved at compile
// time so callers do not need to know about the underlying compiler.
package schema

import (
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
		return v.sensor.Validate(instance)
	case TargetSignal:
		return v.signal.Validate(instance)
	default:
		return fmt.Errorf("unknown target %q", target)
	}
}
