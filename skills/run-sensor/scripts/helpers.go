//go:build run_computational || run_inferential

package main

import (
	"github.com/iurykrieger/harness-framework/lib/registry"
)

// resolveProjectRoot consults lib/registry.Lookup which honors
// HARNESS_REGISTRY_ROOT first, then walks up from cwd looking for
// sensors/. Falls back to cwd if discovery fails so that an
// out-of-tree invocation still has a chance to find sensors via
// the runner's other resolution (sensor.ResolveByID), which also
// accepts an absolute path.
//
// After the go run -C invocation contract change, the runner's cwd
// is the plugin root (not the user's project). HARNESS_REGISTRY_ROOT
// is set by the skill BEFORE the -C chdir, capturing the user's
// project root. Without the env var, walk-up from the plugin root
// will not find a sensors/ dir and falls back to cwd — the old
// behavior, acceptable for direct invocations from inside a project.
func resolveProjectRoot(cwd string) string {
	res, err := registry.Lookup(cwd)
	if err != nil {
		return cwd
	}
	return res.ProjectRoot
}
