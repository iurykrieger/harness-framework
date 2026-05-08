//go:build run_computational

// Command run-computational runs a streaming computational sensor end-to-end,
// resolving and executing its depends_on graph in topological order.
//
// Usage:
//
//	go run -tags=run_computational ./skills/run-sensor/scripts <sensor-path>
//
// Stdout is JSONL: every dep's aggregate Signal first, then the requested
// sensor's individual Signals (one per matched output line), terminated by
// the requested sensor's aggregate Signal as the LAST line. Exit codes:
// 0 ok (Signals printed), 1 schema/DAG failure, 2 usage or I/O error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/sensor"
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

	cwd, _ := os.Getwd()
	abs, err := sensor.ResolveSensorPath(rest[0], cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return 2
	}
	return orchestrator.RunWithDeps(context.Background(), abs, schemasDir, stdout, stderr)
}
