package step

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// OutputSpec mirrors $defs/StepOutputs[*]: a From source plus exactly one
// modifier (Regex|JSONPath|Trim), or no modifier for raw passthrough.
type OutputSpec struct {
	From     string
	Regex    string
	JSONPath string
	Trim     bool
}

// OutputSource carries every channel a step can extract from. Fields not
// relevant to the chosen From are zero.
type OutputSource struct {
	Stdout         string
	Stderr         string
	ResponseBody   []byte
	ResponseStatus int
	ResponseDurMS  int
	ResponseHeader map[string]string
}

// ExtractOutput resolves one OutputSpec against the source. Returns an
// error when extraction fails: unsupported From, regex compile error,
// regex no-match, jsonpath against non-JSON, jsonpath no-result.
//
// Regex with at least one capture group returns the first group; with no
// groups it returns the full match.
func ExtractOutput(spec OutputSpec, src OutputSource) (string, error) {
	raw, err := selectFrom(spec.From, src)
	if err != nil {
		return "", err
	}
	switch {
	case spec.Regex != "":
		// Compile in multiline mode so ^/$ anchor per-line, matching the
		// convention shell tools follow when emitting line-oriented stdout.
		re, err := regexp.Compile("(?m)" + spec.Regex)
		if err != nil {
			return "", fmt.Errorf("regex compile: %w", err)
		}
		m := re.FindStringSubmatch(raw)
		if m == nil {
			return "", fmt.Errorf("regex %q produced no match against From=%q", spec.Regex, spec.From)
		}
		if len(m) < 2 {
			return m[0], nil
		}
		return m[1], nil
	case spec.JSONPath != "":
		if !json.Valid([]byte(raw)) {
			return "", fmt.Errorf("jsonpath requested but From=%q is not valid JSON", spec.From)
		}
		path := strings.TrimPrefix(spec.JSONPath, "$.")
		res := gjson.Get(raw, path)
		if !res.Exists() {
			return "", fmt.Errorf("jsonpath %q produced no result", spec.JSONPath)
		}
		return res.String(), nil
	case spec.Trim:
		return strings.TrimSpace(raw), nil
	}
	return raw, nil
}

func selectFrom(from string, src OutputSource) (string, error) {
	switch {
	case from == "stdout":
		return src.Stdout, nil
	case from == "stderr":
		return src.Stderr, nil
	case from == "response.body":
		return string(src.ResponseBody), nil
	case from == "response.status":
		return strconv.Itoa(src.ResponseStatus), nil
	case from == "response.duration_ms":
		return strconv.Itoa(src.ResponseDurMS), nil
	case strings.HasPrefix(from, "response.headers."):
		h := strings.TrimPrefix(from, "response.headers.")
		if v, ok := src.ResponseHeader[h]; ok {
			return v, nil
		}
		return "", fmt.Errorf("response has no header %q", h)
	}
	return "", fmt.Errorf("unsupported From=%q", from)
}

// extractJSONPath is the matcher's sibling helper. It accepts the value
// the matcher is comparing against (string or []byte), parses it as JSON,
// applies the path, and returns the extracted scalar as a string.
func extractJSONPath(value interface{}, path string) (interface{}, bool) {
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case []byte:
		raw = string(v)
	default:
		return nil, false
	}
	if !json.Valid([]byte(raw)) {
		return nil, false
	}
	res := gjson.Get(raw, strings.TrimPrefix(path, "$."))
	if !res.Exists() {
		return nil, false
	}
	return res.String(), true
}
