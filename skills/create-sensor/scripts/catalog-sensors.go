//go:build catalog_sensors

// Command catalog-sensors emits a JSONL digest of every sensor JSON file
// under <projectRoot>/.harness/sensors/, plus warn envelopes for files
// that fail to parse or fail schema validation. Used by /create-sensor
// to seed the clarification dialogue with the user's existing sensor
// inventory.
//
// Usage:
//
//	catalog-sensors
//
// Exit codes: 0 normal completion, 1 registry discovery failed,
// 2 usage error, 2 schema validator init failed.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("catalog-sensors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: catalog-sensors")
		return 2
	}

	boot := cli.Bootstrap("catalog-sensors", stdout, stderr)
	if boot.ExitCode != 0 {
		return boot.ExitCode
	}

	sensorsDir := filepath.Join(boot.Res.ProjectRoot, ".harness", "sensors")
	entries, err := os.ReadDir(sensorsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintln(stderr, "error: read dir:", err)
		return 2
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		fpath := filepath.Join(sensorsDir, name)
		body, err := os.ReadFile(fpath)
		if err != nil {
			emitJSON(stdout, warnSignal(name, fmt.Sprintf("read %s: %v", fpath, err)))
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(body, &m); err != nil {
			emitJSON(stdout, warnSignal(name, fmt.Sprintf("parse %s: %v", fpath, err)))
			continue
		}
		if err := boot.Validator.Validate(schema.TargetSensor, m); err != nil {
			emitJSON(stdout, warnSignal(name, fmt.Sprintf("schema-invalid %s: %v", fpath, err)))
			continue
		}
		emitJSON(stdout, digest(m))
	}
	return 0
}

// digest projects the fields /create-sensor consumes from the sensor JSON.
func digest(m map[string]interface{}) map[string]interface{} {
	id, _ := m["id"].(string)
	blocking := false
	if exec, ok := m["execution"].(map[string]interface{}); ok {
		if b, ok := exec["blocking"].(bool); ok {
			blocking = b
		}
	}
	return map[string]interface{}{
		"id":          id,
		"kind":        m["kind"],
		"type":        m["type"],
		"output":      m["output"],
		"blocking":    blocking,
		"description": m["description"],
		"path":        path.Join(".harness", "sensors", id+".json"),
	}
}

func warnSignal(file, rationale string) map[string]interface{} {
	return signal.NewBuilder("catalog-sensors", "0.1.0").
		WithVerdict("warn", "low").
		WithKind("catalog_entry_skipped").
		WithEvidence([]interface{}{map[string]interface{}{"rationale": rationale, "file": file}}).
		Build()
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}
