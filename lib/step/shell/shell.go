// Package shell implements the type: shell step. Streaming uses
// subprocess.Start + StreamHandle.Run so the step's parse: patterns can
// drain stdout and emit individual signals. The PreflightGate invariant
// covers this site by allowlist (lib/step/shell/shell.go in
// lib/orchestrator/gate_invariant_test.go) — the gate fires upstream in
// orchestrator.RunOne before the engine ever reaches this primitive.
package shell

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
	"github.com/iurykrieger/harness-framework/lib/template"
)

// Step is the shell-type Step implementation.
type Step struct {
	cfg sensor.StepConfig
}

// New constructs a Step from a YAML-decoded StepConfig. The config's Type
// must be "shell" and Run must be non-empty.
func New(cfg sensor.StepConfig) (step.Step, error) {
	if cfg.Type != "shell" {
		return nil, fmt.Errorf("shell.New: type=%q (want shell)", cfg.Type)
	}
	if cfg.Run == "" {
		return nil, fmt.Errorf("shell.New: run is required")
	}
	return &Step{cfg: cfg}, nil
}

// ID returns the configured step id.
func (s *Step) ID() string { return s.cfg.ID }

// Type returns the discriminator string for the shell step.
func (s *Step) Type() string { return "shell" }

// Execute runs the shell command after rendering the script through the
// actions template and resolving the with: block into env variables. parse:
// patterns are streamed line-by-line through subprocess.Start+Run; declared
// outputs are extracted post-run.
//
// The subprocess's verbatim stdout+stderr is captured via a temp RunDir
// (raw.log) so res.Stdout reflects what the program printed, independent of
// the JSONL signal stream that the streamer emits to its own Stdout writer.
func (s *Step) Execute(ctx context.Context, ec *step.ExecContext) *step.StepResult {
	res := &step.StepResult{
		Status:  step.StatusAborted,
		Verdict: signal.VerdictError,
		Outputs: map[string]string{},
	}

	actionsCtx := buildActionsContext(ec)
	rendered, err := template.RenderActions(s.cfg.Run, actionsCtx)
	if err != nil {
		res.Err = fmt.Errorf("render run: %w", err)
		return res
	}

	extraEnv, err := resolveWith(s.cfg.With, ec, actionsCtx)
	if err != nil {
		res.Err = err
		return res
	}
	envMap := mergeEnv(ec.Env, extraEnv)

	patterns, err := compileParsePatterns(s.cfg.Parse)
	if err != nil {
		res.Err = fmt.Errorf("compile parse patterns: %w", err)
		return res
	}

	// Allocate a temp RunDir so the streamer tees subprocess stdout+stderr
	// into raw.log. We read raw.log back into res.Stdout, then clean up.
	runDir, err := os.MkdirTemp("", "harness-shell-")
	if err != nil {
		res.Err = fmt.Errorf("mkdtemp: %w", err)
		return res
	}
	defer os.RemoveAll(runDir)

	var jsonlSink, errSink bytes.Buffer
	cfg := subprocess.StreamConfig{
		Command:  rendered,
		Env:      envMap,
		Patterns: patterns,
		Stdout:   &jsonlSink,
		Stderr:   &errSink,
		RunDir:   runDir,
		Dir:      ec.Cwd,
	}
	handle, err := subprocess.Start(ctx, cfg)
	if err != nil {
		res.Err = fmt.Errorf("subprocess.Start: %w", err)
		return res
	}
	sr := handle.Run()
	res.Status = step.StatusCompleted
	if raw, rerr := os.ReadFile(filepath.Join(runDir, "raw.log")); rerr == nil {
		res.Stdout = string(raw)
	}
	res.Stderr = sr.StderrExcerpt
	res.Signals = sr.Individuals

	// Map exit code to verdict, then fold the worst pattern verdict.
	res.Verdict = mapExit(s.cfg.ExitCodeMap, sr.ExitCode)
	if streamV, _ := signal.MaxStreamVerdict(sr.Individuals); streamV != "" {
		res.Verdict = worst(res.Verdict, signal.Verdict(streamV))
	}

	// Extract declared outputs.
	src := step.OutputSource{Stdout: res.Stdout, Stderr: sr.StderrExcerpt}
	for name, spec := range s.cfg.Outputs {
		v, err := step.ExtractOutput(step.OutputSpec{
			From: spec.From, Regex: spec.Regex, JSONPath: spec.JSONPath, Trim: spec.Trim,
		}, src)
		if err != nil {
			res.Err = fmt.Errorf("extract output %q: %w", name, err)
			res.Verdict = signal.VerdictError
			return res
		}
		res.Outputs[name] = v
	}
	return res
}

// resolveWith expands the step's with: block into an env-var injection map.
// Scalar values become HARNESS_INPUT_<UPPER_KEY>=<rendered>. A {fixture: name}
// value becomes HARNESS_FIXTURE_<UPPER_NAME>=<abs-path>; the first fixture
// also seeds HARNESS_FIXTURE_PATH (singular) for the common case where the
// step only consumes one fixture.
func resolveWith(with map[string]interface{}, ec *step.ExecContext, actionsCtx template.ActionContext) (map[string]string, error) {
	out := map[string]string{}
	var firstFixture string
	for k, v := range with {
		switch x := v.(type) {
		case string:
			rendered, err := template.RenderActions(x, actionsCtx)
			if err != nil {
				return nil, fmt.Errorf("with[%q]: %w", k, err)
			}
			out["HARNESS_INPUT_"+envKey(k)] = rendered
		case map[string]interface{}:
			name, _ := x["fixture"].(string)
			if name == "" {
				return nil, fmt.Errorf("with[%q]: object form must declare fixture", k)
			}
			abs, ok := ec.Fixtures[name]
			if !ok {
				return nil, fmt.Errorf("with[%q]: fixture %q not in pool", k, name)
			}
			out["HARNESS_FIXTURE_"+envKey(name)] = abs
			if firstFixture == "" {
				firstFixture = abs
			}
		default:
			out["HARNESS_INPUT_"+envKey(k)] = fmt.Sprint(v)
		}
	}
	if firstFixture != "" {
		out["HARNESS_FIXTURE_PATH"] = firstFixture
	}
	return out, nil
}

// envKey upper-cases the identifier and converts dashes to underscores so
// it is a valid shell env-var name.
func envKey(k string) string {
	return strings.ToUpper(strings.ReplaceAll(k, "-", "_"))
}

func mergeEnv(base, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// mapExit looks up the exit code in the step's ExitCodeMap and returns the
// mapped verdict. With an unset map (or no entry for the code), the default
// is pass for 0 and fail otherwise.
func mapExit(m map[string]sensor.Verdict, code int) signal.Verdict {
	if v, ok := m[strconv.Itoa(code)]; ok {
		return signal.Verdict(v)
	}
	if code == 0 {
		return signal.VerdictPass
	}
	return signal.VerdictFail
}

// worst returns the higher-rank verdict per the pass < warn < fail < error
// ordering used everywhere else in the framework.
func worst(a, b signal.Verdict) signal.Verdict {
	if signal.VerdictRank[string(b)] > signal.VerdictRank[string(a)] {
		return b
	}
	return a
}

// buildActionsContext converts an ExecContext into the read-only context
// the actions template uses for ${{ … }} resolution. Per-step subpackages
// keep their own copy of this small helper (Rule of three: shell, http,
// assert, sensor each duplicate until a shared signature emerges).
func buildActionsContext(ec *step.ExecContext) template.ActionContext {
	out := template.ActionContext{
		Fixtures: ec.Fixtures,
		Env:      ec.Env,
		Steps:    map[string]template.ActionStep{},
	}
	for id, sr := range ec.Steps {
		as := template.ActionStep{
			Verdict: string(sr.Verdict),
			Outputs: sr.Outputs,
		}
		if sr.Response != nil {
			headers := map[string]string{}
			for k, vs := range sr.Response.Headers {
				if len(vs) > 0 {
					headers[strings.ToLower(k)] = vs[0]
				}
			}
			as.Response = &template.ActionResponse{
				Status:  sr.Response.Status,
				Headers: headers,
			}
		}
		out.Steps[id] = as
	}
	return out
}
