package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// RunWithDeps loads the sensor at sensorPath, resolves its depends_on
// transitively, runs each sensor in topo order through RunOne, and emits
// cascade Signals for any dependent skipped because an earlier sensor
// failed. The aggregate Signal of the requested sensor is the LAST line
// on stdout (contract preserved from the prior streaming-sensors design).
//
// sensorPath must be located at <projectRoot>/sensors/<id>.json so that
// RunDeps can discover siblings via filepath.Join(projectRoot, "sensors").
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
	projectRoot := filepath.Dir(filepath.Dir(abs))

	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}

	rootID := StripJSONExt(filepath.Base(abs))
	holderPID := os.Getpid()
	pre := RunDeps(ctx, rootID, projectRoot, schemasDir, rootID, holderPID, v, stdout, stderr)

	defer func() {
		for i := len(pre.LiveStack) - 1; i >= 0; i-- {
			DetachLiveDep(pre.LiveStack[i], projectRoot, rootID, v, stdout, stderr)
		}
	}()

	if pre.ExitCode != 0 {
		return pre.ExitCode
	}
	if pre.CascadeSig != nil {
		if err := v.Validate(schema.TargetSignal, pre.CascadeSig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(pre.CascadeSig)
		return 1
	}

	target := pre.Order[len(pre.Order)-1]
	_, code = RunOne(ctx, target, schemasDir, v, stdout, stderr)
	return code
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
