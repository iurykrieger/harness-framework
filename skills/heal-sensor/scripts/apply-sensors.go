//go:build heal_apply_sensors

// Command apply-sensors reads a Setup Plan and persists every
// sensor_patches[] (with lib/heal.BumpPatch first) and
// new_setup_sensors[] entry through lib/sensor.ValidateAndPersist —
// the SAME primitive detect-sensors uses. No duplicate persistence
// path.
//
// Usage:
//
//	go run -tags=heal_apply_sensors ./skills/heal-sensor/scripts \
//	  --plan=PATH --out=DIR [--schemas-dir=DIR]
//
// Exit codes: 0 all sensors persisted, 1 validation fail (some may
// have been written; written ones were valid), 2 usage / I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apply-sensors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var planPath, outDir, schemasDir string
	fs.StringVar(&planPath, "plan", "", "Setup Plan JSON (required)")
	fs.StringVar(&outDir, "out", "", "sensors directory (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if planPath == "" || outDir == "" {
		fmt.Fprintln(stderr, "usage: apply-sensors --plan=PATH --out=DIR [--schemas-dir=DIR]")
		return 2
	}

	planBody, err := schema.ReadAsJSON(planPath)
	if err != nil {
		fmt.Fprintln(stderr, "read plan:", err)
		return 2
	}
	plan, err := heal.ParsePlan(planBody)
	if err != nil {
		fmt.Fprintln(stderr, "parse plan:", err)
		return 2
	}

	written := []string{}
	for _, p := range plan.SensorPatches {
		body, err := json.Marshal(p.Patch)
		if err != nil {
			fmt.Fprintln(stderr, "marshal patch:", err)
			return 1
		}
		bumped, err := heal.BumpPatch(body)
		if err != nil {
			fmt.Fprintln(stderr, "bump patch version for", p.ID, ":", err)
			return 1
		}
		path, err := sensor.ValidateAndPersist(bumped, sensor.PersistOpts{
			OutDir:     outDir,
			SchemasDir: schemasDir,
		})
		if err != nil {
			fmt.Fprintln(stderr, "persist patch", p.ID, ":", err)
			return 1
		}
		written = append(written, path)
	}
	for _, n := range plan.NewSetupSensors {
		body, err := json.Marshal(n.JSON)
		if err != nil {
			fmt.Fprintln(stderr, "marshal new sensor:", err)
			return 1
		}
		path, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
			OutDir:     outDir,
			SchemasDir: schemasDir,
		})
		if err != nil {
			fmt.Fprintln(stderr, "persist new sensor", n.ID, ":", err)
			return 1
		}
		written = append(written, path)
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]interface{}{"written": written})
	return 0
}
