package sensor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

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
	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return nil, "", code
	}
	var s map[string]interface{}
	if code := readJSONFile(sensorPath, &s, stderr); code != 0 {
		return nil, "", code
	}
	if err := v.Validate(schema.TargetSensor, s); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return nil, "", 1
	}
	return s, sensorPath, 0
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
