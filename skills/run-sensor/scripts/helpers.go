//go:build run_computational || run_inferential

package main

import (
	"github.com/iurykrieger/harness-framework/lib/registry"
)

// resolveProjectRoot consults lib/registry.Lookup which honors
// HARNESS_REGISTRY_ROOT first, then walks up from cwd looking for a
// .harness/ directory (the canonical project-root marker after the
// .harness/ layout migration). Falls back to cwd if discovery fails,
// so an out-of-tree invocation still has a chance to find sensors via
// sensor.Resolve (which accepts absolute and @-prefixed paths in
// addition to bare ids).
//
// After the go run -C invocation contract change, the runner's cwd
// is the plugin root (not the user's project). HARNESS_REGISTRY_ROOT
// is set by the skill BEFORE the -C chdir, capturing the user's
// project root. Without the env var, walk-up from the plugin root
// will not find a .harness/ dir and falls back to cwd — the old
// behavior, acceptable for direct invocations from inside a project.
func resolveProjectRoot(cwd string) string {
	res, err := registry.Lookup(cwd)
	if err != nil {
		return cwd
	}
	return res.ProjectRoot
}
