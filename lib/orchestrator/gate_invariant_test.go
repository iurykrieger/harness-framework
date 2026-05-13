package orchestrator_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSpawnCallSitesGated enforces project rule #11: every call to
// subprocess.StreamSubprocess, subprocess.Start, or subprocess.SpawnDetached
// that executes a sensor's execution.command must be preceded (in the same
// file) by an orchestrator.PreflightGate call. Files in the allowlist are
// exempted because they either define the primitives themselves or spawn
// non-sensor processes (watcher, prepare/teardown step commands, tests).
func TestSpawnCallSitesGated(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}

	allowedFiles := map[string]bool{
		// Primitives — these are the definitions.
		"lib/subprocess/stream.go": true,
		"lib/subprocess/detach.go": true,
		"lib/subprocess/step.go":   true,
	}
	allowedDirs := []string{
		"lib/subprocess/",
		"lib/watcher/",
	}

	spawnPatterns := []string{
		"subprocess.StreamSubprocess",
		"subprocess.Start",
		"subprocess.SpawnDetached",
	}

	var violations []string
	err = filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			// Skip vendor/.git/etc directories to keep the walk fast.
			name := d.Name()
			if name == "vendor" || name == ".git" || name == "node_modules" || name == ".claude" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		if allowedFiles[rel] {
			return nil
		}
		for _, dir := range allowedDirs {
			if strings.HasPrefix(rel, dir) {
				return nil
			}
		}

		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(body)

		hasSpawn := false
		for _, p := range spawnPatterns {
			if strings.Contains(text, p) {
				hasSpawn = true
				break
			}
		}
		if !hasSpawn {
			return nil
		}
		if !strings.Contains(text, "PreflightGate") {
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("project rule #11 violated — files spawn without PreflightGate:\n  %s\n\nIf the call site is legitimate (e.g., spawns a non-sensor process), add the file to allowedFiles/allowedDirs in this test.", strings.Join(violations, "\n  "))
	}
}

// findRepoRoot returns the absolute path of the repo root by walking up
// from this test file's own location until it finds a go.mod. Independent
// of process cwd, so order-sensitive tests (e.g., cwd_test.go which
// chdir's to a temp dir) cannot break this static-analysis check.
func findRepoRoot() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", os.ErrNotExist
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
