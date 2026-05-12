//go:build write_stack

// Command write-stack reads a draft stack JSON payload, validates it
// against schemas/stack.json, cross-checks that every
// log_shapes[].produced_by[] references an existing components[].name,
// and persists it to <project-root>/.harness/stack.json.
//
// Usage:
//
//	go run -tags=write_stack ./skills/detect-sensors/scripts \
//	  --out=<project-root> [--schemas-dir=<dir>] <stack-payload.json>
//
// Exit codes: 0 stack written, 1 schema/cross-check failure,
// 2 usage or I/O error.
package main

import (
	"encoding/json"
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
	fs.StringVar(&outDir, "out", "", "project root to write .harness/stack.json into (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if outDir == "" {
		fmt.Fprintln(stderr, "error: --out is required")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: write-stack --out=PROJECT_ROOT [--schemas-dir=DIR] <stack-payload.json>")
		return 2
	}
	payloadPath := fs.Arg(0)

	body, err := os.ReadFile(payloadPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return 2
	}

	if err := crossCheckProducedBy(body); err != nil {
		fmt.Fprintln(stderr, "error: stack_produced_by_orphan:", err)
		return 1
	}

	path, err := stack.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	fmt.Fprintln(stdout, path)
	return 0
}

// crossCheckProducedBy validates that every log_shapes[].produced_by[]
// entry matches some components[].name. Runs BEFORE schema validation
// (cheap parse, fail-fast on the most common author mistake).
func crossCheckProducedBy(body []byte) error {
	var m struct {
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
		LogShapes []struct {
			ID         string   `json:"id"`
			ProducedBy []string `json:"produced_by"`
		} `json:"log_shapes"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		// Schema validator will catch malformed JSON with a richer message.
		return nil
	}
	names := map[string]struct{}{}
	for _, c := range m.Components {
		names[c.Name] = struct{}{}
	}
	for _, sh := range m.LogShapes {
		for _, pb := range sh.ProducedBy {
			if _, ok := names[pb]; !ok {
				return fmt.Errorf("log_shape %q references unknown component %q", sh.ID, pb)
			}
		}
	}
	return nil
}
