// Package lib holds the harness primitives shared by every script: schema
// validation, sensor envelope construction, path resolution, exit-code
// mapping, template rendering, regex pattern matching, subprocess streaming,
// and aggregate-verdict computation. Scripts under skills/<skill>/scripts/
// import this package; they themselves stay skill-local.
package lib

import (
	"errors"
	"fmt"
	"io"
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

// FindSchemasDir walks up from start looking for schemas/sensor.json + schemas/signal.json.
func FindSchemasDir(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(abs, "schemas")
		if hasFile(filepath.Join(candidate, "sensor.json")) && hasFile(filepath.Join(candidate, "signal.json")) {
			return candidate, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("schemas directory not found by walking up from %s", start)
		}
		abs = parent
	}
}

func hasFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// PrintValidationError writes an indented rendering of the error tree.
func PrintValidationError(w io.Writer, err *jsonschema.ValidationError, indent string) {
	path := err.InstanceLocation
	if path == "" {
		path = "<root>"
	}
	fmt.Fprintf(w, "%sINVALID at %s: %s\n", indent, path, err.Message)
	for _, c := range err.Causes {
		PrintValidationError(w, c, indent+"  ")
	}
}

// PrintValidationOrPlain prints an indented validation tree if err is a
// jsonschema.ValidationError; otherwise it prints err.Error().
func PrintValidationOrPlain(err error, stderr io.Writer) {
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		PrintValidationError(stderr, ve, "")
	} else {
		fmt.Fprintln(stderr, "INVALID:", err)
	}
}

// LoadValidator resolves schemasDir (walks up if empty) and returns a Validator.
// Returns (nil, exit code) on failure, with the message already printed to stderr.
func LoadValidator(schemasDir string, stderr io.Writer) (*Validator, int) {
	if schemasDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, "error: getwd:", err)
			return nil, 2
		}
		d, err := FindSchemasDir(cwd)
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return nil, 2
		}
		schemasDir = d
	}
	v, err := NewValidator(schemasDir)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return nil, 2
	}
	return v, 0
}
