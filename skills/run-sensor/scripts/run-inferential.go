//go:build run_inferential

// Command run-inferential runs an inferential sensor end-to-end via a single
// Anthropic Messages API call and emits a Signal to stdout.
//
// Usage:
//
//	ANTHROPIC_API_KEY=... go run -tags=run_inferential \
//	  ./skills/run-sensor/scripts \
//	  [--slot key=value]... <sensor-path>
//
// Exit codes: 0 ok, 1 validation/unbound-slot/malformed-LLM-output, 2 usage
// or I/O error (incl. missing API key).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iurykrieger/harness-framework/skills/run-sensor/scripts/lib"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run-inferential", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemasDir string
	var apiBase string
	var slotArgs lib.MultiFlag
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	fs.StringVar(&apiBase, "api-base", "https://api.anthropic.com", "Anthropic API base URL (override for testing)")
	fs.Var(&slotArgs, "slot", "key=value binding for {{slot}} placeholders (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: run-inferential [--schemas-dir=DIR] [--api-base=URL] [--slot k=v]... <sensor-path>")
		return 2
	}

	bindings := map[string]string{}
	for _, s := range slotArgs {
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			fmt.Fprintf(stderr, "error: invalid --slot %q (want key=value)\n", s)
			return 2
		}
		bindings[k] = v
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(stderr, "error: ANTHROPIC_API_KEY is required for run-inferential")
		return 2
	}
	return lib.ExecuteInferential(rest[0], schemasDir, bindings, apiBase, apiKey, stdout, stderr)
}
