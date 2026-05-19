//go:build catalog_sensors

// Command catalog-sensors emits a JSONL digest of every sensor YAML file
// under <projectRoot>/.harness/sensors/ — walking recursively so root-tier
// platform primitives AND per-usecase bundle folders are both included.
// Schema-invalid or malformed sensors produce a verdict=warn Signal envelope
// and are skipped.
//
// Usage:
//
//	catalog-sensors
//
// Exit codes: 0 normal completion, 1 registry discovery failed,
// 2 usage / catalog error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

type digestEntry struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Type        string `json:"type"`
	Output      string `json:"output"`
	Blocking    bool   `json:"blocking"`
	Layer       string `json:"layer,omitempty"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

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

	entries, warns := walkCatalog(boot.Res.ProjectRoot, boot.Validator)
	for _, w := range warns {
		emitJSON(stdout, w)
	}
	for _, e := range entries {
		emitJSON(stdout, e)
	}
	return 0
}

// walkCatalog recursively walks .harness/sensors/ under projectRoot,
// validating and projecting each YAML. It returns sorted digest entries
// and warn signals for files that fail validation.
func walkCatalog(projectRoot string, validator *schema.Validator) ([]map[string]interface{}, []map[string]interface{}) {
	sensorsRoot := filepath.Join(projectRoot, ".harness", "sensors")
	var entries []digestEntry
	var warns []map[string]interface{}

	_ = filepath.Walk(sensorsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			warns = append(warns, warnSignal(path, "read error: "+err.Error()))
			return nil
		}
		var instance interface{}
		if err := yaml.Unmarshal(body, &instance); err != nil {
			warns = append(warns, warnSignal(path, "parse error: "+err.Error()))
			return nil
		}
		if err := validator.Validate(schema.TargetSensor, instance); err != nil {
			warns = append(warns, warnSignal(path, err.Error()))
			return nil
		}
		var s sensor.Sensor
		if err := yaml.Unmarshal(body, &s); err != nil {
			warns = append(warns, warnSignal(path, "decode error: "+err.Error()))
			return nil
		}
		rel, _ := filepath.Rel(projectRoot, path)
		entries = append(entries, digestEntry{
			ID:          s.ID,
			Kind:        string(s.Kind),
			Type:        string(s.Type),
			Output:      string(s.Output),
			Blocking:    s.Execution.Blocking,
			Layer:       string(s.Layer),
			Description: s.Description,
			Path:        filepath.ToSlash(rel),
		})
		return nil
	})

	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })

	out := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		out = append(out, digestToMap(e))
	}
	return out, warns
}

func digestToMap(e digestEntry) map[string]interface{} {
	m := map[string]interface{}{
		"id":       e.ID,
		"kind":     e.Kind,
		"type":     e.Type,
		"output":   e.Output,
		"blocking": e.Blocking,
		"path":     e.Path,
	}
	if e.Layer != "" {
		m["layer"] = e.Layer
	}
	if e.Description != "" {
		m["description"] = e.Description
	}
	return m
}

func warnSignal(file, reason string) map[string]interface{} {
	return signal.NewBuilder("catalog-sensors", "0.1.0").
		WithVerdict("warn", "low").
		WithKind("catalog_entry_skipped").
		WithEvidence([]interface{}{map[string]interface{}{
			"rationale": reason,
			"file":      file,
		}}).
		Build()
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}
