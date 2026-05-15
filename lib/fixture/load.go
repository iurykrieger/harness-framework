// Package fixture discovers and resolves the top-level fixture pool
// at <projectRoot>/.harness/fixtures/. Fixtures are static files
// authored alongside sensors; they are referenced from sensor steps
// via `with: { fixture: <name> }` and `${{ fixtures.<name> }}`.
package fixture

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

const defaultMaxBytes = 1 << 20 // 1 MiB

// Pool maps fixture names (with their original extension and any sub-path
// segments) to their absolute filesystem paths.
type Pool map[string]string

// Discover walks <projectRoot>/.harness/fixtures/ and returns a Pool. Paths
// inside the fixtures directory become names verbatim (with forward slashes
// regardless of platform). Files larger than the configured cap (default 1 MiB,
// overridable by HARNESS_FIXTURE_MAX_BYTES) cause Discover to return an error
// citing the offending path. A missing fixtures directory yields an empty pool
// and no error.
func Discover(projectRoot string) (Pool, error) {
	if projectRoot == "" {
		return nil, fmt.Errorf("fixture.Discover: projectRoot is required")
	}
	root := filepath.Join(projectRoot, ".harness", "fixtures")

	cap := defaultMaxBytes
	if raw := os.Getenv("HARNESS_FIXTURE_MAX_BYTES"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			cap = n
		}
	}

	// A missing fixtures root is not an error; it just yields an empty pool.
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Pool{}, nil
		}
		return nil, err
	}

	pool := Pool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if int(info.Size()) > cap {
			return fmt.Errorf("fixture %q exceeds %d bytes (size=%d); raise HARNESS_FIXTURE_MAX_BYTES to override",
				path, cap, info.Size())
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		pool[name] = path
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pool, nil
}
