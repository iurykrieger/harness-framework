package shell

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

// compileParsePatterns turns a *sensor.ParseConfig (raw, schema-shaped) into
// the []signal.Pattern that the streaming pipeline consumes. Compilation
// delegates to signal.CompilePatterns via a tiny raw-map shim so we share
// the single regex.Compile call site with stream.go.
func compileParsePatterns(p *sensor.ParseConfig) ([]signal.Pattern, error) {
	if p == nil || len(p.Patterns) == 0 {
		return nil, nil
	}
	raw := make([]interface{}, 0, len(p.Patterns))
	for _, pat := range p.Patterns {
		m := map[string]interface{}{
			"regex":    pat.Regex,
			"verdict":  pat.Verdict,
			"severity": pat.Severity,
		}
		if pat.Captures != nil {
			cap := map[string]interface{}{}
			if pat.Captures.File != nil {
				cap["file"] = *pat.Captures.File
			}
			if pat.Captures.LineStart != nil {
				cap["line_start"] = *pat.Captures.LineStart
			}
			if pat.Captures.LineEnd != nil {
				cap["line_end"] = *pat.Captures.LineEnd
			}
			if pat.Captures.Excerpt != nil {
				cap["excerpt"] = *pat.Captures.Excerpt
			}
			if pat.Captures.Rationale != nil {
				cap["rationale"] = *pat.Captures.Rationale
			}
			m["captures"] = cap
		}
		raw = append(raw, m)
	}
	return signal.CompilePatterns(raw)
}
