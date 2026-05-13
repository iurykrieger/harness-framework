package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// RunDepsResult carries the post-pre-flight state for the caller to
// decide the root's fate.
type RunDepsResult struct {
	// Order is the topo-sorted DAG (root last). Always populated when
	// ExitCode==0.
	Order []Sensor

	// Signals maps non-root sensor id → its emitted signal (RunOne
	// aggregate, AttachLiveDep ack, or BuildCascadeSignal for skipped deps).
	Signals map[string]map[string]interface{}

	// LiveStack is the ordered list of blocking dep handles that
	// AttachLiveDep succeeded on. Each carries (ID, RunID) so detach
	// can address the exact blocking entry we attached to, not any
	// sibling non-blocking entry of the same sensor. Caller iterates
	// in reverse for detach.
	LiveStack []LiveDep

	// CascadeSig is non-nil when a dep of the root produced fail/error
	// and the root would cascade. Caller emits and detaches LiveStack.
	CascadeSig map[string]interface{}

	// ExitCode: 0 ok, 1 DAG/schema failure, 2 io error.
	ExitCode int
}

// RunDeps resolves targetID's requires[kind=sensor] graph, validates every
// sensor against schemas/sensor.json, and iterates topologically — emitting
// per-dep aggregate (non-blocking via RunOne) or attach acks (blocking
// via AttachLiveDep). Cascade signals for intermediate deps are emitted
// on stdout during the loop. The root is NOT processed; caller handles
// it.
//
// Intermediate cascade: a non-blocking dep whose own dep failed gets a
// cascade signal emitted in stdout (metadata.kind=cascade), recorded in
// Signals, and processing continues. The cascade chain propagates: any
// dependent of the cascade-marked dep also cascades.
//
// Root cascade: when iteration finishes, if FirstFailedDep returns
// non-nil for the root sensor, BuildCascadeSignal is built but NOT
// emitted — returned in CascadeSig so the caller can wrap it (e.g.
// /start-sensor translates it to a `failed` signal with
// metadata.cause=dep_cascade).
func RunDeps(
	ctx context.Context,
	targetID, projectRoot, schemasDir, holderID string,
	holderPID int,
	v *schema.Validator,
	stdout, stderr io.Writer,
) *RunDepsResult {
	res := &RunDepsResult{
		Signals: map[string]map[string]interface{}{},
	}

	order, err := Resolve(targetID, projectRoot)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		res.ExitCode = 1
		return res
	}
	res.Order = order

	for _, s := range order {
		if err := v.Validate(schema.TargetSensor, s.JSON); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			res.ExitCode = 1
			return res
		}
	}

	for _, s := range order {
		if s.ID == targetID {
			continue
		}
		execMap, _ := s.JSON["execution"].(map[string]interface{})
		blocking, _ := execMap["blocking"].(bool)
		if blocking {
			result, attachErr := AttachLiveDep(ctx, s, projectRoot, holderID, holderPID, v, stdout, stderr)
			if attachErr != nil {
				cascade := buildSimpleSignal(targetID, "error", "high", "dep_start_failed", attachErr.Error())
				_ = json.NewEncoder(stdout).Encode(cascade)
				res.ExitCode = 1
				return res
			}
			if result.GateSignal != nil {
				// AttachLiveDep already emitted on stdout and validated.
				// Record so FirstFailedDep / BuildCascadeSignal propagate to
				// dependents (including the root) on later iterations.
				res.Signals[s.ID] = result.GateSignal
				continue
			}
			res.LiveStack = append(res.LiveStack, result.Live)
			res.Signals[s.ID] = map[string]interface{}{"verdict": "pass"}
			continue
		}
		if blocker := FirstFailedDep(s, res.Signals); blocker != nil {
			cascade := BuildCascadeSignal(s, blocker)
			if err := v.Validate(schema.TargetSignal, cascade); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				res.ExitCode = 1
				return res
			}
			_ = json.NewEncoder(stdout).Encode(cascade)
			res.Signals[s.ID] = cascade
			continue
		}
		sig, sigCode := RunOne(ctx, s, projectRoot, schemasDir, v, stdout, stderr)
		if sigCode != 0 {
			res.ExitCode = sigCode
			return res
		}
		res.Signals[s.ID] = sig
	}

	rootSensor := order[len(order)-1]
	if blocker := FirstFailedDep(rootSensor, res.Signals); blocker != nil {
		res.CascadeSig = BuildCascadeSignal(rootSensor, blocker)
	}
	return res
}

// RunPreparePhase runs prepare steps (requires[kind=step] via sensor.Project)
// fail-fast. Returns the per-step results (shaped for inclusion in
// metadata.lifecycle.prepare) and a bool indicating whether the phase failed
// (first non-pass step triggers fail-fast).
//
// projectRoot is forwarded to StepConfig.Dir so prepare steps run in the
// user's project directory, not in the runner's own cwd.
//
// Delegates to lifecycle.go::runPreparePhase so callers that need only the
// prepare phase (notably /start-sensor before its detached spawn) can run it
// without paying for command + teardown.
func RunPreparePhase(ctx context.Context, target Sensor, projectRoot string, defaultTimeoutMS int) (results []interface{}, failed bool) {
	return runPreparePhase(ctx, target.JSON, projectRoot, defaultTimeoutMS)
}
