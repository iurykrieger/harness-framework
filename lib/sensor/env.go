package sensor

import (
	"fmt"
	"os"
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

// BuildMissingEnvSignal is a thin wrapper around BuildRequiresGateSignal
// kept for backwards compatibility with call sites that still produce
// []MissingEnv. New code should call CheckRequiresGate + BuildRequiresGateSignal
// directly.
func BuildMissingEnvSignal(env Envelope, outputMode string, missing []MissingEnv) map[string]interface{} {
	gate := Gate{Failures: make([]Failure, 0, len(missing))}
	for _, m := range missing {
		gate.Failures = append(gate.Failures, Failure{
			Kind:       "env",
			Identifier: m.Name,
			Rationale:  missingEnvRationale(m),
			HealShape:  "missing-env",
		})
	}
	return BuildRequiresGateSignal(env, outputMode, gate)
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

