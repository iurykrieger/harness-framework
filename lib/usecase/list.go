package usecase

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ListByJourney scans usecasesDir for the canonical layout written by
// ValidateAndPersist (<usecasesDir>/<journey-id>/<id>.yaml) and returns
// a map keyed by journey-id where each value is a sorted slice of
// usecase IDs (filename without the .yaml extension).
//
// An absent usecasesDir returns an empty map and nil error: no use cases
// persisted yet is a valid state, not an I/O failure.
//
// Skips:
//   - non-directory entries directly under usecasesDir,
//   - entries whose name starts with a dot,
//   - files inside a journey dir that do not end in ".yaml".
func ListByJourney(usecasesDir string) (map[string][]string, error) {
	out := map[string][]string{}
	entries, err := os.ReadDir(usecasesDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return nil, fmt.Errorf("read %s: %w", usecasesDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		journeyDir := filepath.Join(usecasesDir, name)
		files, err := os.ReadDir(journeyDir)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", journeyDir, err)
		}
		var ids []string
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			fn := f.Name()
			if strings.HasPrefix(fn, ".") {
				continue
			}
			if filepath.Ext(fn) != ".yaml" {
				continue
			}
			ids = append(ids, strings.TrimSuffix(fn, ".yaml"))
		}
		sort.Strings(ids)
		out[name] = ids
	}
	return out, nil
}
