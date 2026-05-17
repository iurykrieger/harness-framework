package http

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/step"
	"github.com/iurykrieger/harness-framework/lib/template"
)

// buildBody resolves the body_from union into the request body bytes.
// Exactly one of Fixture, Inline, or Template must be populated; otherwise
// the result is a nil/empty body (no body_from declared).
//
//   - Fixture: read the absolute file path from ec.Fixtures and return the
//     bytes verbatim. The fixture name MUST be in the pool — pool seeding
//     happens at engine entry via lib/fixture (Task 3).
//   - Inline: JSON-encode the value. Inline accepts any JSON-compatible
//     interface{} (string, number, map, array).
//   - Template: render through the actions template against actx and
//     return the rendered string as bytes.
func buildBody(b *sensor.BodyFromConfig, ec *step.ExecContext, actx template.ActionContext) ([]byte, error) {
	if b == nil {
		return nil, nil
	}
	declared := 0
	if b.Fixture != "" {
		declared++
	}
	if b.Template != "" {
		declared++
	}
	if b.Inline != nil {
		declared++
	}
	if declared == 0 {
		return nil, nil
	}
	if declared > 1 {
		return nil, fmt.Errorf("body_from: exactly one of {fixture, template, inline} must be set (declared %d)", declared)
	}

	switch {
	case b.Fixture != "":
		path, ok := ec.Fixtures[b.Fixture]
		if !ok {
			return nil, fmt.Errorf("body_from.fixture %q not in pool", b.Fixture)
		}
		return os.ReadFile(path)
	case b.Inline != nil:
		return json.Marshal(b.Inline)
	case b.Template != "":
		rendered, err := template.RenderActions(b.Template, actx)
		if err != nil {
			return nil, fmt.Errorf("body_from.template: %w", err)
		}
		return []byte(rendered), nil
	}
	return nil, nil
}
