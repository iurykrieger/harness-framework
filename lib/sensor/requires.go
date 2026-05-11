package sensor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Failure describes a single unmet precondition detected by CheckRequiresGate.
type Failure struct {
	Kind       string
	Identifier string
	Rationale  string
	HealShape  string
}

// Gate collects zero or more Failures discovered when scanning requires[].
type Gate struct{ Failures []Failure }

// Failed reports whether any precondition is unmet.
func (g Gate) Failed() bool { return len(g.Failures) > 0 }

// GateOpts carries injectable hooks for CheckRequiresGate so tests can
// substitute the OS calls without mutating process state.
type GateOpts struct {
	LookupEnv func(string) (string, bool)
	LookPath  func(string) (string, error)
	Stat      func(string) error
}

// resolveOpts fills in nil hooks with the real OS implementations.
func resolveOpts(opts GateOpts) GateOpts {
	if opts.LookupEnv == nil {
		opts.LookupEnv = LookupEnvFn
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	if opts.Stat == nil {
		opts.Stat = statHelper
	}
	return opts
}

// statHelper wraps os.Stat and returns only the error.
func statHelper(path string) error {
	_, err := os.Stat(path)
	return err
}

// CheckRequiresGate scans requires[] for tool, context, and env entries and
// returns a Gate whose Failures are ordered tool → context → env, with
// within-kind order matching the requires[] array position.
//
// Entries with kind=sensor, kind=step, kind=permission are silently ignored —
// those are handled by other parts of the harness (orchestrator DAG, prepare
// phase, Claude Code's permission engine respectively).
func CheckRequiresGate(sensorJSON map[string]interface{}, opts GateOpts) Gate {
	opts = resolveOpts(opts)
	var g Gate
	g.Failures = append(g.Failures, checkTool(sensorJSON, opts)...)
	g.Failures = append(g.Failures, checkContext(sensorJSON, opts)...)
	g.Failures = append(g.Failures, checkEnv(sensorJSON, opts)...)
	return g
}

// checkTool returns a Failure for each requires[kind=tool] entry whose name
// is not findable via opts.LookPath.
func checkTool(sensorJSON map[string]interface{}, opts GateOpts) []Failure {
	entries := Project(sensorJSON, "tool")
	var out []Failure
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		if name == "" {
			continue
		}
		if _, err := opts.LookPath(name); err == nil {
			continue
		}
		out = append(out, Failure{
			Kind:       "tool",
			Identifier: name,
			Rationale:  fmt.Sprintf(`Required tool %q is not on PATH`, name),
			HealShape:  "binary-not-found",
		})
	}
	return out
}

// checkContext returns a Failure for each requires[kind=context] entry whose
// path cannot be stat'd.
func checkContext(sensorJSON map[string]interface{}, opts GateOpts) []Failure {
	entries := Project(sensorJSON, "context")
	var out []Failure
	for _, entry := range entries {
		path, _ := entry["path"].(string)
		if path == "" {
			continue
		}
		err := opts.Stat(path)
		if err == nil {
			continue
		}
		rationale := fmt.Sprintf(`Required context path %q does not exist`, path)
		if !errors.Is(err, os.ErrNotExist) {
			rationale = fmt.Sprintf(`Required context path %q: cannot stat: %v`, path, err)
		}
		out = append(out, Failure{
			Kind:       "context",
			Identifier: path,
			Rationale:  rationale,
			HealShape:  "missing-context",
		})
	}
	return out
}

// checkEnv returns a Failure for each requires[kind=env] entry that is
// non-optional and not set in the environment.
func checkEnv(sensorJSON map[string]interface{}, opts GateOpts) []Failure {
	entries := Project(sensorJSON, "env")
	var out []Failure
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		if name == "" {
			continue
		}
		optional, _ := entry["optional"].(bool)
		if optional {
			continue
		}
		if _, set := opts.LookupEnv(name); set {
			continue
		}
		description, _ := entry["description"].(string)
		rationale := fmt.Sprintf("Required environment variable %s is not set", name)
		if description != "" {
			rationale = rationale + ": " + description
		}
		out = append(out, Failure{
			Kind:       "env",
			Identifier: name,
			Rationale:  rationale,
			HealShape:  "missing-env",
		})
	}
	return out
}

// BuildRequiresGateSignal constructs the verdict=error aggregate Signal
// emitted when CheckRequiresGate returns a non-empty Gate.
//
// The Signal contains one evidence entry per Failure, metadata.heal_hint
// shaped from the FIRST failure (to drive /heal-sensor routing), and a
// remediation listing all failures.
func BuildRequiresGateSignal(env Envelope, outputMode string, gate Gate) map[string]interface{} {
	finished := NowFn().Format("2006-01-02T15:04:05Z")
	evidence := make([]interface{}, 0, len(gate.Failures))
	for _, f := range gate.Failures {
		evidence = append(evidence, map[string]interface{}{
			"rationale": f.Rationale,
		})
	}
	md := map[string]interface{}{
		"kind":        "aggregate",
		"output_mode": outputMode,
	}
	if len(gate.Failures) > 0 {
		first := gate.Failures[0]
		md["heal_hint"] = first.HealShape + ":" + first.Identifier
	}
	sig := map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": finished,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
	if rem := buildRequiresGateRemediation(gate); rem != "" {
		sig["remediation"] = map[string]interface{}{"instructions": rem}
	}
	return sig
}

// buildRequiresGateRemediation produces the aggregate-level remediation string
// listing actionable steps for each failure. Returns "" when the gate is empty.
func buildRequiresGateRemediation(gate Gate) string {
	if len(gate.Failures) == 0 {
		return ""
	}
	parts := make([]string, 0, len(gate.Failures))
	for _, f := range gate.Failures {
		switch f.Kind {
		case "tool":
			parts = append(parts, fmt.Sprintf(`install or expose %q on PATH`, f.Identifier))
		case "context":
			parts = append(parts, fmt.Sprintf(`create the required path %q`, f.Identifier))
		case "env":
			parts = append(parts, fmt.Sprintf(`set env %s`, f.Identifier))
		}
	}
	return "Resolve the following preconditions before re-running: " + strings.Join(parts, "; ") + "."
}
