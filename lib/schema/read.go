package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// ReadAsJSON reads a file at path and returns its contents as JSON bytes.
// For .yaml and .yml files it parses the YAML and converts to JSON via
// sigs.k8s.io/yaml; for .json files (and any other extension) it returns
// the raw bytes unchanged. Used as the canonical entry point for any
// authored harness artifact (sensors, use cases, stacks, drafts).
// Format is detected purely by file extension; a JSON document misnamed
// with .yaml/.yml will be parsed as YAML and re-serialized.
func ReadAsJSON(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		js, err := yaml.YAMLToJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("parse YAML %s: %w", path, err)
		}
		return js, nil
	default:
		return raw, nil
	}
}
