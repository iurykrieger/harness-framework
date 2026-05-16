// Package http implements the type: http step. It performs a single HTTP
// request through net/http, evaluates the response against the declarative
// expect: block (status, headers, body matchers), and extracts declared
// outputs. The PreflightGate invariant is satisfied upstream — the gate
// fires in orchestrator.RunOne before the engine ever reaches this step
// type. Per CLAUDE.md rule 6, deterministic helpers (URL/body/header
// rendering, matcher conversion, signal construction) live in this package
// alongside Execute.
package http

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
	"github.com/iurykrieger/harness-framework/lib/template"
)

// Step is the http-type Step implementation.
type Step struct {
	cfg sensor.StepConfig
}

// New constructs a Step from a YAML-decoded StepConfig. The config's Type
// must be "http"; Method and URL must be non-empty.
func New(cfg sensor.StepConfig) (step.Step, error) {
	if cfg.Type != "http" {
		return nil, fmt.Errorf("http.New: type=%q (want http)", cfg.Type)
	}
	if cfg.Method == "" {
		return nil, fmt.Errorf("http.New: method is required")
	}
	if cfg.URL == "" {
		return nil, fmt.Errorf("http.New: url is required")
	}
	return &Step{cfg: cfg}, nil
}

// ID returns the configured step id.
func (s *Step) ID() string { return s.cfg.ID }

// Type returns the discriminator string for the http step.
func (s *Step) Type() string { return "http" }

// Execute issues the configured HTTP request, evaluates expect:, captures
// the response on res.Response, extracts declared outputs, and emits a
// single signal with metadata.kind=http_observation.
func (s *Step) Execute(ctx context.Context, ec *step.ExecContext) *step.StepResult {
	res := &step.StepResult{
		Status:  step.StatusAborted,
		Verdict: signal.VerdictError,
		Outputs: map[string]string{},
	}

	actionsCtx := buildActionsContext(ec)

	url, err := template.RenderActions(s.cfg.URL, actionsCtx)
	if err != nil {
		res.Err = fmt.Errorf("render url: %w", err)
		return res
	}

	body, err := buildBody(s.cfg.BodyFrom, ec, actionsCtx)
	if err != nil {
		res.Err = fmt.Errorf("build body: %w", err)
		return res
	}

	req, err := http.NewRequestWithContext(ctx, s.cfg.Method, url, bytes.NewReader(body))
	if err != nil {
		res.Err = fmt.Errorf("new request: %w", err)
		return res
	}
	for k, v := range s.cfg.Headers {
		rv, err := template.RenderActions(v, actionsCtx)
		if err != nil {
			res.Err = fmt.Errorf("render header %q: %w", k, err)
			return res
		}
		req.Header.Set(k, rv)
	}

	timeout := 30 * time.Second
	if s.cfg.Timeout != "" {
		if d, err := time.ParseDuration(s.cfg.Timeout); err == nil {
			timeout = d
		}
	}

	client := &http.Client{Timeout: timeout}
	start := time.Now()
	resp, err := client.Do(req)
	dur := int(time.Since(start) / time.Millisecond)
	if err != nil {
		// Network error: emit one error-verdict signal so call sites have
		// uniform shape even when no response was captured.
		res.Err = err
		res.Verdict = signal.VerdictError
		res.Status = step.StatusCompleted
		res.Signals = []map[string]interface{}{
			buildHTTPSignal(s.cfg.ID, s.cfg.Method, url, 0, dur, signal.VerdictError, err.Error()),
		}
		return res
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	res.Response = &step.HttpResponse{
		Status:     resp.StatusCode,
		Body:       bodyBytes,
		Headers:    resp.Header,
		DurationMs: dur,
	}

	verdict, evidence := evalExpect(s.cfg.Expect, resp.StatusCode, bodyBytes, resp.Header)
	res.Verdict = verdict
	res.Status = step.StatusCompleted
	res.Signals = []map[string]interface{}{
		buildHTTPSignal(s.cfg.ID, s.cfg.Method, url, resp.StatusCode, dur, verdict, evidence),
	}

	// Outputs extraction from the captured response.
	src := step.OutputSource{
		ResponseBody:   bodyBytes,
		ResponseStatus: resp.StatusCode,
		ResponseDurMS:  dur,
		ResponseHeader: flattenHeaders(resp.Header),
	}
	for name, spec := range s.cfg.Outputs {
		v, err := step.ExtractOutput(step.OutputSpec{
			From: spec.From, Regex: spec.Regex, JSONPath: spec.JSONPath, Trim: spec.Trim,
		}, src)
		if err != nil {
			res.Err = fmt.Errorf("output %q: %w", name, err)
			res.Verdict = signal.VerdictError
			return res
		}
		res.Outputs[name] = v
	}
	return res
}

// buildHTTPSignal returns the single observation signal the http step
// emits. Schema-shape compatible with signal.yaml (verdict + metadata.kind
// + rationale) so downstream readers do not need a special case.
func buildHTTPSignal(stepID, method, url string, status, durMS int, verdict signal.Verdict, evidence string) map[string]interface{} {
	return map[string]interface{}{
		"verdict":  string(verdict),
		"severity": string(severityFor(verdict)),
		"evidence": []interface{}{
			map[string]interface{}{"rationale": evidence},
		},
		"metadata": map[string]interface{}{
			"kind":        "http_observation",
			"step_id":     stepID,
			"method":      method,
			"url":         url,
			"status":      status,
			"duration_ms": durMS,
		},
	}
}

// severityFor maps the verdict to a reasonable default severity for the
// observation signal. Step-level severity is informational; the aggregate
// signal computed by lib/exec picks the canonical sensor-level severity.
func severityFor(v signal.Verdict) signal.Severity {
	switch v {
	case signal.VerdictPass:
		return signal.SeverityInfo
	case signal.VerdictWarn:
		return signal.SeverityMedium
	case signal.VerdictFail:
		return signal.SeverityHigh
	case signal.VerdictError:
		return signal.SeverityCritical
	}
	return signal.SeverityInfo
}

// flattenHeaders converts an http.Header into a lower-cased single-value
// map suitable for OutputSource.ResponseHeader lookups by name.
func flattenHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, vs := range h {
		if len(vs) > 0 {
			out[strings.ToLower(k)] = vs[0]
		}
	}
	return out
}

// buildActionsContext converts an ExecContext into the read-only context
// the actions template uses for ${{ … }} resolution. Per-step subpackages
// keep their own copy of this small helper (rule of three: shell, http,
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
