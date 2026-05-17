//go:build coverage_report

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
)

// stackBody returns a minimal but valid stack YAML body declaring the
// given journey ids (each as an http-api journey on POST /<id>).
func stackBody(t *testing.T, journeyIDs ...string) []byte {
	t.Helper()
	doc := map[string]interface{}{
		"version":     "0.2.0",
		"detected_at": "2026-05-14T10:00:00Z",
		"detected_by": "manual",
		"languages":   []map[string]interface{}{{"name": "go"}},
		"components": []map[string]interface{}{{
			"role":     "http-server",
			"name":     "net/http",
			"evidence": []map[string]interface{}{{"file": "x.go", "rationale": "x"}},
		}},
		"log_shapes": []map[string]interface{}{{
			"id":          "x",
			"produced_by": []string{"net/http"},
			"format":      "plain",
			"sample":      "x",
		}},
		"archetypes": []string{"http-api"},
	}
	var journeys []map[string]interface{}
	for _, id := range journeyIDs {
		journeys = append(journeys, map[string]interface{}{
			"id":        id,
			"name":      id,
			"summary":   id,
			"archetype": "http-api",
			"entry_points": []map[string]interface{}{{
				"kind":     "http-route",
				"method":   "POST",
				"path":     "/" + id,
				"evidence": map[string]interface{}{"file": "x.go", "rationale": "x"},
			}},
		})
	}
	doc["journeys"] = journeys

	body, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func writeStack(t *testing.T, projectRoot string, body []byte) {
	t.Helper()
	harness := filepath.Join(projectRoot, ".harness")
	if err := os.MkdirAll(harness, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(harness, "stack.yaml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeUseCase(t *testing.T, projectRoot, journeyID, id string) {
	t.Helper()
	dir := filepath.Join(projectRoot, ".harness", "usecases", journeyID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCoverage_AllJourneysCovered(t *testing.T) {
	projectRoot := t.TempDir()
	writeStack(t, projectRoot, stackBody(t, "user-registration", "user-login"))
	writeUseCase(t, projectRoot, "user-registration", "create-user-with-email")
	writeUseCase(t, projectRoot, "user-login", "login-happy-path")

	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"Coverage matrix",
		"user-registration",
		"user-login",
		"1 use case",
		"Full coverage",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q. full output:\n%s", want, out)
		}
	}
}

func TestCoverage_UncoveredJourneys(t *testing.T) {
	projectRoot := t.TempDir()
	writeStack(t, projectRoot, stackBody(t, "covered-journey", "uncovered-journey", "another-blank"))
	writeUseCase(t, projectRoot, "covered-journey", "happy")

	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1 (incomplete coverage); stderr=%s stdout=%s",
			code, stderr.String(), stdout.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"uncovered-journey",
		"another-blank",
		"0 use cases",
		"BLOCKER",
		"Incomplete coverage",
		"2 of 3 journeys uncovered",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q. full output:\n%s", want, out)
		}
	}
}

func TestCoverage_StackMissing(t *testing.T) {
	projectRoot := t.TempDir() // no .harness/stack.yaml
	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d want 2 (setup error)", code)
	}
	if !strings.Contains(stderr.String(), "stack") {
		t.Errorf("stderr %q should mention stack", stderr.String())
	}
}

func TestCoverage_NoJourneysInStack(t *testing.T) {
	projectRoot := t.TempDir()
	writeStack(t, projectRoot, stackBody(t)) // zero journeys
	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
	}, &stdout, &stderr)
	// No journeys declared = no coverage required; treat as warn (code 0)
	// so /detect-usecases does not block but the warning is visible.
	if code != 0 {
		t.Fatalf("code=%d want 0 when stack has no journeys", code)
	}
	if !strings.Contains(stdout.String(), "no journeys") {
		t.Errorf("stdout should mention no journeys; got %q", stdout.String())
	}
}

func TestCoverage_OrphanUseCases(t *testing.T) {
	projectRoot := t.TempDir()
	writeStack(t, projectRoot, stackBody(t, "real-journey"))
	writeUseCase(t, projectRoot, "real-journey", "happy")
	writeUseCase(t, projectRoot, "ghost-journey", "abandoned")

	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d want 0 (orphans don't block coverage)", code)
	}
	if !strings.Contains(stdout.String(), "Orphan") {
		t.Errorf("stdout should flag orphan use cases; got:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ghost-journey") {
		t.Errorf("stdout should name the orphan journey id; got:\n%s", stdout.String())
	}
}

func TestCoverage_JourneyFilter_Covered(t *testing.T) {
	projectRoot := t.TempDir()
	writeStack(t, projectRoot, stackBody(t, "a", "b"))
	writeUseCase(t, projectRoot, "a", "happy")
	// b uncovered — but we ask only about a.

	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
		"--journey", "a",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d want 0 (a is covered)", code)
	}
	if strings.Contains(stdout.String(), "b") && !strings.Contains(stdout.String(), "Orphan") {
		t.Errorf("stdout should not include unrelated journey b; got:\n%s", stdout.String())
	}
}

func TestCoverage_JourneyFilter_Uncovered(t *testing.T) {
	projectRoot := t.TempDir()
	writeStack(t, projectRoot, stackBody(t, "a", "b"))
	writeUseCase(t, projectRoot, "a", "happy")

	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
		"--journey", "b",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code=%d want 1 (b uncovered)", code)
	}
}

func TestCoverage_JourneyFilter_Unknown(t *testing.T) {
	projectRoot := t.TempDir()
	writeStack(t, projectRoot, stackBody(t, "a"))
	schemasDir := schematest.RepoSchemasDir(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--project-root", projectRoot,
		"--schemas-dir", schemasDir,
		"--journey", "ghost",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d want 2 (journey not in stack)", code)
	}
	if !strings.Contains(stderr.String(), "ghost") {
		t.Errorf("stderr should name unknown journey; got %q", stderr.String())
	}
}

func TestCoverage_MissingProjectRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
}
