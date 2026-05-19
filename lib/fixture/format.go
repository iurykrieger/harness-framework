package fixture

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"strings"

	"sigs.k8s.io/yaml"
)

// formatPayload returns payload canonicalized for the structured-data
// extension ext (case-insensitive). Supported formats:
//
//	.json         → json.Indent, 2-space, original key order preserved.
//	.yaml / .yml  → sigs.k8s.io/yaml round-trip; canonical block style.
//	.xml          → xml.Encoder.Indent, 2-space; whitespace-only chardata
//	                between elements is dropped so re-indentation is clean.
//
// Any other extension (.txt, .jsonl, .log, .csv, …) and payloads that
// fail to parse as the declared format pass through unchanged — Write
// never validates payload shape.
func formatPayload(payload []byte, ext string) []byte {
	switch strings.ToLower(ext) {
	case ".json":
		return formatJSON(payload)
	case ".yaml", ".yml":
		return formatYAML(payload)
	case ".xml":
		return formatXML(payload)
	default:
		return payload
	}
}

func formatJSON(payload []byte) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, payload, "", "  "); err != nil {
		return payload
	}
	return ensureTrailingNewline(buf.Bytes())
}

// formatYAML normalizes a YAML payload by round-tripping it through
// sigs.k8s.io/yaml. Side effects of the round-trip: comments are
// dropped (the library is JSON-backed) and mapping keys are emitted in
// canonical (sorted) order. Both are documented project-wide trade-offs
// (see CLAUDE.md: "Comments in YAML artifacts are not preserved").
func formatYAML(payload []byte) []byte {
	if len(bytes.TrimSpace(payload)) == 0 {
		return payload
	}
	var v any
	if err := yaml.Unmarshal(payload, &v); err != nil {
		return payload
	}
	out, err := yaml.Marshal(v)
	if err != nil {
		return payload
	}
	return out
}

func formatXML(payload []byte) []byte {
	if len(bytes.TrimSpace(payload)) == 0 {
		return payload
	}
	dec := xml.NewDecoder(bytes.NewReader(payload))
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return payload
		}
		if cd, ok := tok.(xml.CharData); ok {
			if len(bytes.TrimSpace(cd)) == 0 {
				continue
			}
		}
		if err := enc.EncodeToken(tok); err != nil {
			return payload
		}
	}
	if err := enc.Close(); err != nil {
		return payload
	}
	return ensureTrailingNewline(buf.Bytes())
}

func ensureTrailingNewline(b []byte) []byte {
	if bytes.HasSuffix(b, []byte{'\n'}) {
		return b
	}
	out := make([]byte, len(b)+1)
	copy(out, b)
	out[len(b)] = '\n'
	return out
}
