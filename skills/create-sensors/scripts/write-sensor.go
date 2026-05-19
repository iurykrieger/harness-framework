//go:build write_sensor

// Command write-sensor persists a draft sensor (JSON or YAML) to
// <projectRoot>/.harness/sensors/<usecase-id>/<id>.yaml (per-usecase) or
// <projectRoot>/.harness/sensors/<id>.yaml (root-tier) via
// lib/sensor.ValidateAndPersist — the single shared persistence entrypoint.
// This wrapper sets the strict options (RejectIfExists,
// RequireUseCaseFilesOnDisk) appropriate to /create-sensors' authoring
// contract. The --out flag accepts both root-tier and per-usecase paths;
// intermediate directories are created automatically.
//
// Usage:
//
//	write-sensor --out <dir> [--schemas-dir <dir>] <draft>
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

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
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

	body, err := schema.ReadAsJSON(draftPath)
	if err != nil {
		emitJSON(stdout, errorSignal("read_draft", err.Error()))
		return 2
	}

	// Parse draft only to extract id/kind/type for the success signal
	// metadata. lib/sensor.ValidateAndPersist re-parses and validates.
	var draft map[string]interface{}
	if err := json.Unmarshal(body, &draft); err != nil {
		emitJSON(stdout, errorSignal("read_draft", "parse draft JSON: "+err.Error()))
		return 2
	}

	cwd, _ := os.Getwd()
	res, err := registry.Lookup(cwd)
	if err != nil {
		emitJSON(stdout, registry.DiscoveryErrorSignal(err, "write-sensor"))
		return 2
	}

	path, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:                    outDir,
		SchemasDir:                schemasDir,
		RejectIfExists:            true,
		RequireUseCaseFilesOnDisk: true,
		ProjectRoot:               res.ProjectRoot,
	})
	if err != nil {
		var saee *sensor.SensorAlreadyExistsError
		if errors.As(err, &saee) {
			emitJSON(stdout, sensorAlreadyExistsSignal(saee.Path))
			return 2
		}
		var muc *sensor.MissingUseCaseError
		if errors.As(err, &muc) {
			emitJSON(stdout, errorSignal("usecase_not_found", muc.Error()))
			return 2
		}
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			emitJSON(stdout, errorSignal("schema_invalid", err.Error()))
			return 1
		}
		emitJSON(stdout, errorSignal("persist_failed", err.Error()))
		return 2
	}

	id, _ := draft["id"].(string)
	emitJSON(stdout, passSignal(path, id, draft))
	fmt.Fprintln(stdout, path)
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
