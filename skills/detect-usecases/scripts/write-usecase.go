//go:build write_usecase

// Command write-usecase reads a draft UseCase (JSON or YAML), validates
// it via lib/usecase.ValidateAndPersist (schema + journey_id cross-check
// + evidence file existence), and writes <out>/<id>.yaml.
//
// Usage:
//
//	go run -tags=write_usecase ./skills/detect-usecases/scripts \
//	  --out=<dir> --project-root=<dir> [--schemas-dir=<dir>] <draft>
//
// Exit codes: 0 written, 1 validation failed (schema or cross-check),
// 2 usage or I/O error.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-usecase", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var outDir, projectRoot, schemasDir string
	fs.StringVar(&outDir, "out", "", "directory to write the usecase file into (required)")
	fs.StringVar(&projectRoot, "project-root", "", "project root (required, holds .harness/stack.yaml)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if outDir == "" {
		fmt.Fprintln(stderr, "error: --out is required")
		return 2
	}
	if projectRoot == "" {
		fmt.Fprintln(stderr, "error: --project-root is required")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: write-usecase --out=DIR --project-root=DIR [--schemas-dir=DIR] <draft>")
		return 2
	}
	draftPath := fs.Arg(0)

	body, err := schema.ReadAsJSON(draftPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: read draft:", err)
		return 2
	}

	stk, code := loadStack(projectRoot, stderr)
	if code != 0 {
		return code
	}

	path, err := usecase.ValidateAndPersist(body, outDir, projectRoot, stk, schemasDir)
	if err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		var cce *stack.CrossCheckError
		if errors.As(err, &cce) {
			fmt.Fprintf(stderr, "error: %s\n", cce.Error())
			return 1
		}
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	fmt.Fprintln(stdout, path)
	return 0
}

func loadStack(projectRoot string, stderr io.Writer) (*stack.Stack, int) {
	stackPath := filepath.Join(projectRoot, ".harness", "stack.yaml")
	if _, err := os.Stat(stackPath); err != nil {
		fmt.Fprintf(stderr, "error: stack_missing: %s — run /detect-sensors first\n", stackPath)
		return nil, 2
	}
	body, err := schema.ReadAsJSON(stackPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: read stack:", err)
		return nil, 2
	}
	var s stack.Stack
	if err := json.Unmarshal(body, &s); err != nil {
		fmt.Fprintln(stderr, "error: parse stack:", err)
		return nil, 2
	}
	return &s, 0
}
