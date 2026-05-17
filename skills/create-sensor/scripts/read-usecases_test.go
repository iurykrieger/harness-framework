//go:build read_usecases

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSingleUsecaseByID(t *testing.T) {
	projectRoot := filepath.Join("testdata")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--usecases", "tail-sensor-no-registry",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: %d; stderr=%s; stdout=%s", code, stderr.String(), stdout.String())
	}
	var lg Ledger
	if err := json.Unmarshal(stdout.Bytes(), &lg); err != nil {
		t.Fatalf("unmarshal ledger: %v; stdout=%s", err, stdout.String())
	}
	if len(lg.Usecases) != 1 {
		t.Fatalf("want 1 usecase, got %d", len(lg.Usecases))
	}
	if lg.Usecases[0].ID != "tail-sensor-no-registry" {
		t.Fatalf("unexpected id: %s", lg.Usecases[0].ID)
	}
	if lg.Usecases[0].JourneyID != "tail-sensor" {
		t.Fatalf("unexpected journey: %s", lg.Usecases[0].JourneyID)
	}
}

func TestLoadJourney(t *testing.T) {
	projectRoot := filepath.Join("testdata")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", projectRoot, "--journey", "tail-sensor"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: %d; stderr=%s", code, stderr.String())
	}
	var lg Ledger
	if err := json.Unmarshal(stdout.Bytes(), &lg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(lg.Usecases) != 2 {
		t.Fatalf("want 2 usecases, got %d", len(lg.Usecases))
	}
	if lg.Usecases[0].ID != "tail-sensor-cursor-zero" || lg.Usecases[1].ID != "tail-sensor-no-registry" {
		t.Fatalf("unexpected ordering: %s, %s", lg.Usecases[0].ID, lg.Usecases[1].ID)
	}
}

func TestListOnly(t *testing.T) {
	projectRoot := filepath.Join("testdata")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", projectRoot, "--journey", "tail-sensor", "--list-only"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code: %d; stderr=%s", code, stderr.String())
	}
	var idx indexLedger
	if err := json.Unmarshal(stdout.Bytes(), &idx); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(idx.Usecases) != 2 {
		t.Fatalf("want 2, got %d", len(idx.Usecases))
	}
	if idx.Usecases[0].Name == "" {
		t.Fatal("name missing in --list-only output")
	}
}

func TestListOnlyRejectsIncludeFlags(t *testing.T) {
	for _, extra := range []string{"--include-stack", "--include-catalog"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--project-root", "testdata", "--journey", "tail-sensor", "--list-only", extra}, &stdout, &stderr)
		if code != 2 {
			t.Fatalf("[%s] want exit 2, got %d; stdout=%s", extra, code, stdout.String())
		}
		if !bytes.Contains(stdout.Bytes(), []byte(`"kind":"usage"`)) {
			t.Fatalf("[%s] expected metadata.kind=usage Signal; got %s", extra, stdout.String())
		}
	}
}

func TestMissingUsecaseIsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", "testdata", "--usecases", "does-not-exist"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("want exit 1, got %d; stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"kind":"usecase_not_found"`)) {
		t.Fatalf("expected usecase_not_found Signal; got %s", stdout.String())
	}
}

func TestMalformedYAMLYieldsWarnNotError(t *testing.T) {
	root := t.TempDir()
	usecasesDir := filepath.Join(root, ".harness", "usecases", "mixed")
	if err := os.MkdirAll(usecasesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcValid := filepath.Join("testdata", ".harness", "usecases", "tail-sensor", "tail-sensor-no-registry.yaml")
	bodyValid, err := os.ReadFile(srcValid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usecasesDir, "tail-sensor-no-registry.yaml"), bodyValid, 0o644); err != nil {
		t.Fatal(err)
	}
	srcBad := filepath.Join("testdata", "usecases-malformed", "bad-schema.yaml")
	bodyBad, err := os.ReadFile(srcBad)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(usecasesDir, "bad-schema.yaml"), bodyBad, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", root, "--journey", "mixed"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("want exit 0 (warn is non-fatal), got %d; stdout=%s; stderr=%s", code, stdout.String(), stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"kind":"usecase_schema_invalid"`)) {
		t.Fatalf("expected usecase_schema_invalid warn; got %s", stdout.String())
	}
}

func TestIncludeStack(t *testing.T) {
	root := t.TempDir()
	uc := filepath.Join(root, ".harness", "usecases", "tail-sensor")
	if err := os.MkdirAll(uc, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("testdata", ".harness", "usecases", "tail-sensor", "tail-sensor-no-registry.yaml")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uc, "tail-sensor-no-registry.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	stack := []byte("languages:\n  - { name: go }\n")
	if err := os.WriteFile(filepath.Join(root, ".harness", "stack.yaml"), stack, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", root, "--usecases", "tail-sensor-no-registry", "--include-stack"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: stderr=%s; stdout=%s", code, stderr.String(), stdout.String())
	}
	var lg Ledger
	if err := json.Unmarshal(stdout.Bytes(), &lg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if lg.Stack == nil {
		t.Fatal("expected stack populated")
	}
}

func TestIncludeCatalog(t *testing.T) {
	root := t.TempDir()
	uc := filepath.Join(root, ".harness", "usecases", "tail-sensor")
	if err := os.MkdirAll(uc, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join("testdata", ".harness", "usecases", "tail-sensor", "tail-sensor-no-registry.yaml")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uc, "tail-sensor-no-registry.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	// Copy a real, schema-valid sensor from the framework's own catalog
	// rather than handcrafting a minimal one — lib/sensor.Catalog runs
	// full schema validation and rejects anything thinner.
	srcSensor := filepath.Join("testdata", "sensors", "dummy-sensor.yaml")
	sensorBody, err := os.ReadFile(srcSensor)
	if err != nil {
		t.Skipf("no test fixture for sensors catalog at %s; skipping --include-catalog round-trip: %v", srcSensor, err)
	}
	sensorsDir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sensorsDir, "dummy-sensor.yaml"), sensorBody, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--project-root", root, "--usecases", "tail-sensor-no-registry", "--include-catalog"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d: stderr=%s; stdout=%s", code, stderr.String(), stdout.String())
	}
	var lg Ledger
	if err := json.Unmarshal(stdout.Bytes(), &lg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(lg.Catalog) != 1 {
		t.Fatalf("want 1 catalog entry, got %d", len(lg.Catalog))
	}
	if lg.Catalog[0].ID != "dummy-sensor" {
		t.Fatalf("unexpected catalog entry id: %s", lg.Catalog[0].ID)
	}
}
