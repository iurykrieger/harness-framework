//go:build write_stack

// Command write-stack reads a draft stack payload (JSON or YAML),
// validates it against schemas/stack (including the library cross-checks
// in lib/stack), and persists it to <project-root>/.harness/stack.yaml.
//
// Usage:
//
//	go run -tags=write_stack ./skills/detect-sensors/scripts \
//	  --out=<project-root> [--schemas-dir=<dir>] <stack-payload>
//
// Exit codes: 0 stack written, 1 schema/cross-check failure,
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
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-stack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var outDir, schemasDir string
	fs.StringVar(&outDir, "out", "", "project root to write .harness/stack.yaml into (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if outDir == "" {
		fmt.Fprintln(stderr, "error: --out is required")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: write-stack --out=PROJECT_ROOT [--schemas-dir=DIR] <stack-payload>")
		return 2
	}
	payloadPath := fs.Arg(0)

	body, err := schema.ReadAsJSON(payloadPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return 2
	}

	path, err := stack.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		var cce *stack.CrossCheckError
		if errors.As(err, &cce) {
			fmt.Fprintf(stderr, "error: stack_%s: %s\n", cce.Kind, cce.Message)
			return 1
		}
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	fmt.Fprintln(stdout, path)
	return 0
}
