// Command write-sensor reads a draft sensor JSON file and persists it
// via lib/sensor.ValidateAndPersist (validate against schemas + atomic
// write). Thin CLI wrapper around the shared primitive.
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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
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

	body, err := os.ReadFile(draftPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return 2
	}

	path, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		// Anything else (parse error, I/O, schema-load failure): exit 2.
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	fmt.Fprintln(stdout, path)
	return 0
}
