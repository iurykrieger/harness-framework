//go:build run_computational

// Command run-computational runs a computational sensor end-to-end and emits
// a Signal to stdout.
//
// Usage:
//
//	go run -tags=run_computational ./skills/run-sensor/scripts <sensor-path>
//
// Exit codes: 0 ok (Signal printed), 1 schema validation failed, 2 usage or
// I/O error.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/skills/run-sensor/scripts/lib"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run-computational", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemasDir string
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: run-computational [--schemas-dir=DIR] <sensor-path>")
		return 2
	}
	return lib.ExecuteComputational(rest[0], schemasDir, stdout, stderr)
}
