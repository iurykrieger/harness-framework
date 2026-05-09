// lib/heal/version.go
package heal

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// BumpPatch parses sensor JSON, increments the patch component of its
// version (M.m.p → M.m.p+1), and returns the re-marshalled bytes.
// Returns an error when the version is missing or malformed.
func BumpPatch(sensorJSON []byte) ([]byte, error) {
	var s map[string]interface{}
	if err := json.Unmarshal(sensorJSON, &s); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	v, ok := s["version"].(string)
	if !ok {
		return nil, fmt.Errorf("version missing or not string")
	}
	bumped, err := bumpSemverPatch(v)
	if err != nil {
		return nil, err
	}
	s["version"] = bumped
	return json.Marshal(s)
}

var semverRegex = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

func bumpSemverPatch(v string) (string, error) {
	m := semverRegex.FindStringSubmatch(v)
	if m == nil {
		return "", fmt.Errorf("malformed version %q", v)
	}
	patch, err := strconv.Atoi(m[3])
	if err != nil {
		return "", fmt.Errorf("malformed version %q", v)
	}
	return fmt.Sprintf("%s.%s.%d", m[1], m[2], patch+1), nil
}
