package sensor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// idRegex matches the sensor.id shape required by schemas/sensor.yaml.
var idRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Resolve returns the canonical absolute path for a sensor identified by
// a bare id ("my-sensor"), a prefixed path ("@.harness/sensors/my.yaml"),
// or a relative/absolute path. When idOrPath matches the id regex, it is
// resolved as <baseDir>/.harness/sensors/<id>.yaml; otherwise it is
// treated as a path (with @ removed, and relative paths resolved against
// baseDir).
//
// Returns descriptive errors for empty input, malformed id, path traversal,
// and missing files.
func Resolve(idOrPath, baseDir string) (string, error) {
	if idOrPath == "" {
		return "", errors.New("empty sensor reference")
	}
	if looksLikePath(idOrPath) {
		return resolvePath(idOrPath, baseDir)
	}
	if !idRegex.MatchString(idOrPath) {
		return "", fmt.Errorf("sensor id %q does not match ^[a-z][a-z0-9-]*$", idOrPath)
	}
	return resolveInDir(idOrPath, filepath.Join(baseDir, ".harness", "sensors"))
}

// resolveInDir is the internal helper used by the orchestrator: assumes
// that sensorRoot is already the directory containing <id>.yaml (does
// not append ".harness/sensors/" automatically).
//
// Lookup order:
//  1. Root tier: <sensorRoot>/<id>.yaml — the canonical platform-primitive
//     location populated by /detect-sensors.
//  2. Per-usecase tier: <sensorRoot>/*/<id>.yaml — the layer-bundle
//     location populated by /create-sensors. The walk is one directory
//     deep on purpose; sensors do not nest further. Exactly one match is
//     required: zero matches surfaces a clear not-found error;
//     two-or-more surfaces an ambiguous-id error so the operator
//     renames the colliding sensor.
func resolveInDir(id, sensorRoot string) (string, error) {
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid sensor id %q (no path separators)", id)
	}
	rootPath := filepath.Join(sensorRoot, id+".yaml")
	if _, err := os.Stat(rootPath); err == nil {
		return rootPath, nil
	}
	// Fall back to per-usecase bundles.
	matches, _ := filepath.Glob(filepath.Join(sensorRoot, "*", id+".yaml"))
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("sensor %q not found at %s (and no match in %s/*/%s.yaml)", id, rootPath, sensorRoot, id)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("sensor id %q is ambiguous: %d candidates under %s/*/%s.yaml", id, len(matches), sensorRoot, id)
	}
}

func resolvePath(arg, baseDir string) (string, error) {
	arg = strings.TrimPrefix(arg, "@")
	if arg == "" {
		return "", errors.New("empty path after trimming @")
	}
	if strings.Contains(arg, "..") {
		return "", fmt.Errorf("path traversal not allowed: %q", arg)
	}
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(baseDir, arg)
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "@") ||
		strings.ContainsAny(s, "/\\") ||
		strings.HasSuffix(s, ".yaml") || strings.HasSuffix(s, ".yml")
}
