// lib/sensor/fixture.go
package sensor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FixturePathEscapeError is returned by WriteFixture when relPath, after
// cleaning, resolves outside <projectRoot>/.harness/sensors/fixtures/.
type FixturePathEscapeError struct {
	Rel          string
	FixturesRoot string
}

func (e *FixturePathEscapeError) Error() string {
	return fmt.Sprintf("path %q resolves outside %s", e.Rel, e.FixturesRoot)
}

// WriteFixture is the single entrypoint for atomically writing a fixture
// payload under <projectRoot>/.harness/sensors/fixtures/. The relPath is
// relative to projectRoot; both projectRoot and relPath are required.
//
// Rejects (with *FixturePathEscapeError) any relPath that, after
// filepath.Clean, does not have <projectRoot>/.harness/sensors/fixtures/
// as a prefix, and rejects relPaths that resolve exactly to the fixtures
// root (no file name).
//
// Creates parent directories with mode 0o755. Writes atomically via
// tmp+rename. Idempotent: re-writing the same content to the same path
// is allowed.
//
// On success, returns the absolute path of the written file.
func WriteFixture(projectRoot, relPath string, payload []byte) (string, error) {
	if projectRoot == "" {
		return "", fmt.Errorf("WriteFixture: projectRoot is required")
	}
	if relPath == "" {
		return "", fmt.Errorf("WriteFixture: relPath is required")
	}

	fixturesRoot := filepath.Join(projectRoot, ".harness", "sensors", "fixtures")
	target := filepath.Clean(filepath.Join(projectRoot, relPath))
	sep := string(os.PathSeparator)
	if target == fixturesRoot ||
		!strings.HasPrefix(target+sep, fixturesRoot+sep) {
		return "", &FixturePathEscapeError{Rel: relPath, FixturesRoot: fixturesRoot}
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-fixture-*")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename: %w", err)
	}
	return target, nil
}
