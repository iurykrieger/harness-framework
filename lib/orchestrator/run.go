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

	rootID := StripJSONExt(filepath.Base(abs))
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
	var liveStack []string

	projectRoot := filepath.Dir(filepath.Dir(abs))

	defer func() {
		// Detach in reverse order. Even if RunWithDeps panics or returns
		// early, blocking deps must come down.
		for i := len(liveStack) - 1; i >= 0; i-- {
			DetachLiveDep(liveStack[i], projectRoot, rootID, v, stdout, stderr)
		}
	}()

	for _, s := range order {
		execMap, _ := s.JSON["execution"].(map[string]interface{})
		blocking, _ := execMap["blocking"].(bool)
		if blocking && s.ID != rootID {
			depID, err := AttachLiveDep(ctx, s, projectRoot, rootID, v, stdout, stderr)
			if err != nil {
				cascade := buildSimpleSignal(rootID, "error", "high", "dep_start_failed", err.Error())
				_ = json.NewEncoder(stdout).Encode(cascade)
				return 1
			}
			liveStack = append(liveStack, depID)
			signals[s.ID] = map[string]interface{}{"verdict": "pass"}
			continue
		}
		if blocker := FirstFailedDep(s, signals); blocker != nil {
			cascade := BuildCascadeSignal(s, blocker)
			if err := v.Validate(schema.TargetSignal, cascade); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				return 1
			}
			_ = json.NewEncoder(stdout).Encode(cascade)
			signals[s.ID] = cascade
			continue
		}
		sig, sigCode := RunOne(ctx, s, schemasDir, v, stdout, stderr)
		if sigCode != 0 {
			return sigCode
		}
		signals[s.ID] = sig
	}
	return 0
}

// FirstFailedDep returns the Signal of the first dep id (in declaration
// order) of s that has a fail/error verdict, or nil when none failed.
func FirstFailedDep(s Sensor, signals map[string]map[string]interface{}) map[string]interface{} {
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

// StripJSONExt removes a trailing ".json" extension from a filename. It is
// the inverse of FindSensorByID's "<id>.json" filename convention and is
// exported so runner scripts can derive a sensor id from its on-disk path.
func StripJSONExt(name string) string {
	if len(name) > 5 && name[len(name)-5:] == ".json" {
		return name[:len(name)-5]
	}
	return name
}
