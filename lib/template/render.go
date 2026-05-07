// Package template renders {{slot}} placeholders against a bindings map.
// It is the deterministic substitution helper used to fill an inferential
// sensor's user_prompt_template before the runner spawns the LLM CLI.
package template

import "regexp"

// slotPattern matches {{slot_name}} (whitespace tolerated).
var slotPattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}`)

// RenderTemplate substitutes {{slot}} placeholders. Returns the rendered text
// and the deduplicated list of slots that were referenced but not bound.
func RenderTemplate(tmpl string, bindings map[string]string) (string, []string) {
	var missing []string
	seen := map[string]bool{}
	rendered := slotPattern.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := slotPattern.FindStringSubmatch(match)[1]
		if val, ok := bindings[key]; ok {
			return val
		}
		if !seen[key] {
			seen[key] = true
			missing = append(missing, key)
		}
		return match
	})
	return rendered, missing
}
