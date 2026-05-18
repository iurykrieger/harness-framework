//go:build derive_from_contract

// Command derive-from-contract reads a contract declaration and emits the
// minimum valid JSON fixture payload for it. It exposes
// lib/fixture.DeriveFromContract to the skill subprocess layer.
//
// Usage:
//
//	go run -tags=derive_from_contract ./skills/detect-usecases/scripts \
//	  --source=<json-schema|openapi-component|avro|protobuf> \
//	  --decl-path=<path>
//
// For openapi-component, declPath must be "<file>#/components/schemas/<Name>".
// For protobuf, declPath must be "<file>:<MessageName>".
//
// Outputs a single JSON object to stdout:
//
//	{"source":"contract","ext":"json","payload_b64":"<b64>","blind_spots":["..."]}
//
// Exit codes: 0 success, 1 derivation error (including unsupported source),
// 2 usage error.
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/fixture"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("derive-from-contract", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var source, declPath string
	fs.StringVar(&source, "source", "", "contract source (json-schema|openapi-component|avro|protobuf)")
	fs.StringVar(&declPath, "decl-path", "", "contract declaration path")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if source == "" || declPath == "" {
		fmt.Fprintln(stderr, "error: --source and --decl-path are required")
		return 2
	}
	s, err := fixture.DeriveFromContract(fixture.Hint{}, fixture.SourceKind(source), declPath)
	if err != nil {
		if errors.Is(err, fixture.ErrUnsupportedContractSource) {
			fmt.Fprintln(stderr, "error: unsupported contract source:", source)
			return 1
		}
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if s == nil {
		fmt.Fprintln(stderr, "error: nil sample returned")
		return 1
	}
	json.NewEncoder(stdout).Encode(map[string]any{
		"source":      s.Source,
		"ext":         s.Ext,
		"payload_b64": base64.StdEncoding.EncodeToString(s.Payload),
		"blind_spots": s.BlindSpots,
	})
	return 0
}
