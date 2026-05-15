//go:build catalog_sensors

// Command catalog-sensors emits a JSONL digest of every sensor JSON file
// under <projectRoot>/.harness/sensors/ via lib/sensor.Catalog — the
// single shared enumeration entrypoint. Schema-invalid or malformed
// sensors produce a verdict=warn Signal envelope and are skipped.
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
	"path"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/sensor"
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
	sensors, warns, err := sensor.Catalog(sensorsDir, boot.Validator)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}

	for _, w := range warns {
		emitJSON(stdout, warnSignal(w))
	}
	for _, s := range sensors {
		emitJSON(stdout, digest(s))
	}
	return 0
}

// digest projects the wire-format fields /create-sensor consumes from a
// canonical *Sensor. Path uses forward slashes (the field is consumed as
// a string by tooling, not as a filesystem path).
func digest(s *sensor.Sensor) map[string]interface{} {
	return map[string]interface{}{
		"id":          s.ID,
		"kind":        s.Kind,
		"type":        s.Type,
		"output":      s.Output,
		"blocking":    s.Execution.Blocking,
		"description": s.Description,
		"path":        path.Join(".harness", "sensors", s.ID+".yaml"),
	}
}

func warnSignal(w sensor.CatalogWarn) map[string]interface{} {
	return signal.NewBuilder("catalog-sensors", "0.1.0").
		WithVerdict("warn", "low").
		WithKind("catalog_entry_skipped").
		WithEvidence([]interface{}{map[string]interface{}{
			"rationale": w.Reason,
			"file":      w.File,
		}}).
		Build()
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}
