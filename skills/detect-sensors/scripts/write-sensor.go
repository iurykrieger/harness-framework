// Command write-sensor reads a draft sensor JSON file, validates it
// against schemas/sensor.json (Draft 2020-12, with cross-file $ref to
// signal.json), and writes the canonicalised JSON to <out>/<id>.json.
//
// The skill's detection logic is intentionally LLM-driven (see
// skills/detect-sensors/SKILL.md): no project archetype, capability list,
// or command template is hardcoded here. This script is the one
// deterministic step in that loop — it makes sure every persisted sensor
// is well-formed before /run-sensor ever sees it.
//
// Usage:
//
//	go run ./skills/detect-sensors/scripts \
//	  --out=<dir> [--schemas-dir=<dir>] <draft-sensor.json>
//
// Exit codes: 0 sensor written, 1 schema validation failed,
// 2 usage or I/O error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-sensor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var outDir, schemasDir string
	fs.StringVar(&outDir, "out", "", "directory to write the sensor file into (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if outDir == "" {
		fmt.Fprintln(stderr, "error: --out is required")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: write-sensor --out=DIR [--schemas-dir=DIR] <draft-sensor.json>")
		return 2
	}
	draftPath := fs.Arg(0)

	sensor, code := readSensorFile(draftPath, stderr)
	if code != 0 {
		return code
	}

	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}
	if err := v.Validate(schema.TargetSensor, sensor); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return 1
	}

	id, ok := sensor["id"].(string)
	if !ok || id == "" {
		// The schema's id pattern guarantees this branch is unreachable,
		// but keep a clear error in case schema and code drift apart.
		fmt.Fprintln(stderr, "error: sensor.id missing or empty after validation")
		return 1
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintln(stderr, "error: mkdir:", err)
		return 2
	}
	outPath := filepath.Join(outDir, id+".json")
	if err := writeCanonical(outPath, sensor); err != nil {
		fmt.Fprintln(stderr, "error: write:", err)
		return 2
	}

	abs, err := filepath.Abs(outPath)
	if err != nil {
		abs = outPath
	}
	fmt.Fprintln(stdout, abs)
	return 0
}

func readSensorFile(path string, stderr io.Writer) (map[string]interface{}, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return nil, 2
	}
	var sensor map[string]interface{}
	if err := json.Unmarshal(data, &sensor); err != nil {
		fmt.Fprintln(stderr, "error: parse sensor JSON:", err)
		return nil, 2
	}
	return sensor, 0
}

func writeCanonical(path string, sensor map[string]interface{}) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sensor); err != nil {
		return err
	}
	return nil
}
