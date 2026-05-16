// Package step defines the Step interface and the cross-package contract
// types used by every step implementation (shell, http, assert, sensor) and
// by the engine (lib/exec) that composes them.
//
// ExecContext is the read-mostly snapshot a step receives: the fixture pool
// (name → absolute path), the sealed env snapshot, and the results of every
// previously executed step in the current run. StepResult is what a Step
// returns: a verdict, a status (completed vs. aborted before producing
// observable behavior), declared outputs, captured stdout, optional HTTP
// response, and individual signals emitted by parse: patterns.
//
// SubrunFunc is the inversion-of-control hook the sensor step uses to invoke
// another sensor by ref without taking a direct dependency on the engine.
package step

import (
	"context"
	"net/http"

	"github.com/iurykrieger/harness-framework/lib/signal"
)

// ExecContext is the per-run scope a Step observes when it executes.
// Fixtures maps fixture names to absolute paths on disk. Env is the sealed
// environment snapshot (no live os.Environ access). Steps holds the result
// of every previously executed step in the current run, keyed by step ID.
type ExecContext struct {
	Fixtures map[string]string
	Env      map[string]string
	Steps    map[string]*StepResult
}

// StepResult is the outcome of a single Step.Execute call.
//
// Verdict is the canonical signal verdict (pass/warn/fail/error). Status is
// "completed" when the step ran to its natural end and "aborted" when an
// internal precondition (rendering, fixture resolution, output extraction)
// failed before the step produced observable behavior. Outputs is the
// declared step.outputs (post-extraction). Stdout is the verbatim stdout for
// step types that produce it (shell). Response is populated only by the
// http step. Signals is the list of individual signals produced by parse:
// patterns during streaming.
type StepResult struct {
	Verdict  signal.Verdict
	Status   string
	Outputs  map[string]string
	Stdout   string
	Response *HttpResponse
	Signals  []map[string]interface{}
	Err      error
}

// HttpResponse is the captured response from an http step.
type HttpResponse struct {
	Status     int
	Body       []byte
	Headers    http.Header
	DurationMs int
}

// Step is the contract every step type implements. ID is the user-declared
// step.id from sensor YAML; Type is the step type discriminator
// ("shell"|"http"|"assert"|"sensor").
type Step interface {
	ID() string
	Type() string
	Execute(ctx context.Context, ec *ExecContext) *StepResult
}

// SubrunFunc invokes another sensor (by ref) as a sub-run within a parent
// step. The sensor step uses this to compose without taking a direct
// dependency on the engine.
type SubrunFunc func(ctx context.Context, ref string, fixtures, env map[string]string) (*StepResult, error)

// Canonical status values for StepResult.Status.
const (
	StatusCompleted = "completed"
	StatusAborted   = "aborted"
)
