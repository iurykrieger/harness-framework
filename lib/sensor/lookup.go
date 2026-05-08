package sensor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FindSensorByID resolves a bare sensor id (e.g. "start-postgres") to its
// canonical file path under sensorRoot ("<sensorRoot>/<id>.json"). Returns
// an error if the file does not exist or the id contains path separators.
func FindSensorByID(id, sensorRoot string) (string, error) {
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid sensor id %q (no path separators)", id)
	}
	path := filepath.Join(sensorRoot, id+".json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("sensor %q not found at %s: %w", id, path, err)
	}
	return path, nil
}
