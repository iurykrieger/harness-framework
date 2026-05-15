// lib/sensor/load.go
package sensor

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// Load reads a sensor YAML file at path, schema-validates it via v, and
// decodes the typed *Sensor. After decoding, Load normalizes the legacy
// `command:` shortcut into a single shell step under Execution.Steps so
// downstream consumers see one shape regardless of how the on-disk YAML
// was authored. The on-disk YAML is never rewritten by Load — callers
// reading the file directly still see the declared shape.
//
// Cross-field validation rules (cycle detection, output↔parse coherence,
// blocking↔steps exclusion, etc.) are intentionally NOT enforced here;
// they live in lib/sensor/validate.go (added in Task 9 of the
// complex-commands plan).
func Load(path string, v *schema.Validator) (*Sensor, error) {
	if v == nil {
		return nil, fmt.Errorf("Load: validator is required")
	}
	body, err := schema.ReadAsJSON(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal(body, &asMap); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := v.Validate(schema.TargetSensor, asMap); err != nil {
		return nil, fmt.Errorf("schema-invalid %s: %w", path, err)
	}
	var s Sensor
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	normalizeCommandShortcut(&s)
	return &s, nil
}

// normalizeCommandShortcut promotes the legacy execution.command field
// into a single-element execution.steps[] in memory. This is a pure
// in-memory convenience for the engine; the on-disk YAML is untouched.
//
// The id "main" is reserved for this synthetic step. Its exit_code_map
// is converted from the legacy []ExitCodeMapEntry slice (which carries
// per-entry severity) into a map[string]Verdict by dropping severity and
// stringifying the exit code; downstream consumers that need severity
// must consult Execution.ExitCodeMap directly.
func normalizeCommandShortcut(s *Sensor) {
	if s.Execution.Command == "" || len(s.Execution.Steps) > 0 {
		return
	}
	s.Execution.Steps = []StepConfig{{
		ID:          "main",
		Type:        "shell",
		Run:         s.Execution.Command,
		ExitCodeMap: legacyExitCodeMap(s.Execution.ExitCodeMap),
		Parse:       legacyParse(s.Execution.OutputParsing),
	}}
}

// legacyExitCodeMap converts the per-entry slice into the step shape's
// map keyed by stringified exit code. Wildcard "*" entries are preserved
// as the literal string key. Severity is discarded — the step shape does
// not carry it, and the runner can re-derive severity from the verdict
// when needed.
func legacyExitCodeMap(entries []ExitCodeMapEntry) map[string]Verdict {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]Verdict, len(entries))
	for _, e := range entries {
		var key string
		switch v := e.ExitCode.(type) {
		case string:
			key = v
		case float64:
			key = strconv.Itoa(int(v))
		case int:
			key = strconv.Itoa(v)
		default:
			continue
		}
		out[key] = Verdict(e.Verdict)
	}
	return out
}

// legacyParse copies output_parsing.patterns into the step shape's
// parse: block. Returns nil when no patterns are declared so the
// resulting step matches the canonical "no parse" form.
func legacyParse(op *OutputParsing) *ParseConfig {
	if op == nil || len(op.Patterns) == 0 {
		return nil
	}
	pats := make([]Pattern, len(op.Patterns))
	copy(pats, op.Patterns)
	return &ParseConfig{Patterns: pats}
}
