package lib

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// MultiFlag implements flag.Value for repeatable string flags (--slot k=v --slot k2=v2).
type MultiFlag []string

func (m *MultiFlag) String() string     { return strings.Join(*m, ",") }
func (m *MultiFlag) Set(s string) error { *m = append(*m, s); return nil }

// LoadAndValidateSensor resolves the path argument, reads, parses, and
// schema-validates the sensor JSON against the sensor schema. Returns sensor,
// abs path, exit code (0 on success).
func LoadAndValidateSensor(arg, schemasDir string, stderr io.Writer) (map[string]interface{}, string, int) {
	cwd, _ := os.Getwd()
	sensorPath, err := ResolveSensorPath(arg, cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return nil, "", 2
	}
	v, code := LoadValidator(schemasDir, stderr)
	if code != 0 {
		return nil, "", code
	}
	var sensor map[string]interface{}
	if code := readJSONFile(sensorPath, &sensor, stderr); code != 0 {
		return nil, "", code
	}
	if err := v.Validate(TargetSensor, sensor); err != nil {
		PrintValidationOrPlain(err, stderr)
		return nil, "", 1
	}
	return sensor, sensorPath, 0
}

func readJSONFile(path string, dst interface{}, stderr io.Writer) int {
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return 2
	}
	if err := json.Unmarshal(b, dst); err != nil {
		fmt.Fprintln(stderr, "error: parse:", err)
		return 2
	}
	return 0
}
