package schema

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

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
