# Sensor Dependencies & Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement sensor dependency resolution and prepare/teardown lifecycle phases so a `run-project` sensor can declare and reuse setup steps (start postgres, populate `.env`, install deps) without inlining shell `&&` chains.

**Architecture:** Add three top-level fields to `schemas/sensor.json` (`kind`, `depends_on`, `execution.prepare`/`teardown`) and a new `lib/orchestrator/` package that resolves the DAG of dependencies, runs each sensor's prepare → command → teardown in topo order, and cascades failure verdicts. Both runner scripts (`run-computational.go`, `run-inferential.go`) delegate to a single orchestrator entry point.

**Tech Stack:** Go 1.25, JSON Schema Draft 2020-12 (`github.com/santhosh-tekuri/jsonschema/v5`), `github.com/iurykrieger/harness-framework` module. Standard `testing` package, table-driven tests.

**Spec:** `docs/superpowers/specs/2026-05-08-sensor-dependencies-design.md`

---

## File Structure

**Schema:**
- Modify: `schemas/sensor.json` (add `kind`, `depends_on`, `execution.prepare`, `execution.teardown`, `$defs/ExitCodeMapEntry`, `$defs/LifecycleStep`; remove `requires.upstream_sensors`)

**lib/sensor/ (existing package):**
- Create: `lib/sensor/lookup.go` — find sensor by ID inside a sensor root directory
- Create: `lib/sensor/lookup_test.go`

**lib/subprocess/ (existing package):**
- Create: `lib/subprocess/step.go` — run a single silent shell step (no patterns), return exit code + elapsed + stderr excerpt
- Create: `lib/subprocess/step_test.go`

**lib/orchestrator/ (NEW package):**
- Create: `lib/orchestrator/dag.go` — `Resolve(rootID, sensorRoot, schemasDir)` returns sensors topo-sorted with cycle detection
- Create: `lib/orchestrator/dag_test.go`
- Create: `lib/orchestrator/lifecycle.go` — `RunOne(sensor, schemasDir, stdout, stderr)` runs prepare → command → teardown for one sensor
- Create: `lib/orchestrator/lifecycle_test.go`
- Create: `lib/orchestrator/cascade.go` — `BuildCascadeSignal(skipped, failedDep)` returns a Signal map for skipped dependents
- Create: `lib/orchestrator/cascade_test.go`
- Create: `lib/orchestrator/run.go` — `RunWithDeps(sensorPath, schemasDir, stdout, stderr) int` is the entry point both runners call
- Create: `lib/orchestrator/run_test.go`

**Test fixtures:**
- Modify: `lib/testfixtures/sensor.go` — add `kind` to `ValidSensorComputational`/`ValidSensorInferential`; add `ValidSensorSetup()`

**Existing sensors (atomic with schema bump):**
- Modify: every file in `sensors/*.json` — add `"kind": "assertion"` (the existing 12 sensors are all assertions)

**Runner scripts:**
- Modify: `skills/run-sensor/scripts/run-computational.go` — replace inline pipeline with `orchestrator.RunWithDeps`
- Modify: `skills/run-sensor/scripts/run-computational_test.go` — adapt assertions for orchestrator output
- Modify: `skills/run-sensor/scripts/run-inferential.go` — same as computational
- Modify: `skills/run-sensor/scripts/run-inferential_test.go` — adapt assertions

**Skills & docs:**
- Modify: `skills/detect-sensors/SKILL.md` — add taxonomy guidance + lifecycle authoring
- Modify: `skills/run-sensor/SKILL.md` — document new JSONL stream format + cascade behavior
- Modify: `CLAUDE.md` (Architecture section, NOT Project rules) — add `kind`, lifecycle phases, setup-sensor vocabulary

---

## Task 1: Schema bump + sensor migration + fixture update (atomic)

**Why atomic:** Making `kind` required (no default) invalidates every existing sensor and every test fixture against the new schema. Schema change, sensor migration, and fixture update MUST land in the same commit so `go test ./...` stays green continuously.

**Files:**
- Modify: `schemas/sensor.json`
- Modify: `lib/testfixtures/sensor.go`
- Modify: every file in `sensors/*.json`
- Test: `lib/schema/validator_test.go`

- [ ] **Step 1.1: Read existing schema and sensor files for context**

```bash
cat schemas/sensor.json | head -50
ls sensors/*.json
go test ./lib/schema/... -run TestValidator -v
```

Expected: All current schema tests pass.

- [ ] **Step 1.2: Write new validator tests for kind, depends_on, prepare, teardown**

Append to `lib/schema/validator_test.go`:

```go
func TestValidator_KindRequired(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, err := NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	delete(s, "kind")
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected validation to fail without kind")
	}
}

func TestValidator_KindEnumRejectsUnknown(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, err := NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	s["kind"] = "diagnostic"
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected validation to fail for kind='diagnostic'")
	}
}

func TestValidator_DependsOnAcceptsIDArray(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, err := NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	s["depends_on"] = []interface{}{"start-postgres", "setup-env"}
	if err := v.Validate(TargetSensor, s); err != nil {
		t.Fatalf("expected valid depends_on to validate: %v", err)
	}
}

func TestValidator_DependsOnRejectsBadID(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, err := NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	s["depends_on"] = []interface{}{"Bad-ID"} // uppercase rejected
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected validation to fail for uppercase id in depends_on")
	}
}

func TestValidator_PrepareTeardownAccepted(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, err := NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	exec := s["execution"].(map[string]interface{})
	exec["prepare"] = []interface{}{
		map[string]interface{}{"command": "echo prep", "timeout_ms": 1000},
	}
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "echo down"},
	}
	if err := v.Validate(TargetSensor, s); err != nil {
		t.Fatalf("expected prepare+teardown to validate: %v", err)
	}
}

func TestValidator_UpstreamSensorsRemoved(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, err := NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	s := testfixtures.ValidSensorComputational()
	requires := map[string]interface{}{
		"upstream_sensors": []interface{}{"x"},
	}
	s["requires"] = requires
	if err := v.Validate(TargetSensor, s); err == nil {
		t.Fatal("expected requires.upstream_sensors to be rejected (additionalProperties false)")
	}
}
```

- [ ] **Step 1.3: Run new tests, verify they fail**

```bash
go test ./lib/schema/... -run "TestValidator_(KindRequired|KindEnumRejectsUnknown|DependsOn|PrepareTeardownAccepted|UpstreamSensorsRemoved)" -v
```

Expected: All six new tests FAIL (kind not in schema yet, depends_on not in schema, etc.).

- [ ] **Step 1.4: Update `lib/testfixtures/sensor.go` to add `kind` to existing fixtures and add `ValidSensorSetup`**

Modify `lib/testfixtures/sensor.go`:

```go
// In ValidSensorComputational(), add the kind field at the top of the returned map:
"id": "smoke-comp", "version": "0.1.0",
"name": "smoke", "description": "fixture",
"kind": "assertion",   // ADDED
"type": "computational", "regulation": "maintainability",

// In ValidSensorInferential(), do the same:
"id": "smoke-inf", "version": "0.1.0",
"name": "smoke inf", "description": "fixture",
"kind": "assertion",   // ADDED
"type": "inferential", "regulation": "maintainability",
```

Add a new function in the same file:

```go
// ValidSensorSetup returns a minimal setup sensor (kind=setup) that passes the schema.
func ValidSensorSetup() map[string]interface{} {
	return map[string]interface{}{
		"id": "smoke-setup", "version": "0.1.0",
		"name": "smoke setup", "description": "fixture: idempotent setup",
		"kind": "setup",
		"type": "computational", "regulation": "behaviour",
		"phase": "on-demand", "determinism": "high",
		"output": "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 100, "timeout_ms": 5000},
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 64},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "agent-request"}},
		"execution": map[string]interface{}{
			"command": "true",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "x", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
	}
}
```

- [ ] **Step 1.5: Update `schemas/sensor.json` with all schema changes**

In `schemas/sensor.json`:

1. Add `kind` to the top-level `required` array (between `description` and `type`):

```json
"required": [
  "id",
  "version",
  "name",
  "description",
  "kind",
  "type",
  "regulation",
  "phase",
  "determinism",
  "output",
  "cost",
  "triggers",
  "execution",
  "verification"
],
```

2. Add `kind` to top-level `properties` (right after `description`):

```json
"kind": {
  "type": "string",
  "enum": ["observation", "assertion", "setup"],
  "description": "Sensor category. observation = observes behavior with no fixed expectation (run-project, fetch-logs, fetch-metrics, trace-request, watch-build); verdict describes the health of the observation. assertion = checks against an expectation (unit-test, integration-test, e2e-test, lint, type-check, build, schema-validate); verdict pass/fail is semantic. setup = idempotent auxiliary sensor that makes a precondition true (start-postgres, setup-env-from-example, install-deps-pnpm, login-gcloud); typically referenced via depends_on. Metadata only — the runner treats all three identically; tooling (detect-sensors, listings) uses it to classify and name."
},
```

3. Add `depends_on` at top-level (after `triggers`):

```json
"depends_on": {
  "type": "array",
  "default": [],
  "description": "IDs of sensors that must run (and PASS) before this one. The runner resolves the transitive closure in topological order and propagates failures. Use to chain setup sensors (start-postgres) or assertion sensors (unit-tests before e2e-tests). Cycles (including self-loops A → A) are detected and abort with exit 1.",
  "items": {
    "type": "string",
    "pattern": "^[a-z][a-z0-9-]*$"
  },
  "uniqueItems": true
},
```

4. Add the new `$defs` (replacing the existing `$defs.Signal`):

```json
"$defs": {
  "Signal": { "$ref": "signal.json" },
  "ExitCodeMapEntry": {
    "type": "object",
    "additionalProperties": false,
    "required": ["exit_code", "verdict", "severity"],
    "properties": {
      "exit_code": {
        "oneOf": [
          { "type": "integer", "minimum": 0 },
          { "type": "string", "const": "*" }
        ]
      },
      "verdict":  { "$ref": "signal.json#/$defs/Verdict" },
      "severity": { "$ref": "signal.json#/$defs/Severity" }
    }
  },
  "LifecycleStep": {
    "type": "object",
    "additionalProperties": false,
    "required": ["command"],
    "properties": {
      "command":       { "type": "string", "description": "Shell invocation (sh -c). MUST be idempotent." },
      "timeout_ms":    { "type": "integer", "minimum": 1, "description": "Hard cap; falls back to cost.latency.timeout_ms when omitted." },
      "exit_code_map": {
        "type": "array",
        "minItems": 1,
        "description": "Override only when tooling uses non-standard codes (e.g. 2 = 'already exists', treat as pass). When omitted, runner applies the default [{0:pass/info},{*:fail/high}].",
        "items": { "$ref": "#/$defs/ExitCodeMapEntry" }
      }
    }
  }
},
```

5. Add `prepare` and `teardown` to `execution.properties` (anywhere inside it, alongside `command`):

```json
"prepare": {
  "type": "array",
  "default": [],
  "description": "Silent shell commands run before execution.command. Used to satisfy the sensor's preconditions (generate files, populate local .env from .env.example, intermediate builds). NOT observed via patterns; per-step verdict is folded into the sensor's aggregate metadata.lifecycle.prepare. Fail-fast: first non-pass step aborts prepare and skips the main command; teardown still runs.",
  "items": { "$ref": "#/$defs/LifecycleStep" }
},
"teardown": {
  "type": "array",
  "default": [],
  "description": "Silent shell commands run AFTER execution.command with finally semantics — they run on prepare failure, command failure, and command timeout. Per-step results are folded into metadata.lifecycle.teardown; teardown failures contribute warn evidence but do NOT downgrade the aggregate verdict.",
  "items": { "$ref": "#/$defs/LifecycleStep" }
},
```

6. Refactor `execution.exit_code_map.items` to reference the shared $def:

```json
"exit_code_map": {
  "type": "array",
  "description": "Maps process exit codes to verdict/severity. Required for computational sensors; optional for inferential (when omitted, the inferential runner defaults to exit 0 → pass/info, anything else → error/high). Use exit_code='*' as wildcard fallback.",
  "minItems": 1,
  "items": { "$ref": "#/$defs/ExitCodeMapEntry" }
},
```

7. Remove the entire `requires.properties.upstream_sensors` block.

- [ ] **Step 1.6: Run validator tests, verify all pass**

```bash
go test ./lib/schema/... -v
```

Expected: All tests PASS, including the six new ones.

- [ ] **Step 1.7: Run all lib/ tests to confirm fixture changes didn't break anything**

```bash
go test ./lib/...
```

Expected: All packages PASS. (The `kind` addition to `ValidSensorComputational`/`Inferential` is now required by the schema, so older tests that round-tripped them would have already broken without the fixture update.)

- [ ] **Step 1.8: Migrate every sensor in `sensors/*.json` adding `"kind": "assertion"`**

Run this script (one-shot, idempotent):

```bash
for f in sensors/*.json; do
  if ! grep -q '"kind"' "$f"; then
    # Insert "kind": "assertion" after the "description" line.
    # Use jq to keep formatting consistent.
    jq '. + {kind: "assertion"} | {id, version, name, description, kind, type, regulation, phase, determinism, output, cost, triggers, requires, execution, verification, blind_spots, calibration, references} | with_entries(select(.value != null))' "$f" > "$f.tmp"
    mv "$f.tmp" "$f"
    echo "migrated: $f"
  fi
done
```

Then verify each file is still valid JSON and the schema accepts it:

```bash
for f in sensors/*.json; do
  jq -e . "$f" > /dev/null || echo "INVALID JSON: $f"
done

# Sanity check: validator runs cleanly via the existing detect-sensors writer.
for f in sensors/*.json; do
  go run ./skills/detect-sensors/scripts --out=/tmp/migrate-check "$f" || echo "REJECTED: $f"
done
rm -rf /tmp/migrate-check
```

Expected: every file echoes a write path, none are rejected.

- [ ] **Step 1.9: Run the entire test suite**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
```

Expected: ALL packages PASS.

- [ ] **Step 1.10: Commit**

```bash
git add schemas/sensor.json lib/testfixtures/sensor.go lib/schema/validator_test.go sensors/
git commit -m "$(cat <<'EOF'
feat(schema): add kind, depends_on, execution.prepare/teardown

- kind (required, enum: observation|assertion|setup) classifies sensors
- depends_on (top-level string array) declares which sensors must run first
- execution.prepare[] / teardown[] carry silent lifecycle steps
- Removes requires.upstream_sensors (replaced by depends_on)
- Adds $defs/ExitCodeMapEntry and $defs/LifecycleStep
- Migrates all 12 existing sensors to kind=assertion (atomic with schema bump)
- Updates lib/testfixtures with the new required kind field

Implements Phase 1 + Phase 3 of the sensor dependencies design spec.
Phase 1 alone would invalidate every existing sensor; landing them
together keeps the test suite green.

Spec: docs/superpowers/specs/2026-05-08-sensor-dependencies-design.md

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: lib/sensor/lookup.go — find sensor by ID

**Files:**
- Create: `lib/sensor/lookup.go`
- Test: `lib/sensor/lookup_test.go`

The orchestrator needs to load a sensor by its bare ID (e.g. `"start-postgres"`) when traversing `depends_on`. `ResolveSensorPath` only handles paths/`@`-prefixed args.

- [ ] **Step 2.1: Write failing test**

Create `lib/sensor/lookup_test.go`:

```go
package sensor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindSensorByID_Found(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "start-postgres.json")
	if err := os.WriteFile(target, []byte(`{"id":"start-postgres"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FindSensorByID("start-postgres", root)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("got %q want %q", got, target)
	}
}

func TestFindSensorByID_Missing(t *testing.T) {
	root := t.TempDir()
	if _, err := FindSensorByID("nope", root); err == nil {
		t.Fatal("expected error when sensor file missing")
	}
}

func TestFindSensorByID_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := FindSensorByID("../escape", root); err == nil {
		t.Fatal("expected error when id contains path separators")
	}
}
```

- [ ] **Step 2.2: Run test, verify failure**

```bash
go test ./lib/sensor/ -run TestFindSensorByID -v
```

Expected: FAIL — `undefined: FindSensorByID`.

- [ ] **Step 2.3: Implement FindSensorByID**

Create `lib/sensor/lookup.go`:

```go
package sensor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FindSensorByID resolves a bare sensor id (e.g. "start-postgres") to its
// canonical file path under sensorRoot ("<sensorRoot>/<id>.json"). Returns
// an error if the file does not exist or the id contains path separators.
func FindSensorByID(id, sensorRoot string) (string, error) {
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid sensor id %q (no path separators)", id)
	}
	path := filepath.Join(sensorRoot, id+".json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("sensor %q not found at %s: %w", id, path, err)
	}
	return path, nil
}
```

- [ ] **Step 2.4: Run test, verify pass**

```bash
go test ./lib/sensor/ -run TestFindSensorByID -v
```

Expected: PASS for all three cases.

- [ ] **Step 2.5: Commit**

```bash
git add lib/sensor/lookup.go lib/sensor/lookup_test.go
git commit -m "$(cat <<'EOF'
feat(sensor): add FindSensorByID helper for orchestrator lookups

The orchestrator needs to load sensors by bare id (e.g. "start-postgres")
when traversing depends_on. The existing ResolveSensorPath handles
paths and @-prefixed args; this fills the bare-id gap and rejects
path-traversal attempts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: lib/orchestrator/dag.go — DAG resolution

**Files:**
- Create: `lib/orchestrator/dag.go`
- Test: `lib/orchestrator/dag_test.go`

Resolve the transitive closure of a sensor's `depends_on` and return a topo-sorted slice (deps first, root last). Detect cycles (including self-loops) and missing deps.

- [ ] **Step 3.1: Write failing test**

Create `lib/orchestrator/dag_test.go`:

```go
package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSensorJSON(t *testing.T, root, id string, depsOn []string) {
	t.Helper()
	deps := "[]"
	if len(depsOn) > 0 {
		var s string
		for i, d := range depsOn {
			if i > 0 {
				s += ","
			}
			s += `"` + d + `"`
		}
		deps = "[" + s + "]"
	}
	body := `{"id":"` + id + `","depends_on":` + deps + `}`
	if err := os.WriteFile(filepath.Join(root, id+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve_Linear(t *testing.T) {
	root := t.TempDir()
	writeSensorJSON(t, root, "a", nil)
	writeSensorJSON(t, root, "b", []string{"a"})
	writeSensorJSON(t, root, "c", []string{"b"})

	order, err := Resolve("c", root)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, s := range order {
		got = append(got, s.ID)
	}
	want := []string{"a", "b", "c"}
	if !equal(got, want) {
		t.Fatalf("topo order = %v, want %v", got, want)
	}
}

func TestResolve_Diamond(t *testing.T) {
	// d → b, c ; b → a ; c → a   ⇒   a before b,c  ; b,c before d
	root := t.TempDir()
	writeSensorJSON(t, root, "a", nil)
	writeSensorJSON(t, root, "b", []string{"a"})
	writeSensorJSON(t, root, "c", []string{"a"})
	writeSensorJSON(t, root, "d", []string{"b", "c"})

	order, err := Resolve("d", root)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, s := range order {
		pos[s.ID] = i
	}
	if pos["a"] >= pos["b"] || pos["a"] >= pos["c"] || pos["b"] >= pos["d"] || pos["c"] >= pos["d"] {
		t.Fatalf("diamond order violates dependencies: %v", pos)
	}
	if len(order) != 4 {
		t.Fatalf("expected 4 sensors in order, got %d", len(order))
	}
}

func TestResolve_Cycle(t *testing.T) {
	root := t.TempDir()
	writeSensorJSON(t, root, "a", []string{"b"})
	writeSensorJSON(t, root, "b", []string{"a"})

	if _, err := Resolve("a", root); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestResolve_SelfLoop(t *testing.T) {
	root := t.TempDir()
	writeSensorJSON(t, root, "a", []string{"a"})

	if _, err := Resolve("a", root); err == nil {
		t.Fatal("expected self-loop to be rejected")
	}
}

func TestResolve_MissingDep(t *testing.T) {
	root := t.TempDir()
	writeSensorJSON(t, root, "a", []string{"ghost"})

	if _, err := Resolve("a", root); err == nil {
		t.Fatal("expected missing dep error")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 3.2: Run test, verify failure**

```bash
go test ./lib/orchestrator/ -run TestResolve -v
```

Expected: FAIL — `package lib/orchestrator does not exist`.

- [ ] **Step 3.3: Implement Resolve**

Create `lib/orchestrator/dag.go`:

```go
// Package orchestrator resolves and runs sensor dependency graphs. A
// sensor's depends_on declares ids of other sensors that must run and
// pass before it; this package walks that closure, sorts topologically
// (deps first), and runs each sensor's prepare → command → teardown
// lifecycle. Failures cascade: dependents of a failed sensor never run
// and emit cascade Signals instead.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// Sensor is the parsed-JSON form of one sensor along the dependency path.
type Sensor struct {
	ID    string
	Path  string
	JSON  map[string]interface{}
}

// Resolve loads the sensor identified by rootID from sensorRoot, walks
// its depends_on transitively, and returns the slice topo-sorted (leaves
// first, rootID last). Cycles (including self-loops A → A) and missing
// dependency files cause an error and an empty slice.
func Resolve(rootID, sensorRoot string) ([]Sensor, error) {
	sensors := map[string]Sensor{}
	deps := map[string][]string{}
	if err := loadRecursive(rootID, sensorRoot, sensors, deps, map[string]bool{}); err != nil {
		return nil, err
	}
	return topoSort(rootID, sensors, deps)
}

func loadRecursive(id, root string, sensors map[string]Sensor, deps map[string][]string, visiting map[string]bool) error {
	if _, ok := sensors[id]; ok {
		return nil
	}
	if visiting[id] {
		return fmt.Errorf("dependency cycle detected at sensor %q", id)
	}
	visiting[id] = true
	defer delete(visiting, id)

	path, err := sensor.FindSensorByID(id, root)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read sensor %q: %w", id, err)
	}
	var s map[string]interface{}
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("parse sensor %q: %w", id, err)
	}
	abs, _ := filepath.Abs(path)
	sensors[id] = Sensor{ID: id, Path: abs, JSON: s}

	depIDs := readDepsArray(s)
	deps[id] = depIDs
	for _, depID := range depIDs {
		if depID == id {
			return fmt.Errorf("dependency cycle detected at sensor %q (self-loop)", id)
		}
		if err := loadRecursive(depID, root, sensors, deps, visiting); err != nil {
			return err
		}
	}
	return nil
}

func readDepsArray(s map[string]interface{}) []string {
	raw, _ := s["depends_on"].([]interface{})
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if id, ok := item.(string); ok {
			out = append(out, id)
		}
	}
	return out
}

// topoSort runs Kahn's algorithm starting from leaves and ending at rootID.
func topoSort(rootID string, sensors map[string]Sensor, deps map[string][]string) ([]Sensor, error) {
	indegree := map[string]int{}
	dependents := map[string][]string{}
	for id := range sensors {
		indegree[id] = 0
	}
	for id, ds := range deps {
		for _, d := range ds {
			indegree[id]++
			dependents[d] = append(dependents[d], id)
		}
	}

	var queue []string
	for id, deg := range indegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	out := make([]Sensor, 0, len(sensors))
	for len(queue) > 0 {
		// Pop the lexicographically smallest id for stable ordering.
		minIdx := 0
		for i := range queue {
			if queue[i] < queue[minIdx] {
				minIdx = i
			}
		}
		id := queue[minIdx]
		queue = append(queue[:minIdx], queue[minIdx+1:]...)
		out = append(out, sensors[id])
		for _, dep := range dependents[id] {
			indegree[dep]--
			if indegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}
	if len(out) != len(sensors) {
		return nil, fmt.Errorf("dependency cycle detected (resolved %d of %d sensors)", len(out), len(sensors))
	}
	if out[len(out)-1].ID != rootID {
		return nil, fmt.Errorf("internal: topo sort did not end at root %q", rootID)
	}
	return out, nil
}
```

- [ ] **Step 3.4: Run test, verify pass**

```bash
go test ./lib/orchestrator/ -run TestResolve -v
```

Expected: PASS for Linear, Diamond, Cycle, SelfLoop, MissingDep.

- [ ] **Step 3.5: Commit**

```bash
git add lib/orchestrator/dag.go lib/orchestrator/dag_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): add Resolve for dependency DAG with cycle detection

Walks a sensor's depends_on transitively and topo-sorts the result
(leaves first, root last). Catches cycles (including self-loops A → A)
and missing dependency files. Returns the parsed JSON of every
sensor in the graph for downstream lifecycle execution.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: lib/subprocess/step.go — silent step runner

**Files:**
- Create: `lib/subprocess/step.go`
- Test: `lib/subprocess/step_test.go`

`prepare`/`teardown` items run silently (no patterns) — we only need exit code, elapsed time, and a stderr excerpt for failed steps.

- [ ] **Step 4.1: Write failing test**

Create `lib/subprocess/step_test.go`:

```go
package subprocess

import (
	"context"
	"strings"
	"testing"
)

func TestRunStep_Pass(t *testing.T) {
	res, err := RunStep(context.Background(), StepConfig{Command: "true", TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d want 0", res.ExitCode)
	}
	if res.TimedOut {
		t.Fatal("did not expect timeout")
	}
}

func TestRunStep_NonZeroExit(t *testing.T) {
	res, err := RunStep(context.Background(), StepConfig{Command: "false", TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatal("expected non-zero exit")
	}
}

func TestRunStep_StderrExcerpt(t *testing.T) {
	res, err := RunStep(context.Background(), StepConfig{Command: "echo woops 1>&2; exit 7", TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit=%d want 7", res.ExitCode)
	}
	if !strings.Contains(res.StderrExcerpt, "woops") {
		t.Fatalf("stderr excerpt = %q", res.StderrExcerpt)
	}
}

func TestRunStep_Timeout(t *testing.T) {
	res, err := RunStep(context.Background(), StepConfig{Command: "sleep 5", TimeoutMS: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("expected timeout")
	}
}

func TestRunStep_EmptyCommandError(t *testing.T) {
	if _, err := RunStep(context.Background(), StepConfig{Command: "", TimeoutMS: 1000}); err == nil {
		t.Fatal("expected empty-command error")
	}
}
```

- [ ] **Step 4.2: Run test, verify failure**

```bash
go test ./lib/subprocess/ -run TestRunStep -v
```

Expected: FAIL — `undefined: RunStep / StepConfig`.

- [ ] **Step 4.3: Implement RunStep**

Create `lib/subprocess/step.go`:

```go
package subprocess

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// StepConfig is the input to RunStep.
type StepConfig struct {
	Command   string
	Env       map[string]string
	TimeoutMS int
}

// StepResult captures everything a lifecycle phase needs to fold into the
// aggregate Signal: exit code, elapsed time, timeout flag, and a short
// stderr excerpt (helpful for surfacing why a prepare/teardown step
// failed).
type StepResult struct {
	ExitCode      int
	ElapsedMS     int
	TimedOut      bool
	StderrExcerpt string
}

const stderrExcerptCap = 4096

// RunStep spawns sh -c <Command>, captures stderr fully (truncated at
// stderrExcerptCap), discards stdout, and returns the result. Patterns
// are NOT applied — this is for prepare/teardown only.
func RunStep(ctx context.Context, cfg StepConfig) (StepResult, error) {
	if cfg.Command == "" {
		return StepResult{}, errors.New("step: empty command")
	}
	if cfg.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)
	if len(cfg.Env) > 0 {
		envList := append([]string{}, cmd.Environ()...)
		for k, v := range cfg.Env {
			envList = append(envList, k+"="+v)
		}
		cmd.Env = envList
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := int(time.Since(start) / time.Millisecond)

	res := StepResult{
		ElapsedMS:     elapsed,
		TimedOut:      errors.Is(ctx.Err(), context.DeadlineExceeded),
		StderrExcerpt: truncate(stderr.String(), stderrExcerptCap),
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	} else if runErr != nil {
		res.ExitCode = -1
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
```

- [ ] **Step 4.4: Run test, verify pass**

```bash
go test ./lib/subprocess/ -run TestRunStep -v
```

Expected: PASS for all five cases.

- [ ] **Step 4.5: Commit**

```bash
git add lib/subprocess/step.go lib/subprocess/step_test.go
git commit -m "$(cat <<'EOF'
feat(subprocess): add RunStep for silent prepare/teardown lifecycle steps

Patterns are not applied to prepare/teardown; only exit code, elapsed
time, timeout flag, and stderr excerpt matter. RunStep captures exactly
that, sized for inclusion in metadata.lifecycle.{prepare,teardown}.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: lib/orchestrator/cascade.go — build cascade Signals

**Files:**
- Create: `lib/orchestrator/cascade.go`
- Test: `lib/orchestrator/cascade_test.go`

When a dep fails, every transitively-dependent sensor emits a cascade Signal. This task isolates the envelope construction.

- [ ] **Step 5.1: Write failing test**

Create `lib/orchestrator/cascade_test.go`:

```go
package orchestrator

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestBuildCascadeSignal_Envelope(t *testing.T) {
	skipped := Sensor{
		ID: "e2e-tests",
		JSON: map[string]interface{}{
			"id":      "e2e-tests",
			"version": "0.1.0",
			"execution": map[string]interface{}{
				"command": "pnpm playwright test",
			},
		},
	}
	failedDepSignal := map[string]interface{}{
		"sensor_id": "start-postgres",
		"run_id":    "run-pg-1",
		"verdict":   "fail",
		"severity":  "high",
	}

	prevNow := sensor.NowFn
	prevID := sensor.NewRunIDFn
	defer func() { sensor.NowFn = prevNow; sensor.NewRunIDFn = prevID }()
	sensor.NowFn = func() time.Time { return time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC) }
	sensor.NewRunIDFn = func() string { return "run-cascade-1" }

	sig := BuildCascadeSignal(skipped, failedDepSignal)

	checks := map[string]interface{}{
		"sensor_id":  "e2e-tests",
		"version":    "0.1.0",
		"run_id":     "run-cascade-1",
		"verdict":    "error",
		"severity":   "high",
		"confidence": 1.0,
	}
	for k, want := range checks {
		if got := sig[k]; got != want {
			t.Errorf("sig[%q] = %v, want %v", k, got, want)
		}
	}
	if sig["started_at"] != sig["finished_at"] {
		t.Errorf("started_at != finished_at for cascade signal")
	}
	cost := sig["cost_actual"].(map[string]interface{})
	if cost["latency_ms"] != 0 {
		t.Errorf("cost_actual.latency_ms = %v, want 0", cost["latency_ms"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "cascade" {
		t.Errorf("metadata.kind = %v, want cascade", md["kind"])
	}
	if md["failed_dep_id"] != "start-postgres" {
		t.Errorf("metadata.failed_dep_id = %v", md["failed_dep_id"])
	}
	if md["failed_dep_run_id"] != "run-pg-1" {
		t.Errorf("metadata.failed_dep_run_id = %v", md["failed_dep_run_id"])
	}
	ev := sig["evidence"].([]interface{})
	if len(ev) != 1 {
		t.Fatalf("evidence len = %d", len(ev))
	}
	first := ev[0].(map[string]interface{})
	rationale, _ := first["rationale"].(string)
	if rationale == "" {
		t.Error("expected non-empty evidence[0].rationale")
	}
}
```

Add this import line at the top of `cascade_test.go`: `import "time"` (alongside the existing `testing` and `sensor` imports).

- [ ] **Step 5.2: Run test, verify failure**

```bash
go test ./lib/orchestrator/ -run TestBuildCascadeSignal -v
```

Expected: FAIL — `undefined: BuildCascadeSignal`.

- [ ] **Step 5.3: Implement BuildCascadeSignal**

Create `lib/orchestrator/cascade.go`:

```go
package orchestrator

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// BuildCascadeSignal constructs the Signal map emitted for a sensor that
// was skipped because one of its (transitive) dependencies produced a
// non-pass verdict. The structure is described in
// docs/superpowers/specs/2026-05-08-sensor-dependencies-design.md
// (section "Cascade Signal envelope").
//
// failedDepSignal is the aggregate Signal of the dep that failed; the
// caller is responsible for ensuring it carries verdict, severity,
// sensor_id, and run_id.
func BuildCascadeSignal(skipped Sensor, failedDepSignal map[string]interface{}) map[string]interface{} {
	now := sensor.NowFn().Format("2006-01-02T15:04:05Z")
	failedID, _ := failedDepSignal["sensor_id"].(string)
	failedRunID, _ := failedDepSignal["run_id"].(string)
	failedVerdict, _ := failedDepSignal["verdict"].(string)
	failedSeverity, _ := failedDepSignal["severity"].(string)

	version, _ := skipped.JSON["version"].(string)
	execMap, _ := skipped.JSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)

	rationale := fmt.Sprintf(
		"Skipped: dependency %q produced verdict=%s/%s in run_id=%s. See its Signal earlier in this JSONL stream.",
		failedID, failedVerdict, failedSeverity, failedRunID,
	)

	return map[string]interface{}{
		"sensor_id":   skipped.ID,
		"version":     version,
		"run_id":      sensor.NewRunIDFn(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence": []interface{}{
			map[string]interface{}{
				"rationale": rationale,
				"file":      fmt.Sprintf("sensors/%s.json", failedID),
			},
		},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":                 "cascade",
			"command":              command,
			"exit_code":            nil,
			"timed_out":            false,
			"counts":               map[string]interface{}{"pass": 0, "warn": 0, "fail": 0, "error": 1},
			"failed_dep_id":        failedID,
			"failed_dep_run_id":    failedRunID,
			"failed_dep_verdict":   failedVerdict,
			"failed_dep_severity":  failedSeverity,
		},
	}
}
```

- [ ] **Step 5.4: Run test, verify pass**

```bash
go test ./lib/orchestrator/ -run TestBuildCascadeSignal -v
```

Expected: PASS.

- [ ] **Step 5.5: Verify cascade Signal validates against signal.json**

Add a second test to `cascade_test.go`:

```go
func TestBuildCascadeSignal_ValidatesAgainstSchema(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, err := schema.NewValidator(schemasDir)
	if err != nil {
		t.Fatal(err)
	}
	skipped := Sensor{
		ID: "e2e-tests",
		JSON: map[string]interface{}{
			"id":      "e2e-tests",
			"version": "0.1.0",
			"execution": map[string]interface{}{"command": "pnpm test"},
		},
	}
	failed := map[string]interface{}{
		"sensor_id": "start-postgres", "run_id": "r1",
		"verdict": "fail", "severity": "high",
	}
	sig := BuildCascadeSignal(skipped, failed)
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		t.Fatalf("cascade Signal failed signal.json validation: %v", err)
	}
}
```

Add the imports at the top of `cascade_test.go`:

```go
"github.com/iurykrieger/harness-framework/lib/schema"
"github.com/iurykrieger/harness-framework/lib/testfixtures"
```

Run:

```bash
go test ./lib/orchestrator/ -run TestBuildCascadeSignal -v
```

Expected: PASS for both tests.

- [ ] **Step 5.6: Commit**

```bash
git add lib/orchestrator/cascade.go lib/orchestrator/cascade_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): build cascade Signals for skipped dependents

When a dep fails, every transitively-dependent sensor must emit a
Signal so the caller sees a predictable count. The cascade envelope
populates all signal.json required fields plus failed-dep pointers
under top-level metadata (free-form per signal.json:124-127).
Validates against signal.json.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: lib/orchestrator/lifecycle.go — prepare → command → teardown

**Files:**
- Create: `lib/orchestrator/lifecycle.go`
- Test: `lib/orchestrator/lifecycle_test.go`

`RunOne` runs a single sensor's full lifecycle: prepare[] (fail-fast, silent), then command (delegates to existing pipeline), then teardown[] (best-effort, silent). All three phases fold per-step results into the aggregate's `metadata.lifecycle`.

- [ ] **Step 6.1: Write failing tests for RunOne happy path**

Create `lib/orchestrator/lifecycle_test.go`:

```go
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func TestRunOne_SimpleNoLifecycle(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	s := Sensor{ID: "smoke-comp", JSON: testfixtures.ValidSensorComputational()}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict=%v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if _, ok := md["lifecycle"]; ok {
		t.Fatal("metadata.lifecycle should be absent when prepare/teardown both empty")
	}
}

func TestRunOne_PrepareFailFast(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := testfixtures.ValidSensorComputational()
	exec := js["execution"].(map[string]interface{})
	exec["prepare"] = []interface{}{
		map[string]interface{}{"command": "false"},
		map[string]interface{}{"command": "echo should-not-run"},
	}
	exec["command"] = "echo should-not-run-either"
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if sig["verdict"] != "error" {
		t.Fatalf("expected verdict=error, got %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	lc := md["lifecycle"].(map[string]interface{})
	prep := lc["prepare"].([]interface{})
	if len(prep) != 1 {
		t.Fatalf("expected 1 prepare step (fail-fast), got %d", len(prep))
	}
	first := prep[0].(map[string]interface{})
	if first["verdict"] != "fail" {
		t.Errorf("first prepare verdict = %v", first["verdict"])
	}
	if !strings.Contains(out.String(), `"sensor_id":"smoke-comp"`) {
		t.Error("expected aggregate Signal on stdout")
	}
}

func TestRunOne_TeardownBestEffort(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := testfixtures.ValidSensorComputational()
	exec := js["execution"].(map[string]interface{})
	exec["command"] = "true"
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "false"},      // first fails
		map[string]interface{}{"command": "true"},       // second still runs
	}
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	// Teardown failure does NOT downgrade aggregate verdict.
	if sig["verdict"] != "pass" {
		t.Fatalf("aggregate verdict=%v, want pass (teardown failures are warn evidence only)", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	lc := md["lifecycle"].(map[string]interface{})
	td := lc["teardown"].([]interface{})
	if len(td) != 2 {
		t.Fatalf("expected 2 teardown steps, got %d (best-effort means all run)", len(td))
	}
	first := td[0].(map[string]interface{})
	second := td[1].(map[string]interface{})
	if first["verdict"] != "warn" {
		t.Errorf("first teardown verdict = %v, want warn", first["verdict"])
	}
	if second["verdict"] != "pass" {
		t.Errorf("second teardown verdict = %v, want pass", second["verdict"])
	}
}

func TestRunOne_TeardownRunsAfterCommandFail(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	js := testfixtures.ValidSensorComputational()
	exec := js["execution"].(map[string]interface{})
	exec["command"] = "false"
	exec["teardown"] = []interface{}{
		map[string]interface{}{"command": "true"},
	}
	s := Sensor{ID: js["id"].(string), JSON: js}

	var out, errBuf bytes.Buffer
	sig, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	md := sig["metadata"].(map[string]interface{})
	lc, ok := md["lifecycle"].(map[string]interface{})
	if !ok {
		t.Fatal("expected metadata.lifecycle to exist")
	}
	td := lc["teardown"].([]interface{})
	if len(td) != 1 || td[0].(map[string]interface{})["verdict"] != "pass" {
		t.Fatalf("teardown should still run after command fail; got %v", td)
	}
}

// The aggregate Signal emitted on stdout is valid JSON and the LAST line.
func TestRunOne_OutputIsValidJSON(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, _ := schema.NewValidator(schemasDir)
	s := Sensor{ID: "smoke-comp", JSON: testfixtures.ValidSensorComputational()}

	var out, errBuf bytes.Buffer
	if _, code := RunOne(context.Background(), s, schemasDir, v, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d", code)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	last := lines[len(lines)-1]
	var got map[string]interface{}
	if err := json.Unmarshal([]byte(last), &got); err != nil {
		t.Fatalf("last line is not valid JSON: %v", err)
	}
	md := got["metadata"].(map[string]interface{})
	if md["kind"] != "aggregate" {
		t.Errorf("last line metadata.kind = %v, want aggregate", md["kind"])
	}
}
```

- [ ] **Step 6.2: Run test, verify failure**

```bash
go test ./lib/orchestrator/ -run TestRunOne -v
```

Expected: FAIL — `undefined: RunOne`.

- [ ] **Step 6.3: Implement RunOne**

Create `lib/orchestrator/lifecycle.go`:

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

// RunOne executes a single sensor's full lifecycle:
//
//	1. prepare[] (fail-fast, silent — first failure aborts and skips command)
//	2. execution.command (existing streaming pipeline; emits individual JSONL Signals)
//	3. teardown[] (best-effort, silent — every step runs regardless)
//
// On exit, RunOne emits exactly one aggregate Signal as a JSONL line on
// stdout. Per-step lifecycle results are folded into metadata.lifecycle.
//
// Returns (signal, exitCode). exitCode is 0 unless schema validation
// fails (1) or input is malformed (2).
func RunOne(ctx context.Context, s Sensor, schemasDir string, v *schema.Validator, stdout, stderr io.Writer) (map[string]interface{}, int) {
	envelope, err := sensor.BuildEnvelope(s.JSON)
	if err != nil {
		fmt.Fprintln(stderr, "error: envelope:", err)
		return nil, 2
	}
	execMap, _ := s.JSON["execution"].(map[string]interface{})
	output, _ := s.JSON["output"].(string)

	timeoutMS := readTimeoutMS(s.JSON)

	// Phase 1: prepare (fail-fast).
	prepResults, prepFailed := runLifecyclePhase(ctx, execMap, "prepare", timeoutMS, true)

	var aggregateMD map[string]interface{}
	var aggVerdict, aggSeverity string
	var commandRun string
	var elapsedMS int

	if prepFailed {
		// Skip command. Build degraded aggregate.
		aggVerdict, aggSeverity = "error", "high"
		commandRun, _ = execMap["command"].(string)
	} else {
		// Phase 2: command (existing streaming pipeline).
		command, _ := execMap["command"].(string)
		longRunning, _ := execMap["long_running"].(bool)
		envExtra := readEnvMap(execMap)

		var patterns []signal.Pattern
		if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
			raw, _ := op["patterns"].([]interface{})
			ps, perr := signal.CompilePatterns(raw)
			if perr != nil {
				fmt.Fprintln(stderr, "error:", perr)
				return nil, 1
			}
			patterns = ps
		}

		res, _ := subprocess.StreamSubprocess(ctx, subprocess.StreamConfig{
			Command:   command,
			Env:       envExtra,
			TimeoutMS: timeoutMS,
			Patterns:  patterns,
			Envelope:  envelope,
			Validator: v,
			Stdout:    stdout,
			Stderr:    stderr,
		})

		ecMap, _ := execMap["exit_code_map"].([]interface{})
		exitVerd, exitSev := signal.MapExitCode(res.ExitCode, ecMap)
		streamVerd, streamSev := signal.MaxStreamVerdict(res.Individuals)
		agg := signal.Aggregate(signal.AggregateInput{
			ExitVerdict:    exitVerd,
			ExitSeverity:   exitSev,
			StreamVerdict:  streamVerd,
			StreamSeverity: streamSev,
			TimedOut:       res.TimedOut,
			LongRunning:    longRunning,
		})
		aggVerdict, aggSeverity = agg.Verdict, agg.Severity
		commandRun = command
		elapsedMS = res.ElapsedMS

		aggregateMD = map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": output,
			"command":     command,
			"exit_code":   res.ExitCode,
			"timed_out":   res.TimedOut,
			"counts":      signal.CountVerdicts(res.Individuals),
		}
		if longRunning {
			aggregateMD["long_running"] = true
		}
	}

	// Phase 3: teardown (best-effort, runs regardless of prepare/command outcome).
	tdResults, _ := runLifecyclePhase(ctx, execMap, "teardown", timeoutMS, false)

	if aggregateMD == nil {
		aggregateMD = map[string]interface{}{
			"kind":        "aggregate",
			"output_mode": output,
			"command":     commandRun,
			"exit_code":   nil,
			"timed_out":   false,
			"counts":      map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 1},
		}
	}
	if len(prepResults) > 0 || len(tdResults) > 0 {
		lc := map[string]interface{}{}
		if len(prepResults) > 0 {
			lc["prepare"] = prepResults
		}
		if len(tdResults) > 0 {
			lc["teardown"] = tdResults
		}
		aggregateMD["lifecycle"] = lc
	}

	finished := sensor.NowFn().Format("2006-01-02T15:04:05Z")
	sig := map[string]interface{}{
		"sensor_id":   envelope.SensorID,
		"version":     envelope.Version,
		"run_id":      envelope.RunID,
		"started_at":  envelope.StartedAt,
		"finished_at": finished,
		"verdict":     aggVerdict,
		"severity":    aggSeverity,
		"confidence":  1.0,
		"evidence":    buildLifecycleEvidence(prepResults, tdResults),
		"cost_actual": map[string]interface{}{"latency_ms": elapsedMS},
		"metadata":    aggregateMD,
	}

	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	_ = json.NewEncoder(stdout).Encode(sig)
	return sig, 0
}

// runLifecyclePhase walks execMap[phase] (prepare or teardown), runs each
// step via subprocess.RunStep, and returns a slice of result maps shaped
// for inclusion under metadata.lifecycle. On failFast=true, the first
// non-pass step short-circuits (returns prepFailed=true).
func runLifecyclePhase(ctx context.Context, execMap map[string]interface{}, phase string, defaultTimeoutMS int, failFast bool) ([]interface{}, bool) {
	steps, _ := execMap[phase].([]interface{})
	var out []interface{}
	for _, raw := range steps {
		step, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		cmd, _ := step["command"].(string)
		t := defaultTimeoutMS
		if v, ok := step["timeout_ms"]; ok {
			t = int(asNumber(v))
		}
		res, _ := subprocess.RunStep(ctx, subprocess.StepConfig{Command: cmd, TimeoutMS: t})
		ecMap, _ := step["exit_code_map"].([]interface{})
		verdict, severity := mapStepExitCode(res.ExitCode, ecMap, phase)
		entry := map[string]interface{}{
			"command":   cmd,
			"exit_code": res.ExitCode,
			"latency_ms": res.ElapsedMS,
			"timed_out": res.TimedOut,
			"verdict":   verdict,
			"severity":  severity,
		}
		if res.StderrExcerpt != "" {
			entry["stderr_excerpt"] = res.StderrExcerpt
		}
		out = append(out, entry)
		if failFast && verdict != "pass" {
			return out, true
		}
	}
	return out, false
}

// mapStepExitCode applies the step's exit_code_map (if any), or the
// default rule (0 → pass/info; non-zero → fail/high for prepare,
// warn/low for teardown).
func mapStepExitCode(code int, ecMap []interface{}, phase string) (string, string) {
	if len(ecMap) > 0 {
		v, s := signal.MapExitCode(code, ecMap)
		if v != "" {
			return v, s
		}
	}
	if code == 0 {
		return "pass", "info"
	}
	if phase == "teardown" {
		return "warn", "low"
	}
	return "fail", "high"
}

// buildLifecycleEvidence produces evidence[] entries for any non-pass
// lifecycle steps. Uses only signal.json's allowed evidence fields
// (rationale, excerpt).
func buildLifecycleEvidence(prep, td []interface{}) []interface{} {
	var out []interface{}
	for _, items := range [][]interface{}{prep, td} {
		for _, raw := range items {
			step, _ := raw.(map[string]interface{})
			verdict, _ := step["verdict"].(string)
			if verdict == "pass" {
				continue
			}
			cmd, _ := step["command"].(string)
			excerpt, _ := step["stderr_excerpt"].(string)
			rationale := fmt.Sprintf("lifecycle step %q produced verdict=%s (exit=%v)", cmd, verdict, step["exit_code"])
			ev := map[string]interface{}{"rationale": rationale}
			if excerpt != "" {
				ev["excerpt"] = excerpt
			}
			out = append(out, ev)
		}
	}
	return out
}

func readEnvMap(execMap map[string]interface{}) map[string]string {
	out := map[string]string{}
	if envObj, ok := execMap["env"].(map[string]interface{}); ok {
		for k, val := range envObj {
			out[k] = fmt.Sprintf("%v", val)
		}
	}
	return out
}

func readTimeoutMS(s map[string]interface{}) int {
	cost, _ := s["cost"].(map[string]interface{})
	if cost == nil {
		return 0
	}
	lat, _ := cost["latency"].(map[string]interface{})
	if lat == nil {
		return 0
	}
	return int(asNumber(lat["timeout_ms"]))
}

func asNumber(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
```

- [ ] **Step 6.4: Run test, verify pass**

```bash
go test ./lib/orchestrator/ -run TestRunOne -v
```

Expected: PASS for all five subtests.

- [ ] **Step 6.5: Commit**

```bash
git add lib/orchestrator/lifecycle.go lib/orchestrator/lifecycle_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): RunOne executes prepare → command → teardown

prepare[] is fail-fast and silent; first non-pass step aborts and
skips the main command but teardown still runs (finally semantics).
teardown[] is best-effort: every step runs regardless of failure;
teardown failures contribute warn evidence but do NOT downgrade the
aggregate verdict.

Per-step results fold into metadata.lifecycle.{prepare,teardown}.
Existing StreamSubprocess pipeline is reused unchanged for the main
command — no behavior change for sensors with empty prepare/teardown.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: lib/orchestrator/run.go — RunWithDeps entry point

**Files:**
- Create: `lib/orchestrator/run.go`
- Test: `lib/orchestrator/run_test.go`

`RunWithDeps` is the single entry point both `run-computational.go` and `run-inferential.go` will call. It loads the requested sensor, resolves its deps, and runs each sensor in topo order — emitting cascade Signals for dependents when an earlier sensor fails.

- [ ] **Step 7.1: Write failing test**

Create `lib/orchestrator/run_test.go`:

```go
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func writeSensorWithDeps(t *testing.T, dir, id string, depsOn []string, command string) {
	t.Helper()
	s := testfixtures.ValidSensorComputational()
	s["id"] = id
	if len(depsOn) > 0 {
		ds := []interface{}{}
		for _, d := range depsOn {
			ds = append(ds, d)
		}
		s["depends_on"] = ds
	}
	exec := s["execution"].(map[string]interface{})
	exec["command"] = command
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunWithDeps_ChainPasses(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "setup-a", nil, "true")
	writeSensorWithDeps(t, root, "use-a",  []string{"setup-a"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, "use-a.json"), schemasDir, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}

	lines := splitJSONL(out.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 aggregate Signals, got %d:\n%s", len(lines), out.String())
	}
	last := decode(t, lines[len(lines)-1])
	if last["sensor_id"] != "use-a" {
		t.Errorf("last sensor_id = %v, want use-a", last["sensor_id"])
	}
}

func TestRunWithDeps_CascadesOnDepFail(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "setup-fail", nil, "false")
	writeSensorWithDeps(t, root, "use-it",     []string{"setup-fail"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, "use-it.json"), schemasDir, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}

	lines := splitJSONL(out.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 Signals (dep + cascade), got %d", len(lines))
	}
	depSig := decode(t, lines[0])
	cascade := decode(t, lines[1])
	if depSig["verdict"] != "fail" {
		t.Errorf("dep verdict = %v, want fail", depSig["verdict"])
	}
	if cascade["verdict"] != "error" {
		t.Errorf("cascade verdict = %v, want error", cascade["verdict"])
	}
	md := cascade["metadata"].(map[string]interface{})
	if md["kind"] != "cascade" {
		t.Errorf("cascade metadata.kind = %v", md["kind"])
	}
}

func TestRunWithDeps_CycleAborts(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	root := t.TempDir()
	writeSensorWithDeps(t, root, "a", []string{"b"}, "true")
	writeSensorWithDeps(t, root, "b", []string{"a"}, "true")

	var out, errBuf bytes.Buffer
	code := RunWithDeps(context.Background(), filepath.Join(root, "a.json"), schemasDir, &out, &errBuf)
	if code != 1 {
		t.Fatalf("expected exit=1 for cycle, got %d", code)
	}
	if !strings.Contains(errBuf.String(), "cycle") {
		t.Errorf("expected stderr to mention cycle, got %q", errBuf.String())
	}
}

func splitJSONL(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func decode(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", s, err)
	}
	return m
}
```

- [ ] **Step 7.2: Run test, verify failure**

```bash
go test ./lib/orchestrator/ -run TestRunWithDeps -v
```

Expected: FAIL — `undefined: RunWithDeps`.

- [ ] **Step 7.3: Implement RunWithDeps**

Create `lib/orchestrator/run.go`:

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// RunWithDeps loads the sensor at sensorPath, resolves its depends_on
// transitively, runs each sensor in topo order through RunOne, and emits
// cascade Signals for any dependent skipped because an earlier sensor
// failed. The aggregate Signal of the requested sensor is the LAST line
// on stdout (contract preserved from the prior streaming-sensors design).
//
// Exit codes:
//   0 — every requested-or-implied sensor produced a Signal (some may be
//        cascade or fail/error; emission is what matters for exit 0).
//   1 — DAG resolution failed (cycle, missing dep, malformed sensor JSON).
//   2 — schema/io error opening the sensor or schemas.
func RunWithDeps(ctx context.Context, sensorPath, schemasDir string, stdout, stderr io.Writer) int {
	abs, err := filepath.Abs(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: abs path:", err)
		return 2
	}
	root := filepath.Dir(abs)

	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return code
	}

	rootID := stripJSONExt(filepath.Base(abs))
	order, err := Resolve(rootID, root)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	// Validate every sensor in the graph against schemas/sensor.json before
	// running anything. Dependencies that fail validation should abort
	// the run, not be discovered mid-pipeline.
	for _, s := range order {
		if err := v.Validate(schema.TargetSensor, s.JSON); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
	}

	signals := map[string]map[string]interface{}{}
	failed := map[string]map[string]interface{}{}

	for _, s := range order {
		if blocker := firstFailedDep(s, signals); blocker != nil {
			cascade := BuildCascadeSignal(s, blocker)
			if err := v.Validate(schema.TargetSignal, cascade); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				return 1
			}
			_ = json.NewEncoder(stdout).Encode(cascade)
			signals[s.ID] = cascade
			failed[s.ID] = cascade
			continue
		}
		sig, sigCode := RunOne(ctx, s, schemasDir, v, stdout, stderr)
		if sigCode != 0 {
			return sigCode
		}
		signals[s.ID] = sig
		verdict, _ := sig["verdict"].(string)
		if verdict == "fail" || verdict == "error" {
			failed[s.ID] = sig
		}
	}
	return 0
}

// firstFailedDep returns the Signal of the first dep id (in declaration
// order) of s that has a fail/error verdict, or nil when none failed.
func firstFailedDep(s Sensor, signals map[string]map[string]interface{}) map[string]interface{} {
	depIDs := readDepsArray(s.JSON)
	for _, d := range depIDs {
		sig := signals[d]
		if sig == nil {
			continue
		}
		verdict, _ := sig["verdict"].(string)
		if verdict == "fail" || verdict == "error" {
			return sig
		}
	}
	return nil
}

func stripJSONExt(name string) string {
	if len(name) > 5 && name[len(name)-5:] == ".json" {
		return name[:len(name)-5]
	}
	return name
}
```

- [ ] **Step 7.4: Run test, verify pass**

```bash
go test ./lib/orchestrator/ -run TestRunWithDeps -v
```

Expected: PASS for ChainPasses, CascadesOnDepFail, CycleAborts.

- [ ] **Step 7.5: Run full orchestrator test suite to confirm nothing regressed**

```bash
go test ./lib/orchestrator/...
```

Expected: ALL PASS (DAG, lifecycle, cascade, run).

- [ ] **Step 7.6: Commit**

```bash
git add lib/orchestrator/run.go lib/orchestrator/run_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): RunWithDeps glues DAG, lifecycle, and cascade

Single entry point for both runner scripts. Resolves the requested
sensor's dependency closure, validates every sensor in the graph
against schemas/sensor.json, then walks topo-sorted order: each
sensor either runs through RunOne or — if any of its deps already
failed — emits a cascade Signal pointing at the failure.

The aggregate Signal of the requested sensor remains the LAST
JSONL line on stdout, preserving the contract from the prior
streaming-sensors design.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Wire run-computational.go to orchestrator

**Files:**
- Modify: `skills/run-sensor/scripts/run-computational.go`
- Modify: `skills/run-sensor/scripts/run-computational_test.go`

The script becomes a thin CLI shell — argv parse + delegate to `orchestrator.RunWithDeps`.

- [ ] **Step 8.1: Read existing test to understand assertions**

```bash
go test -tags=run_computational ./skills/run-sensor/scripts/ -v
```

Expected: ALL PASS (current state).

- [ ] **Step 8.2: Replace `run-computational.go` with thin orchestrator shell**

Rewrite `skills/run-sensor/scripts/run-computational.go`:

```go
//go:build run_computational

// Command run-computational runs a streaming computational sensor end-to-end,
// resolving and executing its depends_on graph in topological order.
//
// Usage:
//
//	go run -tags=run_computational ./skills/run-sensor/scripts <sensor-path>
//
// Stdout is JSONL: every dep's aggregate Signal first, then the requested
// sensor's individual Signals (one per matched output line), terminated by
// the requested sensor's aggregate Signal as the LAST line. Exit codes:
// 0 ok (Signals printed), 1 schema/DAG failure, 2 usage or I/O error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run-computational", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemasDir string
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: run-computational [--schemas-dir=DIR] <sensor-path>")
		return 2
	}

	cwd, _ := os.Getwd()
	abs, err := sensor.ResolveSensorPath(rest[0], cwd)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return 2
	}
	return orchestrator.RunWithDeps(context.Background(), abs, schemasDir, stdout, stderr)
}
```

- [ ] **Step 8.3: Update run-computational_test.go assertions**

Existing tests assume the script handles type=computational, missing-env detection, etc. directly. Now that's all in the orchestrator. The thin shell only needs to pass through `RunWithDeps`. Keep tests focused on:

1. Usage errors (exit 2)
2. Round-trip: a single computational sensor with no deps produces a Signal
3. A sensor with one dep produces 2 Signals on stdout

Update `skills/run-sensor/scripts/run-computational_test.go` (replace the existing file content):

```go
//go:build run_computational

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func writeSensor(t *testing.T, dir, id string, mut func(map[string]interface{})) string {
	t.Helper()
	s := testfixtures.ValidSensorComputational()
	s["id"] = id
	if mut != nil {
		mut(s)
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_NoDeps(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	dir := t.TempDir()
	path := writeSensor(t, dir, "noop", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, path}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 aggregate Signal, got %d:\n%s", len(lines), out.String())
	}
}

func TestRun_WithDep(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	dir := t.TempDir()
	writeSensor(t, dir, "dep", func(s map[string]interface{}) {
		s["execution"].(map[string]interface{})["command"] = "true"
	})
	mainPath := writeSensor(t, dir, "main", func(s map[string]interface{}) {
		s["depends_on"] = []interface{}{"dep"}
		s["execution"].(map[string]interface{})["command"] = "true"
	})

	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", schemasDir, mainPath}, &out, &errBuf)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 Signals (dep + main), got %d", len(lines))
	}
	var lastSig map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &lastSig); err != nil {
		t.Fatal(err)
	}
	if lastSig["sensor_id"] != "main" {
		t.Errorf("last sensor_id = %v, want main", lastSig["sensor_id"])
	}
}

func TestRun_UsageError(t *testing.T) {
	var out, errBuf bytes.Buffer
	if code := run([]string{}, &out, &errBuf); code != 2 {
		t.Fatalf("expected 2 (no args), got %d", code)
	}
	if code := run([]string{"a", "b"}, &out, &errBuf); code != 2 {
		t.Fatalf("expected 2 (extra args), got %d", code)
	}
}

func TestRun_DraftFileMissing(t *testing.T) {
	var out, errBuf bytes.Buffer
	code := run([]string{"--schemas-dir", testfixtures.RepoSchemasDir(t), "/nonexistent/x.json"}, &out, &errBuf)
	if code != 2 {
		t.Fatalf("expected 2 when sensor missing, got %d", code)
	}
}
```

- [ ] **Step 8.4: Run script tests, verify pass**

```bash
go test -tags=run_computational ./skills/run-sensor/scripts/ -v
```

Expected: ALL PASS.

- [ ] **Step 8.5: Smoke-test against a real existing sensor**

```bash
go run -tags=run_computational ./skills/run-sensor/scripts sensors/lint-gofmt.json | tail -n 1 | jq '{verdict, severity, sensor_id}'
```

Expected: real Signal output, verdict reflects current repo state.

- [ ] **Step 8.6: Commit**

```bash
git add skills/run-sensor/scripts/run-computational.go skills/run-sensor/scripts/run-computational_test.go
git commit -m "$(cat <<'EOF'
refactor(run-computational): delegate to orchestrator.RunWithDeps

The script becomes a thin CLI shell — argv parse, path resolve,
delegate. All schema validation, env checking, command execution,
and aggregate construction now lives in lib/orchestrator. Sensors
with no deps and no prepare/teardown produce bit-identical output
to the previous version.

Tests updated for the new JSONL stream shape (deps emit Signals
before the requested sensor's aggregate).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Wire run-inferential.go to orchestrator

**Files:**
- Modify: `skills/run-sensor/scripts/run-inferential.go`
- Modify: `skills/run-sensor/scripts/run-inferential_test.go`

Same shape as Task 8, but inferential preserves the `--slot` flags and the `HARNESS_AGGREGATE_CONFIDENCE` calibration shimming. Those features stay in this script (they're inferential-specific). The orchestrator runs the prepare/teardown/cascade pipeline; inferential post-processes only the requested sensor's aggregate confidence downgrade.

- [ ] **Step 9.1: Read existing run-inferential.go to understand inferential-specific logic**

```bash
wc -l skills/run-sensor/scripts/run-inferential.go
head -200 skills/run-sensor/scripts/run-inferential.go
```

Note the parts that are inferential-specific: `--slot` parsing, `HARNESS_PROMPT` env injection, `HARNESS_AGGREGATE_CONFIDENCE` line filtering, calibration `fail → warn` downgrade.

- [ ] **Step 9.2: Add an inferential post-processing hook to the orchestrator**

The cleanest way: keep all inferential logic in `run-inferential.go`, but expose `orchestrator.RunOne` already-public so the script can call it directly for sensors with `kind=observation/assertion` deps and itself for the inferential aggregate. Since `RunWithDeps` already iterates topologically and the calibration only affects the FINAL aggregate of the requested sensor, do this:

1. Add an optional `PostProcess` hook to `RunWithDeps` so callers can mutate the requested sensor's aggregate before validation/emission.

Modify `lib/orchestrator/run.go`:

```go
// RunOptions configures RunWithDeps.
type RunOptions struct {
	// PostProcessRoot, if non-nil, is called with the requested sensor's
	// aggregate Signal map BEFORE schema validation. Callers may mutate
	// the map in place (e.g. inferential confidence downgrade).
	PostProcessRoot func(rootSensor Sensor, signal map[string]interface{}) error
}

// RunWithDepsOpts is RunWithDeps + options. RunWithDeps remains as a
// zero-options helper for callers that don't need a hook.
func RunWithDepsOpts(ctx context.Context, sensorPath, schemasDir string, stdout, stderr io.Writer, opts RunOptions) int {
	// (existing body of RunWithDeps lives here; replace direct emission of
	// the rootSensor's aggregate with a hook call before encoding)
}

func RunWithDeps(ctx context.Context, sensorPath, schemasDir string, stdout, stderr io.Writer) int {
	return RunWithDepsOpts(ctx, sensorPath, schemasDir, stdout, stderr, RunOptions{})
}
```

To keep the per-sensor stdout streaming working, the simplest implementation is: for the requested sensor, RunOne writes to a buffered writer, then PostProcessRoot is applied to the parsed Signal, then the modified Signal replaces the buffered last line, and the buffer is flushed to stdout.

Actual implementation: `RunOne` already returns the Signal map. Modify the loop in `RunWithDeps` so for the LAST sensor in topo order (the requested one), it captures the Signal, lets the hook mutate it, validates, and emits via `stdout`. Earlier sensors are already streamed by `RunOne`. The trick: `RunOne` ALSO emits to stdout itself. We need either:

- **Option A:** `RunOne` returns the Signal but does NOT write to stdout itself; the orchestrator does the writing.
- **Option B:** For inferential, the requested sensor swaps the writer to a tee/buffer.

Option A is cleaner. Refactor `RunOne` to NOT call `json.NewEncoder(stdout).Encode(sig)` itself; the orchestrator does it. Patterns from individuals are still streamed via `subprocess.StreamSubprocess` (which writes to stdout directly during streaming).

Update `lib/orchestrator/lifecycle.go` to remove the final `_ = json.NewEncoder(stdout).Encode(sig)` line. Update tests in Task 6 step 6.1 if any rely on RunOne emitting; they do (e.g. `TestRunOne_OutputIsValidJSON` reads from `out.String()`). Adjust by writing the aggregate inside the test caller, not RunOne.

Or: add a boolean `EmitAggregate` flag to `RunOne` (default true). Defaults preserve backward compat; orchestrator calls with `false` for the rootSensor when a PostProcess is set.

For simplicity, and given this is the FIRST consumer of orchestrator, do this:

**Simplification: skip the post-process hook for v1.** Keep run-inferential as-is (its own pipeline) AND add a thin adapter: when run-inferential's sensor has `depends_on`, it also delegates to orchestrator BUT only for resolving and running the deps; the inferential sensor's own command runs via run-inferential's existing pipeline (with HARNESS_PROMPT, calibration, etc.).

Concretely:

Modify `skills/run-sensor/scripts/run-inferential.go`:

```go
// In run(), after path resolution and schema load:
//
//   1. Build the orchestrator.Resolve graph from sensorPath.
//   2. Run all sensors EXCEPT the last (requested) via orchestrator.RunOne with a stdout writer.
//   3. Run the requested sensor via the existing inferential pipeline.
```

Pseudo-code addition (insert near the top of run() in run-inferential.go after path resolution):

```go
import "github.com/iurykrieger/harness-framework/lib/orchestrator"

// ... existing setup ...

// Resolve the dependency closure. The last entry is the requested sensor.
order, err := orchestrator.Resolve(stripJSONExt(filepath.Base(sensorAbsPath)), filepath.Dir(sensorAbsPath))
if err != nil {
	fmt.Fprintln(stderr, "error:", err)
	return 1
}

signals := map[string]map[string]interface{}{}
for i, s := range order[:len(order)-1] {
	_ = i
	if blocker := orchestrator.FirstFailedDep(s, signals); blocker != nil {
		cascade := orchestrator.BuildCascadeSignal(s, blocker)
		_ = json.NewEncoder(stdout).Encode(cascade)
		signals[s.ID] = cascade
		continue
	}
	sig, code := orchestrator.RunOne(ctx, s, schemasDir, v, stdout, stderr)
	if code != 0 {
		return code
	}
	signals[s.ID] = sig
}

// If a dep of the requested sensor failed, emit cascade for it and return.
requested := order[len(order)-1]
if blocker := orchestrator.FirstFailedDep(requested, signals); blocker != nil {
	cascade := orchestrator.BuildCascadeSignal(requested, blocker)
	_ = json.NewEncoder(stdout).Encode(cascade)
	return 0
}

// ... existing inferential pipeline runs the requested sensor's command ...
```

`FirstFailedDep` is currently unexported (`firstFailedDep`). Export it: rename in `lib/orchestrator/run.go` from `firstFailedDep` to `FirstFailedDep`. Same for `stripJSONExt` if used outside.

Implementation steps:

1. In `lib/orchestrator/run.go`, rename `firstFailedDep` → `FirstFailedDep`, `stripJSONExt` → `StripJSONExt`.
2. Update `lib/orchestrator/run_test.go` to use exported names if it referenced them (it doesn't currently; skip).
3. Modify `run-inferential.go` per the snippet above.

- [ ] **Step 9.3: Apply the orchestrator function exports**

```bash
sed -i.bak 's/firstFailedDep/FirstFailedDep/g; s/stripJSONExt/StripJSONExt/g' lib/orchestrator/run.go
rm lib/orchestrator/run.go.bak
go build ./lib/orchestrator/...
go test ./lib/orchestrator/...
```

Expected: build clean, tests pass.

- [ ] **Step 9.4: Modify run-inferential.go to use orchestrator for deps**

In `skills/run-sensor/scripts/run-inferential.go`, after the existing `LoadAndValidateSensor` call, BEFORE the inferential pipeline, insert dep resolution:

```go
// Resolve depends_on graph. Run every dep via orchestrator.RunOne;
// the requested sensor itself goes through the inferential pipeline below.
sensorAbsPath, _ := sensor.ResolveSensorPath(rest[0], func() string { d, _ := os.Getwd(); return d }())
sensorRoot := filepath.Dir(sensorAbsPath)
rootID := orchestrator.StripJSONExt(filepath.Base(sensorAbsPath))

order, err := orchestrator.Resolve(rootID, sensorRoot)
if err != nil {
	fmt.Fprintln(stderr, "error:", err)
	return 1
}

depSignals := map[string]map[string]interface{}{}
for _, dep := range order[:len(order)-1] {
	if err := v.Validate(schema.TargetSensor, dep.JSON); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return 1
	}
	if blocker := orchestrator.FirstFailedDep(dep, depSignals); blocker != nil {
		cascade := orchestrator.BuildCascadeSignal(dep, blocker)
		if err := v.Validate(schema.TargetSignal, cascade); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(cascade)
		depSignals[dep.ID] = cascade
		continue
	}
	sig, code := orchestrator.RunOne(context.Background(), dep, schemasDir, v, stdout, stderr)
	if code != 0 {
		return code
	}
	depSignals[dep.ID] = sig
}

// If any dep of the requested sensor failed, cascade and stop.
requested := order[len(order)-1]
if blocker := orchestrator.FirstFailedDep(requested, depSignals); blocker != nil {
	cascade := orchestrator.BuildCascadeSignal(requested, blocker)
	if err := v.Validate(schema.TargetSignal, cascade); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(cascade)
	return 0
}

// (continue with the existing inferential pipeline below)
```

Add the imports:

```go
"context"
"path/filepath"

"github.com/iurykrieger/harness-framework/lib/orchestrator"
```

- [ ] **Step 9.5: Update run-inferential_test.go**

The existing test file likely round-trips a single inferential sensor. Add a test for one-dep:

Append to `skills/run-sensor/scripts/run-inferential_test.go`:

```go
func TestRun_InferentialWithComputationalDep(t *testing.T) {
	schemasDir := testfixtures.RepoSchemasDir(t)
	dir := t.TempDir()

	// Setup dep: a kind=setup computational sensor.
	depJSON := testfixtures.ValidSensorSetup()
	depJSON["id"] = "setup-x"
	depExec := depJSON["execution"].(map[string]interface{})
	depExec["command"] = "true"
	depBytes, _ := json.MarshalIndent(depJSON, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "setup-x.json"), depBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	// Inferential requested sensor.
	infJSON := testfixtures.ValidSensorInferential()
	infJSON["id"] = "inf-with-dep"
	infJSON["depends_on"] = []interface{}{"setup-x"}
	infExec := infJSON["execution"].(map[string]interface{})
	infExec["command"] = "echo '{\"sensor_id\":\"inf-with-dep\",\"version\":\"0.1.0\",\"run_id\":\"r\",\"started_at\":\"2026-05-08T00:00:00Z\",\"finished_at\":\"2026-05-08T00:00:01Z\",\"verdict\":\"pass\",\"severity\":\"info\",\"confidence\":0.9,\"evidence\":[],\"cost_actual\":{\"latency_ms\":100}}'"
	infBytes, _ := json.MarshalIndent(infJSON, "", "  ")
	infPath := filepath.Join(dir, "inf-with-dep.json")
	if err := os.WriteFile(infPath, infBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errBuf bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, infPath}, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 Signals (dep + inf aggregate), got %d:\n%s", len(lines), out.String())
	}
}
```

(Add necessary imports at the top of the file.)

- [ ] **Step 9.6: Run inferential tests**

```bash
go test -tags=run_inferential ./skills/run-sensor/scripts/ -v
```

Expected: ALL PASS.

- [ ] **Step 9.7: Run full suite**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
```

Expected: ALL PASS.

- [ ] **Step 9.8: Commit**

```bash
git add skills/run-sensor/scripts/run-inferential.go skills/run-sensor/scripts/run-inferential_test.go lib/orchestrator/run.go
git commit -m "$(cat <<'EOF'
refactor(run-inferential): use orchestrator for dep resolution

Inferential keeps its own command pipeline (HARNESS_PROMPT injection,
calibration confidence downgrade, --slot flags) but defers depends_on
resolution to lib/orchestrator. Deps run via orchestrator.RunOne in
topo order; cascade Signals fire when a dep of the inferential sensor
fails. The inferential sensor's aggregate remains the LAST JSONL line.

Exports orchestrator.FirstFailedDep and orchestrator.StripJSONExt
for the runner-script consumer.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: Update SKILL.md and CLAUDE.md docs

**Files:**
- Modify: `skills/detect-sensors/SKILL.md`
- Modify: `skills/run-sensor/SKILL.md`
- Modify: `CLAUDE.md` (Architecture section, NOT Project rules)

- [ ] **Step 10.1: Update `skills/detect-sensors/SKILL.md`**

Add a new section after section 1 (between "Read the schema first" and "Inspect the project"). Title: `### 1.5 Classify each sensor`. Body:

```markdown
### 1.5 Classify each sensor: kind = observation | assertion | setup

Every sensor MUST declare `kind` (top-level, required). Pick by purpose:

- **observation** — observes behavior with no fixed expectation. Verdict describes the *health of the observation*, not pass/fail of an assertion. Examples: `run-project-nest`, `fetch-logs-cloudrun`, `fetch-metrics-datadog`, `trace-request`, `watch-build`, `tail-logs-local`. Naming convention: `run-*`, `watch-*`, `fetch-*`, `trace-*`, `tail-*`.
- **assertion** — checks against an expectation. Verdict pass/fail is semantic. Examples: `lint-eslint`, `unit-test-vitest`, `e2e-playwright`, `type-check-tsc`, `build-vite`, `schema-validate-json`, `validate-plugin-manifest`. Naming convention: `lint-*`, `build-*`, `unit-test-*`, `integration-test-*`, `e2e-*`, `validate-*`, `schema-*`.
- **setup** — idempotent auxiliary sensor that makes a precondition true. Typically referenced by other sensors via `depends_on`. Examples: `start-postgres`, `setup-env-from-example`, `install-deps-pnpm`, `login-gcloud`, `seed-db`, `provision-tunnel`. Naming convention: `start-*`, `setup-*`, `install-*`, `seed-*`, `login-*`, `provision-*`. Setup sensors MUST be idempotent (re-running with the same input is a no-op when the world is already in the desired state); document the strategy in `description` (`"test -f .env || cp .env.example .env"`, `"docker compose up -d postgres"` is idempotent by default, etc.).

Inferential setup sensors are technically allowed but **discouraged**: setup operations should be deterministic and idempotent; LLM-driven setup is neither. Do not emit `kind: "setup"` paired with `type: "inferential"`.
```

Also add a new section after section 4 (after "Draft each sensor"). Title: `### 4.5 Authoring lifecycle phases (prepare / teardown)`. Body:

```markdown
### 4.5 Authoring lifecycle phases (prepare / teardown)

A sensor's `execution` ships three phases: `prepare[]`, `command` (the observed one), and `teardown[]`. Use them to keep the sensor self-contained when its setup is specific to it (vs reusable across many sensors → use a setup sensor + `depends_on`).

- **prepare[]** — silent, fail-fast. Each item: `{ command, timeout_ms?, exit_code_map? }`. The first non-pass step aborts and skips the main command (but teardown still runs). Use for: generating intermediate artifacts (`pnpm prisma generate`, `make protos`), populating local config (`test -f .env || cp .env.example .env`), running pre-build steps that aren't worth a separate sensor.
- **teardown[]** — silent, best-effort. Same item shape. Every step runs regardless of prepare/command outcome (finally semantics). Use for: dropping local DB after E2E (`pnpm prisma migrate reset --force --skip-seed`), stopping containers, removing temp files. Teardown failures contribute warn evidence but do NOT downgrade the sensor's aggregate verdict — the command is the source of truth.

**When to use prepare vs a setup sensor with depends_on:**
- Reusable across multiple sensors → setup sensor (`depends_on: ["start-postgres"]`)
- Specific to this sensor only → `prepare[]`

Example (E2E sensor with full lifecycle):

```jsonc
{
  "id": "e2e-tests",
  "kind": "assertion",
  "depends_on": ["start-postgres", "setup-env-from-example"],
  "execution": {
    "prepare": [
      { "command": "pnpm prisma migrate deploy", "timeout_ms": 30000 },
      { "command": "pnpm prisma db seed",        "timeout_ms": 15000 }
    ],
    "command": "pnpm playwright test",
    "exit_code_map": [...],
    "output_parsing": { "patterns": [...] },
    "teardown": [
      { "command": "pnpm prisma migrate reset --force --skip-seed", "timeout_ms": 15000 },
      { "command": "docker compose stop postgres",                  "timeout_ms": 10000 }
    ]
  }
}
```
```

- [ ] **Step 10.2: Update `skills/run-sensor/SKILL.md`**

Read the existing skill briefly:

```bash
head -80 skills/run-sensor/SKILL.md
```

Add a new section near the top (right after the existing intro / before the existing "How to run" section):

```markdown
## Dependency resolution

When a sensor declares `depends_on: ["setup-x", "setup-y"]`, the runner resolves the transitive closure, sorts topologically (deps first), and runs each sensor's full lifecycle (prepare → command → teardown) before the requested sensor starts. Cycles (including self-loops `A → A`) are detected and abort with exit 1. Missing deps (referenced id has no file under `sensors/`) also abort with exit 1.

The JSONL stream on stdout for `/run-sensor X` (where X has deps D1, D2) looks like:

```
{aggregate Signal of D1}
{aggregate Signal of D2}
{individual Signal 1 of X}    ← only when X has output: stream
{individual Signal 2 of X}
...
{aggregate Signal of X}        ← LAST line, contract preserved
```

Callers using `tail -n 1 | jq` continue to see exactly the requested sensor's aggregate. Callers persisting all lines see deps' Signals interleaved.

## Cascade behavior

When a dep produces verdict=fail or verdict=error, every transitively-dependent sensor is **skipped** and emits a "cascade" Signal (verdict=error, severity=high) pointing at the failed dep:

- `metadata.kind = "cascade"`
- `metadata.failed_dep_id`, `metadata.failed_dep_run_id`, `metadata.failed_dep_verdict`, `metadata.failed_dep_severity`
- `evidence[0].rationale` describes which dep failed and where to find its Signal.

The skipped sensor never runs its `command` or its prepare/teardown — only the cascade Signal is emitted.
```

- [ ] **Step 10.3: Update `CLAUDE.md` Architecture section**

Read the current Architecture section:

```bash
sed -n '/^## Architecture/,/^## Build/p' CLAUDE.md
```

Append to the Architecture section (before the "## Build, validate, test" header) a new subsection:

```markdown
### Dependencies and lifecycle

A sensor's top-level `kind` is one of `observation`, `assertion`, `setup`. The first two are regulatory; the third is auxiliary (idempotent, makes a precondition true: `start-postgres`, `setup-env-from-example`, `install-deps-pnpm`).

A sensor's `depends_on: [<id>...]` declares ids that must run and pass before it. The runner resolves the transitive closure topologically (deps first, requested sensor last), runs each sensor's full lifecycle (prepare → command → teardown), and propagates failures: dependents of a failed sensor never run and emit "cascade" Signals (`metadata.kind = "cascade"`) instead.

Lifecycle phases live under `execution`:

- `prepare[]` — silent, fail-fast (first non-pass step aborts and skips command, but teardown still runs). Use for sensor-specific setup that isn't worth a reusable setup sensor.
- `command` — the observed step (existing streaming pipeline; emits individual JSONL Signals for matched output lines).
- `teardown[]` — silent, best-effort, finally semantics. Runs regardless of prepare/command outcome. Per-step failures contribute warn evidence but do NOT downgrade the aggregate verdict.

Per-step lifecycle results fold into the aggregate Signal under `metadata.lifecycle.{prepare,teardown}` (free-form per signal.json). The aggregate Signal of the requested sensor remains the LAST JSONL line on stdout — deps' aggregates appear earlier in the stream.

The orchestrator lives in `lib/orchestrator/` (DAG resolution + lifecycle execution + cascade construction) and is reused by both `run-computational` and `run-inferential` runner scripts.
```

- [ ] **Step 10.4: Verify markdown is well-formed**

```bash
markdownlint CLAUDE.md skills/detect-sensors/SKILL.md skills/run-sensor/SKILL.md 2>/dev/null || true
grep -c '^## ' CLAUDE.md
grep -c '^## ' skills/detect-sensors/SKILL.md
grep -c '^## ' skills/run-sensor/SKILL.md
```

(Linter optional; the grep just confirms headers parse.)

- [ ] **Step 10.5: Commit**

```bash
git add CLAUDE.md skills/detect-sensors/SKILL.md skills/run-sensor/SKILL.md
git commit -m "$(cat <<'EOF'
docs: document kind taxonomy, depends_on, and lifecycle phases

- skills/detect-sensors/SKILL.md: new section 1.5 (kind classification
  with naming conventions) and 4.5 (prepare/teardown authoring guide
  with the prepare-vs-setup-sensor decision).
- skills/run-sensor/SKILL.md: new sections describing the dependency
  resolution stream format and cascade behavior on dep failure.
- CLAUDE.md (Architecture): adds vocabulary for kind, lifecycle
  phases, and the orchestrator's responsibilities.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: End-to-end smoke (golden fixture sensor)

**Files:**
- Create: `sensors/run-with-deps-smoke.json` — a small computational sensor exercising real deps
- Create: `sensors/setup-touch-file.json` — a setup sensor it depends on
- Create: `sensors/fixtures/run-with-deps-smoke/clean.txt` — fixture for golden_cases

This task validates the entire pipeline end-to-end against the real schema, real validator, real orchestrator.

- [ ] **Step 11.1: Author setup sensor**

Create `sensors/setup-touch-file.json`:

```json
{
  "id": "setup-touch-file",
  "version": "0.1.0",
  "name": "Setup: touch sentinel file",
  "description": "Idempotent setup that creates /tmp/harness-smoke-sentinel; downstream sensors verify it exists. Auto-detected via /detect-sensors as a smoke fixture for the dependency runner.",
  "kind": "setup",
  "type": "computational",
  "regulation": "behaviour",
  "phase": "on-demand",
  "determinism": "high",
  "output": "single",
  "cost": {
    "class": "cheap",
    "latency": { "p50_ms": 50, "p95_ms": 200, "timeout_ms": 5000 },
    "compute": { "cpu": "low", "memory_mb": 32 }
  },
  "triggers": [{ "on": "agent-request" }],
  "execution": {
    "command": "touch /tmp/harness-smoke-sentinel",
    "exit_code_map": [
      { "exit_code": 0, "verdict": "pass", "severity": "info" },
      { "exit_code": "*", "verdict": "fail", "severity": "high" }
    ]
  },
  "verification": {
    "golden_cases": [
      { "fixture": "sensors/fixtures/setup-touch-file/clean.txt", "expected_verdict": "pass", "expected_severity": "info" }
    ]
  }
}
```

- [ ] **Step 11.2: Author observation sensor with deps + lifecycle**

Create `sensors/run-with-deps-smoke.json`:

```json
{
  "id": "run-with-deps-smoke",
  "version": "0.1.0",
  "name": "Smoke: run-project-style sensor with deps and teardown",
  "description": "Smoke test for the orchestrator: depends on setup-touch-file, runs prepare to verify the sentinel exists, runs the main command (which echoes a known string), then teardown removes the sentinel. Auto-detected via /detect-sensors.",
  "kind": "observation",
  "type": "computational",
  "regulation": "behaviour",
  "phase": "on-demand",
  "determinism": "high",
  "output": "stream",
  "depends_on": ["setup-touch-file"],
  "cost": {
    "class": "cheap",
    "latency": { "p50_ms": 100, "p95_ms": 500, "timeout_ms": 5000 },
    "compute": { "cpu": "low", "memory_mb": 64 }
  },
  "triggers": [{ "on": "manual" }],
  "execution": {
    "prepare": [
      { "command": "test -f /tmp/harness-smoke-sentinel", "timeout_ms": 1000 }
    ],
    "command": "echo SMOKE_OK",
    "exit_code_map": [
      { "exit_code": 0, "verdict": "pass", "severity": "info" },
      { "exit_code": "*", "verdict": "fail", "severity": "high" }
    ],
    "output_parsing": {
      "patterns": [
        { "regex": "SMOKE_OK", "verdict": "pass", "severity": "info" }
      ]
    },
    "teardown": [
      { "command": "rm -f /tmp/harness-smoke-sentinel", "timeout_ms": 1000 }
    ]
  },
  "verification": {
    "golden_cases": [
      { "fixture": "sensors/fixtures/run-with-deps-smoke/clean.txt", "expected_verdict": "pass", "expected_severity": "info" }
    ]
  }
}
```

- [ ] **Step 11.3: Author fixture files**

```bash
mkdir -p sensors/fixtures/setup-touch-file sensors/fixtures/run-with-deps-smoke
echo "SMOKE_OK" > sensors/fixtures/run-with-deps-smoke/clean.txt
echo "" > sensors/fixtures/setup-touch-file/clean.txt
```

- [ ] **Step 11.4: Persist sensors via the validator**

```bash
go run ./skills/detect-sensors/scripts --out=sensors sensors/setup-touch-file.json
go run ./skills/detect-sensors/scripts --out=sensors sensors/run-with-deps-smoke.json
```

Expected: each emits an absolute path on stdout. Files validate.

- [ ] **Step 11.5: Run the smoke sensor end-to-end**

```bash
rm -f /tmp/harness-smoke-sentinel
go run -tags=run_computational ./skills/run-sensor/scripts sensors/run-with-deps-smoke.json | jq -c '{sensor_id, verdict, severity}'
```

Expected: TWO Signals (one for setup-touch-file aggregate, one for run-with-deps-smoke aggregate). The last has `sensor_id="run-with-deps-smoke"`, `verdict="pass"`, and the sentinel file no longer exists (`teardown` removed it).

```bash
test ! -f /tmp/harness-smoke-sentinel && echo "teardown OK"
```

Expected: prints "teardown OK".

- [ ] **Step 11.6: Verify cascade by making the dep fail**

Force the dep to fail temporarily:

```bash
jq '.execution.command = "false"' sensors/setup-touch-file.json > /tmp/setup-fail.json
go run ./skills/detect-sensors/scripts --out=sensors /tmp/setup-fail.json

go run -tags=run_computational ./skills/run-sensor/scripts sensors/run-with-deps-smoke.json | jq -c '{sensor_id, verdict, "kind": .metadata.kind}'
```

Expected output:

```
{"sensor_id":"setup-touch-file","verdict":"fail","kind":"aggregate"}
{"sensor_id":"run-with-deps-smoke","verdict":"error","kind":"cascade"}
```

Restore the working version:

```bash
git checkout sensors/setup-touch-file.json
rm /tmp/setup-fail.json
```

- [ ] **Step 11.7: Run full test suite one final time**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
```

Expected: ALL PASS, vet clean.

- [ ] **Step 11.8: Commit**

```bash
git add sensors/setup-touch-file.json sensors/run-with-deps-smoke.json sensors/fixtures/setup-touch-file/ sensors/fixtures/run-with-deps-smoke/
git commit -m "$(cat <<'EOF'
test(sensors): add smoke sensor exercising deps + lifecycle end-to-end

- setup-touch-file (kind=setup): touches a sentinel file in /tmp.
- run-with-deps-smoke (kind=observation): depends_on the setup sensor,
  prepare verifies the sentinel exists, command echoes a known string
  matched by an output pattern, teardown removes the sentinel.

Verifies the orchestrator end-to-end against the real schema, real
validator, and real subprocess pipeline. Cascade behavior validated
manually by temporarily flipping setup-touch-file's command to "false".

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist

After completing all tasks, run this self-review against the spec:

**Spec coverage:**

| Spec section | Covered by |
|---|---|
| `kind` top-level (required, enum) | Task 1 |
| `depends_on` top-level (string array, unique, pattern) | Task 1 |
| `execution.prepare[]` (silent, fail-fast) | Tasks 1, 6 |
| `execution.teardown[]` (silent, best-effort) | Tasks 1, 6 |
| `$defs/ExitCodeMapEntry`, `$defs/LifecycleStep` | Task 1 |
| `requires.upstream_sensors` removed | Task 1 |
| Existing sensors migrated to `kind` (atomic) | Task 1 |
| `lib/sensor/lookup.go` (FindSensorByID) | Task 2 |
| `lib/orchestrator/dag.go` (Resolve + cycle/self-loop) | Task 3 |
| `lib/subprocess/step.go` (silent step runner) | Task 4 |
| `lib/orchestrator/cascade.go` (cascade Signal envelope) | Task 5 |
| `lib/orchestrator/lifecycle.go` (RunOne) | Task 6 |
| `lib/orchestrator/run.go` (RunWithDeps + topo + validation) | Task 7 |
| `run-computational.go` delegates to orchestrator | Task 8 |
| `run-inferential.go` delegates to orchestrator for deps | Task 9 |
| Skill docs (detect-sensors + run-sensor) updated | Task 10 |
| `CLAUDE.md` Architecture updated | Task 10 |
| End-to-end smoke fixture | Task 11 |
| 3-deep transitive chain test | Task 3 (tested via Diamond depth-3 in Resolve test); manually exercisable in Task 11 |
| Inferential setup non-goal | Task 10 (documented in detect-sensors SKILL.md) |
| Cascade Signal validates against signal.json | Task 5 (`TestBuildCascadeSignal_ValidatesAgainstSchema`) |

**Type/name consistency check:**

- `Sensor` struct (orchestrator) — used in dag.go, lifecycle.go, cascade.go, run.go ✓
- `RunOne(ctx, Sensor, schemasDir, *Validator, stdout, stderr) → (Signal, exitCode)` — same signature in lifecycle.go and run.go ✓
- `BuildCascadeSignal(skipped Sensor, failedDepSignal map)` — same in cascade.go and run.go ✓
- `FindSensorByID(id, sensorRoot) → (path, error)` — used by dag.go ✓
- `RunStep` / `StepConfig` / `StepResult` — used by lifecycle.go ✓

**No placeholders:** every step shows the actual code or command. No "TODO", "implement later", or "similar to Task N".

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-08-sensor-dependencies.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. Good for this plan because Task 1 is atomic and large; isolating it in a fresh agent keeps the rest of the implementation lean.

2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
