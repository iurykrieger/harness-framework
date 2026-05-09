package sensor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ResolveSensorPath strips a leading @, makes the path absolute (relative to
// baseDir), and verifies the file exists.
func ResolveSensorPath(arg, baseDir string) (string, error) {
	arg = strings.TrimPrefix(arg, "@")
	if arg == "" {
		return "", errors.New("empty path")
	}
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(baseDir, arg)
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// idRegex matches the sensor.id shape required by schemas/sensor.json:
// lowercase letters/digits/dashes, must start with a letter.
var idRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ResolveByID resolves a bare sensor id to its on-disk path under
// <baseDir>/sensors/<id>.json. The id MUST match the schema's id pattern
// to prevent path traversal via "../foo" or absolute-path inputs.
func ResolveByID(id, baseDir string) (string, error) {
	if id == "" {
		return "", errors.New("empty sensor id")
	}
	if !idRegex.MatchString(id) {
		return "", fmt.Errorf("sensor id %q does not match ^[a-z][a-z0-9-]*$", id)
	}
	path := filepath.Join(baseDir, "sensors", id+".json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("sensor %q: %w", id, err)
	}
	return path, nil
}
