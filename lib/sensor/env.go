package sensor

import (
	"fmt"
	"os"
	"strings"
)

// MissingEnv names a required env var that was declared by requires[kind=env]
// but is not present in the runner's process environment. The runner uses this
// to emit a verdict=error Signal with a remediation that names the missing var.
type MissingEnv struct {
	Name        string
	Description string
}

// LookupEnvFn is the package-level hook that CheckRequiredEnv consults. Tests
// override it to inject a synthetic environment without mutating the process.
var LookupEnvFn = os.LookupEnv

// CheckRequiredEnv reads requires[kind=env] entries and returns the list
// of NON-OPTIONAL env vars whose names are missing from the runner's
// environment. Optional vars are skipped — they may legitimately be unset.
//
// The function never panics on malformed input; entries that do not match the
// schema (missing name, wrong type) are silently ignored. Schema validation
// is the caller's responsibility (the runner runs it first).
func CheckRequiredEnv(s map[string]interface{}) []MissingEnv {
	entries := Project(s, "env")
	if len(entries) == 0 {
		return nil
	}
	var missing []MissingEnv
	for _, m := range entries {
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		optional, _ := m["optional"].(bool)
		if optional {
			continue
		}
		if _, set := LookupEnvFn(name); set {
			continue
		}
		description, _ := m["description"].(string)
		missing = append(missing, MissingEnv{Name: name, Description: description})
	}
	return missing
}

// BuildMissingEnvSignal constructs the verdict=error aggregate Signal the
// runner emits when requires[kind=env] declares non-optional vars that are
// absent from the runner's environment.
//
// The returned Signal has ONE evidence entry per missing var (not one block
// listing all), so heal/rules/missing_env's per-entry regex walker can fire
// on each var independently. Each rationale is shaped as:
//
//	Required environment variable NAME is not set: <description>
//
// (The trailing ": <description>" is omitted when description is empty.)
//
// The aggregate-level remediation lists all missing var names so the agent
// can see the full set in one place.
func BuildMissingEnvSignal(env Envelope, outputMode string, missing []MissingEnv) map[string]interface{} {
	finished := NowFn().Format("2006-01-02T15:04:05Z")
	evidence := make([]interface{}, 0, len(missing))
	for _, m := range missing {
		evidence = append(evidence, map[string]interface{}{
			"rationale": missingEnvRationale(m),
		})
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
		"metadata": map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": outputMode,
		},
	}
	if rem := missingEnvRemediation(missing); rem != "" {
		sig["remediation"] = map[string]interface{}{"instructions": rem}
	}
	return sig
}

// missingEnvRationale produces a single evidence rationale string for a
// missing env var. Format is locked by lib/heal/rules.missingEnvRegex —
// the heal classifier walks evidence[] entries and matches the per-var
// phrasing "Required environment variable NAME is not set" verbatim.
func missingEnvRationale(m MissingEnv) string {
	if m.Description != "" {
		return fmt.Sprintf("Required environment variable %s is not set: %s", m.Name, m.Description)
	}
	return fmt.Sprintf("Required environment variable %s is not set", m.Name)
}

// missingEnvRemediation produces the aggregate-level remediation string
// listing every missing var name. Returns "" when missing is empty.
func missingEnvRemediation(missing []MissingEnv) string {
	if len(missing) == 0 {
		return ""
	}
	names := make([]string, 0, len(missing))
	for _, m := range missing {
		names = append(names, m.Name)
	}
	return "Set the following env var(s) before invoking /run-sensor: " + strings.Join(names, ", ") + ". Source them from your shell, a .env file, or the secret manager backing this project."
}
