//go:build find_on_disk

// Command find-on-disk walks caller-supplied search paths for a fixture
// file matching the given role. It exposes lib/fixture.FindOnDisk to the
// skill subprocess layer.
//
// Usage:
//
//	go run -tags=find_on_disk ./skills/detect-usecases/scripts \
//	  --role=<trigger|outcome|body|log-line|event> \
//	  --search-paths=<path1>,<path2>,...
//
// Outputs a single JSON object to stdout. When a match is found:
//
//	{"source":"disk","source_path":"<abs>","ext":"json","payload_b64":"<b64>","blind_spots":null}
//
// When no match is found:
//
//	{"source":""}
//
// Exit codes: 0 success (including no-match), 1 I/O error, 2 usage error.
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/fixture"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("find-on-disk", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var role, paths string
	fs.StringVar(&role, "role", "", "fixture role (trigger|outcome|body|log-line|event)")
	fs.StringVar(&paths, "search-paths", "", "comma-separated search paths")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if role == "" {
		fmt.Fprintln(stderr, "error: --role is required")
		return 2
	}
	var splits []string
	if paths != "" {
		splits = strings.Split(paths, ",")
	}
	s, err := fixture.FindOnDisk(fixture.Hint{Role: role}, splits)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if s == nil {
		json.NewEncoder(stdout).Encode(map[string]any{"source": ""})
		return 0
	}
	json.NewEncoder(stdout).Encode(map[string]any{
		"source":      s.Source,
		"source_path": s.SourcePath,
		"ext":         s.Ext,
		"payload_b64": base64.StdEncoding.EncodeToString(s.Payload),
		"blind_spots": s.BlindSpots,
	})
	return 0
}
