package sensor_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
	"sigs.k8s.io/yaml"
)

func TestValidateAndPersist_ValidComputational(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())

	path, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:     outDir,
		SchemasDir: schemasDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(outDir, "smoke-comp.yaml")
	abs, _ := filepath.Abs(want)
	if path != abs {
		t.Fatalf("path = %q, want %q", path, abs)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

func TestValidateAndPersist_InvalidJSON(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	_, err := sensor.ValidateAndPersist([]byte("not-json"), sensor.PersistOpts{
		OutDir:     t.TempDir(),
		SchemasDir: schemasDir,
	})
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestValidateAndPersist_SchemaViolation(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	bad := sensortest.LoadComputational(t).AsMap()
	delete(bad, "regulation")
	body, _ := json.Marshal(bad)

	outDir := t.TempDir()
	_, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:     outDir,
		SchemasDir: schemasDir,
	})
	if err == nil {
		t.Fatal("expected schema error, got nil")
	}
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Fatalf("expected outDir empty, found %d entries", len(entries))
	}
}

func TestValidateAndPersist_Idempotent(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())
	opts := sensor.PersistOpts{OutDir: outDir, SchemasDir: schemasDir}

	p1, err := sensor.ValidateAndPersist(body, opts)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := sensor.ValidateAndPersist(body, opts)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("paths differ: %q vs %q", p1, p2)
	}
	a, _ := os.ReadFile(p1)
	b, _ := os.ReadFile(p2)
	if string(a) != string(b) {
		t.Fatal("repeat write produced different content")
	}
}

func TestValidateAndPersist_OverwritesStaleByDefault(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	stale := filepath.Join(outDir, "smoke-comp.yaml")
	if err := os.WriteFile(stale, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())

	if _, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:     outDir,
		SchemasDir: schemasDir,
	}); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(stale)
	if strings.Contains(string(out), "STALE") {
		t.Fatal("stale content not overwritten")
	}
}

func TestValidateAndPersist_RejectIfExists(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	stale := filepath.Join(outDir, "smoke-comp.yaml")
	if err := os.WriteFile(stale, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())

	_, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:         outDir,
		SchemasDir:     schemasDir,
		RejectIfExists: true,
	})
	if err == nil {
		t.Fatal("expected SensorAlreadyExistsError")
	}
	var saee *sensor.SensorAlreadyExistsError
	if !errors.As(err, &saee) {
		t.Fatalf("expected *SensorAlreadyExistsError, got %T: %v", err, err)
	}
	body2, _ := os.ReadFile(stale)
	if !strings.Contains(string(body2), "STALE") {
		t.Fatal("stale content was modified")
	}
}

func TestValidateAndPersist_RequireFixturesOnDisk_Missing(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := t.TempDir()

	s := sensortest.LoadComputational(t)
	s.Verification.GoldenCases = []sensor.GoldenCase{
		{Fixture: ".harness/sensors/fixtures/smoke-comp/pass.txt", ExpectedVerdict: "pass", ExpectedSeverity: "info"},
	}
	body, _ := json.Marshal(s.AsMap())

	_, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:                outDir,
		SchemasDir:            schemasDir,
		RequireFixturesOnDisk: true,
		ProjectRoot:           projectRoot,
	})
	if err == nil {
		t.Fatal("expected MissingFixtureError")
	}
	var mfe *sensor.MissingFixtureError
	if !errors.As(err, &mfe) {
		t.Fatalf("expected *MissingFixtureError, got %T: %v", err, err)
	}
	if mfe.Rel != ".harness/sensors/fixtures/smoke-comp/pass.txt" {
		t.Fatalf("Rel=%q", mfe.Rel)
	}
}

func TestValidateAndPersist_RequireFixturesOnDisk_Present(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := t.TempDir()
	projectRoot := t.TempDir()

	fixtureRel := ".harness/sensors/fixtures/smoke-comp/pass.txt"
	full := filepath.Join(projectRoot, fixtureRel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := sensortest.LoadComputational(t)
	s.Verification.GoldenCases = []sensor.GoldenCase{
		{Fixture: fixtureRel, ExpectedVerdict: "pass", ExpectedSeverity: "info"},
	}
	body, _ := json.Marshal(s.AsMap())

	if _, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:                outDir,
		SchemasDir:            schemasDir,
		RequireFixturesOnDisk: true,
		ProjectRoot:           projectRoot,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAndPersist_RequireFixturesOnDisk_WithoutProjectRoot(t *testing.T) {
	_, err := sensor.ValidateAndPersist([]byte("{}"), sensor.PersistOpts{
		OutDir:                t.TempDir(),
		RequireFixturesOnDisk: true,
	})
	if err == nil {
		t.Fatal("expected error when ProjectRoot missing")
	}
}

func TestValidateAndPersist_OutDirRequired(t *testing.T) {
	_, err := sensor.ValidateAndPersist([]byte("{}"), sensor.PersistOpts{})
	if err == nil {
		t.Fatal("expected error when OutDir missing")
	}
}

func TestValidateAndPersist_CreatesNestedOutDir(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	parent := t.TempDir()
	out := filepath.Join(parent, "deep", ".harness", "sensors")
	body, _ := json.Marshal(sensortest.LoadComputational(t).AsMap())

	if _, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{
		OutDir:     out,
		SchemasDir: schemasDir,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "smoke-comp.yaml")); err != nil {
		t.Fatalf("nested out dir not created: %v", err)
	}
}

func TestRegexRoundTripThroughYAML(t *testing.T) {
	// Each pattern is what would appear inside
	// execution.output_parsing.patterns[].regex. The round-trip path is
	// json.Marshal → yaml.JSONToYAML (Persist path) →
	// yaml.YAMLToJSON (ReadAsJSON path) → json.Unmarshal.
	patterns := []string{
		`^FAIL\s+(.+)$`,
		`(?i)error:`,
		`^\[\d{4}-\d{2}-\d{2}T[\d:.Z+-]+\]`,
		`^---$`,
		`: panic: `,
		`# warning`,
		`& fail`,
		`* error *`,
		`!critical!`,
		`| stderr`,
		`> stdout`,
		`  leading space`,
		`trailing space  `,
		"with\nembedded newline",
		`unicode: ✓ ✗ → ←`,
	}
	for _, p := range patterns {
		t.Run(p, func(t *testing.T) {
			// Build a tiny instance carrying the pattern.
			in := map[string]interface{}{"regex": p}
			jb, err := json.Marshal(in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			yb, err := yaml.JSONToYAML(jb)
			if err != nil {
				t.Fatalf("JSONToYAML: %v", err)
			}
			jb2, err := yaml.YAMLToJSON(yb)
			if err != nil {
				t.Fatalf("YAMLToJSON: %v", err)
			}
			var out map[string]interface{}
			if err := json.Unmarshal(jb2, &out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, _ := out["regex"].(string)
			if got != p {
				t.Fatalf("round-trip mismatch:\n  in:  %q\n  out: %q\n  yaml:\n%s", p, got, string(yb))
			}
		})
	}
}

func TestPersistCanonicalIndependentOfDraftStyle(t *testing.T) {
	// Three logically-identical drafts in different input styles must
	// produce byte-identical files on disk.
	jsonDraft := []byte(`{"id":"x","version":"1.0.0","name":"x","description":"x","kind":"assertion","type":"computational","regulation":"maintainability","phase":"on-demand","determinism":"high","output":"single","cost":{"class":"cheap","compute":{"cpu":"low","memory_mb":64},"latency":{"p50_ms":10,"p95_ms":100,"timeout_ms":5000}},"triggers":[{"on":"manual"}],"execution":{"command":"true","exit_code_map":[{"exit_code":0,"verdict":"pass","severity":"info"}]},"verification":{"golden_cases":[{"fixture":"x","expected_verdict":"pass","expected_severity":"info"}]}}`)

	flowYAML, err := yaml.JSONToYAML(jsonDraft)
	if err != nil {
		t.Fatalf("JSONToYAML for setup: %v", err)
	}
	// Re-marshal then unmarshal again as a second style permutation.
	var asMap map[string]interface{}
	if err := yaml.Unmarshal(flowYAML, &asMap); err != nil {
		t.Fatalf("Unmarshal setup: %v", err)
	}
	reJSON, err := json.Marshal(asMap)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	blockYAML, err := yaml.JSONToYAML(reJSON)
	if err != nil {
		t.Fatalf("JSONToYAML 2: %v", err)
	}

	tmpA := t.TempDir()
	tmpB := t.TempDir()
	tmpC := t.TempDir()

	schemasDir := schematest.RepoSchemasDir(t)

	for _, c := range []struct {
		name string
		body []byte
		yaml bool
		out  string
	}{
		{"json", jsonDraft, false, tmpA},
		{"yamlA", flowYAML, true, tmpB},
		{"yamlB", blockYAML, true, tmpC},
	} {
		body := c.body
		if c.yaml {
			// ValidateAndPersist consumes JSON; the YAML-draft variants
			// reach it through the same JSON gateway that the CLI uses
			// (schema.ReadAsJSON when loading from disk).
			jb, err := yaml.YAMLToJSON(c.body)
			if err != nil {
				t.Fatalf("%s YAMLToJSON: %v", c.name, err)
			}
			body = jb
		}
		if _, err := sensor.ValidateAndPersist(body, sensor.PersistOpts{OutDir: c.out, SchemasDir: schemasDir}); err != nil {
			t.Fatalf("%s persist: %v", c.name, err)
		}
	}

	readA, _ := os.ReadFile(filepath.Join(tmpA, "x.yaml"))
	readB, _ := os.ReadFile(filepath.Join(tmpB, "x.yaml"))
	readC, _ := os.ReadFile(filepath.Join(tmpC, "x.yaml"))

	if !bytes.Equal(readA, readB) || !bytes.Equal(readB, readC) {
		t.Fatalf("canonical form differs by draft style:\nA=%s\nB=%s\nC=%s", readA, readB, readC)
	}
}
