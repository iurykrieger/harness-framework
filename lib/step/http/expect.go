package http

import (
	"fmt"
	"net/http"

	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
)

// evalExpect resolves the http step's expect: block against the captured
// response. expect may be nil (status-default verdict applies), a
// map[string]interface{} with optional status/headers/body sub-matchers, or
// a malformed shape (returns verdict=error).
//
// expect.body is the most flexible knob: either a single Matcher map (AND
// with status/headers) or an array of Matcher maps (ALL must pass).
func evalExpect(expect interface{}, status int, body []byte, headers http.Header) (signal.Verdict, string) {
	if expect == nil {
		return defaultVerdict(status), defaultEvidence(status)
	}
	m, ok := expect.(map[string]interface{})
	if !ok {
		return signal.VerdictError, "expect: unexpected shape (want map)"
	}

	if rawStatus, ok := m["status"]; ok {
		if !step.Match(matcherFrom(rawStatus), status) {
			return signal.VerdictFail, fmt.Sprintf("status %d did not match expectation", status)
		}
	}

	if rawHeaders, ok := m["headers"]; ok {
		h, ok := rawHeaders.(map[string]interface{})
		if !ok {
			return signal.VerdictError, "expect.headers: unexpected shape (want map)"
		}
		for k, v := range h {
			got := headers.Get(k)
			if !step.Match(matcherFrom(v), got) {
				return signal.VerdictFail, fmt.Sprintf("header %q did not match (got %q)", k, got)
			}
		}
	}

	if rawBody, ok := m["body"]; ok {
		switch x := rawBody.(type) {
		case map[string]interface{}:
			if !step.Match(matcherFrom(x), string(body)) {
				return signal.VerdictFail, "body did not match"
			}
		case []interface{}:
			for i, item := range x {
				if !step.Match(matcherFrom(item), string(body)) {
					return signal.VerdictFail, fmt.Sprintf("body[%d] did not match", i)
				}
			}
		default:
			return signal.VerdictError, "expect.body: unexpected shape (want map or array)"
		}
	}

	return signal.VerdictPass, "all expectations met"
}

// defaultVerdict applies when no expect: is declared: 2xx and 3xx pass,
// 4xx/5xx fail, anything else (would be unusual — 1xx, 0) is error.
func defaultVerdict(status int) signal.Verdict {
	switch {
	case status >= 200 && status < 400:
		return signal.VerdictPass
	case status >= 400 && status < 600:
		return signal.VerdictFail
	default:
		return signal.VerdictError
	}
}

func defaultEvidence(status int) string {
	return fmt.Sprintf("no expect; status=%d %s", status, http.StatusText(status))
}

// matcherFrom converts a YAML-decoded matcher (typically a
// map[string]interface{}) into a step.Matcher. A bare scalar collapses to
// {equals: <scalar>}, which is the lowest-friction form for short YAML.
// Per CLAUDE.md rule 4 (rule of three), this helper is duplicated in
// lib/step/assert; do not extract until a fourth caller appears.
func matcherFrom(in interface{}) step.Matcher {
	m, ok := in.(map[string]interface{})
	if !ok {
		return step.Matcher{Equals: in}
	}
	out := step.Matcher{}
	if v, ok := m["equals"]; ok {
		out.Equals = v
	}
	if v, ok := m["matches"].(string); ok {
		out.Matches = v
	}
	if v, ok := m["contains"].(string); ok {
		out.Contains = v
	}
	if f, ok := numericField(m["gte"]); ok {
		out.Gte = &f
	}
	if f, ok := numericField(m["lte"]); ok {
		out.Lte = &f
	}
	if v, ok := m["jsonpath"].(string); ok {
		out.JSONPath = v
	}
	if v, ok := m["type"].(string); ok {
		out.Type = v
	}
	if n, ok := intField(m["min_length"]); ok {
		out.MinLength = &n
	}
	if n, ok := intField(m["max_length"]); ok {
		out.MaxLength = &n
	}
	if v, ok := m["value"]; ok {
		out.Value = v
	}
	return out
}

// numericField accepts ints, floats, or numeric strings (rare). YAML
// integer literals decode to int via sigs.k8s.io/yaml, so int is the
// common case; JSON literals come in as float64.
func numericField(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func intField(v interface{}) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}
