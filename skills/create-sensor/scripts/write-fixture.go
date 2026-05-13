//go:build write_fixture

// Command write-fixture writes a fixture payload atomically to a path
// under <projectRoot>/.harness/sensors/fixtures/. Used by /create-sensor
// to materialize the fixture files referenced by a sensor's
// verification.golden_cases[].
//
// Usage:
//
//	write-fixture [--from-file <src>] <target-relative-path>
//
// Exit codes: 0 success, 2 usage / path escape / I/O failure.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/sensor"
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
	projectRoot := res.ProjectRoot

	fixturesRoot := filepath.Join(projectRoot, ".harness", "sensors", "fixtures")
	target := filepath.Clean(filepath.Join(projectRoot, relPath))
	if !strings.HasPrefix(target+string(os.PathSeparator), fixturesRoot+string(os.PathSeparator)) {
		emitJSON(stdout, errorSignal("fixture_path_escape", fmt.Sprintf("path %q resolves outside %s", relPath, fixturesRoot)))
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

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		emitJSON(stdout, errorSignal("mkdir", err.Error()))
		return 2
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-fixture-*")
	if err != nil {
		emitJSON(stdout, errorSignal("create_tmp", err.Error()))
		return 2
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		emitJSON(stdout, errorSignal("write_tmp", err.Error()))
		return 2
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		emitJSON(stdout, errorSignal("sync_tmp", err.Error()))
		return 2
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		emitJSON(stdout, errorSignal("close_tmp", err.Error()))
		return 2
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		emitJSON(stdout, errorSignal("rename", err.Error()))
		return 2
	}

	emitJSON(stdout, passSignal(target))
	return 0
}

func passSignal(target string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "write-fixture",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "fixture written"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "fixture_written", "path": target},
	}
}

func errorSignal(kind, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "write-fixture",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": kind},
	}
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}
