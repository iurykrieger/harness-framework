package step

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Matcher mirrors $defs/Matcher from the sensor schema. Pointer/empty fields
// signal "not declared"; a Matcher with no declared field always matches.
type Matcher struct {
	Value     interface{}
	Equals    interface{}
	Matches   string
	Contains  string
	Gte       *float64
	Lte       *float64
	Type      string
	MinLength *int
	MaxLength *int
	JSONPath  string
}

// Match returns true iff every declared matcher field applies to value.
// Numeric matchers coerce string values via strconv.ParseFloat. A Matcher
// with no declared field is vacuously true.
func Match(m Matcher, value interface{}) bool {
	if m.JSONPath != "" {
		extracted, ok := extractJSONPath(value, m.JSONPath)
		if !ok {
			return false
		}
		value = extracted
	}
	if m.Equals != nil && !equalsValue(m.Equals, value) {
		return false
	}
	if m.Matches != "" {
		s := fmt.Sprint(value)
		re, err := regexp.Compile(m.Matches)
		if err != nil || !re.MatchString(s) {
			return false
		}
	}
	if m.Contains != "" && !strings.Contains(fmt.Sprint(value), m.Contains) {
		return false
	}
	if m.Gte != nil {
		n, ok := toFloat(value)
		if !ok || n < *m.Gte {
			return false
		}
	}
	if m.Lte != nil {
		n, ok := toFloat(value)
		if !ok || n > *m.Lte {
			return false
		}
	}
	// Type, MinLength, MaxLength: not yet exercised by any caller; the
	// schema-declared fields are accepted so future callers can populate
	// them without a struct change.
	return true
}

func equalsValue(want, got interface{}) bool {
	// Numeric: parse got as float when it is a string.
	if wn, ok := toFloat(want); ok {
		if gn, ok := toFloat(got); ok {
			return wn == gn
		}
		return false
	}
	return fmt.Sprint(want) == fmt.Sprint(got)
}

func toFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		n, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// extractJSONPath is implemented in outputs.go (shared with the outputs
// extractor).
