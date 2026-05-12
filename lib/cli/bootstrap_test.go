package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestBootstrap_HappyPathInProjectRoot(t *testing.T) {
	tmp := t.TempDir()
	// fake a project root: must contain a sensors/ dir for registry.Lookup
	if err := os.MkdirAll(filepath.Join(tmp, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", tmp)

	// For the schemas to be discoverable, we need to chdir to somewhere
	// that can walk up to find the repo's schemas/. We'll use the repo root.
	repoRoot := testfixtures.RepoSchemasDir(t)
	repoRoot = filepath.Dir(repoRoot) // go up from schemas/ to repo root
	chdir(t, repoRoot)

	var out, errBuf bytes.Buffer
	res := cli.Bootstrap("my-skill", &out, &errBuf)
	if res.ExitCode != 0 {
		t.Fatalf("exit %d, stderr=%q", res.ExitCode, errBuf.String())
	}
	if res.Validator == nil {
		t.Fatal("validator nil")
	}
	if res.Diagnose["registry_path"] == "" {
		t.Fatalf("diagnose: %v", res.Diagnose)
	}
}

func TestBootstrap_DiscoveryFailureEmitsSignalAndExits(t *testing.T) {
	tmp := t.TempDir() // no sensors/ subdir
	chdir(t, tmp)
	var out, errBuf bytes.Buffer
	res := cli.Bootstrap("my-skill", &out, &errBuf)
	if res.ExitCode == 0 {
		t.Fatal("expected non-zero exit")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &sig); err != nil {
		t.Fatalf("decode emitted signal: %v (bytes=%q)", err, out.String())
	}
	if sig["verdict"] != "error" {
		t.Fatalf("verdict: %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "registry_discovery_failed" {
		t.Fatalf("kind: %v", md["kind"])
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}
