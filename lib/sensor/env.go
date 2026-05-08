package sensor

import "os"

// MissingEnv names a required env var that was declared by sensor.requires.env
// but is not present in the runner's process environment. The runner uses this
// to emit a verdict=error Signal with a remediation that names the missing var.
type MissingEnv struct {
	Name        string
	Description string
}

// LookupEnvFn is the package-level hook that CheckRequiredEnv consults. Tests
// override it to inject a synthetic environment without mutating the process.
var LookupEnvFn = os.LookupEnv

// CheckRequiredEnv reads sensor.requires.env (if present) and returns the list
// of NON-OPTIONAL env vars whose names are missing from the runner's
// environment. Optional vars are skipped — they may legitimately be unset.
//
// The function never panics on malformed input; entries that do not match the
// schema (missing name, wrong type) are silently ignored. Schema validation
// is the caller's responsibility (the runner runs it first).
func CheckRequiredEnv(sensor map[string]interface{}) []MissingEnv {
	requires, _ := sensor["requires"].(map[string]interface{})
	if requires == nil {
		return nil
	}
	envSpec, _ := requires["env"].([]interface{})
	if len(envSpec) == 0 {
		return nil
	}
	var missing []MissingEnv
	for _, item := range envSpec {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
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
