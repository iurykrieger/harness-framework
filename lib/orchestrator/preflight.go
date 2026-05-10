package orchestrator

import (
	"context"
)

// RunPreparePhase runs sensor.execution.prepare[] fail-fast. Returns the
// per-step results (shaped for inclusion in metadata.lifecycle.prepare)
// and a bool indicating whether the phase failed (first non-pass step
// triggers fail-fast).
//
// Extracted from lifecycle.go::runLifecyclePhase("prepare", failFast=true)
// so callers that need only the prepare phase (notably /start-sensor
// before its detached spawn) can run it without paying for command +
// teardown.
func RunPreparePhase(ctx context.Context, target Sensor, defaultTimeoutMS int) (results []interface{}, failed bool) {
	execMap, _ := target.JSON["execution"].(map[string]interface{})
	if execMap == nil {
		return nil, false
	}
	return runLifecyclePhase(ctx, execMap, "prepare", defaultTimeoutMS, true)
}
