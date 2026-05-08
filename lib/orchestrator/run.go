package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// RunWithDeps loads the sensor at sensorPath, resolves its depends_on
// transitively, runs each sensor in topo order through RunOne, and emits
// cascade Signals for any dependent skipped because an earlier sensor
// failed. The aggregate Signal of the requested sensor is the LAST line
// on stdout (contract preserved from the prior streaming-sensors design).
//
// Exit codes:
//
//	0 — every requested-or-implied sensor produced a Signal (some may be
//	     cascade or fail/error; emission is what matters for exit 0).
//	1 — DAG resolution failed (cycle, missing dep, malformed sensor JSON).
//	2 — schema/io error opening the sensor or schemas.
func RunWithDeps(ctx context.Context, sensorPath, schemasDir string, stdout, stderr io.Writer) int {
	abs, err := filepath.Abs(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: abs path:", err)
		return 2
	}
	root := filepath.Dir(abs)

	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}

	rootID := stripJSONExt(filepath.Base(abs))
	order, err := Resolve(rootID, root)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	// Validate every sensor in the graph against schemas/sensor.json before
	// running anything. Dependencies that fail validation should abort
	// the run, not be discovered mid-pipeline.
	for _, s := range order {
		if err := v.Validate(schema.TargetSensor, s.JSON); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
	}

	signals := map[string]map[string]interface{}{}
	failed := map[string]map[string]interface{}{}

	for _, s := range order {
		if blocker := firstFailedDep(s, signals); blocker != nil {
			cascade := BuildCascadeSignal(s, blocker)
			if err := v.Validate(schema.TargetSignal, cascade); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				return 1
			}
			_ = json.NewEncoder(stdout).Encode(cascade)
			signals[s.ID] = cascade
			failed[s.ID] = cascade
			continue
		}
		sig, sigCode := RunOne(ctx, s, schemasDir, v, stdout, stderr)
		if sigCode != 0 {
			return sigCode
		}
		signals[s.ID] = sig
		verdict, _ := sig["verdict"].(string)
		if verdict == "fail" || verdict == "error" {
			failed[s.ID] = sig
		}
	}
	return 0
}

// firstFailedDep returns the Signal of the first dep id (in declaration
// order) of s that has a fail/error verdict, or nil when none failed.
func firstFailedDep(s Sensor, signals map[string]map[string]interface{}) map[string]interface{} {
	depIDs := readDepsArray(s.JSON)
	for _, d := range depIDs {
		sig := signals[d]
		if sig == nil {
			continue
		}
		verdict, _ := sig["verdict"].(string)
		if verdict == "fail" || verdict == "error" {
			return sig
		}
	}
	return nil
}

func stripJSONExt(name string) string {
	if len(name) > 5 && name[len(name)-5:] == ".json" {
		return name[:len(name)-5]
	}
	return name
}
