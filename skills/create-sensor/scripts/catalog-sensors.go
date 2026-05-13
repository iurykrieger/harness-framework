//go:build catalog_sensors

// Command catalog-sensors emits a JSONL digest of every sensor JSON file
// under <projectRoot>/.harness/sensors/, plus warn envelopes for files
// that fail to parse or fail schema validation. Used by /create-sensor
// to seed the clarification dialogue with the user's existing sensor
// inventory.
//
// Usage:
//
//	catalog-sensors [--sensors-dir <dir>] [--schemas-dir <dir>]
//
// Exit codes: 0 normal completion, 2 usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("catalog-sensors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sensorsDir, schemasDir string
	fs.StringVar(&sensorsDir, "sensors-dir", "", "directory to scan (default: <projectRoot>/.harness/sensors/)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory; when set, each sensor is schema-validated")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: catalog-sensors [--sensors-dir DIR] [--schemas-dir DIR]")
		return 2
	}

	// Resolve sensorsDir via registry discovery if not explicit.
	projectRoot := ""
	if sensorsDir == "" {
		cwd, _ := os.Getwd()
		res, err := registry.Lookup(cwd)
		if err != nil {
			// Discovery failed; emit the canonical signal and exit 0 (empty catalog).
			emitJSON(stdout, registry.DiscoveryErrorSignal(err, "catalog-sensors"))
			return 0
		}
		projectRoot = res.ProjectRoot
		sensorsDir = filepath.Join(projectRoot, ".harness", "sensors")
	} else {
		// When the dir is explicit, derive projectRoot from it for the "path" field.
		projectRoot = deriveProjectRoot(sensorsDir)
	}

	entries, err := os.ReadDir(sensorsDir)
	if err != nil {
		// Missing directory: empty catalog, exit 0.
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintln(stderr, "error: read dir:", err)
		return 2
	}

	// Build a stable order for deterministic output.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var validator *schema.Validator
	if schemasDir != "" {
		v, vErr := schema.NewValidator(schemasDir)
		if vErr != nil {
			fmt.Fprintln(stderr, "error: load schemas:", vErr)
			return 2
		}
		validator = v
	}

	for _, name := range names {
		path := filepath.Join(sensorsDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			emitJSON(stdout, warnSignal(name, fmt.Sprintf("read %s: %v", path, err)))
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(body, &m); err != nil {
			emitJSON(stdout, warnSignal(name, fmt.Sprintf("parse %s: %v", path, err)))
			continue
		}
		if validator != nil {
			if err := validator.Validate(schema.TargetSensor, m); err != nil {
				emitJSON(stdout, warnSignal(name, fmt.Sprintf("schema-invalid %s: %v", path, err)))
				continue
			}
		}
		emitJSON(stdout, digest(m, projectRoot))
	}
	return 0
}

// digest projects the fields /create-sensor consumes from the sensor JSON.
func digest(m map[string]interface{}, projectRoot string) map[string]interface{} {
	id, _ := m["id"].(string)
	blocking := false
	if exec, ok := m["execution"].(map[string]interface{}); ok {
		if b, ok := exec["blocking"].(bool); ok {
			blocking = b
		}
	}
	relPath := filepath.Join(".harness", "sensors", id+".json")
	out := map[string]interface{}{
		"id":          id,
		"kind":        m["kind"],
		"type":        m["type"],
		"output":      m["output"],
		"blocking":    blocking,
		"description": m["description"],
		"path":        relPath,
	}
	return out
}

func warnSignal(file, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "catalog-sensors",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "warn",
		"severity":    "low",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale, "file": file}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "catalog_entry_skipped"},
	}
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}

// deriveProjectRoot strips trailing .harness/sensors/ from an explicit
// sensorsDir to recover the project root used in digest path fields.
// When the input does not match that suffix, the function returns "".
func deriveProjectRoot(sensorsDir string) string {
	abs, err := filepath.Abs(sensorsDir)
	if err != nil {
		return ""
	}
	clean := filepath.Clean(abs)
	parent := filepath.Dir(clean)
	if filepath.Base(parent) == ".harness" {
		return filepath.Dir(parent)
	}
	return ""
}
