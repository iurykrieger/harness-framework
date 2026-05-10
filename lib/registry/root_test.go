package registry_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// makeProjectTree builds <root>/sensors/ and returns the project root.
// Tests use it to anchor the walk-up marker.
func makeProjectTree(t *testing.T, parent string) string {
	t.Helper()
	root := filepath.Join(parent, "proj")
	if err := os.MkdirAll(filepath.Join(root, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscover_EnvVarAbsoluteAndExists(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	t.Setenv("HARNESS_REGISTRY_ROOT", proj)
	got, source, err := registry.Discover("/tmp/whatever")
	if err != nil {
		t.Fatal(err)
	}
	if source != registry.SourceEnv {
		t.Errorf("source: got %q, want %q", source, registry.SourceEnv)
	}
	if got != proj {
		t.Errorf("root: got %q, want %q", got, proj)
	}
}

func TestDiscover_EnvVarNotAbsolute(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "relative/path")
	_, _, err := registry.Discover(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("err message should mention 'absolute', got: %v", err)
	}
}

func TestDiscover_EnvVarNotExists(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "/nonexistent/path/that/should/not/exist/12345")
	_, _, err := registry.Discover(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not exist") && !strings.Contains(err.Error(), "no such") {
		t.Errorf("err message should mention 'not exist' or 'no such', got: %v", err)
	}
}

func TestDiscover_EnvVarPointsToFileNotDir(t *testing.T) {
	parent := t.TempDir()
	file := filepath.Join(parent, "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", file)
	_, _, err := registry.Discover(parent)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not a directory") && !strings.Contains(err.Error(), "directory") {
		t.Errorf("err message should mention 'directory', got: %v", err)
	}
}

func TestDiscover_EnvVarSymlinkResolved(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	link := filepath.Join(parent, "link-to-proj")
	if err := os.Symlink(proj, link); err != nil {
		t.Skipf("symlink not supported on this filesystem: %v", err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", link)
	got, source, err := registry.Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if source != registry.SourceEnv {
		t.Errorf("source: got %q, want %q", source, registry.SourceEnv)
	}
	gotResolved, _ := filepath.EvalSymlinks(got)
	projResolved, _ := filepath.EvalSymlinks(proj)
	if gotResolved != projResolved {
		t.Errorf("root: got %q (resolved %q), want %q (resolved %q)", got, gotResolved, proj, projResolved)
	}
}

func TestDiscover_WalkUpFindsSensorsTwoLevels(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	deep := filepath.Join(proj, "nested", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	got, source, err := registry.Discover(deep)
	if err != nil {
		t.Fatal(err)
	}
	if source != registry.SourceWalkUp {
		t.Errorf("source: got %q, want %q", source, registry.SourceWalkUp)
	}
	if got != proj {
		t.Errorf("root: got %q, want %q", got, proj)
	}
}

func TestDiscover_WalkUpFromProjectRoot(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	got, source, err := registry.Discover(proj)
	if err != nil {
		t.Fatal(err)
	}
	if source != registry.SourceWalkUp {
		t.Errorf("source: got %q, want %q", source, registry.SourceWalkUp)
	}
	if got != proj {
		t.Errorf("root: got %q, want %q", got, proj)
	}
}

func TestDiscover_WalkUpEmptySensorsDirAcceptable(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent) // sensors/ created but empty
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	got, _, err := registry.Discover(proj)
	if err != nil {
		t.Fatal(err)
	}
	if got != proj {
		t.Errorf("root: got %q, want %q", got, proj)
	}
}

func TestDiscover_NoMarkerNoEnv_ErrorMentionsBothStrategies(t *testing.T) {
	parent := t.TempDir() // no sensors/ anywhere up to filesystem root from here
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, err := registry.Discover(parent)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "HARNESS_REGISTRY_ROOT") {
		t.Errorf("err should mention HARNESS_REGISTRY_ROOT, got: %v", err)
	}
	if !strings.Contains(msg, "sensors") {
		t.Errorf("err should mention 'sensors', got: %v", err)
	}
}

func TestDiscover_DiscoveryError_IsTyped(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, err := registry.Discover(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	var de *registry.DiscoveryError
	if !errors.As(err, &de) {
		t.Errorf("error should be *registry.DiscoveryError, got %T: %v", err, err)
	}
}
