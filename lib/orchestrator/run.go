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
// sensorPath is typically located at <projectRoot>/.harness/sensors/<id>.yaml.
// When an explicit project root is supplied (via RunWithDepsRoot or the
// explicitProjectRoot parameter of runWithDepsImpl), callers may pass an
// arbitrary absolute path outside the project tree; the logical sensor id is
// then read from the JSON's "id" field rather than inferred from the filename.
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
	return runWithDepsImpl(ctx, sensorPath, schemasDir, nil, "", stdout, stderr)
}

// RunWithDepsAtRoot is RunWithDeps with an explicit projectRoot. Use when
// sensorPath lives outside the canonical <projectRoot>/.harness/sensors/
// layout — e.g. per-usecase bundles at
// <projectRoot>/.harness/sensors/<usecase-id>/<id>.yaml, where the default
// "three Dir() calls above" heuristic would resolve projectRoot to
// <root>/.harness/ instead of <root>. validate-usecase relies on this
// to spawn layer entrypoints with the correct subprocess cwd.
func RunWithDepsAtRoot(ctx context.Context, sensorPath, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
	return runWithDepsImpl(ctx, sensorPath, schemasDir, nil, projectRoot, stdout, stderr)
}

// runWithDepsImpl is the shared implementation for RunWithDeps and the
// Root-aware paths. When root is non-nil, the target sensor is run via
// RunOneWithRoot so the run is registered under .harness/runtime/<id>/<run-id>/.
// Cascade-skipped roots do NOT touch the registry — the cascade Signal
// is emitted unchanged on stdout.
//
// explicitProjectRoot, when non-empty, overrides the projectRoot derived
// from sensorPath structure. This is required when sensorPath is an
// absolute path outside <projectRoot>/.harness/sensors/ (e.g. a temp file).
func runWithDepsImpl(ctx context.Context, sensorPath, schemasDir string, root *registry.Root, explicitProjectRoot string, stdout, stderr io.Writer) int {
	abs, err := filepath.Abs(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: abs path:", err)
		return 2
	}
	// Sensor files live at <projectRoot>/.harness/sensors/<id>.yaml, so the
	// project root is three Dir() calls above the abs sensor path.
	// When the caller supplies an explicit project root (e.g. for out-of-tree
	// sensor paths), use it instead.
	projectRoot := explicitProjectRoot
	if projectRoot == "" {
		projectRoot = filepath.Dir(filepath.Dir(filepath.Dir(abs)))
	}

	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}

	// Derive rootID from the sensor's id field when an explicit project
	// root is provided (out-of-tree path), so dep resolution uses the logical
	// id rather than the filename. For in-tree sensors the filename and id are
	// identical; falling back to the filename preserves existing behaviour.
	rootID := StripSensorExt(filepath.Base(abs))
	if explicitProjectRoot != "" {
		if b, readErr := schema.ReadAsJSON(abs); readErr != nil {
			fmt.Fprintln(stderr, "warn: read sensor for rootID:", readErr)
		} else {
			var m map[string]interface{}
			if json.Unmarshal(b, &m) == nil {
				if jsonID, ok := m["id"].(string); ok && jsonID != "" {
					rootID = jsonID
				}
			}
		}
	}
	holderPID := os.Getpid()
	pre := RunDeps(ctx, abs, projectRoot, schemasDir, rootID, holderPID, v, stdout, stderr)
	// RunDeps uses abs as the lookup key for DAG resolution (so sensor.Resolve
	// finds the file directly). Fix up the root sensor's ID in the resolved
	// order and in any cascade Signal so they carry the logical id rather than
	// the abs path — schema validation and cascade Signals depend on the
	// logical id matching sensor.yaml's ^[a-z][a-z0-9-]*$ constraint.
	if rootID != abs {
		if len(pre.Order) > 0 && pre.Order[len(pre.Order)-1].ID == abs {
			pre.Order[len(pre.Order)-1].ID = rootID
		}
		if pre.CascadeSig != nil {
			if id, _ := pre.CascadeSig["sensor_id"].(string); id == abs {
				pre.CascadeSig["sensor_id"] = rootID
			}
		}
	}

	// detachAll consumes the live-stack: iterate in reverse (LIFO)
	// calling DetachLiveDep, then clear the slice so the deferred
	// safety-net invocation below becomes a no-op. The deferred call
	// remains as a backstop for panic / mid-function early-return
	// paths where the explicit call did not happen.
	detachAll := func() {
		for i := len(pre.LiveStack) - 1; i >= 0; i-- {
			DetachLiveDep(pre.LiveStack[i], projectRoot, rootID, v, stdout, stderr)
		}
		pre.LiveStack = nil
	}
	defer detachAll()

	if pre.ExitCode != 0 {
		detachAll()
		return pre.ExitCode
	}
	if pre.CascadeSig != nil {
		if err := v.Validate(schema.TargetSignal, pre.CascadeSig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		detachAll()
		_ = json.NewEncoder(stdout).Encode(pre.CascadeSig)
		return 1
	}

	target := pre.Order[len(pre.Order)-1]

	// Wrap ctx so a mid-run death of any live blocking dep can cancel
	// the root sensor's subprocess instead of letting it spin until its
	// own timeout. Without this, a dep that boots healthy but crashes
	// while the dependent is wait-looping (issue #49) burns wall-clock
	// until the dependent's own deadline; the death-watcher closes the
	// gap by polling each LiveDep's registry entry and cancelling on the
	// first observed death.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	deathCh := WatchLiveDepsForDeath(runCtx, projectRoot, pre.LiveStack, depDeathPollInterval)
	var depDeath DepDeath
	var depDeathSet bool
	deathDone := make(chan struct{})
	go func() {
		defer close(deathDone)
		select {
		case d, ok := <-deathCh:
			if ok {
				depDeath = d
				depDeathSet = true
				cancelRun()
			}
		case <-runCtx.Done():
			// Root finished on its own; drain the channel so the watcher
			// goroutine inside WatchLiveDepsForDeath can exit cleanly.
			<-deathCh
		}
	}()

	sig, code := RunOneWithRootCapture(runCtx, target, projectRoot, schemasDir, v, root, stdout, stderr)
	// Stop the watcher before reading depDeath. cancelRun unblocks
	// WatchLiveDepsForDeath's ctx.Done branch and our local goroutine's
	// runCtx.Done branch; <-deathDone ensures the goroutine has settled
	// before we read depDeath* below (no data race).
	cancelRun()
	<-deathDone

	if depDeathSet && sig != nil {
		annotateCancelledByDep(sig, depDeath)
	}

	detachAll()
	if sig != nil {
		_ = json.NewEncoder(stdout).Encode(sig)
	}
	return code
}

// annotateCancelledByDep patches the root sensor's aggregate Signal to
// reflect that it was cancelled because a live blocking dep died
// mid-run. Forces verdict=error/severity=high (distinguishing this from
// a natural fail/timeout) and records cancelled_by/cancellation_reason
// under metadata so FirstFailedDep, heal-sensor, and reports can surface
// the cause. terminated_externally is left intact: the orchestrator did
// cancel the subprocess, but cancelled_by names the specific dep that
// triggered the cancellation.
func annotateCancelledByDep(sig map[string]interface{}, death DepDeath) {
	sig["verdict"] = "error"
	sig["severity"] = "high"
	md, ok := sig["metadata"].(map[string]interface{})
	if !ok || md == nil {
		md = map[string]interface{}{}
		sig["metadata"] = md
	}
	md["cancelled_by"] = death.DepID
	md["cancellation_reason"] = death.Reason
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

// StripSensorExt removes a trailing ".yaml" extension from a filename. It is
// the inverse of sensor.Resolve's "<id>.yaml" filename convention and is
// exported so runner scripts can derive a sensor id from its on-disk path.
func StripSensorExt(name string) string {
	if len(name) > 5 && name[len(name)-5:] == ".yaml" {
		return name[:len(name)-5]
	}
	return name
}
