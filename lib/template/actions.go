package template

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ActionContext is the resolution scope for ${{ … }} accessors.
type ActionContext struct {
	Fixtures map[string]string
	Env      map[string]string
	Steps    map[string]ActionStep
}

// ActionStep is the snapshot of a previously executed step exposed to the
// renderer. Verdict and Severity are always populated; Outputs and Response
// are populated only when the step declared outputs or performed HTTP I/O.
type ActionStep struct {
	Verdict  string
	Severity string
	Outputs  map[string]string
	Response *ActionResponse
}

// ActionResponse is the HTTP response captured by an http step.
type ActionResponse struct {
	Status  int
	Headers map[string]string
}

// actionsPattern matches ${{ <expr> }} with whitespace tolerated.
var actionsPattern = regexp.MustCompile(`\$\{\{\s*([^}]+?)\s*\}\}`)

// accessorPattern matches one or more dot-separated identifiers, where each
// identifier is [a-zA-Z_][a-zA-Z0-9_-]*. Dots separate segments; no spaces,
// operators, parentheses, or quotes are allowed anywhere in the expression.
var accessorPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]*(\.[a-zA-Z_][a-zA-Z0-9_.-]*)*$`)

// RenderActions substitutes ${{ accessor }} placeholders in input. The
// expression inside the braces must be a single dot-separated identifier
// path. Any operator, function call, comparison, or whitespace-separated
// token is a render-time error.
func RenderActions(input string, c ActionContext) (string, error) {
	var firstErr error
	out := actionsPattern.ReplaceAllStringFunc(input, func(match string) string {
		if firstErr != nil {
			return match
		}
		expr := strings.TrimSpace(actionsPattern.FindStringSubmatch(match)[1])
		if !accessorPattern.MatchString(expr) {
			firstErr = fmt.Errorf("invalid accessor %q: only dot-separated identifiers are allowed", expr)
			return match
		}
		val, err := resolveAccessor(expr, c)
		if err != nil {
			firstErr = err
			return match
		}
		return val
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

func resolveAccessor(expr string, c ActionContext) (string, error) {
	parts := strings.SplitN(expr, ".", 2)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty accessor")
	}
	switch parts[0] {
	case "fixtures":
		if len(parts) < 2 || parts[1] == "" {
			return "", fmt.Errorf("fixtures accessor needs a name")
		}
		if p, ok := c.Fixtures[parts[1]]; ok {
			return p, nil
		}
		return "", fmt.Errorf("fixture %q not found", parts[1])
	case "env":
		if len(parts) < 2 || parts[1] == "" {
			return "", fmt.Errorf("env accessor needs a name")
		}
		if v, ok := c.Env[parts[1]]; ok {
			return v, nil
		}
		return "", fmt.Errorf("env var %q not in sealed snapshot", parts[1])
	case "steps":
		if len(parts) < 2 {
			return "", fmt.Errorf("steps accessor needs a step id")
		}
		rest := strings.SplitN(parts[1], ".", 2)
		stepID := rest[0]
		s, ok := c.Steps[stepID]
		if !ok {
			return "", fmt.Errorf("step %q has not run yet (or does not exist)", stepID)
		}
		if len(rest) < 2 {
			return "", fmt.Errorf("steps.%s accessor needs a field (verdict|severity|outputs.<k>|response.<k>)", stepID)
		}
		return resolveStepAccessor(stepID, rest[1], s)
	}
	return "", fmt.Errorf("unknown accessor root %q", parts[0])
}

func resolveStepAccessor(stepID, suffix string, s ActionStep) (string, error) {
	switch {
	case suffix == "verdict":
		return s.Verdict, nil
	case suffix == "severity":
		return s.Severity, nil
	case strings.HasPrefix(suffix, "outputs."):
		key := strings.TrimPrefix(suffix, "outputs.")
		v, ok := s.Outputs[key]
		if !ok {
			return "", fmt.Errorf("step %q did not declare output %q", stepID, key)
		}
		return v, nil
	case suffix == "response.status":
		if s.Response == nil {
			return "", fmt.Errorf("step %q is not an http step", stepID)
		}
		return strconv.Itoa(s.Response.Status), nil
	case strings.HasPrefix(suffix, "response.headers."):
		if s.Response == nil {
			return "", fmt.Errorf("step %q is not an http step", stepID)
		}
		h := strings.TrimPrefix(suffix, "response.headers.")
		if v, ok := s.Response.Headers[h]; ok {
			return v, nil
		}
		return "", fmt.Errorf("step %q response has no header %q", stepID, h)
	}
	return "", fmt.Errorf("steps.%s.%s is not a valid accessor", stepID, suffix)
}
