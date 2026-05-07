// Package lib — patterns.go compiles output_parsing.patterns[] into Go regex
// objects and matches a single subprocess output line, extracting capture
// groups into a structured PatternMatch.
package lib

import (
	"fmt"
	"regexp"
	"strconv"
)

// Pattern is a compiled output_parsing.patterns[] entry.
type Pattern struct {
	Regex    *regexp.Regexp
	Verdict  string
	Severity string
	Captures map[string]int // evidence field name -> 1-based capture-group index
}

// CompilePatterns turns a raw output_parsing.patterns array (as parsed from
// JSON: []interface{} of map[string]interface{}) into compiled Patterns.
func CompilePatterns(raw []interface{}) ([]Pattern, error) {
	out := make([]Pattern, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("pattern[%d]: not an object", i)
		}
		rawRegex, _ := m["regex"].(string)
		re, err := regexp.Compile(rawRegex)
		if err != nil {
			return nil, fmt.Errorf("pattern[%d] regex: %w", i, err)
		}
		verdict, _ := m["verdict"].(string)
		severity, _ := m["severity"].(string)
		captures := map[string]int{}
		if cap, ok := m["captures"].(map[string]interface{}); ok {
			for k, v := range cap {
				switch n := v.(type) {
				case float64:
					captures[k] = int(n)
				case int:
					captures[k] = n
				}
			}
		}
		out = append(out, Pattern{Regex: re, Verdict: verdict, Severity: severity, Captures: captures})
	}
	return out, nil
}

// PatternMatch is the result of a successful pattern match against a line.
type PatternMatch struct {
	Verdict   string
	Severity  string
	File      string
	LineStart *int // pointer so omission is distinguishable from zero
	LineEnd   *int
	Excerpt   string
	Rationale string
	Line      string // raw matched line
}

// MatchLine walks patterns in order; first match wins. Returns ok=false if no
// pattern matched. When a match has no `rationale` capture, Rationale falls
// back to the whole line.
func MatchLine(line string, patterns []Pattern) (PatternMatch, bool) {
	for _, p := range patterns {
		m := p.Regex.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		match := PatternMatch{
			Verdict:  p.Verdict,
			Severity: p.Severity,
			Line:     line,
		}
		if idx, ok := p.Captures["file"]; ok && idx < len(m) {
			match.File = m[idx]
		}
		if idx, ok := p.Captures["excerpt"]; ok && idx < len(m) {
			match.Excerpt = m[idx]
		}
		if idx, ok := p.Captures["rationale"]; ok && idx < len(m) {
			match.Rationale = m[idx]
		} else {
			match.Rationale = line
		}
		if idx, ok := p.Captures["line_start"]; ok && idx < len(m) {
			if n, err := strconv.Atoi(m[idx]); err == nil {
				match.LineStart = &n
			}
		}
		if idx, ok := p.Captures["line_end"]; ok && idx < len(m) {
			if n, err := strconv.Atoi(m[idx]); err == nil {
				match.LineEnd = &n
			}
		}
		return match, true
	}
	return PatternMatch{}, false
}
