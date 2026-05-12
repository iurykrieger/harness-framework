package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

// RunWithDeps loads the sensor at sensorPath, resolves its requires[kind=sensor]
// transitively, runs each sensor in topo order through RunOne, and emits
// cascade Signals for any dependent skipped because an earlier sensor
// failed. The aggregate Signal of the requested sensor is the LAST line
// on stdout (contract preserved from the prior streaming-sensors design).
//
// sensorPath must be located at <projectRoot>/.harness/sensors/<id>.json so that
// RunDeps can discover siblings via filepath.Join(projectRoot, ".harness", "sensors").
//
// Exit codes:
//
//	0 — root sensor ran via RunOne and its aggregate was emitted (verdict
//	     pass/warn/fail/error baked into the Signal — exit code reflects
//	     emission, not verdict).
//	1 — root sensor did not run: either DAG resolution failed (cycle,
//	     missing dep, malformed sensor JSON), or a dep produced
//	     fail/error and the root was cascade-skipped. A cascade Signal is
//	     still emitted to stdout before the non-zero exit.
//	2 — schema/io error opening the sensor or schemas.
func RunWithDeps(ctx context.Context, sensorPath, schemasDir string, stdout, stderr io.Writer) int {
	return runWithDepsImpl(ctx, sensorPath, schemasDir, nil, stdout, stderr)
}

// runWithDepsImpl is the shared implementation for RunWithDeps and the
// Root-aware paths. When root is non-nil, the target sensor is run via
// RunOneWithRoot so the run is registered under .harness/runtime/<id>/<run-id>/.
// Cascade-skipped roots do NOT touch the registry — the cascade Signal
// is emitted unchanged on stdout.
func runWithDepsImpl(ctx context.Context, sensorPath, schemasDir string, root *registry.Root, stdout, stderr io.Writer) int {
	abs, err := filepath.Abs(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: abs path:", err)
		return 2
	}
	// Sensor files live at <projectRoot>/.harness/sensors/<id>.json, so the
	// project root is three Dir() calls above the abs sensor path.
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(abs)))

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
	_, code = RunOneWithRoot(ctx, target, schemasDir, v, root, stdout, stderr)
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
// the inverse of sensor.Resolve's "<id>.json" filename convention and is
// exported so runner scripts can derive a sensor id from its on-disk path.
func StripJSONExt(name string) string {
	if len(name) > 5 && name[len(name)-5:] == ".json" {
		return name[:len(name)-5]
	}
	return name
}
