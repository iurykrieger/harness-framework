//go:build write_fixture

// Command write-fixture writes a fixture payload atomically under
// <projectRoot>/.harness/sensors/fixtures/ via lib/sensor.WriteFixture —
// the single shared fixture-persistence entrypoint. Path-escape guard,
// parent-dir creation, and tmp+rename atomicity all live in the lib.
//
// Usage:
//
//	write-fixture [--from-file <src>] <target-relative-path>
//
// Exit codes: 0 success, 2 usage / path escape / I/O failure.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	os.Exit(runWithStdin(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runWithStdin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-fixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var fromFile string
	fs.StringVar(&fromFile, "from-file", "", "read payload from this file (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: write-fixture [--from-file SRC] <target-relative-path>")
		return 2
	}
	relPath := fs.Arg(0)

	cwd, _ := os.Getwd()
	res, err := registry.Lookup(cwd)
	if err != nil {
		emitJSON(stdout, registry.DiscoveryErrorSignal(err, "write-fixture"))
		return 2
	}

	var payload []byte
	if fromFile != "" {
		payload, err = os.ReadFile(fromFile)
	} else {
		payload, err = io.ReadAll(stdin)
	}
	if err != nil {
		emitJSON(stdout, errorSignal("read_payload", err.Error()))
		return 2
	}

	abs, err := sensor.WriteFixture(res.ProjectRoot, relPath, payload)
	if err != nil {
		var fpe *sensor.FixturePathEscapeError
		if errors.As(err, &fpe) {
			emitJSON(stdout, errorSignal("fixture_path_escape", fpe.Error()))
			return 2
		}
		emitJSON(stdout, errorSignal("write_failed", err.Error()))
		return 2
	}

	emitJSON(stdout, passSignal(abs))
	return 0
}

func passSignal(target string) map[string]interface{} {
	return signal.NewBuilder("write-fixture", "0.1.0").
		WithVerdict("pass", "info").
		WithKind("fixture_written").
		WithRationale("fixture written").
		WithMetadata(map[string]interface{}{"path": target}).
		Build()
}

func errorSignal(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("write-fixture", "0.1.0").
		WithVerdict("error", "high").
		WithKind(kind).
		WithRationale(rationale).
		Build()
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}
