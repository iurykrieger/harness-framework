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

func TestLookup_FileAbsent(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	res, err := registry.Lookup(proj)
	if err != nil {
		t.Fatal(err)
	}
	if res.Exists {
		t.Errorf("Exists: got true, want false")
	}
	if res.ProjectRoot != proj {
		t.Errorf("ProjectRoot: got %q, want %q", res.ProjectRoot, proj)
	}
	if res.Source != registry.SourceWalkUp {
		t.Errorf("Source: got %q, want %q", res.Source, registry.SourceWalkUp)
	}
	if res.State.Version != 1 {
		t.Errorf("State.Version: got %d, want 1", res.State.Version)
	}
	if len(res.State.Entries) != 0 {
		t.Errorf("State.Entries: got %d, want 0", len(res.State.Entries))
	}
	if res.Root.RegistryFile() == "" {
		t.Error("Root unwired")
	}
}

func TestLookup_FilePresentWithEntries(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	r := registry.NewRoot(proj)
	want := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "loop", PID: 1234, StartedAt: "2026-05-10T00:00:00Z"},
		},
	}
	if err := registry.Save(r, want); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	res, err := registry.Lookup(proj)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exists {
		t.Errorf("Exists: got false, want true")
	}
	if len(res.State.Entries) != 1 || res.State.Entries[0].SensorID != "loop" {
		t.Errorf("State.Entries: got %+v", res.State.Entries)
	}
}

func TestLookup_DiscoveryFailurePropagates(t *testing.T) {
	parent := t.TempDir() // no sensors/ marker anywhere
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, err := registry.Lookup(parent)
	if err == nil {
		t.Fatal("expected error")
	}
	var de *registry.DiscoveryError
	if !errors.As(err, &de) {
		t.Errorf("expected DiscoveryError, got %T: %v", err, err)
	}
}

func TestLookup_MalformedJSONReturnsError(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.RegistryFile(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, err := registry.Lookup(proj)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestDiscoveryErrorSignal_Shape(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, derr := registry.Discover(t.TempDir())
	if derr == nil {
		t.Fatal("expected discovery error")
	}
	sig := registry.DiscoveryErrorSignal(derr, "list-sensors")

	if sig["sensor_id"] != "list-sensors" {
		t.Errorf("sensor_id: got %v, want %q", sig["sensor_id"], "list-sensors")
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict: got %v, want \"error\"", sig["verdict"])
	}
	if sig["severity"] != "high" {
		t.Errorf("severity: got %v, want \"high\"", sig["severity"])
	}
	md, ok := sig["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata: got %T", sig["metadata"])
	}
	if md["kind"] != "registry_discovery_failed" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	if _, present := md["registry_path"]; present {
		t.Errorf("metadata.registry_path should be absent on discovery failure, got %v", md["registry_path"])
	}
	ev, _ := sig["evidence"].([]interface{})
	if len(ev) != 1 {
		t.Fatalf("evidence: got %d items, want 1", len(ev))
	}
	rationale := ev[0].(map[string]interface{})["rationale"].(string)
	if !strings.Contains(rationale, derr.Error()) {
		t.Errorf("rationale should contain raw error string, got: %q", rationale)
	}
}

func TestDiagnoseMetadata_Fields(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	r := registry.NewRoot(proj)
	res := registry.Result{
		Root:        r,
		ProjectRoot: proj,
		Source:      registry.SourceWalkUp,
		Exists:      true,
	}
	md := registry.DiagnoseMetadata(res)
	if md["registry_path"] != r.RegistryFile() {
		t.Errorf("registry_path: got %v, want %v", md["registry_path"], r.RegistryFile())
	}
	if md["registry_source"] != "walk_up" {
		t.Errorf("registry_source: got %v, want \"walk_up\"", md["registry_source"])
	}
	if md["registry_exists"] != true {
		t.Errorf("registry_exists: got %v, want true", md["registry_exists"])
	}
	if len(md) != 3 {
		t.Errorf("expected exactly 3 fields, got %d: %+v", len(md), md)
	}
}

func TestDiagnoseMetadata_SourceEnvAndAbsent(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	res := registry.Result{
		Root:        registry.NewRoot(proj),
		ProjectRoot: proj,
		Source:      registry.SourceEnv,
		Exists:      false,
	}
	md := registry.DiagnoseMetadata(res)
	if md["registry_source"] != "env" {
		t.Errorf("registry_source: got %v, want \"env\"", md["registry_source"])
	}
	if md["registry_exists"] != false {
		t.Errorf("registry_exists: got %v, want false", md["registry_exists"])
	}
}

func TestDiscoveryErrorSignal_ValidatesAgainstSchema(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, derr := registry.Discover(t.TempDir())
	sig := registry.DiscoveryErrorSignal(derr, "list-sensors")

	for _, k := range []string{"sensor_id", "version", "run_id", "started_at", "finished_at", "verdict", "severity", "confidence", "evidence", "cost_actual", "metadata"} {
		if _, ok := sig[k]; !ok {
			t.Errorf("required field %q missing", k)
		}
	}
	if conf, ok := sig["confidence"].(float64); !ok || conf <= 0 || conf > 1 {
		t.Errorf("confidence: got %v", sig["confidence"])
	}
	cost, _ := sig["cost_actual"].(map[string]interface{})
	if _, ok := cost["latency_ms"]; !ok {
		t.Errorf("cost_actual.latency_ms missing")
	}
}
