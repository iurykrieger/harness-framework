package sensor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ResolveSensorPath strips a leading @, makes the path absolute (relative to
// baseDir), and verifies the file exists.
func ResolveSensorPath(arg, baseDir string) (string, error) {
	arg = strings.TrimPrefix(arg, "@")
	if arg == "" {
		return "", errors.New("empty path")
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
