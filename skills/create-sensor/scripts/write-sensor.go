//go:build write_sensor

// Command write-sensor persists a draft sensor JSON to
// <projectRoot>/.harness/sensors/<id>.json. Strict mode: refuses if any
// golden_cases[].fixture is missing on disk and refuses to overwrite an
// existing <id>.json. Schema validation and atomic write are delegated
// to lib/sensor.ValidateAndPersist.
//
// Usage:
//
//	write-sensor --out <dir> [--schemas-dir <dir>] <draft.json>
//
// Exit codes: 0 written, 1 schema-invalid, 2 usage / I/O / pre-check.
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

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-sensor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var outDir, schemasDir string
	fs.StringVar(&outDir, "out", "", "directory to write the sensor file into (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		emitJSON(stdout, errorSignal("usage", err.Error()))
		return 2
	}
	if outDir == "" {
		emitJSON(stdout, errorSignal("usage", "--out is required"))
		return 2
	}
	if fs.NArg() != 1 {
		emitJSON(stdout, errorSignal("usage", "exactly one positional draft path required"))
		return 2
	}
	draftPath := fs.Arg(0)

	body, err := os.ReadFile(draftPath)
	if err != nil {
		emitJSON(stdout, errorSignal("read_draft", err.Error()))
		return 2
	}

	// Parse for pre-checks; we re-pass body unchanged to ValidateAndPersist.
	var draft map[string]interface{}
	if err := json.Unmarshal(body, &draft); err != nil {
		emitJSON(stdout, errorSignal("read_draft", "parse draft JSON: "+err.Error()))
		return 2
	}

	// Resolve project root for fixture existence checks.
	cwd, _ := os.Getwd()
	res, err := registry.Lookup(cwd)
	if err != nil {
		emitJSON(stdout, registry.DiscoveryErrorSignal(err, "write-sensor"))
		return 2
	}
	projectRoot := res.ProjectRoot

	if code := checkFixtures(stdout, draft, projectRoot); code != 0 {
		return code
	}

	id, _ := draft["id"].(string)
	if id == "" {
		emitJSON(stdout, errorSignal("usage", "sensor.id missing or empty"))
		return 2
	}
	target := filepath.Join(outDir, id+".json")
	if _, statErr := os.Stat(target); statErr == nil {
		emitJSON(stdout, sensorAlreadyExistsSignal(target))
		return 2
	}

	path, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			emitJSON(stdout, errorSignal("schema_invalid", err.Error()))
			return 1
		}
		emitJSON(stdout, errorSignal("persist_failed", err.Error()))
		return 2
	}
	emitJSON(stdout, passSignal(path, id, draft))
	fmt.Fprintln(stdout, path)
	return 0
}

func checkFixtures(stdout io.Writer, draft map[string]interface{}, projectRoot string) int {
	ver, ok := draft["verification"].(map[string]interface{})
	if !ok {
		return 0 // schema will catch this later
	}
	cases, ok := ver["golden_cases"].([]interface{})
	if !ok {
		return 0
	}
	for _, raw := range cases {
		gc, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		rel, _ := gc["fixture"].(string)
		if rel == "" {
			continue
		}
		full := filepath.Join(projectRoot, rel)
		if _, err := os.Stat(full); err != nil {
			emitJSON(stdout, errorSignal("missing_fixture", fmt.Sprintf("fixture %q not found at %s", rel, full)))
			return 2
		}
	}
	return 0
}

func passSignal(path, id string, draft map[string]interface{}) map[string]interface{} {
	return signal.NewBuilder("write-sensor", "0.1.0").
		WithVerdict("pass", "info").
		WithKind("sensor_persisted").
		WithRationale("sensor persisted").
		WithMetadata(map[string]interface{}{
			"path":      path,
			"id":        id,
			"kind_attr": draft["kind"],
			"type_attr": draft["type"],
		}).
		Build()
}

func sensorAlreadyExistsSignal(target string) map[string]interface{} {
	return signal.NewBuilder("write-sensor", "0.1.0").
		WithVerdict("error", "high").
		WithKind("sensor_already_exists").
		WithRationale("sensor file already exists; refusing to overwrite").
		WithMetadata(map[string]interface{}{"path": target}).
		Build()
}

func errorSignal(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("write-sensor", "0.1.0").
		WithVerdict("error", "high").
		WithKind(kind).
		WithRationale(rationale).
		Build()
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}
