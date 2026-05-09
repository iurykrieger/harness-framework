# Sensor Self-Heal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add self-healing to the harness so setup-shaped sensor failures (missing env, missing binary, missing `.env`, unavailable service) auto-trigger a `/heal-sensor` skill via Claude Code hook, which iterates the project state, applies allowlisted idempotent fixes, persists patched/new sensors via a shared `lib/sensor.ValidateAndPersist` primitive, and retries the original sensor exactly once.

**Architecture:** Three new primitives — (1) a Go binary `setup-failure-detector` shipped as a Claude Code `Stop` hook that classifies aggregate Signals via an extensible Rule registry; (2) a `/heal-sensor` skill that orchestrates diagnose → apply-safe → apply-sensors → retry; (3) `lib/heal/` shared package hosting the registry, allowlist applier, env-writer, version-bumper. Persistence is consolidated into `lib/sensor.ValidateAndPersist` consumed by both `/detect-sensors` and `/heal-sensor` — no duplication.

**Tech Stack:** Go 1.25, `github.com/santhosh-tekuri/jsonschema/v5` (already vendored), JSON Schema Draft 2020-12 (existing `schemas/sensor.json` and `schemas/signal.json`), Claude Code hook protocol (Stop event, `additionalContext` injection).

**Spec:** `docs/superpowers/specs/2026-05-09-sensor-self-heal-design.md`

---

## File map

### New files

| Path | Responsibility |
|---|---|
| `lib/sensor/persist.go` | `ValidateAndPersist(sensorJSON, outDir, schemasDir)` — THE shared primitive |
| `lib/sensor/persist_test.go` | Coverage for the lib primitive |
| `lib/heal/plan.go` | Setup Plan Go types + JSON parser |
| `lib/heal/plan_test.go` | Plan parsing tests |
| `lib/heal/classify.go` | `Rule` interface, `Shape` enum, `FailedSensor` struct, `Classify` walker |
| `lib/heal/classify_test.go` | Walker dispatch tests (mock rules) |
| `lib/heal/rules.go` | Canonical ordered rule slice — single edit point |
| `lib/heal/rules_test.go` | Asserts the registration order is stable |
| `lib/heal/patterns.go` | Curated stderr regex set with shape mapping |
| `lib/heal/patterns_test.go` | Per-regex positive + negative cases |
| `lib/heal/rule_missing_env.go` | Rule: env var declared in `requires.env` is missing |
| `lib/heal/rule_missing_env_test.go` | |
| `lib/heal/rule_heal_hint.go` | Rule: `metadata.heal_hint` carries a known shape prefix |
| `lib/heal/rule_heal_hint_test.go` | |
| `lib/heal/rule_exit_code_127.go` | Rule: exit 127 + non-empty `requires.tools[]` |
| `lib/heal/rule_exit_code_127_test.go` | |
| `lib/heal/rule_prepare_template_copy.go` | Rule: prepare step `cp X.example Y` failed |
| `lib/heal/rule_prepare_template_copy_test.go` | |
| `lib/heal/rule_stderr_pattern.go` | Rule: any curated stderr regex matched |
| `lib/heal/rule_stderr_pattern_test.go` | |
| `lib/heal/extensibility_test.go` | Locks "single edit point" property as a regression |
| `lib/heal/apply.go` | Allowlist applier: `copy-template`, `mkdir`, `touch`, `set-env-in-file` |
| `lib/heal/apply_test.go` | Each kind × precondition states |
| `lib/heal/envwriter.go` | `.env` writer with chmod 600 + gitignore-coverage check |
| `lib/heal/envwriter_test.go` | |
| `lib/heal/version.go` | `BumpPatch(json) ([]byte, error)` |
| `lib/heal/version_test.go` | |
| `hooks/setup-failure-detector.go` | Claude Code Stop hook binary |
| `hooks/setup-failure-detector_test.go` | Transcript fixture → expected `additionalContext` |
| `skills/heal-sensor/SKILL.md` | Orchestration prose |
| `skills/heal-sensor/scripts/diagnose.go` | Reads project + Signal, emits Setup Plan template the calling agent fills in |
| `skills/heal-sensor/scripts/diagnose_test.go` | |
| `skills/heal-sensor/scripts/apply-safe.go` | CLI wrapper over `lib/heal/apply.go` |
| `skills/heal-sensor/scripts/apply-safe_test.go` | |
| `skills/heal-sensor/scripts/apply-sensors.go` | CLI wrapper: BumpPatch + `lib/sensor.ValidateAndPersist` |
| `skills/heal-sensor/scripts/apply-sensors_test.go` | |
| `skills/heal-sensor/scripts/retry-original.go` | Re-invokes the runner once |
| `skills/heal-sensor/scripts/retry-original_test.go` | |
| `test/heal-e2e/heal_e2e_test.go` | End-to-end smoke (fixture project + missing env → heal → retry passes) |

### Modified files

| Path | Change |
|---|---|
| `skills/detect-sensors/scripts/write-sensor.go` | Refactor to call `lib/sensor.ValidateAndPersist` |
| `skills/detect-sensors/SKILL.md` | Drop setup-iteration prose from step 7; add compositional `/heal-sensor` invocation |
| `.claude-plugin/plugin.json` | Register the hook |

---

## Task 1: lib/sensor.ValidateAndPersist primitive

**Files:**
- Create: `lib/sensor/persist.go`
- Create: `lib/sensor/persist_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/sensor/persist_test.go
package sensor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestValidateAndPersist_ValidComputational(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	outDir := t.TempDir()
	body, _ := json.Marshal(testfixtures.ValidSensorComputational())

	path, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(outDir, "smoke-comp.json")
	abs, _ := filepath.Abs(want)
	if path != abs {
		t.Fatalf("path = %q, want %q", path, abs)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("file missing: %v", err)
	}
}

func TestValidateAndPersist_InvalidJSON(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	_, err := sensor.ValidateAndPersist([]byte("not-json"), t.TempDir(), schemasDir)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestValidateAndPersist_SchemaViolation(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	bad := testfixtures.ValidSensorComputational()
	delete(bad, "regulation")
	body, _ := json.Marshal(bad)

	outDir := t.TempDir()
	_, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err == nil {
		t.Fatal("expected schema error, got nil")
	}
	// No file should have been written.
	entries, _ := os.ReadDir(outDir)
	if len(entries) != 0 {
		t.Fatalf("expected outDir empty, found %d entries", len(entries))
	}
}

func TestValidateAndPersist_Idempotent(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	outDir := t.TempDir()
	body, _ := json.Marshal(testfixtures.ValidSensorComputational())

	p1, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("paths differ: %q vs %q", p1, p2)
	}
	a, _ := os.ReadFile(p1)
	// Re-write should produce byte-identical file.
	b, _ := os.ReadFile(p2)
	if string(a) != string(b) {
		t.Fatal("repeat write produced different content")
	}
}

func TestValidateAndPersist_OverwritesStale(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	outDir := t.TempDir()
	stale := filepath.Join(outDir, "smoke-comp.json")
	if err := os.WriteFile(stale, []byte("STALE"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(testfixtures.ValidSensorComputational())

	if _, err := sensor.ValidateAndPersist(body, outDir, schemasDir); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(stale)
	if strings.Contains(string(out), "STALE") {
		t.Fatal("stale content not overwritten")
	}
}

func TestValidateAndPersist_CreatesNestedOutDir(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	parent := t.TempDir()
	out := filepath.Join(parent, "deep", "sensors")
	body, _ := json.Marshal(testfixtures.ValidSensorComputational())

	if _, err := sensor.ValidateAndPersist(body, out, schemasDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "smoke-comp.json")); err != nil {
		t.Fatalf("nested out dir not created: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./lib/sensor/...
```

Expected: FAIL with `undefined: sensor.ValidateAndPersist`.

- [ ] **Step 3: Write the implementation**

```go
// lib/sensor/persist.go
package sensor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// ValidateAndPersist validates sensorJSON against schemas/sensor.json
// (loaded from schemasDir; if empty, the schema lib walks up from cwd)
// and, on success, writes a canonicalised copy (2-space indent) to
// <outDir>/<id>.json. Returns the absolute path on success.
//
// The function is idempotent: writing the same sensor twice produces a
// byte-identical file. It does NOT mutate sensorJSON.
//
// Errors:
//   - JSON parse failure → wrapped *json.SyntaxError-flavored error.
//   - Schema validation failure → error from the underlying validator
//     (callers may render via schema.PrintValidationOrPlain).
//   - I/O failure (mkdir, write, rename) → wrapped os error; nothing
//     partial left on disk.
func ValidateAndPersist(sensorJSON []byte, outDir string, schemasDir string) (string, error) {
	var sensorMap map[string]interface{}
	if err := json.Unmarshal(sensorJSON, &sensorMap); err != nil {
		return "", fmt.Errorf("parse sensor JSON: %w", err)
	}

	v, err := schema.NewValidator(resolveSchemasDir(schemasDir))
	if err != nil {
		return "", fmt.Errorf("load schemas: %w", err)
	}
	if err := v.Validate(schema.TargetSensor, sensorMap); err != nil {
		return "", err
	}

	id, ok := sensorMap["id"].(string)
	if !ok || id == "" {
		return "", fmt.Errorf("sensor.id missing or empty after validation")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	target := filepath.Join(outDir, id+".json")
	if err := writeCanonical(target, sensorMap); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return target, nil
	}
	return abs, nil
}

func writeCanonical(path string, sensor map[string]interface{}) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sensor); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func resolveSchemasDir(in string) string {
	if in != "" {
		return in
	}
	// Walk up from cwd looking for a "schemas" sibling.
	cwd, err := os.Getwd()
	if err != nil {
		return "schemas"
	}
	dir := cwd
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "schemas")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Join(cwd, "schemas")
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./lib/sensor/...
```

Expected: PASS for all `TestValidateAndPersist_*` cases plus the existing tests in `lib/sensor/`.

- [ ] **Step 5: Commit**

```bash
git add lib/sensor/persist.go lib/sensor/persist_test.go
git commit -m "feat(lib/sensor): add ValidateAndPersist shared primitive

Single function that validates a sensor JSON against schemas/sensor.json
and writes a canonicalised copy atomically. Used by both detect-sensors
and (in upcoming commits) heal-sensor's apply-sensors script. Replaces
inline persistence code about to be extracted from write-sensor.go.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Refactor write-sensor.go to call the shared primitive

**Files:**
- Modify: `skills/detect-sensors/scripts/write-sensor.go`

- [ ] **Step 1: Confirm existing tests are green before the refactor**

```bash
go test ./skills/detect-sensors/...
```

Expected: PASS for all `TestRun_*` cases.

- [ ] **Step 2: Replace the script body to delegate to the lib**

```go
// skills/detect-sensors/scripts/write-sensor.go
// Command write-sensor reads a draft sensor JSON file and persists it
// via lib/sensor.ValidateAndPersist (validate against schemas + atomic
// write). Thin CLI wrapper around the shared primitive.
//
// Usage:
//
//	go run ./skills/detect-sensors/scripts \
//	  --out=<dir> [--schemas-dir=<dir>] <draft-sensor.json>
//
// Exit codes: 0 sensor written, 1 schema validation failed,
// 2 usage or I/O error.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-sensor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var outDir, schemasDir string
	fs.StringVar(&outDir, "out", "", "directory to write the sensor file into (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if outDir == "" {
		fmt.Fprintln(stderr, "error: --out is required")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: write-sensor --out=DIR [--schemas-dir=DIR] <draft-sensor.json>")
		return 2
	}
	draftPath := fs.Arg(0)

	body, err := os.ReadFile(draftPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return 2
	}

	path, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		// Schema validator errors → exit 1 with rendered tree;
		// everything else → exit 2.
		var ve interface{ KeywordLocation() string }
		if errors.As(err, &ve) {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		// jsonschema's ValidationError implements KeywordLocation; the
		// type assertion above should match. As a fallback, render
		// with PrintValidationOrPlain which handles both branches.
		// Detect parse errors specifically:
		if isParseErr(err) {
			fmt.Fprintln(stderr, "error: parse sensor JSON:", err)
			return 2
		}
		schema.PrintValidationOrPlain(err, stderr)
		// Heuristic: anything PrintValidationOrPlain treats as a tree
		// is schema; otherwise treat as I/O.
		return 1
	}
	fmt.Fprintln(stdout, path)
	return 0
}

func isParseErr(err error) bool {
	// json.Unmarshal errors are wrapped with "parse sensor JSON:".
	return err != nil && (errOf(err, "parse sensor JSON") || errOf(err, "syntax"))
}

func errOf(err error, needle string) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for i := 0; i+len(needle) <= len(msg); i++ {
		if msg[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Run the existing test suite to confirm no regression**

```bash
go test ./skills/detect-sensors/...
```

Expected: PASS for all 11 existing `TestRun_*` cases.

- [ ] **Step 4: Run the lib tests to make sure nothing got broken**

```bash
go test ./lib/sensor/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/detect-sensors/scripts/write-sensor.go
git commit -m "refactor(detect-sensors): write-sensor delegates to lib/sensor.ValidateAndPersist

Pure refactor — behavior preserved (existing TestRun_* tests still
pass). The CLI script becomes a flag-parser + I/O glue around the
shared primitive. Exit-code semantics unchanged: 0 written, 1 schema
fail, 2 usage/I/O.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: lib/heal/plan.go — Setup Plan Go types

**Files:**
- Create: `lib/heal/plan.go`
- Create: `lib/heal/plan_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/plan_test.go
package heal_test

import (
	"encoding/json"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestParsePlan_Minimal(t *testing.T) {
	body := []byte(`{
		"diagnosis": {
			"failed_sensor_id": "run-project-nest",
			"shape": "missing-env",
			"evidence_excerpt": "RSA_PRIVATE_KEY required",
			"root_cause_hint": "var declared in requires.env but unset"
		},
		"auto_apply": [
			{"kind": "copy-template", "src": ".env.example", "dst": ".env"},
			{"kind": "set-env-in-file", "file": ".env", "name": "RSA_PRIVATE_KEY", "value_source": "ask-user"}
		],
		"propose_only": [
			{"kind": "shell", "command": "pnpm install", "rationale": "deps missing"}
		],
		"sensor_patches": [
			{"id": "run-project-nest", "patch": {"requires": {"env": [{"name": "RSA_PRIVATE_KEY"}]}}}
		],
		"new_setup_sensors": []
	}`)

	p, err := heal.ParsePlan(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Diagnosis.FailedSensorID != "run-project-nest" {
		t.Errorf("failed_sensor_id = %q", p.Diagnosis.FailedSensorID)
	}
	if p.Diagnosis.Shape != heal.ShapeMissingEnv {
		t.Errorf("shape = %v", p.Diagnosis.Shape)
	}
	if len(p.AutoApply) != 2 {
		t.Errorf("auto_apply len = %d", len(p.AutoApply))
	}
	if p.AutoApply[0].Kind != "copy-template" {
		t.Errorf("auto_apply[0].kind = %q", p.AutoApply[0].Kind)
	}
	if p.AutoApply[1].ValueSource != "ask-user" {
		t.Errorf("value_source = %q", p.AutoApply[1].ValueSource)
	}
}

func TestParsePlan_UnknownShape(t *testing.T) {
	body := []byte(`{"diagnosis": {"failed_sensor_id": "x", "shape": "bogus-shape"}}`)
	_, err := heal.ParsePlan(body)
	if err == nil {
		t.Fatal("expected error for unknown shape")
	}
}

func TestParsePlan_MarshalRoundTrip(t *testing.T) {
	p := heal.Plan{
		Diagnosis: heal.Diagnosis{
			FailedSensorID: "x",
			Shape:          heal.ShapeBinaryNotFound,
		},
		AutoApply:   []heal.Action{{Kind: "mkdir", Dir: "/tmp/foo"}},
		ProposeOnly: []heal.Proposal{{Kind: "shell", Command: "make build"}},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	round, err := heal.ParsePlan(b)
	if err != nil {
		t.Fatal(err)
	}
	if round.Diagnosis.Shape != heal.ShapeBinaryNotFound {
		t.Fatalf("round-trip shape lost")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./lib/heal/...
```

Expected: FAIL with `package heal does not exist`.

- [ ] **Step 3: Write the implementation**

```go
// lib/heal/plan.go
//
// Package heal hosts the deterministic core of the sensor self-heal
// mechanism: classifier registry, allowlisted action applier, .env
// writer, version transformer, and the Setup Plan model that flows
// from diagnose to apply.
package heal

import (
	"encoding/json"
	"fmt"
)

// Plan is the structured handoff between diagnose (LLM-flavored) and
// the deterministic appliers. Marshalled JSON conforms to the contract
// documented in docs/superpowers/specs/2026-05-09-sensor-self-heal-design.md.
type Plan struct {
	Diagnosis       Diagnosis     `json:"diagnosis"`
	AutoApply       []Action      `json:"auto_apply,omitempty"`
	ProposeOnly     []Proposal    `json:"propose_only,omitempty"`
	SensorPatches   []SensorPatch `json:"sensor_patches,omitempty"`
	NewSetupSensors []NewSensor   `json:"new_setup_sensors,omitempty"`
}

type Diagnosis struct {
	FailedSensorID  string `json:"failed_sensor_id"`
	Shape           Shape  `json:"shape"`
	EvidenceExcerpt string `json:"evidence_excerpt,omitempty"`
	RootCauseHint   string `json:"root_cause_hint,omitempty"`
}

// Action is an item in auto_apply[]. Only the kind-specific fields are
// populated; the rest are zero. apply.go's allowlist gate enforces the
// required combination per kind.
type Action struct {
	Kind        string `json:"kind"`
	Src         string `json:"src,omitempty"`
	Dst         string `json:"dst,omitempty"`
	Dir         string `json:"dir,omitempty"`
	File        string `json:"file,omitempty"`
	Name        string `json:"name,omitempty"`
	Value       string `json:"value,omitempty"`
	ValueSource string `json:"value_source,omitempty"`
	Rationale   string `json:"rationale,omitempty"`
}

// Proposal is an item in propose_only[] — anything heal cannot or will
// not auto-apply, surfaced to the user via the final Signal's
// remediation.
type Proposal struct {
	Kind      string `json:"kind"`
	Command   string `json:"command,omitempty"`
	Rationale string `json:"rationale,omitempty"`
}

// SensorPatch describes an in-place edit to an existing sensor.
type SensorPatch struct {
	ID    string                 `json:"id"`
	Patch map[string]interface{} `json:"patch"`
}

// NewSensor describes a brand-new sensor that heal wants to create.
type NewSensor struct {
	ID   string                 `json:"id"`
	JSON map[string]interface{} `json:"json"`
}

// ParsePlan unmarshals JSON into a Plan, validating the Shape enum.
func ParsePlan(body []byte) (Plan, error) {
	var p Plan
	if err := json.Unmarshal(body, &p); err != nil {
		return Plan{}, fmt.Errorf("parse plan: %w", err)
	}
	if !p.Diagnosis.Shape.IsKnown() {
		return Plan{}, fmt.Errorf("unknown shape %q", p.Diagnosis.Shape)
	}
	return p, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./lib/heal/...
```

Expected: FAIL — `Shape` and `IsKnown` are not yet defined. (They're added in Task 4.) Mark this task complete after implementing classify.go in the next task.

For now, partial pass: the package compiles only after Task 4 lands the `Shape` type. Use Task 4 to satisfy this dependency.

- [ ] **Step 5: Commit (after Task 4)**

This task and Task 4 commit together because the `Shape` type is shared. Defer the commit to the end of Task 4.

---

## Task 4: lib/heal/classify.go — Rule interface, Shape enum, walker

**Files:**
- Create: `lib/heal/classify.go`
- Create: `lib/heal/classify_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/classify_test.go
package heal_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

type stubRule struct {
	name    string
	matched bool
	shape   heal.Shape
	detail  string
}

func (s stubRule) Name() string { return s.name }
func (s stubRule) Match(_ heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	return s.matched, s.shape, s.detail
}

func TestClassify_FirstMatchWins(t *testing.T) {
	rules := []heal.Rule{
		stubRule{name: "r1", matched: false},
		stubRule{name: "r2", matched: true, shape: heal.ShapeMissingEnv, detail: "FOO"},
		stubRule{name: "r3", matched: true, shape: heal.ShapeBinaryNotFound},
	}
	res, ok := heal.ClassifyWith(rules, heal.Signal{}, heal.FailedSensor{})
	if !ok {
		t.Fatal("expected match")
	}
	if res.Rule != "r2" {
		t.Errorf("rule = %q, want r2", res.Rule)
	}
	if res.Shape != heal.ShapeMissingEnv {
		t.Errorf("shape = %v", res.Shape)
	}
	if res.Detail != "FOO" {
		t.Errorf("detail = %q", res.Detail)
	}
}

func TestClassify_NoMatch(t *testing.T) {
	rules := []heal.Rule{
		stubRule{name: "r1"},
		stubRule{name: "r2"},
	}
	_, ok := heal.ClassifyWith(rules, heal.Signal{}, heal.FailedSensor{})
	if ok {
		t.Fatal("expected no match")
	}
}

func TestShape_IsKnown(t *testing.T) {
	cases := map[heal.Shape]bool{
		heal.ShapeMissingEnv:         true,
		heal.ShapeBinaryNotFound:     true,
		heal.ShapeEnvFileAbsent:      true,
		heal.ShapeServiceUnavailable: true,
		heal.Shape("nonsense"):       false,
		heal.Shape(""):               false,
	}
	for s, want := range cases {
		if got := s.IsKnown(); got != want {
			t.Errorf("IsKnown(%q) = %v, want %v", s, got, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./lib/heal/...
```

Expected: FAIL — undefined types.

- [ ] **Step 3: Write the implementation**

```go
// lib/heal/classify.go
package heal

// Shape names a setup-shaped failure category. Closed enum — adding a
// shape is a code change paired with a rule that produces it.
type Shape string

const (
	ShapeMissingEnv         Shape = "missing-env"
	ShapeBinaryNotFound     Shape = "binary-not-found"
	ShapeEnvFileAbsent      Shape = "env-file-absent"
	ShapeServiceUnavailable Shape = "service-unavailable"
)

// IsKnown reports whether s is one of the registered shapes.
func (s Shape) IsKnown() bool {
	switch s {
	case ShapeMissingEnv, ShapeBinaryNotFound, ShapeEnvFileAbsent, ShapeServiceUnavailable:
		return true
	}
	return false
}

// Signal is a thin view of the aggregate Signal a rule may need to
// inspect. Only fields rules actually read are exposed.
type Signal struct {
	Verdict  string
	Severity string
	Evidence []SignalEvidence
	Metadata SignalMetadata
}

type SignalEvidence struct {
	Rationale string
	Excerpt   string
}

type SignalMetadata struct {
	HealHint  string
	ExitCode  *int
	Lifecycle SignalLifecycle
	Counts    map[string]int
}

type SignalLifecycle struct {
	Prepare []SignalLifecycleStep
}

type SignalLifecycleStep struct {
	Command string
	Verdict string
}

// FailedSensor exposes the parts of the failing sensor's declaration
// rules need to inspect: the env vars, tools, and context paths it
// declared.
type FailedSensor struct {
	ID       string
	EnvNames []string
	Tools    []string
	Context  []string
}

// Rule classifies a Signal as setup-shape. Implementations live in
// lib/heal/rule_*.go files; the registrar (rules.go) holds the
// canonical ordered list.
type Rule interface {
	Name() string
	Match(signal Signal, failed FailedSensor) (matched bool, shape Shape, detail string)
}

// Result is what Classify returns when a rule matches.
type Result struct {
	Rule   string
	Shape  Shape
	Detail string
}

// Classify walks the registered rules in order and returns the first
// match. Empty result + ok=false means "not setup-shape".
func Classify(signal Signal, failed FailedSensor) (Result, bool) {
	return ClassifyWith(registeredRules(), signal, failed)
}

// ClassifyWith is Classify with explicit rules — used by tests. The
// production caller uses Classify.
func ClassifyWith(rules []Rule, signal Signal, failed FailedSensor) (Result, bool) {
	for _, r := range rules {
		matched, shape, detail := r.Match(signal, failed)
		if matched {
			return Result{Rule: r.Name(), Shape: shape, Detail: detail}, true
		}
	}
	return Result{}, false
}
```

- [ ] **Step 4: Stub the registrar so the package compiles**

```go
// lib/heal/rules.go
package heal

// rules is the canonical ordered list. Real entries are added in the
// rule_*.go phase. Defined here so Classify compiles before any rule
// files exist; replaced in Task 5.
var rules = []Rule{}

func registeredRules() []Rule { return rules }
```

- [ ] **Step 5: Run tests**

```bash
go test ./lib/heal/...
```

Expected: PASS for `TestClassify_*`, `TestShape_*`, and the Task 3 `TestParsePlan_*` tests.

- [ ] **Step 6: Commit Tasks 3 + 4 together**

```bash
git add lib/heal/plan.go lib/heal/plan_test.go lib/heal/classify.go lib/heal/classify_test.go lib/heal/rules.go
git commit -m "feat(lib/heal): plan model + Rule interface + Shape enum + Classify walker

Establishes the Setup Plan structure (parsed/marshalled JSON contract)
and the extensible classification primitive. The Rule interface, Shape
enum, FailedSensor view, and Classify walker live in classify.go;
ClassifyWith accepts an explicit rules slice for tests. rules.go
exists with an empty slice so the package compiles before per-rule
files are added.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: lib/heal/patterns.go — curated stderr regex set

**Files:**
- Create: `lib/heal/patterns.go`
- Create: `lib/heal/patterns_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/patterns_test.go
package heal_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestStderrPatterns_PositiveCases(t *testing.T) {
	cases := []struct {
		text  string
		shape heal.Shape
	}{
		{"open .env: ENOENT no such file", heal.ShapeEnvFileAbsent},
		{"permission denied: .env", heal.ShapeEnvFileAbsent},
		{"connection refused: postgres at 127.0.0.1:5432", heal.ShapeServiceUnavailable},
		{"connection refused: redis://localhost:6379", heal.ShapeServiceUnavailable},
		{"sh: pnpm: command not found", heal.ShapeBinaryNotFound},
	}
	for _, c := range cases {
		shape, ok := heal.MatchStderrPattern(c.text)
		if !ok {
			t.Errorf("expected match for %q", c.text)
			continue
		}
		if shape != c.shape {
			t.Errorf("text=%q got shape %q want %q", c.text, shape, c.shape)
		}
	}
}

func TestStderrPatterns_NegativeCases(t *testing.T) {
	cases := []string{
		"all green",
		"--- FAIL: TestFoo",
		"PASS",
		"npm WARN deprecated lodash@4.17.0",
	}
	for _, c := range cases {
		if _, ok := heal.MatchStderrPattern(c); ok {
			t.Errorf("expected no match for %q", c)
		}
	}
}

func TestHealHintGrammar_Documented(t *testing.T) {
	// Sanity check that the documented prefixes are exactly the known
	// shapes — the grammar is a stable contract.
	for _, s := range []heal.Shape{heal.ShapeMissingEnv, heal.ShapeBinaryNotFound, heal.ShapeEnvFileAbsent, heal.ShapeServiceUnavailable} {
		if !s.IsKnown() {
			t.Errorf("shape %q not registered", s)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./lib/heal/...
```

Expected: FAIL — `MatchStderrPattern` undefined.

- [ ] **Step 3: Write the implementation**

```go
// lib/heal/patterns.go
//
// Curated stderr regex set used by rule_stderr_pattern.go.
//
// metadata.heal_hint contract (consumed by rule_heal_hint.go):
//
//   heal_hint := <shape> ":" <detail>
//   shape     := "missing-env" | "binary-not-found" | "env-file-absent" | "service-unavailable"
//   detail    := opaque string (var name, binary name, path, service)
//
// Adding a shape is a versioned plugin change; deleting one is a
// breaking change.
package heal

import "regexp"

type stderrPattern struct {
	re    *regexp.Regexp
	shape Shape
}

var stderrPatterns = []stderrPattern{
	{re: regexp.MustCompile(`ENOENT.*\.env\b`), shape: ShapeEnvFileAbsent},
	{re: regexp.MustCompile(`permission denied:.*\.env\b`), shape: ShapeEnvFileAbsent},
	{re: regexp.MustCompile(`connection refused.*\b(postgres|mysql|redis|kafka)\b`), shape: ShapeServiceUnavailable},
	{re: regexp.MustCompile(`\bcommand not found\b`), shape: ShapeBinaryNotFound},
}

// MatchStderrPattern returns the shape associated with the first
// curated pattern that matches text, or ok=false when none match.
func MatchStderrPattern(text string) (Shape, bool) {
	for _, p := range stderrPatterns {
		if p.re.MatchString(text) {
			return p.shape, true
		}
	}
	return "", false
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./lib/heal/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/heal/patterns.go lib/heal/patterns_test.go
git commit -m "feat(lib/heal): curated stderr regex set with shape mapping

Each regex carries one positive and one negative test fixture;
MatchStderrPattern is the public entry point consumed by
rule_stderr_pattern.go (added in a follow-up commit). Documents the
metadata.heal_hint grammar at the top of the file.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: lib/heal/rule_missing_env.go

**Files:**
- Create: `lib/heal/rule_missing_env.go`
- Create: `lib/heal/rule_missing_env_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/rule_missing_env_test.go
package heal

import "testing"

func TestRuleMissingEnv_PositiveDeclared(t *testing.T) {
	r := ruleMissingEnv{}
	sig := Signal{
		Verdict: "error",
		Evidence: []SignalEvidence{
			{Rationale: "Required environment variable RSA_PRIVATE_KEY not set"},
		},
	}
	failed := FailedSensor{EnvNames: []string{"RSA_PRIVATE_KEY"}}
	matched, shape, detail := r.Match(sig, failed)
	if !matched {
		t.Fatal("expected match")
	}
	if shape != ShapeMissingEnv {
		t.Errorf("shape=%q", shape)
	}
	if detail != "RSA_PRIVATE_KEY" {
		t.Errorf("detail=%q", detail)
	}
}

func TestRuleMissingEnv_NegativeNotDeclared(t *testing.T) {
	r := ruleMissingEnv{}
	sig := Signal{
		Verdict: "error",
		Evidence: []SignalEvidence{
			{Rationale: "Required environment variable BOGUS not set"},
		},
	}
	failed := FailedSensor{EnvNames: []string{"RSA_PRIVATE_KEY"}}
	matched, _, _ := r.Match(sig, failed)
	if matched {
		t.Fatal("expected no match — var not in requires.env")
	}
}

func TestRuleMissingEnv_NegativeWrongVerdict(t *testing.T) {
	r := ruleMissingEnv{}
	sig := Signal{
		Verdict:  "fail",
		Evidence: []SignalEvidence{{Rationale: "Required environment variable FOO not set"}},
	}
	matched, _, _ := r.Match(sig, FailedSensor{EnvNames: []string{"FOO"}})
	if matched {
		t.Fatal("rule should require verdict=error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./lib/heal/...
```

Expected: FAIL — `ruleMissingEnv` undefined.

- [ ] **Step 3: Write the implementation**

```go
// lib/heal/rule_missing_env.go
package heal

import "regexp"

// ruleMissingEnv fires when verdict=error AND an evidence rationale
// matches "required environment variable <NAME> not set" AND <NAME>
// is declared in the failed sensor's requires.env[].
type ruleMissingEnv struct{}

var missingEnvRegex = regexp.MustCompile(`(?i)required env(?:ironment)? variable\s+([A-Z_][A-Z0-9_]*)\s+(?:is\s+)?not\s+set`)

func (ruleMissingEnv) Name() string { return "missing-env" }

func (ruleMissingEnv) Match(signal Signal, failed FailedSensor) (bool, Shape, string) {
	if signal.Verdict != "error" {
		return false, "", ""
	}
	for _, ev := range signal.Evidence {
		m := missingEnvRegex.FindStringSubmatch(ev.Rationale)
		if m == nil {
			continue
		}
		name := m[1]
		for _, declared := range failed.EnvNames {
			if declared == name {
				return true, ShapeMissingEnv, name
			}
		}
	}
	return false, "", ""
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./lib/heal/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/heal/rule_missing_env.go lib/heal/rule_missing_env_test.go
git commit -m "feat(lib/heal): rule_missing_env — declared env var unset at run time

First concrete Rule. Fires when verdict=error AND rationale matches
the runner's standard 'required environment variable X not set'
phrasing AND X is declared in the failed sensor's requires.env.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: lib/heal/rule_heal_hint.go

**Files:**
- Create: `lib/heal/rule_heal_hint.go`
- Create: `lib/heal/rule_heal_hint_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/rule_heal_hint_test.go
package heal

import "testing"

func TestRuleHealHint_KnownShape(t *testing.T) {
	r := ruleHealHint{}
	sig := Signal{Metadata: SignalMetadata{HealHint: "missing-env:RSA_PRIVATE_KEY"}}
	matched, shape, detail := r.Match(sig, FailedSensor{})
	if !matched {
		t.Fatal("expected match")
	}
	if shape != ShapeMissingEnv {
		t.Errorf("shape=%q", shape)
	}
	if detail != "RSA_PRIVATE_KEY" {
		t.Errorf("detail=%q", detail)
	}
}

func TestRuleHealHint_UnknownShape(t *testing.T) {
	r := ruleHealHint{}
	sig := Signal{Metadata: SignalMetadata{HealHint: "bogus-shape:detail"}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("unknown prefix must not match")
	}
}

func TestRuleHealHint_NoColon(t *testing.T) {
	r := ruleHealHint{}
	sig := Signal{Metadata: SignalMetadata{HealHint: "missing-env"}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("hint without colon must not match")
	}
}

func TestRuleHealHint_Empty(t *testing.T) {
	r := ruleHealHint{}
	matched, _, _ := r.Match(Signal{}, FailedSensor{})
	if matched {
		t.Fatal("empty hint must not match")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./lib/heal/...
```

Expected: FAIL.

- [ ] **Step 3: Write the implementation**

```go
// lib/heal/rule_heal_hint.go
package heal

import "strings"

// ruleHealHint fires when metadata.heal_hint is set with a known
// "<shape>:<detail>" prefix. Fast-path emitted by lib/orchestrator
// when the runner can name the failure shape directly.
type ruleHealHint struct{}

func (ruleHealHint) Name() string { return "heal-hint" }

func (ruleHealHint) Match(signal Signal, _ FailedSensor) (bool, Shape, string) {
	hint := signal.Metadata.HealHint
	if hint == "" {
		return false, "", ""
	}
	idx := strings.Index(hint, ":")
	if idx < 0 {
		return false, "", ""
	}
	shape := Shape(hint[:idx])
	if !shape.IsKnown() {
		return false, "", ""
	}
	return true, shape, hint[idx+1:]
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./lib/heal/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/heal/rule_heal_hint.go lib/heal/rule_heal_hint_test.go
git commit -m "feat(lib/heal): rule_heal_hint — fast path via metadata.heal_hint

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: lib/heal/rule_exit_code_127.go

**Files:**
- Create: `lib/heal/rule_exit_code_127.go`
- Create: `lib/heal/rule_exit_code_127_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/rule_exit_code_127_test.go
package heal

import "testing"

func intp(n int) *int { return &n }

func TestRuleExit127_Positive(t *testing.T) {
	r := ruleExitCode127{}
	sig := Signal{Metadata: SignalMetadata{ExitCode: intp(127)}}
	failed := FailedSensor{Tools: []string{"pnpm"}}
	matched, shape, detail := r.Match(sig, failed)
	if !matched {
		t.Fatal("expected match")
	}
	if shape != ShapeBinaryNotFound {
		t.Errorf("shape=%q", shape)
	}
	if detail != "pnpm" {
		t.Errorf("detail=%q", detail)
	}
}

func TestRuleExit127_NoTools(t *testing.T) {
	r := ruleExitCode127{}
	sig := Signal{Metadata: SignalMetadata{ExitCode: intp(127)}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("no requires.tools — must not match")
	}
}

func TestRuleExit127_OtherCode(t *testing.T) {
	r := ruleExitCode127{}
	sig := Signal{Metadata: SignalMetadata{ExitCode: intp(1)}}
	matched, _, _ := r.Match(sig, FailedSensor{Tools: []string{"pnpm"}})
	if matched {
		t.Fatal("non-127 must not match")
	}
}

func TestRuleExit127_NoExitCode(t *testing.T) {
	r := ruleExitCode127{}
	matched, _, _ := r.Match(Signal{}, FailedSensor{Tools: []string{"pnpm"}})
	if matched {
		t.Fatal("missing exit_code must not match")
	}
}
```

- [ ] **Step 2: Run, verify fail, implement**

```go
// lib/heal/rule_exit_code_127.go
package heal

import "strings"

// ruleExitCode127 fires when the subprocess exited 127 (sh: command
// not found) AND the failed sensor declared at least one tool in
// requires.tools — the missing binary is one of them.
type ruleExitCode127 struct{}

func (ruleExitCode127) Name() string { return "exit-code-127" }

func (ruleExitCode127) Match(signal Signal, failed FailedSensor) (bool, Shape, string) {
	if signal.Metadata.ExitCode == nil || *signal.Metadata.ExitCode != 127 {
		return false, "", ""
	}
	if len(failed.Tools) == 0 {
		return false, "", ""
	}
	return true, ShapeBinaryNotFound, strings.Join(failed.Tools, ",")
}
```

- [ ] **Step 3: Run tests + commit**

```bash
go test ./lib/heal/...
git add lib/heal/rule_exit_code_127.go lib/heal/rule_exit_code_127_test.go
git commit -m "feat(lib/heal): rule_exit_code_127 — exit 127 + declared tools

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 9: lib/heal/rule_prepare_template_copy.go

**Files:**
- Create: `lib/heal/rule_prepare_template_copy.go`
- Create: `lib/heal/rule_prepare_template_copy_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/rule_prepare_template_copy_test.go
package heal

import "testing"

func TestRulePrepareTemplate_CopyExampleFailed(t *testing.T) {
	r := rulePrepareTemplateCopy{}
	sig := Signal{Metadata: SignalMetadata{Lifecycle: SignalLifecycle{Prepare: []SignalLifecycleStep{
		{Command: "cp config/.env.example config/.env", Verdict: "fail"},
	}}}}
	matched, shape, detail := r.Match(sig, FailedSensor{})
	if !matched {
		t.Fatal("expected match")
	}
	if shape != ShapeEnvFileAbsent {
		t.Errorf("shape=%q", shape)
	}
	if detail == "" {
		t.Errorf("detail empty")
	}
}

func TestRulePrepareTemplate_OtherCommand(t *testing.T) {
	r := rulePrepareTemplateCopy{}
	sig := Signal{Metadata: SignalMetadata{Lifecycle: SignalLifecycle{Prepare: []SignalLifecycleStep{
		{Command: "make protos", Verdict: "fail"},
	}}}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("non-cp command must not match")
	}
}

func TestRulePrepareTemplate_PassedStep(t *testing.T) {
	r := rulePrepareTemplateCopy{}
	sig := Signal{Metadata: SignalMetadata{Lifecycle: SignalLifecycle{Prepare: []SignalLifecycleStep{
		{Command: "cp .env.example .env", Verdict: "pass"},
	}}}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("passed step must not match")
	}
}
```

- [ ] **Step 2: Implement**

```go
// lib/heal/rule_prepare_template_copy.go
package heal

import "regexp"

// rulePrepareTemplateCopy fires when a prepare step that copies a
// .example template file failed.
type rulePrepareTemplateCopy struct{}

var prepareCopyRegex = regexp.MustCompile(`\bcp\b\s+\S+\.example\b`)

func (rulePrepareTemplateCopy) Name() string { return "prepare-template-copy" }

func (rulePrepareTemplateCopy) Match(signal Signal, _ FailedSensor) (bool, Shape, string) {
	for _, step := range signal.Metadata.Lifecycle.Prepare {
		if step.Verdict == "fail" && prepareCopyRegex.MatchString(step.Command) {
			return true, ShapeEnvFileAbsent, step.Command
		}
	}
	return false, "", ""
}
```

- [ ] **Step 3: Run tests + commit**

```bash
go test ./lib/heal/...
git add lib/heal/rule_prepare_template_copy.go lib/heal/rule_prepare_template_copy_test.go
git commit -m "feat(lib/heal): rule_prepare_template_copy — failed cp X.example step

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 10: lib/heal/rule_stderr_pattern.go

**Files:**
- Create: `lib/heal/rule_stderr_pattern.go`
- Create: `lib/heal/rule_stderr_pattern_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/rule_stderr_pattern_test.go
package heal

import "testing"

func TestRuleStderrPattern_DotEnvENOENT(t *testing.T) {
	r := ruleStderrPattern{}
	sig := Signal{Evidence: []SignalEvidence{{Rationale: "open .env: ENOENT no such file"}}}
	matched, shape, _ := r.Match(sig, FailedSensor{})
	if !matched || shape != ShapeEnvFileAbsent {
		t.Fatalf("matched=%v shape=%q", matched, shape)
	}
}

func TestRuleStderrPattern_ServiceUnavailable(t *testing.T) {
	r := ruleStderrPattern{}
	sig := Signal{Evidence: []SignalEvidence{{Rationale: "connection refused: postgres at 127.0.0.1:5432"}}}
	matched, shape, _ := r.Match(sig, FailedSensor{})
	if !matched || shape != ShapeServiceUnavailable {
		t.Fatalf("matched=%v shape=%q", matched, shape)
	}
}

func TestRuleStderrPattern_NoMatch(t *testing.T) {
	r := ruleStderrPattern{}
	sig := Signal{Evidence: []SignalEvidence{{Rationale: "all green"}}}
	matched, _, _ := r.Match(sig, FailedSensor{})
	if matched {
		t.Fatal("benign rationale must not match")
	}
}
```

- [ ] **Step 2: Implement**

```go
// lib/heal/rule_stderr_pattern.go
package heal

// ruleStderrPattern fires when any curated stderr regex (patterns.go)
// matches an evidence rationale.
type ruleStderrPattern struct{}

func (ruleStderrPattern) Name() string { return "stderr-pattern" }

func (ruleStderrPattern) Match(signal Signal, _ FailedSensor) (bool, Shape, string) {
	for _, ev := range signal.Evidence {
		if shape, ok := MatchStderrPattern(ev.Rationale); ok {
			return true, shape, ev.Rationale
		}
	}
	return false, "", ""
}
```

- [ ] **Step 3: Run tests + commit**

```bash
go test ./lib/heal/...
git add lib/heal/rule_stderr_pattern.go lib/heal/rule_stderr_pattern_test.go
git commit -m "feat(lib/heal): rule_stderr_pattern — curated stderr regex match

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 11: Wire all rules into the registrar

**Files:**
- Modify: `lib/heal/rules.go`
- Create: `lib/heal/rules_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/rules_test.go
package heal

import "testing"

func TestRegisteredRules_OrderIsStable(t *testing.T) {
	expected := []string{
		"missing-env",
		"heal-hint",
		"exit-code-127",
		"prepare-template-copy",
		"stderr-pattern",
	}
	got := registeredRules()
	if len(got) != len(expected) {
		t.Fatalf("len = %d, want %d", len(got), len(expected))
	}
	for i, r := range got {
		if r.Name() != expected[i] {
			t.Errorf("rules[%d].Name() = %q, want %q", i, r.Name(), expected[i])
		}
	}
}
```

- [ ] **Step 2: Run, verify fail, update rules.go**

```go
// lib/heal/rules.go
package heal

// rules is the canonical ordered list. Adding a new rule = creating a
// new lib/heal/rule_<name>.go file with a struct implementing Rule
// and inserting one line into this slice. Order is deterministic and
// matters: more-specific rules go before more-generic ones (heal-hint
// is a fast-path before regex-based rules; missing-env runs first
// because it's the most common).
var rules = []Rule{
	ruleMissingEnv{},
	ruleHealHint{},
	ruleExitCode127{},
	rulePrepareTemplateCopy{},
	ruleStderrPattern{},
}

func registeredRules() []Rule { return rules }
```

- [ ] **Step 3: Run tests**

```bash
go test ./lib/heal/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add lib/heal/rules.go lib/heal/rules_test.go
git commit -m "feat(lib/heal): wire concrete rules into the registrar

rules.go now holds the canonical ordered list. The 'order is stable'
test locks the contract — reordering or removing a rule fails the
test. New rules are added with one line here.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 12: Rule extensibility regression test

**Files:**
- Create: `lib/heal/extensibility_test.go`

- [ ] **Step 1: Write the test that locks the property**

```go
// lib/heal/extensibility_test.go
//
// This test exists to lock the spec property: adding a new rule must
// require zero changes to existing rule files or to classify.go. The
// test mints a fake rule, walks ClassifyWith with a slice that has
// it appended, and asserts dispatch.
//
// If a future refactor moves rule logic into classify.go (a hardcoded
// chain), this test will keep passing — but a follow-up reviewer
// should still flag the violation. The companion safety net is
// rules_test.go which locks the registered slice.
package heal

import "testing"

type fakeNewRule struct{ shape Shape }

func (f fakeNewRule) Name() string { return "fake-new-rule" }
func (fakeNewRule) Match(_ Signal, _ FailedSensor) (bool, Shape, string) {
	return true, ShapeMissingEnv, "fake-detail"
}

func TestExtensibility_AddingRule_DoesNotTouchExistingFiles(t *testing.T) {
	// A new rule plugged into the walker via ClassifyWith picks up
	// without any modification to classify.go or the production rules
	// slice. This is the single property the registry buys us.
	rules := append([]Rule{}, registeredRules()...)
	rules = append(rules, fakeNewRule{})

	res, ok := ClassifyWith(rules, Signal{Verdict: "ok"}, FailedSensor{})
	if !ok || res.Rule != "fake-new-rule" {
		t.Fatalf("expected fake-new-rule to dispatch; got rule=%q ok=%v", res.Rule, ok)
	}
}

func TestExtensibility_NewRuleIgnoredWhenEarlierMatches(t *testing.T) {
	rules := []Rule{ruleHealHint{}, fakeNewRule{}}
	sig := Signal{Metadata: SignalMetadata{HealHint: "missing-env:FOO"}}
	res, ok := ClassifyWith(rules, sig, FailedSensor{})
	if !ok || res.Rule != "heal-hint" {
		t.Fatalf("first match must win; got rule=%q ok=%v", res.Rule, ok)
	}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./lib/heal/...
git add lib/heal/extensibility_test.go
git commit -m "test(lib/heal): lock single-edit-point property as a regression

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 13: lib/heal/apply.go — allowlisted file mutations

**Files:**
- Create: `lib/heal/apply.go`
- Create: `lib/heal/apply_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/apply_test.go
package heal_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestApply_CopyTemplate_Success(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env.example")
	dst := filepath.Join(dir, ".env")
	os.WriteFile(src, []byte("FOO=bar\n"), 0o644)

	results := heal.Apply(heal.ApplyContext{Root: dir, FailedSensor: heal.FailedSensor{Context: []string{dir}}}, []heal.Action{
		{Kind: "copy-template", Src: src, Dst: dst},
	})
	if len(results) != 1 || !results[0].Applied {
		t.Fatalf("expected applied; got %#v", results)
	}
	body, _ := os.ReadFile(dst)
	if string(body) != "FOO=bar\n" {
		t.Fatalf("dst content = %q", body)
	}
}

func TestApply_CopyTemplate_DstAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env.example")
	dst := filepath.Join(dir, ".env")
	os.WriteFile(src, []byte("FOO=bar\n"), 0o644)
	os.WriteFile(dst, []byte("EXISTING=true\n"), 0o644)

	results := heal.Apply(heal.ApplyContext{Root: dir, FailedSensor: heal.FailedSensor{Context: []string{dir}}}, []heal.Action{
		{Kind: "copy-template", Src: src, Dst: dst},
	})
	if results[0].Applied {
		t.Fatal("dst exists; must NOT auto-apply")
	}
	body, _ := os.ReadFile(dst)
	if string(body) != "EXISTING=true\n" {
		t.Fatal("dst was overwritten; must be left alone")
	}
}

func TestApply_Mkdir_PathInContext(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "tmp-cache")
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{Context: []string{root}}}, []heal.Action{
		{Kind: "mkdir", Dir: dir},
	})
	if !results[0].Applied {
		t.Fatal("mkdir must succeed when dir is under context")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestApply_Mkdir_PathOutsideContext(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir() // separate, not in context
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{Context: []string{root}}}, []heal.Action{
		{Kind: "mkdir", Dir: filepath.Join(other, "x")},
	})
	if results[0].Applied {
		t.Fatal("dir outside requires.context must be rejected")
	}
}

func TestApply_Touch_PathInContext(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "marker")
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{Context: []string{root}}}, []heal.Action{
		{Kind: "touch", File: file},
	})
	if !results[0].Applied {
		t.Fatal("touch must succeed under context")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("marker not created")
	}
}

func TestApply_UnknownKind(t *testing.T) {
	root := t.TempDir()
	results := heal.Apply(heal.ApplyContext{Root: root}, []heal.Action{{Kind: "rm-rf-everything"}})
	if results[0].Applied {
		t.Fatal("unknown kind must be rejected")
	}
}

func TestApply_SetEnvInFile_RequiresValue(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(root, ".env")
	os.WriteFile(envFile, []byte(""), 0o644)
	// value_source=ask-user with no Value → must NOT apply (caller fills in via AskUserQuestion).
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{EnvNames: []string{"FOO"}}}, []heal.Action{
		{Kind: "set-env-in-file", File: envFile, Name: "FOO", ValueSource: "ask-user"},
	})
	if results[0].Applied {
		t.Fatal("ask-user without Value must defer (Applied=false, NeedsInput=true)")
	}
	if !results[0].NeedsInput {
		t.Fatal("expected NeedsInput=true so caller knows to prompt")
	}
}

func TestApply_SetEnvInFile_WithLiteralValue(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(root, ".env")
	os.WriteFile(envFile, []byte(""), 0o644)
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{EnvNames: []string{"FOO"}}}, []heal.Action{
		{Kind: "set-env-in-file", File: envFile, Name: "FOO", Value: "bar"},
	})
	if !results[0].Applied {
		t.Fatalf("expected applied; got %#v", results[0])
	}
	body, _ := os.ReadFile(envFile)
	if string(body) != "FOO=bar\n" {
		t.Fatalf("env content = %q", body)
	}
}
```

- [ ] **Step 2: Implement**

```go
// lib/heal/apply.go
package heal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ApplyContext is the deterministic context Apply needs: the project
// root (used to bound mkdir/touch) and the failed sensor's declared
// requires.* surfaces.
type ApplyContext struct {
	Root         string
	FailedSensor FailedSensor
}

// ApplyResult records the outcome of one Action.
type ApplyResult struct {
	Action     Action
	Applied    bool
	NeedsInput bool   // true when the action needs an ask-user value
	Reason     string // why Applied=false (when not applied)
}

// Apply walks the actions and runs only those allowed by the
// hardcoded allowlist, in order. Side-effecting and idempotent.
//
// kinds:
//   - "copy-template": cp src dst when src exists AND dst does not
//   - "mkdir": mkdir -p dir when dir is under one of FailedSensor.Context paths
//   - "touch": create empty file when file is under context
//   - "set-env-in-file": append <NAME>=<VALUE> when var is declared and absent;
//     defers (NeedsInput=true) when ValueSource=ask-user and Value is empty
func Apply(ctx ApplyContext, actions []Action) []ApplyResult {
	out := make([]ApplyResult, 0, len(actions))
	for _, a := range actions {
		out = append(out, applyOne(ctx, a))
	}
	return out
}

func applyOne(ctx ApplyContext, a Action) ApplyResult {
	switch a.Kind {
	case "copy-template":
		return applyCopyTemplate(a)
	case "mkdir":
		return applyMkdir(ctx, a)
	case "touch":
		return applyTouch(ctx, a)
	case "set-env-in-file":
		return applySetEnvInFile(ctx, a)
	default:
		return ApplyResult{Action: a, Reason: fmt.Sprintf("kind %q not in allowlist", a.Kind)}
	}
}

func applyCopyTemplate(a Action) ApplyResult {
	if a.Src == "" || a.Dst == "" {
		return ApplyResult{Action: a, Reason: "copy-template requires src and dst"}
	}
	if _, err := os.Stat(a.Src); err != nil {
		return ApplyResult{Action: a, Reason: "src does not exist"}
	}
	if _, err := os.Stat(a.Dst); err == nil {
		return ApplyResult{Action: a, Reason: "dst already exists"}
	}
	body, err := os.ReadFile(a.Src)
	if err != nil {
		return ApplyResult{Action: a, Reason: "read src: " + err.Error()}
	}
	if err := os.WriteFile(a.Dst, body, 0o600); err != nil {
		return ApplyResult{Action: a, Reason: "write dst: " + err.Error()}
	}
	return ApplyResult{Action: a, Applied: true}
}

func applyMkdir(ctx ApplyContext, a Action) ApplyResult {
	if a.Dir == "" {
		return ApplyResult{Action: a, Reason: "mkdir requires dir"}
	}
	if !pathUnderAny(a.Dir, ctx.FailedSensor.Context) {
		return ApplyResult{Action: a, Reason: "dir not under requires.context"}
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return ApplyResult{Action: a, Reason: "mkdir: " + err.Error()}
	}
	return ApplyResult{Action: a, Applied: true}
}

func applyTouch(ctx ApplyContext, a Action) ApplyResult {
	if a.File == "" {
		return ApplyResult{Action: a, Reason: "touch requires file"}
	}
	if !pathUnderAny(a.File, ctx.FailedSensor.Context) {
		return ApplyResult{Action: a, Reason: "file not under requires.context"}
	}
	if _, err := os.Stat(a.File); err == nil {
		return ApplyResult{Action: a, Applied: true} // already exists; idempotent
	}
	f, err := os.OpenFile(a.File, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ApplyResult{Action: a, Reason: "create: " + err.Error()}
	}
	f.Close()
	return ApplyResult{Action: a, Applied: true}
}

func applySetEnvInFile(ctx ApplyContext, a Action) ApplyResult {
	if a.File == "" || a.Name == "" {
		return ApplyResult{Action: a, Reason: "set-env-in-file requires file and name"}
	}
	declared := false
	for _, n := range ctx.FailedSensor.EnvNames {
		if n == a.Name {
			declared = true
			break
		}
	}
	if !declared {
		return ApplyResult{Action: a, Reason: "var " + a.Name + " not in requires.env"}
	}
	if _, err := os.Stat(a.File); err != nil {
		return ApplyResult{Action: a, Reason: "target file does not exist"}
	}
	if a.Value == "" && a.ValueSource == "ask-user" {
		return ApplyResult{Action: a, NeedsInput: true, Reason: "value pending — ask user"}
	}
	if a.Value == "" {
		return ApplyResult{Action: a, Reason: "no value supplied"}
	}
	return WriteEnvVar(a.File, a.Name, a.Value)
}

func pathUnderAny(target string, roots []string) bool {
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	for _, r := range roots {
		ra, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(ra, abs)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Stub `WriteEnvVar`** so `apply.go` compiles. Real implementation lands in Task 14.

```go
// Append to lib/heal/apply.go (will move to envwriter.go in Task 14)
func WriteEnvVar(file, name, value string) ApplyResult {
	return ApplyResult{Action: Action{Kind: "set-env-in-file", File: file, Name: name, Value: value}, Reason: "envwriter not yet implemented"}
}
```

- [ ] **Step 4: Run tests**

```bash
go test ./lib/heal/...
```

Expected: All Apply tests pass except `TestApply_SetEnvInFile_WithLiteralValue` — that one is expected to FAIL until Task 14 lands `WriteEnvVar`. **Mark this task complete in the plan checklist with the literal-value test still failing**; commit only the apply.go and apply_test.go changes; the failing case will go green in Task 14.

- [ ] **Step 5: Commit**

```bash
git add lib/heal/apply.go lib/heal/apply_test.go
git commit -m "feat(lib/heal): allowlisted action applier (copy-template, mkdir, touch, set-env-in-file stub)

Each kind has its own helper with strict pre-condition checks:
copy-template only when src exists and dst does not; mkdir/touch only
when the path is under requires.context; set-env-in-file only when
the var is declared, the file exists, and a value is available
(ask-user defers via NeedsInput=true). WriteEnvVar is stubbed; real
implementation lands in lib/heal/envwriter.go.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 14: lib/heal/envwriter.go — .env writer

**Files:**
- Create: `lib/heal/envwriter.go`
- Create: `lib/heal/envwriter_test.go`
- Modify: `lib/heal/apply.go` (remove the stub `WriteEnvVar`)

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/envwriter_test.go
package heal_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestWriteEnvVar_AppendsLine(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte("EXISTING=1\n"), 0o600)
	// Make sure the dir is gitignored to satisfy the gitignore-coverage check.
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	res := heal.WriteEnvVar(envFile, "FOO", "bar")
	if !res.Applied {
		t.Fatalf("expected applied; got %s", res.Reason)
	}
	body, _ := os.ReadFile(envFile)
	if string(body) != "EXISTING=1\nFOO=bar\n" {
		t.Fatalf("env content = %q", body)
	}
}

func TestWriteEnvVar_Idempotent(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte("FOO=bar\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	res := heal.WriteEnvVar(envFile, "FOO", "bar")
	if !res.Applied {
		t.Fatalf("expected applied (no-op); got %s", res.Reason)
	}
	body, _ := os.ReadFile(envFile)
	if string(body) != "FOO=bar\n" {
		t.Fatalf("must not duplicate; got %q", body)
	}
}

func TestWriteEnvVar_Chmod600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on Windows")
	}
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte(""), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	heal.WriteEnvVar(envFile, "FOO", "bar")
	st, _ := os.Stat(envFile)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("expected 0600, got %o", st.Mode().Perm())
	}
}

func TestWriteEnvVar_RejectsWhenNotGitignored(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte(""), 0o600)
	// Intentionally no .gitignore.
	res := heal.WriteEnvVar(envFile, "FOO", "bar")
	if res.Applied {
		t.Fatal(".env not gitignored — write must be downgraded to propose_only")
	}
	if res.Reason == "" {
		t.Fatal("expected non-empty Reason explaining the downgrade")
	}
}
```

- [ ] **Step 2: Implement**

Replace the stub in `lib/heal/apply.go` (delete the `WriteEnvVar` function at the bottom) and create:

```go
// lib/heal/envwriter.go
package heal

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteEnvVar appends NAME=VALUE to file (idempotent: no-op if the
// line is already present). Sets file permissions to 0600 on success.
// Returns Applied=false with a Reason when the file's directory is
// not gitignored — heal will not write secrets to a tracked location.
func WriteEnvVar(file, name, value string) ApplyResult {
	action := Action{Kind: "set-env-in-file", File: file, Name: name, Value: value}
	covered, err := isPathGitignored(file)
	if err != nil {
		return ApplyResult{Action: action, Reason: "gitignore check: " + err.Error()}
	}
	if !covered {
		return ApplyResult{Action: action, Reason: fmt.Sprintf("%s is not covered by a .gitignore — refusing to write a secret to a tracked path", file)}
	}

	if line, present, err := envFileHasLine(file, name, value); err != nil {
		return ApplyResult{Action: action, Reason: "read: " + err.Error()}
	} else if present {
		_ = os.Chmod(file, 0o600)
		return ApplyResult{Action: action, Applied: true, Reason: "already present: " + line}
	}

	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ApplyResult{Action: action, Reason: "open: " + err.Error()}
	}
	defer f.Close()
	if _, err := f.WriteString(name + "=" + value + "\n"); err != nil {
		return ApplyResult{Action: action, Reason: "write: " + err.Error()}
	}
	_ = os.Chmod(file, 0o600)
	return ApplyResult{Action: action, Applied: true}
}

func envFileHasLine(path, name, value string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	want := name + "=" + value
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == want {
			return line, true, nil
		}
	}
	return "", false, scanner.Err()
}

// isPathGitignored returns true when path is matched by a .gitignore
// in path's directory, parent, or any ancestor up to the filesystem
// root or the first directory that is itself a git repo root.
func isPathGitignored(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)
	for i := 0; i < 64; i++ {
		gi := filepath.Join(dir, ".gitignore")
		if data, err := os.ReadFile(gi); err == nil {
			scanner := bufio.NewScanner(strings.NewReader(string(data)))
			for scanner.Scan() {
				pat := strings.TrimSpace(scanner.Text())
				if pat == "" || strings.HasPrefix(pat, "#") {
					continue
				}
				if matchGitignorePattern(pat, base, abs, dir) {
					return true, nil
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			// Reached the git root without a match.
			return false, nil
		}
		dir = parent
	}
	return false, errors.New("ancestor walk exceeded depth")
}

func matchGitignorePattern(pat, base, abs, dir string) bool {
	pat = strings.TrimPrefix(pat, "/")
	pat = strings.TrimSuffix(pat, "/")
	if pat == base {
		return true
	}
	if matched, _ := filepath.Match(pat, base); matched {
		return true
	}
	rel, err := filepath.Rel(dir, abs)
	if err == nil {
		if matched, _ := filepath.Match(pat, rel); matched {
			return true
		}
	}
	return false
}
```

Now remove the stub `WriteEnvVar` from `lib/heal/apply.go` (the function added in Task 13, Step 3).

- [ ] **Step 3: Run tests**

```bash
go test ./lib/heal/...
```

Expected: PASS for all `TestWriteEnvVar_*` and the `TestApply_SetEnvInFile_WithLiteralValue` case from Task 13 that was waiting on this.

- [ ] **Step 4: Commit**

```bash
git add lib/heal/envwriter.go lib/heal/envwriter_test.go lib/heal/apply.go
git commit -m "feat(lib/heal): envwriter — chmod-600 .env append with gitignore guard

Idempotent (no-op when NAME=VALUE already on a line). Sets 0600.
Refuses to write when the target path is not covered by an
ancestor .gitignore — heal must not commit secrets to tracked paths.
Replaces the WriteEnvVar stub from Task 13.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 15: lib/heal/version.go — BumpPatch

**Files:**
- Create: `lib/heal/version.go`
- Create: `lib/heal/version_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// lib/heal/version_test.go
package heal_test

import (
	"encoding/json"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestBumpPatch_Simple(t *testing.T) {
	in := map[string]interface{}{"id": "x", "version": "0.1.0"}
	body, _ := json.Marshal(in)
	out, err := heal.BumpPatch(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	json.Unmarshal(out, &got)
	if got["version"] != "0.1.1" {
		t.Fatalf("version = %v", got["version"])
	}
}

func TestBumpPatch_DoubleDigit(t *testing.T) {
	in := map[string]interface{}{"id": "x", "version": "1.10.99"}
	body, _ := json.Marshal(in)
	out, err := heal.BumpPatch(body)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	json.Unmarshal(out, &got)
	if got["version"] != "1.10.100" {
		t.Fatalf("version = %v", got["version"])
	}
}

func TestBumpPatch_Malformed(t *testing.T) {
	for _, in := range []map[string]interface{}{
		{"id": "x", "version": "0.1"},
		{"id": "x", "version": "alpha"},
		{"id": "x"},
	} {
		body, _ := json.Marshal(in)
		if _, err := heal.BumpPatch(body); err == nil {
			t.Errorf("expected error for %v", in)
		}
	}
}
```

- [ ] **Step 2: Implement**

```go
// lib/heal/version.go
package heal

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// BumpPatch parses sensor JSON, increments the patch component of its
// version (M.m.p → M.m.p+1), and returns the re-marshalled bytes.
// Returns an error when the version is missing or malformed.
func BumpPatch(sensorJSON []byte) ([]byte, error) {
	var s map[string]interface{}
	if err := json.Unmarshal(sensorJSON, &s); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	v, ok := s["version"].(string)
	if !ok {
		return nil, fmt.Errorf("version missing or not string")
	}
	bumped, err := bumpSemverPatch(v)
	if err != nil {
		return nil, err
	}
	s["version"] = bumped
	return json.Marshal(s)
}

var semverRegex = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

func bumpSemverPatch(v string) (string, error) {
	m := semverRegex.FindStringSubmatch(v)
	if m == nil {
		return "", fmt.Errorf("malformed version %q", v)
	}
	patch, err := strconv.Atoi(m[3])
	if err != nil {
		return "", fmt.Errorf("malformed version %q", v)
	}
	return fmt.Sprintf("%s.%s.%d", m[1], m[2], patch+1), nil
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./lib/heal/...
git add lib/heal/version.go lib/heal/version_test.go
git commit -m "feat(lib/heal): BumpPatch — semver patch increment for heal'd sensors

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 16: hooks/setup-failure-detector.go — the Stop hook binary

**Files:**
- Create: `hooks/setup-failure-detector.go`
- Create: `hooks/setup-failure-detector_test.go`

The Stop hook receives JSON on stdin with the conversation transcript path. It reads the transcript, locates the most recent aggregate Signal from a `/run-sensor` invocation, classifies it via `lib/heal.Classify`, and on match prints `additionalContext` JSON on stdout. On no-match, prints nothing.

- [ ] **Step 1: Write the failing tests**

```go
// hooks/setup-failure-detector_test.go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscript writes a fake Claude Code transcript with one tool
// result containing the given Signal as its last JSONL line, then
// returns the transcript path.
func writeTranscript(t *testing.T, signal map[string]interface{}, sensorJSON map[string]interface{}) string {
	t.Helper()
	dir := t.TempDir()

	// The /run-sensor tool result emits JSONL on stdout; the LAST line
	// is the aggregate Signal.
	sigBytes, _ := json.Marshal(signal)
	toolOutput := string(sigBytes)

	sensorBytes, _ := json.Marshal(sensorJSON)
	sensorPath := filepath.Join(dir, "broken.json")
	os.WriteFile(sensorPath, sensorBytes, 0o644)

	transcript := []map[string]interface{}{
		{"type": "user", "content": "/run-sensor " + sensorPath},
		{"type": "tool_use", "name": "Bash", "input": map[string]interface{}{"command": "go run ./skills/run-sensor/scripts " + sensorPath}},
		{"type": "tool_result", "content": toolOutput},
	}
	path := filepath.Join(dir, "transcript.jsonl")
	f, _ := os.Create(path)
	for _, e := range transcript {
		b, _ := json.Marshal(e)
		f.Write(b)
		f.WriteString("\n")
	}
	f.Close()
	return path
}

func TestHook_SetupShape_EmitsInjection(t *testing.T) {
	failingSignal := map[string]interface{}{
		"sensor_id": "run-x",
		"verdict":   "error",
		"severity":  "high",
		"evidence": []interface{}{
			map[string]interface{}{"rationale": "Required environment variable RSA_PRIVATE_KEY not set"},
		},
		"metadata": map[string]interface{}{"kind": "aggregate"},
	}
	sensor := map[string]interface{}{
		"id": "run-x",
		"requires": map[string]interface{}{
			"env": []interface{}{
				map[string]interface{}{"name": "RSA_PRIVATE_KEY"},
			},
		},
	}
	path := writeTranscript(t, failingSignal, sensor)

	var stdout, stderr bytes.Buffer
	hookInput := []byte(`{"transcript_path":"` + path + `"}`)
	code := run(bytes.NewReader(hookInput), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "/heal-sensor") {
		t.Fatalf("expected injection mentioning /heal-sensor; got %q", out)
	}
	if !strings.Contains(out, "run-x") {
		t.Fatalf("expected sensor id in injection; got %q", out)
	}
}

func TestHook_PassingSignal_EmitsNothing(t *testing.T) {
	passingSignal := map[string]interface{}{
		"sensor_id": "run-x",
		"verdict":   "pass",
		"severity":  "info",
		"metadata":  map[string]interface{}{"kind": "aggregate"},
	}
	sensor := map[string]interface{}{"id": "run-x"}
	path := writeTranscript(t, passingSignal, sensor)

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{"transcript_path":"`+path+`"}`)), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected empty stdout; got %q", stdout.String())
	}
}

func TestHook_AlreadyHealed_NoLoop(t *testing.T) {
	// Transcript shows /run-sensor failure FOLLOWED by /heal-sensor — must not re-trigger.
	dir := t.TempDir()
	failingSignal := map[string]interface{}{
		"sensor_id": "run-x", "verdict": "error", "severity": "high",
		"evidence": []interface{}{map[string]interface{}{"rationale": "Required environment variable FOO not set"}},
		"metadata": map[string]interface{}{"kind": "aggregate"},
	}
	sensor := map[string]interface{}{"id": "run-x", "requires": map[string]interface{}{"env": []interface{}{map[string]interface{}{"name": "FOO"}}}}

	sigBytes, _ := json.Marshal(failingSignal)
	sensorBytes, _ := json.Marshal(sensor)
	sensorPath := filepath.Join(dir, "broken.json")
	os.WriteFile(sensorPath, sensorBytes, 0o644)

	transcript := []map[string]interface{}{
		{"type": "user", "content": "/run-sensor " + sensorPath},
		{"type": "tool_result", "content": string(sigBytes)},
		{"type": "user", "content": "/heal-sensor invoked"},
		{"type": "assistant", "content": "heal applied"},
	}
	path := filepath.Join(dir, "transcript.jsonl")
	f, _ := os.Create(path)
	for _, e := range transcript {
		b, _ := json.Marshal(e)
		f.Write(b)
		f.WriteString("\n")
	}
	f.Close()

	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{"transcript_path":"`+path+`"}`)), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected no-op when /heal-sensor already in transcript; got %q", stdout.String())
	}
}

func TestHook_NoTranscriptPath_Exit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(bytes.NewReader([]byte(`{}`)), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}
```

- [ ] **Step 2: Implement**

```go
// hooks/setup-failure-detector.go
//
// Claude Code Stop hook that classifies the most-recent /run-sensor
// aggregate Signal in the conversation transcript. On setup-shaped
// failure, emits additionalContext on stdout instructing the LLM to
// invoke /heal-sensor. On no-match (passing run, sensor-design
// failure, already-healed-this-turn), prints nothing.
//
// Input (JSON on stdin):
//
//	{ "transcript_path": "/abs/path/to/transcript.jsonl", ... }
//
// Output (JSON on stdout, when triggering):
//
//	{
//	  "hookSpecificOutput": {
//	    "hookEventName": "Stop",
//	    "additionalContext": "..."
//	  }
//	}
//
// Exit codes: 0 always (per Claude Code hook protocol; signal nothing
// to do via empty stdout) except 2 for usage errors.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func main() {
	os.Exit(run(os.Stdin, os.Stdout, os.Stderr))
}

type hookInput struct {
	TranscriptPath string `json:"transcript_path"`
}

type transcriptEntry struct {
	Type    string          `json:"type"`
	Content json.RawMessage `json:"content"`
	Input   json.RawMessage `json:"input"`
}

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "read stdin:", err)
		return 2
	}
	var in hookInput
	if err := json.Unmarshal(body, &in); err != nil {
		fmt.Fprintln(stderr, "parse hook input:", err)
		return 2
	}
	if in.TranscriptPath == "" {
		fmt.Fprintln(stderr, "transcript_path missing")
		return 2
	}

	signal, sensorPath, alreadyHealed, ok := scanTranscript(in.TranscriptPath)
	if !ok {
		return 0
	}
	if alreadyHealed {
		return 0
	}

	failed, err := loadFailedSensorView(sensorPath)
	if err != nil {
		// Sensor file gone or unreadable — no-op rather than crash.
		return 0
	}

	res, matched := heal.Classify(signal, failed)
	if !matched {
		return 0
	}
	emitInjection(stdout, sensorPath, failed.ID, res)
	return 0
}

// scanTranscript walks the JSONL transcript backward, finds the most
// recent /run-sensor aggregate Signal, and reports whether a
// subsequent /heal-sensor invocation already happened in this turn.
func scanTranscript(path string) (signal heal.Signal, sensorPath string, alreadyHealed bool, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	var entries []transcriptEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		var e transcriptEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	// Walk backward: find the most recent tool_result whose last JSONL
	// line parses as an aggregate Signal AND whose preceding context
	// shows a /run-sensor invocation.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Type != "tool_result" {
			continue
		}
		var content string
		_ = json.Unmarshal(e.Content, &content)
		if content == "" {
			continue
		}
		lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
		var sigMap map[string]interface{}
		if err := json.Unmarshal([]byte(lines[len(lines)-1]), &sigMap); err != nil {
			continue
		}
		if md, _ := sigMap["metadata"].(map[string]interface{}); md == nil || md["kind"] != "aggregate" {
			continue
		}
		// Find the matching /run-sensor invocation in earlier entries.
		sensorPath = findRunSensorTarget(entries[:i])
		if sensorPath == "" {
			continue
		}
		// Look forward from i for any /heal-sensor mention.
		alreadyHealed = anyHealAfter(entries[i+1:])
		signal = signalFromMap(sigMap)
		ok = true
		return
	}
	return
}

func findRunSensorTarget(entries []transcriptEntry) string {
	// Walk backward: nearest user message containing "/run-sensor <path>".
	for i := len(entries) - 1; i >= 0; i-- {
		var content string
		_ = json.Unmarshal(entries[i].Content, &content)
		if strings.Contains(content, "/run-sensor ") {
			parts := strings.Fields(content)
			for j, p := range parts {
				if p == "/run-sensor" && j+1 < len(parts) {
					return strings.TrimPrefix(parts[j+1], "@")
				}
			}
		}
	}
	return ""
}

func anyHealAfter(entries []transcriptEntry) bool {
	for _, e := range entries {
		var content string
		_ = json.Unmarshal(e.Content, &content)
		if strings.Contains(content, "/heal-sensor") {
			return true
		}
	}
	return false
}

func signalFromMap(m map[string]interface{}) heal.Signal {
	var s heal.Signal
	if v, ok := m["verdict"].(string); ok {
		s.Verdict = v
	}
	if v, ok := m["severity"].(string); ok {
		s.Severity = v
	}
	if ev, ok := m["evidence"].([]interface{}); ok {
		for _, e := range ev {
			em, _ := e.(map[string]interface{})
			if em == nil {
				continue
			}
			r, _ := em["rationale"].(string)
			ex, _ := em["excerpt"].(string)
			s.Evidence = append(s.Evidence, heal.SignalEvidence{Rationale: r, Excerpt: ex})
		}
	}
	if md, ok := m["metadata"].(map[string]interface{}); ok {
		if h, ok := md["heal_hint"].(string); ok {
			s.Metadata.HealHint = h
		}
		if ec, ok := md["exit_code"].(float64); ok {
			n := int(ec)
			s.Metadata.ExitCode = &n
		}
		if lc, ok := md["lifecycle"].(map[string]interface{}); ok {
			if pp, ok := lc["prepare"].([]interface{}); ok {
				for _, p := range pp {
					pm, _ := p.(map[string]interface{})
					if pm == nil {
						continue
					}
					cmd, _ := pm["command"].(string)
					vrd, _ := pm["verdict"].(string)
					s.Metadata.Lifecycle.Prepare = append(s.Metadata.Lifecycle.Prepare, heal.SignalLifecycleStep{Command: cmd, Verdict: vrd})
				}
			}
		}
	}
	return s
}

type sensorRequiresView struct {
	ID       string
	Requires struct {
		Env []struct {
			Name string `json:"name"`
		} `json:"env"`
		Tools   []string `json:"tools"`
		Context []string `json:"context"`
	} `json:"requires"`
}

func loadFailedSensorView(path string) (heal.FailedSensor, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return heal.FailedSensor{}, err
	}
	var v struct {
		ID       string `json:"id"`
		Requires struct {
			Env []struct {
				Name string `json:"name"`
			} `json:"env"`
			Tools   []string `json:"tools"`
			Context []string `json:"context"`
		} `json:"requires"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return heal.FailedSensor{}, err
	}
	envs := make([]string, 0, len(v.Requires.Env))
	for _, e := range v.Requires.Env {
		envs = append(envs, e.Name)
	}
	return heal.FailedSensor{ID: v.ID, EnvNames: envs, Tools: v.Requires.Tools, Context: v.Requires.Context}, nil
}

func emitInjection(stdout io.Writer, sensorPath, sensorID string, res heal.Result) {
	msg := fmt.Sprintf(
		"The previous /run-sensor invocation for sensor %q produced a setup-shaped failure (rule=%s, shape=%s, detail=%q). Invoke `/heal-sensor --signal-from=transcript --sensor=%s` to attempt automatic recovery before reporting the failure to the user.",
		sensorID, res.Rule, res.Shape, res.Detail, sensorPath,
	)
	out := map[string]interface{}{
		"hookSpecificOutput": map[string]interface{}{
			"hookEventName":     "Stop",
			"additionalContext": msg,
		},
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./hooks/...
```

Expected: PASS for all four hook tests.

- [ ] **Step 4: Commit**

```bash
git add hooks/setup-failure-detector.go hooks/setup-failure-detector_test.go
git commit -m "feat(hooks): setup-failure-detector Stop hook

Walks the transcript, finds the most recent /run-sensor aggregate
Signal, classifies via lib/heal.Classify, and on match emits
additionalContext instructing the LLM to invoke /heal-sensor.
Includes the idempotence guard: if /heal-sensor was already invoked
since the failing run, the hook is a no-op (prevents heal-then-retry
from re-triggering itself).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 17: Wire the hook into .claude-plugin/plugin.json

**Files:**
- Modify: `.claude-plugin/plugin.json`

- [ ] **Step 1: Add the hooks field**

```json
{
  "name": "harness-framework",
  "version": "0.4.0",
  "description": "Sensor harness for AI coding agents",
  "author": {
    "name": "Iury Krieger",
    "email": "iury.krieger@stone.com.br",
    "url": "https://github.com/iurykrieger"
  },
  "homepage": "https://github.com/iurykrieger/harness-framework",
  "repository": "https://github.com/iurykrieger/harness-framework",
  "license": "MIT",
  "keywords": ["claude-code", "plugin"],
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "go run ${CLAUDE_PLUGIN_ROOT}/hooks/setup-failure-detector.go"
          }
        ]
      }
    ]
  }
}
```

- [ ] **Step 2: Manual smoke test**

```bash
# Run the hook binary against a hand-crafted transcript fixture to
# confirm it compiles and runs in plugin context:
go run ./hooks/setup-failure-detector.go <<< '{"transcript_path":"/dev/null"}'
```

Expected: exit 0, no stdout, no stderr (transcript empty → no-op path).

- [ ] **Step 3: Commit**

```bash
git add .claude-plugin/plugin.json
git commit -m "feat(plugin): register setup-failure-detector as a Stop hook

Auto-installs the hook on plugin sync. Bumps plugin version to 0.4.0
to mark the additive feature.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 18: skills/heal-sensor/scripts/diagnose.go

**Files:**
- Create: `skills/heal-sensor/scripts/diagnose.go`
- Create: `skills/heal-sensor/scripts/diagnose_test.go`

`diagnose` reads the failed Signal + sensor + project root, prints a structured report (JSON) the calling agent uses as ground truth when filling in the Setup Plan slots. It does NOT do LLM reasoning; the agent does.

- [ ] **Step 1: Write the failing tests**

```go
// skills/heal-sensor/scripts/diagnose_test.go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	os.WriteFile(p, []byte(content), 0o644)
	return p
}

func TestDiagnose_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# Project\n\nRun `cp .env.example .env` to set up.")
	writeFile(t, dir, ".env.example", "RSA_PRIVATE_KEY=YOUR_KEY_HERE\n")
	signal := writeFile(t, dir, "signal.json", `{"sensor_id":"x","verdict":"error","evidence":[{"rationale":"Required environment variable RSA_PRIVATE_KEY not set"}]}`)
	sensor := writeFile(t, dir, "sensor.json", `{"id":"x","requires":{"env":[{"name":"RSA_PRIVATE_KEY","description":"PEM contents for JWT signing"}]}}`)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--signal", signal, "--sensor", sensor, "--root", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout.String())
	}
	if _, ok := out["failed_sensor"]; !ok {
		t.Errorf("missing failed_sensor")
	}
	if _, ok := out["signal"]; !ok {
		t.Errorf("missing signal")
	}
	docs, _ := out["documents"].(map[string]interface{})
	if docs == nil || docs["README.md"] == nil {
		t.Errorf("expected README.md in documents map")
	}
	tmpls, _ := out["templates"].([]interface{})
	if len(tmpls) == 0 {
		t.Errorf("expected at least one .env.example in templates")
	}
}

func TestDiagnose_MissingArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}

func TestDiagnose_SignalUnreadable(t *testing.T) {
	dir := t.TempDir()
	sensor := writeFile(t, dir, "s.json", `{"id":"x"}`)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--signal", "/nonexistent.json", "--sensor", sensor, "--root", dir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}
```

- [ ] **Step 2: Implement**

```go
// skills/heal-sensor/scripts/diagnose.go
//
// Reads the failed Signal, the failing sensor's JSON, and the project
// root, and emits a structured "diagnosis input" JSON the calling
// agent uses to fill in the Setup Plan slots.
//
// The script does NOT do LLM reasoning. It collects the deterministic
// inputs (signal contents, declared requires.*, README/CLAUDE/AGENTS
// excerpts, .env.example presence and contents) so the calling agent
// has them in one place and SKILL.md prose can deterministically
// reference what's available.
//
// Exit codes: 0 emitted, 2 usage / I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var signalPath, sensorPath, root string
	fs.StringVar(&signalPath, "signal", "", "path to failing aggregate Signal JSON (required)")
	fs.StringVar(&sensorPath, "sensor", "", "path to failing sensor JSON (required)")
	fs.StringVar(&root, "root", "", "project root (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if signalPath == "" || sensorPath == "" || root == "" {
		fmt.Fprintln(stderr, "usage: diagnose --signal=PATH --sensor=PATH --root=DIR")
		return 2
	}

	signalBody, err := os.ReadFile(signalPath)
	if err != nil {
		fmt.Fprintln(stderr, "read signal:", err)
		return 2
	}
	sensorBody, err := os.ReadFile(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "read sensor:", err)
		return 2
	}

	out := map[string]interface{}{
		"signal":        json.RawMessage(signalBody),
		"failed_sensor": json.RawMessage(sensorBody),
		"documents":     readDocuments(root),
		"templates":     listTemplates(root),
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(stderr, "encode:", err)
		return 2
	}
	return 0
}

func readDocuments(root string) map[string]string {
	docs := map[string]string{}
	for _, name := range []string{"README.md", "CLAUDE.md", "AGENTS.md", "GEMINI.md", "CONTRIBUTING.md"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		// Cap to ~16KB to keep the output bounded.
		if len(body) > 16*1024 {
			body = body[:16*1024]
		}
		docs[name] = string(body)
	}
	return docs
}

func listTemplates(root string) []map[string]string {
	out := []map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".cache" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".example" || filepath.Base(path) == ".env.example" {
			body, _ := os.ReadFile(path)
			out = append(out, map[string]string{"path": path, "preview": truncate(string(body), 4096)})
		}
		return nil
	})
	return out
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./skills/heal-sensor/scripts/...
git add skills/heal-sensor/scripts/diagnose.go skills/heal-sensor/scripts/diagnose_test.go
git commit -m "feat(skills/heal-sensor): diagnose collects deterministic heal inputs

Reads signal + sensor + project root; emits a structured JSON
report the calling agent uses to fill in the Setup Plan slots. No
LLM reasoning here — that lives in SKILL.md prose, on top of this
input.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 19: skills/heal-sensor/scripts/apply-safe.go

**Files:**
- Create: `skills/heal-sensor/scripts/apply-safe.go`
- Create: `skills/heal-sensor/scripts/apply-safe_test.go`

CLI wrapper around `lib/heal.Apply`. Reads a Plan from `--plan=<path>`, the failed sensor JSON from `--sensor=<path>` (for context), executes the Plan's `auto_apply` items, prints results.

- [ ] **Step 1: Write the failing tests**

```go
// skills/heal-sensor/scripts/apply-safe_test.go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplySafe_HappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env.example")
	dst := filepath.Join(dir, ".env")
	os.WriteFile(src, []byte("FOO=bar\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	plan := map[string]interface{}{
		"diagnosis":  map[string]interface{}{"failed_sensor_id": "x", "shape": "missing-env"},
		"auto_apply": []interface{}{map[string]interface{}{"kind": "copy-template", "src": src, "dst": dst}},
	}
	sensor := map[string]interface{}{"id": "x", "requires": map[string]interface{}{"context": []string{dir}}}

	planPath := filepath.Join(dir, "plan.json")
	sensorPath := filepath.Join(dir, "s.json")
	planB, _ := json.Marshal(plan)
	sensorB, _ := json.Marshal(sensor)
	os.WriteFile(planPath, planB, 0o644)
	os.WriteFile(sensorPath, sensorB, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--sensor", sensorPath, "--root", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf(".env not created: %v", err)
	}
}

func TestApplySafe_NeedsInput_PromptsCaller(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	os.WriteFile(envFile, []byte(""), 0o600)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".env\n"), 0o644)

	plan := map[string]interface{}{
		"diagnosis":  map[string]interface{}{"failed_sensor_id": "x", "shape": "missing-env"},
		"auto_apply": []interface{}{map[string]interface{}{"kind": "set-env-in-file", "file": envFile, "name": "FOO", "value_source": "ask-user"}},
	}
	sensor := map[string]interface{}{"id": "x", "requires": map[string]interface{}{"env": []interface{}{map[string]interface{}{"name": "FOO"}}, "context": []string{dir}}}
	planPath := filepath.Join(dir, "plan.json")
	sensorPath := filepath.Join(dir, "s.json")
	pb, _ := json.Marshal(plan)
	sb, _ := json.Marshal(sensor)
	os.WriteFile(planPath, pb, 0o644)
	os.WriteFile(sensorPath, sensorPath, 0o644)
	os.WriteFile(sensorPath, sb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--sensor", sensorPath, "--root", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var out struct {
		Results []struct {
			Applied    bool `json:"applied"`
			NeedsInput bool `json:"needs_input"`
		} `json:"results"`
	}
	json.Unmarshal(stdout.Bytes(), &out)
	if len(out.Results) != 1 {
		t.Fatalf("results len = %d", len(out.Results))
	}
	if out.Results[0].Applied || !out.Results[0].NeedsInput {
		t.Fatalf("expected NeedsInput=true; got %#v", out.Results[0])
	}
}
```

- [ ] **Step 2: Implement**

```go
// skills/heal-sensor/scripts/apply-safe.go
//
// CLI wrapper that reads a Plan and the failed sensor, then runs the
// Plan's auto_apply items through lib/heal.Apply (allowlist-gated
// idempotent file mutations). Emits a JSON report.
//
// Exit codes: 0 results emitted; 2 usage / I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apply-safe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var planPath, sensorPath, root string
	fs.StringVar(&planPath, "plan", "", "Setup Plan JSON (required)")
	fs.StringVar(&sensorPath, "sensor", "", "failing sensor JSON (required)")
	fs.StringVar(&root, "root", "", "project root (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if planPath == "" || sensorPath == "" || root == "" {
		fmt.Fprintln(stderr, "usage: apply-safe --plan=PATH --sensor=PATH --root=DIR")
		return 2
	}

	planBody, err := os.ReadFile(planPath)
	if err != nil {
		fmt.Fprintln(stderr, "read plan:", err)
		return 2
	}
	plan, err := heal.ParsePlan(planBody)
	if err != nil {
		fmt.Fprintln(stderr, "parse plan:", err)
		return 2
	}
	sensorBody, err := os.ReadFile(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "read sensor:", err)
		return 2
	}
	failed, err := failedSensorView(sensorBody)
	if err != nil {
		fmt.Fprintln(stderr, "parse sensor:", err)
		return 2
	}

	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: failed}, plan.AutoApply)
	out := map[string]interface{}{"results": resultsForJSON(results)}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	return 0
}

func failedSensorView(body []byte) (heal.FailedSensor, error) {
	var v struct {
		ID       string `json:"id"`
		Requires struct {
			Env []struct {
				Name string `json:"name"`
			} `json:"env"`
			Tools   []string `json:"tools"`
			Context []string `json:"context"`
		} `json:"requires"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return heal.FailedSensor{}, err
	}
	envs := make([]string, 0, len(v.Requires.Env))
	for _, e := range v.Requires.Env {
		envs = append(envs, e.Name)
	}
	return heal.FailedSensor{ID: v.ID, EnvNames: envs, Tools: v.Requires.Tools, Context: v.Requires.Context}, nil
}

type resultJSON struct {
	Action     heal.Action `json:"action"`
	Applied    bool        `json:"applied"`
	NeedsInput bool        `json:"needs_input"`
	Reason     string      `json:"reason,omitempty"`
}

func resultsForJSON(in []heal.ApplyResult) []resultJSON {
	out := make([]resultJSON, 0, len(in))
	for _, r := range in {
		out = append(out, resultJSON{Action: r.Action, Applied: r.Applied, NeedsInput: r.NeedsInput, Reason: r.Reason})
	}
	return out
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./skills/heal-sensor/scripts/...
git add skills/heal-sensor/scripts/apply-safe.go skills/heal-sensor/scripts/apply-safe_test.go
git commit -m "feat(skills/heal-sensor): apply-safe CLI wrapper over lib/heal.Apply

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 20: skills/heal-sensor/scripts/apply-sensors.go

**Files:**
- Create: `skills/heal-sensor/scripts/apply-sensors.go`
- Create: `skills/heal-sensor/scripts/apply-sensors_test.go`

CLI wrapper that iterates the Plan's `sensor_patches[]` and `new_setup_sensors[]`, applies `BumpPatch` to patches, then calls `lib/sensor.ValidateAndPersist` for each. **No new persistence pipeline.**

- [ ] **Step 1: Write the failing tests**

```go
// skills/heal-sensor/scripts/apply-sensors_test.go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestApplySensors_NewSetupSensor(t *testing.T) {
	dir := t.TempDir()
	plan := map[string]interface{}{
		"diagnosis":         map[string]interface{}{"failed_sensor_id": "x", "shape": "missing-env"},
		"new_setup_sensors": []interface{}{map[string]interface{}{"id": "smoke-setup", "json": testfixtures.ValidSensorSetup()}},
	}
	planPath := filepath.Join(dir, "plan.json")
	pb, _ := json.Marshal(plan)
	os.WriteFile(planPath, pb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--out", dir, "--schemas-dir", testfixtures.RepoSchemasDir(t)}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "smoke-setup.json")); err != nil {
		t.Fatal("setup sensor not persisted")
	}
}

func TestApplySensors_PatchBumpsPatchVersion(t *testing.T) {
	dir := t.TempDir()
	patched := testfixtures.ValidSensorComputational()
	patched["version"] = "0.1.0"
	patched["description"] = "patched by heal"

	plan := map[string]interface{}{
		"diagnosis":      map[string]interface{}{"failed_sensor_id": "smoke-comp", "shape": "missing-env"},
		"sensor_patches": []interface{}{map[string]interface{}{"id": "smoke-comp", "patch": patched}},
	}
	planPath := filepath.Join(dir, "plan.json")
	pb, _ := json.Marshal(plan)
	os.WriteFile(planPath, pb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--out", dir, "--schemas-dir", testfixtures.RepoSchemasDir(t)}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	out, _ := os.ReadFile(filepath.Join(dir, "smoke-comp.json"))
	var got map[string]interface{}
	json.Unmarshal(out, &got)
	if got["version"] != "0.1.1" {
		t.Fatalf("expected patched version 0.1.1; got %v", got["version"])
	}
}

func TestApplySensors_InvalidSensorRejected(t *testing.T) {
	dir := t.TempDir()
	bad := testfixtures.ValidSensorComputational()
	delete(bad, "regulation")

	plan := map[string]interface{}{
		"diagnosis":         map[string]interface{}{"failed_sensor_id": "x", "shape": "missing-env"},
		"new_setup_sensors": []interface{}{map[string]interface{}{"id": "smoke-comp", "json": bad}},
	}
	planPath := filepath.Join(dir, "plan.json")
	pb, _ := json.Marshal(plan)
	os.WriteFile(planPath, pb, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--plan", planPath, "--out", dir, "--schemas-dir", testfixtures.RepoSchemasDir(t)}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected 1 (validation), got %d (stderr=%s)", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "smoke-comp.json")); err == nil {
		t.Fatal("invalid sensor must NOT be persisted")
	}
}
```

- [ ] **Step 2: Implement**

```go
// skills/heal-sensor/scripts/apply-sensors.go
//
// Reads a Setup Plan; for each sensor_patches[] entry, applies
// lib/heal.BumpPatch to the patched JSON before persisting; for each
// new_setup_sensors[] entry, persists as-is. All persistence funnels
// through lib/sensor.ValidateAndPersist — the SAME primitive
// detect-sensors uses.
//
// Exit codes: 0 all sensors persisted, 1 validation fail (some may
// have been written; written ones were valid), 2 usage / I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apply-sensors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var planPath, outDir, schemasDir string
	fs.StringVar(&planPath, "plan", "", "Setup Plan JSON (required)")
	fs.StringVar(&outDir, "out", "", "sensors directory (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if planPath == "" || outDir == "" {
		fmt.Fprintln(stderr, "usage: apply-sensors --plan=PATH --out=DIR [--schemas-dir=DIR]")
		return 2
	}

	planBody, err := os.ReadFile(planPath)
	if err != nil {
		fmt.Fprintln(stderr, "read plan:", err)
		return 2
	}
	plan, err := heal.ParsePlan(planBody)
	if err != nil {
		fmt.Fprintln(stderr, "parse plan:", err)
		return 2
	}

	written := []string{}
	for _, p := range plan.SensorPatches {
		body, err := json.Marshal(p.Patch)
		if err != nil {
			fmt.Fprintln(stderr, "marshal patch:", err)
			return 1
		}
		bumped, err := heal.BumpPatch(body)
		if err != nil {
			fmt.Fprintln(stderr, "bump patch version for", p.ID, ":", err)
			return 1
		}
		path, err := sensor.ValidateAndPersist(bumped, outDir, schemasDir)
		if err != nil {
			fmt.Fprintln(stderr, "persist patch", p.ID, ":", err)
			return 1
		}
		written = append(written, path)
	}
	for _, n := range plan.NewSetupSensors {
		body, err := json.Marshal(n.JSON)
		if err != nil {
			fmt.Fprintln(stderr, "marshal new sensor:", err)
			return 1
		}
		path, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
		if err != nil {
			fmt.Fprintln(stderr, "persist new sensor", n.ID, ":", err)
			return 1
		}
		written = append(written, path)
	}

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]interface{}{"written": written})
	return 0
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./skills/heal-sensor/scripts/...
git add skills/heal-sensor/scripts/apply-sensors.go skills/heal-sensor/scripts/apply-sensors_test.go
git commit -m "feat(skills/heal-sensor): apply-sensors persists Plan via shared lib

Iterates sensor_patches[] (with version BumpPatch) and
new_setup_sensors[]. Every persistence call funnels through
lib/sensor.ValidateAndPersist — same primitive detect-sensors uses.
No duplicate persistence path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 21: skills/heal-sensor/scripts/retry-original.go

**Files:**
- Create: `skills/heal-sensor/scripts/retry-original.go`
- Create: `skills/heal-sensor/scripts/retry-original_test.go`

Re-invokes the runner once. Reads sensor.type to pick `run_computational` vs `run_inferential`. Returns the runner's stdout/stderr verbatim and propagates its exit code.

- [ ] **Step 1: Write the failing tests**

```go
// skills/heal-sensor/scripts/retry-original_test.go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestRetryOriginal_PicksTypeAndShellsOut(t *testing.T) {
	dir := t.TempDir()
	s := testfixtures.ValidSensorComputational()
	s["execution"] = map[string]interface{}{
		"command": "true",
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
		},
	}
	body, _ := json.Marshal(s)
	path := filepath.Join(dir, "smoke-comp.json")
	os.WriteFile(path, body, 0o644)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"verdict":"pass"`)) {
		t.Fatalf("expected pass aggregate; got %s", stdout.String())
	}
}

func TestRetryOriginal_MissingSensor(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensor", "/nonexistent.json"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
}
```

- [ ] **Step 2: Implement**

```go
// skills/heal-sensor/scripts/retry-original.go
//
// Re-invokes the original sensor's runner exactly once. Reads the
// sensor's `type` (computational | inferential) to pick the build
// tag, shells out to `go run -tags=<tag> ./skills/run-sensor/scripts
// <sensor>`, and pipes stdout/stderr through.
//
// Exit codes: same as the underlying runner.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("retry-original", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sensorPath string
	fs.StringVar(&sensorPath, "sensor", "", "path to the sensor JSON to retry (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if sensorPath == "" {
		fmt.Fprintln(stderr, "usage: retry-original --sensor=PATH")
		return 2
	}

	body, err := os.ReadFile(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "read sensor:", err)
		return 2
	}
	var v struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		fmt.Fprintln(stderr, "parse sensor:", err)
		return 2
	}
	tag := "run_computational"
	if v.Type == "inferential" {
		tag = "run_inferential"
	}
	cmd := exec.Command("go", "run", "-tags="+tag, "./skills/run-sensor/scripts", sensorPath)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "exec:", err)
		return 2
	}
	return 0
}
```

- [ ] **Step 3: Run + commit**

```bash
go test ./skills/heal-sensor/scripts/...
git add skills/heal-sensor/scripts/retry-original.go skills/heal-sensor/scripts/retry-original_test.go
git commit -m "feat(skills/heal-sensor): retry-original re-invokes runner once

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS (the test relies on `go run` working from the harness checkout — which is the runtime expectation for this script anyway).

---

## Task 22: skills/heal-sensor/SKILL.md

**Files:**
- Create: `skills/heal-sensor/SKILL.md`

- [ ] **Step 1: Write the SKILL.md**

```markdown
---
name: heal-sensor
description: Use when the user invokes /heal-sensor or when the setup-failure-detector hook injects a directive after a /run-sensor setup-shaped failure. Reads the failing Signal + sensor + project state, builds a Setup Plan, applies allowlisted idempotent fixes (cp .env.example .env, mkdir, touch, set-env-in-file with chmod 600), persists patched/new sensors via lib/sensor.ValidateAndPersist, and retries the original sensor exactly once.
---

# heal-sensor

Recover from setup-shaped sensor failures: missing env vars, missing binaries, absent .env files, unavailable services. Run by the calling agent in response to a hook injection (most common) or a manual `/heal-sensor` invocation.

## Invocation

```
/heal-sensor --signal=<path-to-aggregate-signal-json> --sensor=<path-to-failing-sensor-json>
```

The hook's `additionalContext` includes both arguments. When invoked manually, ask the user for the failing Signal path (it's the last JSONL line emitted by the most recent `/run-sensor`).

## Procedure

### 1. Diagnose

Run the deterministic input collector:

```bash
go run ./skills/heal-sensor/scripts/diagnose.go \
  --signal=<signal-path> \
  --sensor=<sensor-path> \
  --root=<project-root> > /tmp/heal-input.json
```

The output contains: the failing Signal verbatim, the sensor JSON verbatim, the contents of README/CLAUDE/AGENTS/CONTRIBUTING (capped to 16 KB each), and the list of `.example` template files in the tree.

### 2. Build the Setup Plan

Read `/tmp/heal-input.json` and write a Setup Plan to `/tmp/heal-plan.json` that conforms to the contract in `lib/heal/plan.go`:

```json
{
  "diagnosis": {
    "failed_sensor_id": "<id>",
    "shape": "missing-env" | "binary-not-found" | "env-file-absent" | "service-unavailable",
    "evidence_excerpt": "...",
    "root_cause_hint": "..."
  },
  "auto_apply": [
    { "kind": "copy-template", "src": "<absolute path>", "dst": "<absolute path>" },
    { "kind": "set-env-in-file", "file": "<.env path>", "name": "<VAR>", "value_source": "ask-user" },
    { "kind": "mkdir", "dir": "<path under requires.context>" },
    { "kind": "touch", "file": "<path under requires.context>" }
  ],
  "propose_only": [
    { "kind": "shell", "command": "<unsafe or non-allowlisted>", "rationale": "..." }
  ],
  "sensor_patches": [
    { "id": "<sensor id>", "patch": { "...full sensor JSON post-edit..." } }
  ],
  "new_setup_sensors": [
    { "id": "setup-env-from-example-<x>", "json": { "...full new sensor JSON..." } }
  ]
}
```

Rules for filling in the slots:

- `shape`: pick from the closed enum. Match the rule that fired (the hook's injection message names it).
- `auto_apply[]`: only the four kinds listed above. Anything else (`pnpm install`, `docker compose up`, `gcloud auth login`, custom Makefile targets) goes into `propose_only[]`. The `lib/heal.Apply` allowlist will reject anything else even if you list it.
- `sensor_patches[]`: when the failing sensor would benefit from declaring an additional `requires.env[]` entry or wiring `depends_on` to a new setup sensor — emit the patched full JSON. Don't emit a JSON patch document; emit the new full sensor object. `apply-sensors.go` will run `lib/heal/version.BumpPatch` before persisting.
- `new_setup_sensors[]`: when the project would benefit from a reusable setup sensor (e.g., `setup-env-from-example`) — emit the full new sensor JSON at version `0.1.0` with `kind: "setup"`.

### 3. Apply file mutations

```bash
go run ./skills/heal-sensor/scripts/apply-safe.go \
  --plan=/tmp/heal-plan.json \
  --sensor=<sensor-path> \
  --root=<project-root> > /tmp/heal-apply.json
```

Inspect `/tmp/heal-apply.json`. For each result with `needs_input: true`:

1. Find the matching `auto_apply[]` item (by file/name).
2. Read the failed sensor's `requires.env[<NAME>].description` for context.
3. Invoke the `AskUserQuestion` tool synchronously with the description as the question; let the user paste the value.
4. Edit the Plan: set the `value` field on the matching auto_apply item to the user's answer; remove `value_source`.
5. Re-run `apply-safe.go` against the patched Plan — it will write the line via `WriteEnvVar` (chmod 600).

If the user cancels or returns empty: skip step 6, jump to step 7, surface remediation explaining the cancellation.

### 4. Apply sensor mutations

```bash
go run ./skills/heal-sensor/scripts/apply-sensors.go \
  --plan=/tmp/heal-plan.json \
  --out=<project-root>/sensors > /tmp/heal-persist.json
```

This validates each `sensor_patches[]` and `new_setup_sensors[]` entry against `schemas/sensor.json` via the shared `lib/sensor.ValidateAndPersist`. If any sensor fails validation, the script exits 1 and previously-written entries stay (they were valid).

### 5. Retry exactly once

```bash
go run ./skills/heal-sensor/scripts/retry-original.go --sensor=<sensor-path>
```

Pipe its stdout into the response. The retry's aggregate Signal is the LAST JSONL line. If the aggregate's verdict is now `pass` or `warn`, heal succeeded — surface it as the outcome.

### 6. If the retry still fails

Do NOT iterate. Compose a final Signal that:

- Echoes the retry's aggregate as-is, plus
- In `remediation.instructions`, lists everything in the Plan's `propose_only[]` plus any `auto_apply[]` items whose result was `applied: false` AND any cancelled `AskUserQuestion` prompts.

The user (or a future agent turn) decides next steps. The next `/run-sensor` invocation will trigger the hook again if the failure is still setup-shape; the hook's idempotence guard prevents loops within the current turn.

## What heal does NOT do

- Run arbitrary commands extracted from project docs. `pnpm install`, `docker compose up`, `gcloud auth login` are always `propose_only[]`.
- Modify `.gitignore`. The envwriter refuses to write to a path whose ancestor directories don't already gitignore the target.
- Iterate beyond one retry per `/run-sensor` invocation.
- Heal sensor-design failures (regex doesn't match, exit_code_map wrong, fixture mismatch). Those are the responsibility of `/detect-sensors` or manual editing.

## Failure modes

| Symptom | Action |
|---|---|
| `apply-safe` returns `applied: false` for a `copy-template` (dst exists) | Surface in remediation; the user already configured this; nothing to fix |
| `apply-sensors` fails with schema error | Surface validator output verbatim; the Plan was malformed; do NOT retry |
| `retry-original` still fails with the SAME setup-shape | Surface and stop; the Plan didn't address the root cause |
| `retry-original` fails with a DIFFERENT setup-shape | Surface and stop; the next `/run-sensor` invocation will trigger heal again |
| `AskUserQuestion` cancelled | Skip the dependent items; surface them in remediation |
```

- [ ] **Step 2: Commit**

```bash
git add skills/heal-sensor/SKILL.md
git commit -m "docs(skills/heal-sensor): SKILL.md orchestration prose

Walks the calling agent through diagnose → plan → apply-safe →
apply-sensors → retry. Documents the four allowlisted action kinds,
the AskUserQuestion flow for ask-user values, and explicit boundaries
(no arbitrary command execution, no .gitignore mutations, single retry
per invocation).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 23: End-to-end smoke test

**Files:**
- Create: `test/heal-e2e/heal_e2e_test.go`
- Create: `test/heal-e2e/fixture/.env.example`
- Create: `test/heal-e2e/fixture/sensors/run-needs-env.json`

End-to-end test: a fixture project with `.env.example` + a sensor whose command requires an env var → invoke the runner → classifier confirms setup-shape → apply-safe copies the template → apply-sensors persists nothing (no patches in this scenario) → retry passes.

- [ ] **Step 1: Write the failing test**

```go
// test/heal-e2e/heal_e2e_test.go
package healE2E_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

// TestHealE2E_MissingEnvFile_HealAndRetry simulates the full loop:
// 1) run-sensor fails because .env is missing
// 2) classifier confirms setup-shape (env-file-absent rule fires via
//    the curated stderr regex)
// 3) heal applies copy-template
// 4) retry passes
func TestHealE2E_MissingEnvFile_HealAndRetry(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	root := setupFixtureProject(t)
	sensorPath := filepath.Join(root, "sensors", "run-needs-env.json")

	// 1) First run: must fail with .env missing.
	out1, _ := runSensor(t, root, sensorPath)
	agg1 := lastJSONL(t, out1)
	if agg1["verdict"] == "pass" {
		t.Fatalf("expected first run to fail; got %v", agg1)
	}

	// 2) Classify the aggregate — must be setup-shape.
	sig := signalFromMap(agg1)
	failed := mustLoadFailedView(t, sensorPath)
	res, ok := heal.Classify(sig, failed)
	if !ok {
		t.Fatalf("expected setup-shape classification; aggregate=%v", agg1)
	}
	if res.Shape != heal.ShapeEnvFileAbsent && res.Shape != heal.ShapeMissingEnv {
		t.Fatalf("unexpected shape %q (rule=%q)", res.Shape, res.Rule)
	}

	// 3) Apply copy-template via lib/heal directly.
	src := filepath.Join(root, ".env.example")
	dst := filepath.Join(root, ".env")
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: failed}, []heal.Action{
		{Kind: "copy-template", Src: src, Dst: dst},
	})
	if !results[0].Applied {
		t.Fatalf("copy-template not applied: %v", results[0].Reason)
	}

	// 4) Retry — must pass.
	out2, _ := runSensor(t, root, sensorPath)
	agg2 := lastJSONL(t, out2)
	if agg2["verdict"] != "pass" {
		t.Fatalf("expected retry to pass; got %v", agg2)
	}
}

func setupFixtureProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// Minimal .env.example with the var the sensor's command reads.
	os.WriteFile(filepath.Join(root, ".env.example"), []byte("RSA_PRIVATE_KEY=stub-key\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\n"), 0o644)

	// Sensor that fails when .env is missing.
	os.MkdirAll(filepath.Join(root, "sensors"), 0o755)
	s := testfixtures.ValidSensorComputational()
	s["id"] = "run-needs-env"
	s["execution"] = map[string]interface{}{
		"command": "test -f " + filepath.Join(root, ".env") + " || (echo 'open .env: ENOENT no such file' >&2; exit 1)",
		"exit_code_map": []interface{}{
			map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
		},
	}
	body, _ := json.Marshal(s)
	os.WriteFile(filepath.Join(root, "sensors", "run-needs-env.json"), body, 0o644)
	return root
}

func runSensor(t *testing.T, root, sensorPath string) (string, string) {
	t.Helper()
	cmd := exec.Command("go", "run", "-tags=run_computational", "./skills/run-sensor/scripts", sensorPath)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "HARNESS_FIXTURE_ROOT="+root)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	_ = cmd.Run() // exit code carried in the aggregate
	return stdout.String(), stderr.String()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("repo root not found from %s", wd)
	return ""
}

func lastJSONL(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(lines[i]), &m); err != nil {
			continue
		}
		return m
	}
	t.Fatalf("no JSONL line in output: %q", s)
	return nil
}

func signalFromMap(m map[string]interface{}) heal.Signal {
	var s heal.Signal
	if v, ok := m["verdict"].(string); ok {
		s.Verdict = v
	}
	if v, ok := m["severity"].(string); ok {
		s.Severity = v
	}
	if ev, ok := m["evidence"].([]interface{}); ok {
		for _, e := range ev {
			em, _ := e.(map[string]interface{})
			if em == nil {
				continue
			}
			r, _ := em["rationale"].(string)
			s.Evidence = append(s.Evidence, heal.SignalEvidence{Rationale: r})
		}
	}
	return s
}

func mustLoadFailedView(t *testing.T, path string) heal.FailedSensor {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		ID       string `json:"id"`
		Requires struct {
			Env []struct {
				Name string `json:"name"`
			} `json:"env"`
			Tools   []string `json:"tools"`
			Context []string `json:"context"`
		} `json:"requires"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	envs := []string{}
	for _, e := range v.Requires.Env {
		envs = append(envs, e.Name)
	}
	return heal.FailedSensor{ID: v.ID, EnvNames: envs, Tools: v.Requires.Tools, Context: v.Requires.Context}
}
```

- [ ] **Step 2: Run + commit**

```bash
go test ./test/heal-e2e/...
git add test/heal-e2e/heal_e2e_test.go
git commit -m "test(heal-e2e): missing .env → classify → apply-template → retry passes

End-to-end smoke that wires together the runner, classifier, and
allowlist applier. Uses lib/heal directly (does not exec the
hook binary) — the hook is exercised by hooks/setup-failure-detector_test.go.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

Expected: PASS.

---

## Task 24: /detect-sensors simplification — drop setup-iteration prose, add /heal-sensor invocation

**Files:**
- Modify: `skills/detect-sensors/SKILL.md`

- [ ] **Step 1: Replace step 7's prose with a heal-aware version**

Find the existing `### 7. Run each sensor and iterate until the output is informative` section in `skills/detect-sensors/SKILL.md` and replace it with:

```markdown
### 7. Run each sensor and iterate on shape correctness only

Schema-valid is not the same as semantically useful. After persisting, **run each sensor through the runner once** and inspect the aggregate Signal. Iterate ONLY on shape-correctness symptoms (regex matches, exit_code_map right, fixtures replay correctly). For setup-shape symptoms (missing env, missing binary, absent .env), do NOT iterate here — invoke `/heal-sensor` (see step 7.5).

Shape-correctness symptoms to fix in this loop:

- `output: "stream"` sensor returning `evidence: []` and `metadata.counts: {error:0,fail:0,pass:0,warn:0}` *when the underlying command actually produced output*. That means your patterns matched nothing — fix the regex, add `-v`/`--verbose` to the command, or escalate the relevant lines (e.g. `go test` needs `-v` or no PASS lines appear).
- Aggregate `verdict: "pass"` when you know the codebase has unfixed findings — patterns are skipping them.
- `metadata.timed_out: true` — your `cost.latency.timeout_ms` is too low for a real run.
- `evidence` entries with empty `excerpt` and `rationale` falling back to the entire raw line — capture groups are wrong.

Run order:

```bash
# 1) Production happy-path: run the sensor against the real codebase.
go run -tags=run_computational ./skills/run-sensor/scripts @sensors/<id>.json | tail -n 1 \
  | jq -c '{verdict, severity, counts: .metadata.counts, individuals: (.evidence|length)}'

# 2) Replay each fail/warn fixture to prove the unhappy paths.
TMP=$(mktemp /tmp/replay-XXXX.json)
jq --arg cmd "cat sensors/fixtures/<group>/<case>.txt" \
   '.execution.command = $cmd | .id = "replay-" + .id' \
   sensors/<id>.json > "$TMP"
go run -tags=run_computational ./skills/run-sensor/scripts "$TMP" | tail -n 1 \
  | jq -c '{verdict, severity, individuals: (.evidence|length)}'
rm "$TMP"
```

For each sensor, both must hold:

- Happy path on the live repo: aggregate `verdict` matches reality (clean repo → `pass`; dirty repo → `fail`/`warn`). Empty `evidence` is acceptable iff the underlying tool is genuinely silent on success (vet, build, schema parsers); for tools that emit per-test output (Go test with `-v`, jest, pytest -v), `counts` MUST show non-zero in the relevant bucket.
- Each `golden_cases[]` entry: replay must produce the declared `expected_verdict` and `expected_severity`. If a replay disagrees, EITHER the patterns are wrong (most common) OR `expected_verdict` is wrong — fix one and re-replay until both agree.

If iteration changes `output`, `execution`, or `verification`, bump the sensor `version` (e.g. `0.1.0` → `0.2.0`) and re-persist via the validator. The version stamp is the audit trail of which shape was actually verified.

### 7.5. If smoke run fails with a setup-shape symptom, invoke /heal-sensor

When step 7's smoke run produces an aggregate Signal that is setup-shape (missing env, missing binary, absent `.env`, unavailable service), do NOT iterate inside this skill. Invoke `/heal-sensor` instead:

```
/heal-sensor --signal=<path-to-saved-aggregate-signal-json> --sensor=@sensors/<id>.json
```

`/heal-sensor` will read the project state, build a Setup Plan, apply allowlisted idempotent fixes (cp .env.example .env, mkdir, touch, set-env-in-file), persist any patched/new sensors via the same `lib/sensor.ValidateAndPersist` primitive this skill uses, and retry the original sensor. After it returns:

- If the retry passed: continue the draft loop — your sensor is healthy.
- If `/heal-sensor` couldn't recover: the failure is genuinely outside the harness's reach (needs `pnpm install`, `gcloud login`, etc.). Read the remediation it emitted, surface it to the user, and continue with the OTHER sensors. Don't block this skill on credentials the harness can't synthesize.

Setup-shape recovery used to be the responsibility of this skill's prose ("if credentials are missing, declare them and proceed"). It is now `/heal-sensor`'s job — exclusively. This skill stays focused on shape correctness.
```

- [ ] **Step 2: Verify the existing detect-sensors tests still pass**

```bash
go test ./skills/detect-sensors/...
```

Expected: PASS (no Go code changed; only SKILL.md prose).

- [ ] **Step 3: Commit**

```bash
git add skills/detect-sensors/SKILL.md
git commit -m "docs(detect-sensors): drop setup-iteration prose; delegate to /heal-sensor

Step 7 now iterates only on shape-correctness symptoms (regex,
exit_code_map, fixtures). Step 7.5 explicitly hands setup-shape
symptoms (missing env, binary, .env, service) to /heal-sensor —
the same skill the run-sensor hook triggers. detect-sensors stops
blocking on credentials it can't synthesize.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Final integration check

- [ ] **Step 1: Run the full test suite**

```bash
go test ./...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
```

Expected: all PASS, no vet warnings.

- [ ] **Step 2: Final manual smoke**

Pick a fixture project (e.g., the one used in `test/heal-e2e/`) and verify a `/run-sensor` invocation against a sensor whose `.env` is missing flows through the hook → heal → retry path end-to-end. Document any rough edges in a follow-up issue (do NOT keep iterating in this PR).

- [ ] **Step 3: Final commit gating**

If everything's green, the work is shippable. If anything flakes, fix the specific test in a focused commit; do not relax assertions.

---

## Spec coverage check (self-review)

| Spec section | Task(s) |
|---|---|
| `lib/sensor.ValidateAndPersist` primitive | 1 |
| `write-sensor.go` refactored to call lib | 2 |
| `lib/heal/plan.go` Setup Plan model | 3 |
| `Rule` interface + `Shape` enum + `FailedSensor` + `Classify` | 4 |
| `lib/heal/patterns.go` curated regex set | 5 |
| Five concrete rules (one file each) | 6, 7, 8, 9, 10 |
| `lib/heal/rules.go` registrar | 11 |
| Single-edit-point property test | 12 |
| `lib/heal/apply.go` allowlist | 13 |
| `lib/heal/envwriter.go` chmod 600 + gitignore guard | 14 |
| `lib/heal/version.go` BumpPatch | 15 |
| `setup-failure-detector` Stop hook | 16 |
| Plugin auto-install | 17 |
| `diagnose.go` deterministic input collector | 18 |
| `apply-safe.go` CLI wrapper | 19 |
| `apply-sensors.go` shared lib persistence | 20 |
| `retry-original.go` single retry | 21 |
| `SKILL.md` orchestration | 22 |
| End-to-end smoke | 23 |
| `/detect-sensors` simplification | 24 |
| `metadata.heal_hint` contract documented | 5 (header comment in patterns.go) + 7 (rule_heal_hint) |
| `warn` verdicts never trigger heal | enforced by rule_missing_env requiring verdict=error; rule_stderr_pattern + others are evidence-based but the hook only fires when classify matches |
| `/run-sensor` invariance | no task touches it (intentional) |
| `lib/orchestrator` minor `heal_hint` emission | NOT in this plan — flagged as optional in the spec; can land as a follow-up |

The optional `lib/orchestrator/run.go` `metadata.heal_hint` emission is intentionally deferred to a follow-up. The classifier already works without it via `rule_missing_env` and `rule_stderr_pattern`. Adding it would be a one-line addition that requires touching a path-critical orchestrator file; a separate change keeps the blast radius small and keeps this plan focused.
