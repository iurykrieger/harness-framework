//go:build run_computational || run_inferential

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// pluginRootForTest returns the plugin checkout's absolute path. Tests
// must point CLAUDE_PLUGIN_ROOT at it so lib/watcher.Spawn can find the
// watcher source tree. skills/run-sensor/scripts is three directory
// levels below the plugin root.
func pluginRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// skills/run-sensor/scripts -> plugin root
	return filepath.Dir(filepath.Dir(filepath.Dir(wd)))
}

// repoRootForTest returns the absolute path of the repository root by
// walking up from the current working directory until a go.mod file is
// found. Tests that spawn `go run` need an absolute Dir so the module
// resolves regardless of where the test binary is executed from.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found walking up from %s", cwd)
		}
		dir = parent
	}
}
