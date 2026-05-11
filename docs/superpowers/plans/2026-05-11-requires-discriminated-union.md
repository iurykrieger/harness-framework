# Unified `requires[]` Discriminated Union Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse `depends_on`, `requires.{tools,permissions,context,env}`, and `execution.prepare` into a single `requires[]` discriminated union (six `kind` variants) in `schemas/sensor.json` and rewire every consumer through one helper (`lib/sensor.Project()`). Runtime behaviour stays bit-identical; only the schema shape and the read path change.

**Architecture:** Spec at `docs/superpowers/specs/2026-05-11-requires-discriminated-union-design.md` (read it first). Issue: https://github.com/iurykrieger/harness-framework/issues/10. Six commits on a single branch, each CI-green. Commits 1–4 keep v1 readable via a transitional fallback inside `Project()`; commits 5–6 close the v1 path and bump versions.

**Tech Stack:** Go 1.25 (module `github.com/iurykrieger/harness-framework`), JSON Schema Draft 2020-12 via `github.com/santhosh-tekuri/jsonschema/v5`, `testing` package (table-driven tests are the project default). One Go module at the repo root. Scripts live in `skills/<skill>/scripts/` or top-level `scripts/`. Shared code is under `lib/`.

**Touched files (high-level):**

- Create: `lib/sensor/project.go`, `lib/sensor/project_test.go`, `scripts/migrate-requires.go`, `scripts/migrate-requires_test.go`, `CHANGELOG.md`.
- Modify (schema): `schemas/sensor.json`.
- Modify (consumers): `lib/orchestrator/dag.go`, `lib/orchestrator/run.go` (doc only), `lib/orchestrator/lifecycle.go`, `lib/sensor/env.go`, `hooks/setup-failure-detector.go`, `lib/heal/apply.go` (doc/error strings only), `lib/schema/validator.go`, `skills/run-sensor/scripts/run-computational.go`, `skills/run-sensor/scripts/run-inferential.go`, `skills/heal-sensor/scripts/diagnose.go`, `skills/detect-sensors/SKILL.md`, `.claude-plugin/plugin.json`.
- Modify (tests/fixtures): `lib/orchestrator/dag_test.go`, `lib/orchestrator/run_test.go`, `lib/orchestrator/preflight_test.go`, `lib/orchestrator/live_deps_test.go`, `lib/orchestrator/lifecycle_test.go`, `lib/sensor/env_test.go`, `hooks/setup-failure-detector_test.go`, `lib/heal/apply_test.go`, `lib/heal/plan_test.go`, `lib/schema/validator_test.go`.
- Migrate (sensors): `sensors/run-with-deps-smoke.json` (only sensor with v1 fields today; the other 12 stay shape-compatible after schema update).

**File Structure for new code:**

- `lib/sensor/project.go` — single function `Project(s map[string]interface{}, kind string) []map[string]interface{}` plus an unexported `synthesizeV2(s) []map[string]interface{}` used while the transitional fallback is in effect (removed in Task 11). Stays small (~60 LOC including comments). No type definitions.
- `scripts/migrate-requires.go` — `package main` CLI tool. Sibling helpers stay in the same file. Conversion logic is pure-function and testable in isolation.
- `scripts/migrate-requires_test.go` — table-driven cases keyed on (input v1 sensor, expected v2 sensor).

---

## Task 1: Schema v2 with dual `oneOf` for `requires` (Commit 1: `feat(schema)`)

**Goal:** Extend `schemas/sensor.json` so `requires` accepts either the v1 object or the v2 array. Top-level `depends_on` and `execution.prepare` stay declared. Schema's `additionalProperties: false` stays. Test fixtures pass unchanged.

**Files:**
- Modify: `schemas/sensor.json` (top-level `requires` becomes `oneOf`; new `$defs` for `Requirement` and six per-kind sub-schemas).
- Test: `lib/schema/validator_test.go` (new cases: every v1 sensor still validates; a v2 sensor with each kind validates).

- [ ] **Step 1.1: Write a failing test asserting v2 `requires[]` validates**

Append to `lib/schema/validator_test.go`:

```go
func TestValidator_Sensor_RequiresArrayV2(t *testing.T) {
	v := mustValidator(t)
	body := []byte(`{
		"id": "ex-v2",
		"version": "1.0.0",
		"name": "ex",
		"description": "desc",
		"kind": "observation",
		"type": "computational",
		"regulation": "behaviour",
		"phase": "on-demand",
		"determinism": "high",
		"output": "single",
		"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":64},"latency":{"p50_ms":1,"p95_ms":1,"timeout_ms":1000}},
		"triggers": [{"on":"manual"}],
		"requires": [
			{"kind":"sensor","id":"setup-touch-file"},
			{"kind":"tool","name":"docker"},
			{"kind":"env","name":"GH_TOKEN","optional":false},
			{"kind":"context","path":"docs/"},
			{"kind":"permission","scope":"repo:read"},
			{"kind":"step","command":"true"}
		],
		"execution": {"command":"true","exit_code_map":[{"exit_code":0,"verdict":"pass","severity":"info"}]},
		"verification": {"golden_cases":[{"fixture":"f","expected_verdict":"pass","expected_severity":"info"}]}
	}`)
	var instance map[string]interface{}
	if err := json.Unmarshal(body, &instance); err != nil { t.Fatal(err) }
	if err := v.Validate(schema.TargetSensor, instance); err != nil {
		t.Fatalf("expected v2 requires[] to validate, got: %v", err)
	}
}
```

If `mustValidator` does not exist, locate the equivalent setup in the same file and replicate the pattern. The `json` import may need to be added.

- [ ] **Step 1.2: Run the test to confirm it fails**

```
go test ./lib/schema/... -run RequiresArrayV2 -v
```

Expected: FAIL with a schema validation error mentioning `requires` (v2 array shape is not yet allowed).

- [ ] **Step 1.3: Add `$defs/Requirement` and per-kind sub-schemas in `schemas/sensor.json`**

In the `$defs` block (around line 7 of the schema), add — alphabetically after `LifecycleStep` — these definitions, copied verbatim from spec §5:

```jsonc
"Requirement": {
  "oneOf": [
    { "$ref": "#/$defs/RequireSensor" },
    { "$ref": "#/$defs/RequireTool" },
    { "$ref": "#/$defs/RequireEnv" },
    { "$ref": "#/$defs/RequireContext" },
    { "$ref": "#/$defs/RequirePermission" },
    { "$ref": "#/$defs/RequireStep" }
  ]
},
"RequireSensor": {
  "type": "object",
  "additionalProperties": false,
  "required": ["kind", "id"],
  "properties": {
    "kind": { "const": "sensor" },
    "id":   { "type": "string", "pattern": "^[a-z][a-z0-9-]*$" }
  }
},
"RequireTool": {
  "type": "object",
  "additionalProperties": false,
  "required": ["kind", "name"],
  "properties": {
    "kind": { "const": "tool" },
    "name": { "type": "string" }
  }
},
"RequireEnv": {
  "type": "object",
  "additionalProperties": false,
  "required": ["kind", "name"],
  "properties": {
    "kind":        { "const": "env" },
    "name":        { "type": "string", "pattern": "^[A-Z_][A-Z0-9_]*$" },
    "description": { "type": "string" },
    "optional":    { "type": "boolean", "default": false }
  }
},
"RequireContext": {
  "type": "object",
  "additionalProperties": false,
  "required": ["kind", "path"],
  "properties": {
    "kind": { "const": "context" },
    "path": { "type": "string" }
  }
},
"RequirePermission": {
  "type": "object",
  "additionalProperties": false,
  "required": ["kind", "scope"],
  "properties": {
    "kind":  { "const": "permission" },
    "scope": { "type": "string" }
  }
},
"RequireStep": {
  "type": "object",
  "additionalProperties": false,
  "required": ["kind", "command"],
  "properties": {
    "kind":          { "const": "step" },
    "command":       { "type": "string" },
    "timeout_ms":    { "type": "integer", "minimum": 1 },
    "exit_code_map": {
      "type": "array",
      "minItems": 1,
      "items": { "$ref": "#/$defs/ExitCodeMapEntry" }
    }
  }
}
```

- [ ] **Step 1.4: Replace the existing `requires` property with a `oneOf` over the v1 object and the v2 array**

Find the existing `requires` property in `schemas/sensor.json` (currently an object with `additionalProperties: false` and properties `tools/permissions/context/env`). Replace its value with:

```jsonc
"requires": {
  "description": "Preconditions for this sensor. v2 shape: array of discriminated-union items keyed by `kind`. v1 shape (object with tools/permissions/context/env) is still accepted for the duration of this PR; commit 5 drops it.",
  "oneOf": [
    {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "tools":       { "type": "array", "items": { "type": "string" }, "default": [] },
        "permissions": { "type": "array", "items": { "type": "string" }, "default": [] },
        "context":     { "type": "array", "items": { "type": "string" }, "default": [] },
        "env": {
          "type": "array",
          "default": [],
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": ["name"],
            "properties": {
              "name":        { "type": "string", "pattern": "^[A-Z_][A-Z0-9_]*$" },
              "description": { "type": "string" },
              "optional":    { "type": "boolean", "default": false }
            }
          }
        }
      }
    },
    {
      "type": "array",
      "default": [],
      "items": { "$ref": "#/$defs/Requirement" }
    }
  ]
}
```

Keep the top-level `depends_on` property unchanged. Keep `execution.prepare` unchanged. (Both are removed in Task 11.)

- [ ] **Step 1.5: Run the failing test to verify it now passes**

```
go test ./lib/schema/... -run RequiresArrayV2 -v
```

Expected: PASS.

- [ ] **Step 1.6: Run the whole project test suite to confirm nothing regressed**

```
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
```

Expected: all green. The single existing v1 sensor (`sensors/run-with-deps-smoke.json`) and every fixture-built sensor in test files keeps validating against the schema.

- [ ] **Step 1.7: Commit**

```
git add schemas/sensor.json lib/schema/validator_test.go
git commit -m "feat(schema): accept v2 requires[] discriminated union alongside v1 (#10)

Schema now defines \$defs/Requirement as oneOf over six per-kind sub-schemas
(sensor, tool, env, context, permission, step). The top-level requires
property becomes oneOf: [v1 object, v2 array]. depends_on and
execution.prepare stay declared. Both shapes validate; consumers still read
v1 paths (rewired in commit 2)."
```

---

## Task 2: `lib/sensor.Project()` helper + transitional fallback (Commit 2 part A)

**Goal:** Introduce one public function that every downstream consumer will use to read `requires[]`. While v1 sensors still exist, `Project()` synthesizes a v2-shaped array from v1 fields. The synthesis lives in one place; commit 5 (Task 11) removes it.

**Files:**
- Create: `lib/sensor/project.go`
- Create: `lib/sensor/project_test.go`

- [ ] **Step 2.1: Write the failing tests for `Project()`**

Create `lib/sensor/project_test.go`:

```go
package sensor

import (
	"reflect"
	"testing"
)

func TestProject_V2Array_AllKinds(t *testing.T) {
	s := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "sensor", "id": "a"},
			map[string]interface{}{"kind": "tool", "name": "docker"},
			map[string]interface{}{"kind": "env", "name": "X"},
			map[string]interface{}{"kind": "context", "path": "docs/"},
			map[string]interface{}{"kind": "permission", "scope": "repo:read"},
			map[string]interface{}{"kind": "step", "command": "true"},
		},
	}
	for _, kind := range []string{"sensor", "tool", "env", "context", "permission", "step"} {
		got := Project(s, kind)
		if len(got) != 1 {
			t.Fatalf("kind=%s: expected 1 item, got %d (%#v)", kind, len(got), got)
		}
		if got[0]["kind"] != kind {
			t.Fatalf("kind=%s: got kind=%v", kind, got[0]["kind"])
		}
	}
}

func TestProject_V2Array_PreservesOrder(t *testing.T) {
	s := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "step", "command": "first"},
			map[string]interface{}{"kind": "step", "command": "second"},
		},
	}
	got := Project(s, "step")
	if len(got) != 2 || got[0]["command"] != "first" || got[1]["command"] != "second" {
		t.Fatalf("order not preserved: %#v", got)
	}
}

func TestProject_V2Array_SkipsMalformed(t *testing.T) {
	s := map[string]interface{}{
		"requires": []interface{}{
			"not-an-object",
			map[string]interface{}{}, // no kind
			map[string]interface{}{"kind": 123}, // non-string kind
			map[string]interface{}{"kind": "sensor", "id": "a"},
		},
	}
	got := Project(s, "sensor")
	if len(got) != 1 || got[0]["id"] != "a" {
		t.Fatalf("expected only the well-formed sensor entry, got: %#v", got)
	}
}

func TestProject_EmptyAndMissing(t *testing.T) {
	if got := Project(map[string]interface{}{}, "sensor"); got != nil {
		t.Fatalf("missing requires: expected nil, got %#v", got)
	}
	if got := Project(map[string]interface{}{"requires": []interface{}{}}, "sensor"); got != nil {
		t.Fatalf("empty requires: expected nil, got %#v", got)
	}
}

func TestProject_V1Fallback_DependsOn(t *testing.T) {
	s := map[string]interface{}{
		"depends_on": []interface{}{"a", "b"},
	}
	got := Project(s, "sensor")
	if len(got) != 2 || got[0]["id"] != "a" || got[1]["id"] != "b" {
		t.Fatalf("v1 depends_on fallback failed: %#v", got)
	}
}

func TestProject_V1Fallback_RequiresObject(t *testing.T) {
	s := map[string]interface{}{
		"requires": map[string]interface{}{
			"env": []interface{}{
				map[string]interface{}{"name": "GH_TOKEN", "description": "PAT", "optional": false},
			},
			"tools":       []interface{}{"docker"},
			"context":     []interface{}{"docs/"},
			"permissions": []interface{}{"repo:read"},
		},
	}
	if got := Project(s, "env"); len(got) != 1 || got[0]["name"] != "GH_TOKEN" {
		t.Fatalf("env fallback: %#v", got)
	}
	if got := Project(s, "tool"); len(got) != 1 || got[0]["name"] != "docker" {
		t.Fatalf("tool fallback: %#v", got)
	}
	if got := Project(s, "context"); len(got) != 1 || got[0]["path"] != "docs/" {
		t.Fatalf("context fallback: %#v", got)
	}
	if got := Project(s, "permission"); len(got) != 1 || got[0]["scope"] != "repo:read" {
		t.Fatalf("permission fallback: %#v", got)
	}
}

func TestProject_V1Fallback_ExecutionPrepare(t *testing.T) {
	s := map[string]interface{}{
		"execution": map[string]interface{}{
			"prepare": []interface{}{
				map[string]interface{}{"command": "cp .env.example .env", "timeout_ms": float64(1000)},
			},
		},
	}
	got := Project(s, "step")
	if len(got) != 1 || got[0]["command"] != "cp .env.example .env" {
		t.Fatalf("prepare fallback: %#v", got)
	}
}

func TestProject_V1Fallback_CombinedDoesNotDuplicate(t *testing.T) {
	s := map[string]interface{}{
		"depends_on": []interface{}{"a"},
		"requires": map[string]interface{}{
			"tools": []interface{}{"docker"},
		},
		"execution": map[string]interface{}{
			"prepare": []interface{}{
				map[string]interface{}{"command": "true"},
			},
		},
	}
	if got := Project(s, "sensor"); len(got) != 1 {
		t.Fatalf("sensor: %#v", got)
	}
	if got := Project(s, "tool"); len(got) != 1 {
		t.Fatalf("tool: %#v", got)
	}
	if got := Project(s, "step"); len(got) != 1 {
		t.Fatalf("step: %#v", got)
	}
	if got := Project(s, "env"); got != nil {
		t.Fatalf("env: expected nil, got %#v", got)
	}
}

func TestProject_UnknownKindReturnsNil(t *testing.T) {
	s := map[string]interface{}{
		"requires": []interface{}{
			map[string]interface{}{"kind": "sensor", "id": "a"},
		},
	}
	if got := Project(s, "tool"); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

// reflect import kept for parity with table-driven tests elsewhere; not used yet
var _ = reflect.DeepEqual
```

- [ ] **Step 2.2: Run the failing tests**

```
go test ./lib/sensor/... -run Project -v
```

Expected: FAIL — `Project` is not yet defined.

- [ ] **Step 2.3: Implement `Project()` with the transitional fallback**

Create `lib/sensor/project.go`:

```go
package sensor

// Project returns all elements of requires[] whose `kind` equals the given
// kind, preserving array order. Returns nil when requires is absent, empty,
// or no entry matches. Schema validation is the caller's responsibility —
// Project silently skips entries that are not JSON objects or whose kind
// field is missing/non-string.
//
// TRANSITIONAL: while v1 sensors are still on disk, Project also accepts
// the v1 shape (top-level depends_on, requires as object, execution.prepare)
// and synthesizes the v2 array internally. The synthesis lives at the top
// of this function in a single dispatch on the runtime type of requires;
// commit 5 of the unification PR removes it.
func Project(sensor map[string]interface{}, kind string) []map[string]interface{} {
	v2 := asV2Array(sensor)
	if len(v2) == 0 {
		return nil
	}
	var out []map[string]interface{}
	for _, raw := range v2 {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		k, ok := item["kind"].(string)
		if !ok || k != kind {
			continue
		}
		out = append(out, item)
	}
	return out
}

// asV2Array returns the requires[] array, synthesizing one from v1 fields
// when needed. The synthesis is the single transitional point; commit 5
// removes it and inlines `requires, _ := sensor["requires"].([]interface{})`.
func asV2Array(sensor map[string]interface{}) []interface{} {
	if arr, ok := sensor["requires"].([]interface{}); ok {
		return arr
	}
	return synthesizeV2(sensor)
}

// synthesizeV2 builds a v2 requires[] from v1 fields. Order is stable:
// sensor → tool → env → context → permission → step. Step entries are
// never deduplicated. Used only while v1 sensors coexist (commits 1–4).
func synthesizeV2(sensor map[string]interface{}) []interface{} {
	out := []interface{}{}

	if deps, ok := sensor["depends_on"].([]interface{}); ok {
		for _, d := range deps {
			id, ok := d.(string)
			if !ok {
				continue
			}
			out = append(out, map[string]interface{}{"kind": "sensor", "id": id})
		}
	}

	reqObj, _ := sensor["requires"].(map[string]interface{})
	if reqObj != nil {
		if tools, ok := reqObj["tools"].([]interface{}); ok {
			for _, t := range tools {
				name, ok := t.(string)
				if !ok {
					continue
				}
				out = append(out, map[string]interface{}{"kind": "tool", "name": name})
			}
		}
		if envs, ok := reqObj["env"].([]interface{}); ok {
			for _, e := range envs {
				em, ok := e.(map[string]interface{})
				if !ok {
					continue
				}
				entry := map[string]interface{}{"kind": "env"}
				for _, k := range []string{"name", "description", "optional"} {
					if v, ok := em[k]; ok {
						entry[k] = v
					}
				}
				out = append(out, entry)
			}
		}
		if ctxs, ok := reqObj["context"].([]interface{}); ok {
			for _, c := range ctxs {
				p, ok := c.(string)
				if !ok {
					continue
				}
				out = append(out, map[string]interface{}{"kind": "context", "path": p})
			}
		}
		if perms, ok := reqObj["permissions"].([]interface{}); ok {
			for _, p := range perms {
				s, ok := p.(string)
				if !ok {
					continue
				}
				out = append(out, map[string]interface{}{"kind": "permission", "scope": s})
			}
		}
	}

	if exec, ok := sensor["execution"].(map[string]interface{}); ok {
		if steps, ok := exec["prepare"].([]interface{}); ok {
			for _, st := range steps {
				sm, ok := st.(map[string]interface{})
				if !ok {
					continue
				}
				entry := map[string]interface{}{"kind": "step"}
				for _, k := range []string{"command", "timeout_ms", "exit_code_map"} {
					if v, ok := sm[k]; ok {
						entry[k] = v
					}
				}
				out = append(out, entry)
			}
		}
	}

	return out
}
```

- [ ] **Step 2.4: Run the tests to verify they pass**

```
go test ./lib/sensor/... -run Project -v
```

Expected: PASS for all 9 test functions.

- [ ] **Step 2.5: Run `go vet` to catch unused imports or shadowed names**

```
go vet ./lib/sensor/...
```

Expected: no output.

- [ ] **Step 2.6: Commit**

```
git add lib/sensor/project.go lib/sensor/project_test.go
git commit -m "feat(sensor): introduce Project() with transitional v1 fallback (#10)

Project(s, kind) is the single read path consumers will use for requires[].
While v1 sensors coexist, Project synthesizes the v2 array internally from
depends_on, requires.* (object), and execution.prepare[]. The synthesis is
one dispatch at the top of the function; commit 5 removes it."
```

---

## Task 3: Wire `lib/orchestrator/dag.go` through `Project()` (Commit 2 part B)

**Goal:** Replace the `depends_on` read in `readDepsArray` with `sensor.Project(s, "sensor")`. Update the package doc comment in `run.go` to name `requires[kind=sensor]`. Existing tests still pass (they build v1-shape sensor JSON).

**Files:**
- Modify: `lib/orchestrator/dag.go`
- Modify: `lib/orchestrator/run.go` (doc comment only)

- [ ] **Step 3.1: Run the existing dag tests as a baseline**

```
go test ./lib/orchestrator/... -run Resolve -v
```

Expected: PASS. Confirm green before refactoring.

- [ ] **Step 3.2: Replace `readDepsArray` in `lib/orchestrator/dag.go`**

Find:

```go
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
```

Replace with:

```go
func readDepsArray(s map[string]interface{}) []string {
	items := sensor.Project(s, "sensor")
	out := make([]string, 0, len(items))
	for _, item := range items {
		if id, ok := item["id"].(string); ok && id != "" {
			out = append(out, id)
		}
	}
	return out
}
```

The `sensor` package is already imported in this file (see top of `dag.go`).

- [ ] **Step 3.3: Update the package doc comment in `lib/orchestrator/dag.go`**

Find the package-level comment block (lines 1–7) and replace the sentence "A sensor's depends_on declares ids of other sensors that must run and pass before it" with "A sensor's `requires[]` entries of `kind=sensor` declare ids of other sensors that must run and pass before it. (The v1 `depends_on` field is read transparently via `sensor.Project()` until the migration completes.)"

- [ ] **Step 3.4: Update the doc comment in `lib/orchestrator/run.go`**

Find the comment "RunWithDeps loads the sensor at sensorPath, resolves its depends_on" and replace "depends_on" with "`requires[kind=sensor]`". No code change.

- [ ] **Step 3.5: Run the orchestrator tests**

```
go test ./lib/orchestrator/... -v
```

Expected: PASS — the fixtures still produce v1 sensors and `Project()` synthesizes the dependency array.

- [ ] **Step 3.6: Commit**

```
git add lib/orchestrator/dag.go lib/orchestrator/run.go
git commit -m "refactor(orchestrator): read sensor deps via sensor.Project() (#10)

readDepsArray now projects requires[kind=sensor]; the transitional fallback
in Project() makes v1 depends_on still work. Doc comments updated to name
the v2 vocabulary."
```

---

## Task 4: Wire `lib/orchestrator/lifecycle.go` through `Project()` (Commit 2 part C)

**Goal:** Replace `execMap["prepare"].([]interface{})` reads in `runLifecyclePhase` with `sensor.Project(s, "step")` when phase is `prepare`. Keep `teardown` reading from `execution.teardown[]` (unchanged). The Signal-side `metadata.lifecycle.prepare` key name stays exactly the same.

**Files:**
- Modify: `lib/orchestrator/lifecycle.go`

- [ ] **Step 4.1: Run the existing lifecycle tests as a baseline**

```
go test ./lib/orchestrator/... -run Lifecycle -v
```

Expected: PASS.

- [ ] **Step 4.2: Modify `runLifecyclePhase` to dispatch on phase**

In `lib/orchestrator/lifecycle.go`, find:

```go
func runLifecyclePhase(ctx context.Context, execMap map[string]interface{}, phase string, defaultTimeoutMS int, failFast bool) ([]interface{}, bool) {
	steps, _ := execMap[phase].([]interface{})
```

The function only has access to `execMap`, not the full sensor JSON. Change its signature to accept the full sensor JSON. Update the call sites in `RunOne` (two calls — one for `prepare`, one for `teardown`).

Concretely:

In `RunOne`, change:

```go
	// Phase 1: prepare (fail-fast).
	prepResults, prepFailed := runLifecyclePhase(ctx, execMap, "prepare", timeoutMS, true)
```

to:

```go
	// Phase 1: prepare (fail-fast). Reads requires[kind=step] via sensor.Project();
	// the transitional fallback covers v1 execution.prepare[].
	prepResults, prepFailed := runPreparePhase(ctx, s.JSON, timeoutMS)
```

Wait — `RunOne` does not have access to `s.JSON` because `Sensor` is `orchestrator.Sensor` (which has `JSON`). Use `s.JSON` directly. Also update the teardown call to keep its current signature.

Replace `runLifecyclePhase` with two functions — one for prepare (reads via `Project`) and one for teardown (reads `execution.teardown[]` directly):

```go
// runPreparePhase reads requires[kind=step] from the sensor JSON (via
// sensor.Project) and runs each step fail-fast. Per-step results are folded
// into metadata.lifecycle.prepare (name unchanged; phase name, not field
// name).
func runPreparePhase(ctx context.Context, sensorJSON map[string]interface{}, defaultTimeoutMS int) ([]interface{}, bool) {
	steps := sensor.Project(sensorJSON, "step")
	var out []interface{}
	for _, step := range steps {
		cmd, _ := step["command"].(string)
		t := defaultTimeoutMS
		if v, ok := step["timeout_ms"]; ok {
			t = int(asNumber(v))
		}
		res, _ := subprocess.RunStep(ctx, subprocess.StepConfig{Command: cmd, TimeoutMS: t})
		ecMap, _ := step["exit_code_map"].([]interface{})
		verdict, severity := mapStepExitCode(res.ExitCode, ecMap, "prepare")
		entry := map[string]interface{}{
			"command":    cmd,
			"exit_code":  res.ExitCode,
			"latency_ms": res.ElapsedMS,
			"timed_out":  res.TimedOut,
			"verdict":    verdict,
			"severity":   severity,
		}
		if res.StderrExcerpt != "" {
			entry["stderr_excerpt"] = res.StderrExcerpt
		}
		out = append(out, entry)
		if verdict != "pass" {
			return out, true
		}
	}
	return out, false
}

// runTeardownPhase walks execMap["teardown"] (unchanged source) best-effort.
// Teardown lives in execution.teardown[] in both v1 and v2; only prepare
// moves to requires[kind=step].
func runTeardownPhase(ctx context.Context, execMap map[string]interface{}, defaultTimeoutMS int) []interface{} {
	steps, _ := execMap["teardown"].([]interface{})
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
		verdict, severity := mapStepExitCode(res.ExitCode, ecMap, "teardown")
		entry := map[string]interface{}{
			"command":    cmd,
			"exit_code":  res.ExitCode,
			"latency_ms": res.ElapsedMS,
			"timed_out":  res.TimedOut,
			"verdict":    verdict,
			"severity":   severity,
		}
		if res.StderrExcerpt != "" {
			entry["stderr_excerpt"] = res.StderrExcerpt
		}
		out = append(out, entry)
	}
	return out
}
```

Delete the old `runLifecyclePhase` function entirely. Update the teardown call site:

```go
	tdResults, _ := runLifecyclePhase(ctx, execMap, "teardown", timeoutMS, false)
```

becomes:

```go
	tdResults := runTeardownPhase(ctx, execMap, timeoutMS)
```

Delete the unused second return value from the call site.

- [ ] **Step 4.3: Run the lifecycle tests**

```
go test ./lib/orchestrator/... -v
```

Expected: PASS. The single sensor in `sensors/` that uses `prepare` (run-with-deps-smoke) is read through `Project()`'s v1 fallback. Fixtures in `lifecycle_test.go` continue using `execution.prepare` and still work.

- [ ] **Step 4.4: Run the full repo test suite to catch regressions**

```
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go vet ./...
```

Expected: all green.

- [ ] **Step 4.5: Commit**

```
git add lib/orchestrator/lifecycle.go
git commit -m "refactor(orchestrator): read prepare steps via sensor.Project() (#10)

Split runLifecyclePhase into runPreparePhase (reads requires[kind=step] via
Project) and runTeardownPhase (reads execution.teardown[] unchanged).
Lifecycle metadata key remains 'prepare' — it is the phase name, not the
schema field name."
```

---

## Task 5: Wire `lib/sensor/env.go` through `Project()` (Commit 2 part D)

**Goal:** Replace the `requires.env` read in `CheckRequiredEnv` with `sensor.Project(s, "env")`. Output (`[]MissingEnv`) stays identical. Existing tests pass via the v1 fallback.

**Files:**
- Modify: `lib/sensor/env.go`

- [ ] **Step 5.1: Run existing env tests as a baseline**

```
go test ./lib/sensor/... -run CheckRequiredEnv -v
```

Expected: PASS.

- [ ] **Step 5.2: Replace `CheckRequiredEnv` body**

In `lib/sensor/env.go`, find:

```go
func CheckRequiredEnv(sensor map[string]interface{}) []MissingEnv {
	requires, _ := sensor["requires"].(map[string]interface{})
	if requires == nil {
		return nil
	}
	envSpec, _ := requires["env"].([]interface{})
	if len(envSpec) == 0 {
		return nil
	}
	var missing []MissingEnv
	for _, item := range envSpec {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		optional, _ := m["optional"].(bool)
		if optional {
			continue
		}
		if _, set := LookupEnvFn(name); set {
			continue
		}
		description, _ := m["description"].(string)
		missing = append(missing, MissingEnv{Name: name, Description: description})
	}
	return missing
}
```

Replace with:

```go
func CheckRequiredEnv(s map[string]interface{}) []MissingEnv {
	entries := Project(s, "env")
	if len(entries) == 0 {
		return nil
	}
	var missing []MissingEnv
	for _, m := range entries {
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		optional, _ := m["optional"].(bool)
		if optional {
			continue
		}
		if _, set := LookupEnvFn(name); set {
			continue
		}
		description, _ := m["description"].(string)
		missing = append(missing, MissingEnv{Name: name, Description: description})
	}
	return missing
}
```

Rename the parameter from `sensor` to `s` to avoid shadowing the package name (Project is package-local; no clash today, but the rename keeps the file readable).

- [ ] **Step 5.3: Run env tests**

```
go test ./lib/sensor/... -v
```

Expected: PASS. The existing tests still pass because they use v1 shape (`requires: {env: [...]}`), and `Project()` synthesizes the v2 array transparently.

- [ ] **Step 5.4: Commit**

```
git add lib/sensor/env.go
git commit -m "refactor(sensor): CheckRequiredEnv reads via Project() (#10)

CheckRequiredEnv now consumes requires[kind=env] via the sensor.Project
helper. Behaviour unchanged: same MissingEnv slice for the same inputs.
v1 sensors continue to work through Project's transitional fallback."
```

---

## Task 6: Wire `hooks/setup-failure-detector.go::loadFailedSensorView` (Commit 2 part E)

**Goal:** Rewrite `loadFailedSensorView` to read sensor JSON via `sensor.Project()`. Populate `heal.FailedSensor.{EnvNames, Tools, Context}` from items of the corresponding kinds. Output (`heal.FailedSensor`) is unchanged; downstream heal rules see the same struct.

**Files:**
- Modify: `hooks/setup-failure-detector.go`

- [ ] **Step 6.1: Run hook tests as a baseline**

```
go test ./hooks/... -v
```

Expected: PASS.

- [ ] **Step 6.2: Replace `loadFailedSensorView` and remove `sensorRequiresView`**

In `hooks/setup-failure-detector.go`, locate the `sensorRequiresView` type and the `loadFailedSensorView` function (around lines 384–418). Replace both with:

```go
func loadFailedSensorView(path string) (heal.FailedSensor, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return heal.FailedSensor{}, err
	}
	var s map[string]interface{}
	if err := json.Unmarshal(body, &s); err != nil {
		return heal.FailedSensor{}, err
	}
	id, _ := s["id"].(string)

	var envs []string
	for _, e := range sensor.Project(s, "env") {
		if name, ok := e["name"].(string); ok && name != "" {
			envs = append(envs, name)
		}
	}
	var tools []string
	for _, t := range sensor.Project(s, "tool") {
		if name, ok := t["name"].(string); ok && name != "" {
			tools = append(tools, name)
		}
	}
	var contexts []string
	for _, c := range sensor.Project(s, "context") {
		if p, ok := c["path"].(string); ok && p != "" {
			contexts = append(contexts, p)
		}
	}
	return heal.FailedSensor{ID: id, EnvNames: envs, Tools: tools, Context: contexts}, nil
}
```

Add `"github.com/iurykrieger/harness-framework/lib/sensor"` to the imports if not already present.

- [ ] **Step 6.3: Run hook tests**

```
go test ./hooks/... -v
```

Expected: PASS. Hook tests' fixtures build v1 sensors; `Project()` synthesizes their `requires[]` transparently.

- [ ] **Step 6.4: Run full suite**

```
go test ./lib/... ./hooks/...
go vet ./...
```

Expected: all green.

- [ ] **Step 6.5: Commit**

```
git add hooks/setup-failure-detector.go
git commit -m "refactor(hooks): loadFailedSensorView reads via Project() (#10)

The hook now projects requires[kind=env|tool|context] through sensor.Project
to populate heal.FailedSensor. The struct contract downstream is unchanged;
heal rules see the same fields they did before."
```

---

## Task 7: Wire runner skills and `skills/heal-sensor/scripts/diagnose.go` (Commit 2 part F)

**Goal:** Replace direct reads of `execution.prepare` in runner and heal-sensor scripts with `sensor.Project()`. `skills/start-sensor/scripts/start.go` doesn't read `prepare` directly — it calls `orchestrator.RunPreparePhase` (renamed in Task 4) — so confirm its call site is consistent.

**Files:**
- Modify: `skills/run-sensor/scripts/run-computational.go`
- Modify: `skills/run-sensor/scripts/run-inferential.go`
- Modify: `skills/heal-sensor/scripts/diagnose.go`
- Modify: `skills/start-sensor/scripts/start.go` (call-site update if Task 4 renamed the orchestrator function)

- [ ] **Step 7.1: Search for direct prepare reads in skills**

```
grep -nE '(execution.*prepare|s\.JSON\["execution"\]|exec\["prepare"\])' skills/run-sensor/scripts/*.go skills/heal-sensor/scripts/*.go skills/start-sensor/scripts/*.go
```

For each match: if the code reads `prepare` to iterate steps, replace with `sensor.Project(s.JSON, "step")` (or the equivalent local variable). If the code only references `prepare` as a string literal (e.g., metadata key), leave it.

- [ ] **Step 7.2: Confirm `skills/start-sensor/scripts/start.go` compiles**

If Task 4 introduced `runPreparePhase` (unexported) or renamed an exported function used by `start.go`, update the call. If the orchestrator API is unchanged at package boundary, this step is a no-op.

```
go build ./skills/start-sensor/scripts/...
```

Expected: success.

- [ ] **Step 7.3: Run skill tests**

```
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
```

Expected: all green.

- [ ] **Step 7.4: Commit**

```
git add skills/
git commit -m "refactor(skills): runner and heal-sensor read steps via Project() (#10)

run-computational.go, run-inferential.go, and diagnose.go now project
requires[kind=step] through sensor.Project. start.go follows the renamed
orchestrator surface. SKILL.md prose updates land in a later commit."
```

---

## Task 8: Update test fixtures across orchestrator/heal/hook (Commit 2 part G)

**Goal:** Some tests still build sensor JSON inline using v1 paths. Convert them to v2 shape so they exercise the v2 code path directly, not the transitional fallback. This isolates fallback testing to `lib/sensor/project_test.go`.

**Files:**
- Modify: `lib/orchestrator/dag_test.go`
- Modify: `lib/orchestrator/run_test.go`
- Modify: `lib/orchestrator/preflight_test.go`
- Modify: `lib/orchestrator/live_deps_test.go`
- Modify: `lib/orchestrator/lifecycle_test.go`
- Modify: `lib/sensor/env_test.go`
- Modify: `hooks/setup-failure-detector_test.go`
- Modify: `lib/heal/apply_test.go`
- Modify: `lib/heal/plan_test.go`

- [ ] **Step 8.1: Convert `dag_test.go`'s `writeSensorJSON` helper to v2**

In `lib/orchestrator/dag_test.go`, find `writeSensorJSON`:

```go
body := `{"id":"` + id + `","depends_on":` + deps + `}`
```

Replace with a v2 builder:

```go
var requires string
if len(depsOn) > 0 {
    var entries []string
    for _, d := range depsOn {
        entries = append(entries, `{"kind":"sensor","id":"` + d + `"}`)
    }
    requires = "[" + strings.Join(entries, ",") + "]"
} else {
    requires = "[]"
}
body := `{"id":"` + id + `","requires":` + requires + `}`
```

Add `"strings"` to the import block. Remove the now-unused `deps` variable construction.

- [ ] **Step 8.2: Convert `run_test.go`, `preflight_test.go`, `live_deps_test.go`**

For each file, search for `"depends_on"` and `"prepare"` in the fixtures:

```
grep -nE 'depends_on|"prepare":' lib/orchestrator/run_test.go lib/orchestrator/preflight_test.go lib/orchestrator/live_deps_test.go
```

Replace each fixture's v1 shape with the v2 equivalent following the §6 mapping table from the spec. Example transformation:

```go
s["depends_on"] = []interface{}{"a"}
```

becomes:

```go
s["requires"] = []interface{}{
    map[string]interface{}{"kind": "sensor", "id": "a"},
}
```

And:

```go
s["execution"] = map[string]interface{}{
    "command": "true",
    "prepare": []interface{}{
        map[string]interface{}{"command": "step-1"},
    },
}
```

becomes:

```go
s["execution"] = map[string]interface{}{
    "command": "true",
}
s["requires"] = []interface{}{
    map[string]interface{}{"kind": "step", "command": "step-1"},
}
```

If a test sets both `depends_on` and `prepare`, both move into a single `requires[]` array; ordering inside the array follows the §6 synthesis order (sensor entries first, step entries last).

- [ ] **Step 8.3: Convert `lifecycle_test.go` fixtures**

Same pattern: any `execution.prepare` becomes `requires[].kind=step` entries.

- [ ] **Step 8.4: Convert `lib/sensor/env_test.go`**

Existing tests pass `map[string]interface{}{"requires": map[string]interface{}{"env": ...}}`. Convert to v2:

```go
"requires": []interface{}{
    map[string]interface{}{"kind": "env", "name": "GITHUB_TOKEN", "description": "PAT"},
    map[string]interface{}{"kind": "env", "name": "GCP_PROJECT"},
},
```

Leave the existing v1-shape tests in place — they exercise the fallback. Mark them with comments such as `// transitional: exercises v1 fallback in sensor.Project()`.

Alternative: keep v1-shape tests as-is to test the fallback, and add NEW v2-shape tests next to each. This is cleaner. Use this approach.

- [ ] **Step 8.5: Convert `hooks/setup-failure-detector_test.go`**

Search:

```
grep -nE '"depends_on"|"prepare":|"requires":' hooks/setup-failure-detector_test.go
```

For fixtures that build v1 sensors via `os.WriteFile`, replace with v2 shape. Where fixtures are necessary to drive the hook's heal-classifier scan, keep them minimal and v2-shaped.

- [ ] **Step 8.6: Convert `lib/heal/apply_test.go` and `lib/heal/plan_test.go`**

These tests operate on `heal.FailedSensor`, not sensor JSON directly. If any fixtures construct sensor JSON for hook integration, convert them. Otherwise no-op.

- [ ] **Step 8.7: Run all tests**

```
go test ./...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go vet ./...
```

Expected: all green. The v2 path is now the primary exercised one across all consumer tests; `lib/sensor/project_test.go` still exercises the fallback.

- [ ] **Step 8.8: Commit**

```
git add lib/orchestrator/*_test.go lib/sensor/env_test.go hooks/setup-failure-detector_test.go lib/heal/*_test.go
git commit -m "test: rewrite fixtures to v2 requires[] shape (#10)

All orchestrator, env, hook, and heal tests now build v2 sensor JSON
directly. The v1 → v2 fallback path inside sensor.Project() stays covered
by project_test.go alone. Behaviour and assertions unchanged."
```

---

## Task 9: Migration script `scripts/migrate-requires.go` (Commit 3: `feat(scripts)`)

**Goal:** Permanent CLI tool that converts v1 sensor JSON files in place to v2 shape, idempotent on already-v2 files, fail-fast on ambiguity.

**Files:**
- Create: `scripts/migrate-requires.go`
- Create: `scripts/migrate-requires_test.go`

- [ ] **Step 9.1: Confirm `scripts/` directory exists**

```
ls scripts/
```

If empty or missing, the directory will be created with the file. (Already-existing scripts coexist with the new one.)

- [ ] **Step 9.2: Write failing tests first**

Create `scripts/migrate-requires_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func TestConvert_FullV1Sensor(t *testing.T) {
	v1, _ := json.Marshal(map[string]interface{}{
		"id":         "ex",
		"version":    "1.0.0",
		"depends_on": []string{"dep-a", "dep-b"},
		"requires": map[string]interface{}{
			"tools":       []string{"docker"},
			"permissions": []string{"repo:read"},
			"context":     []string{"docs/"},
			"env": []map[string]interface{}{
				{"name": "GH_TOKEN", "description": "PAT", "optional": false},
			},
		},
		"execution": map[string]interface{}{
			"command": "true",
			"prepare": []map[string]interface{}{
				{"command": "cp .env.example .env", "timeout_ms": 1000},
			},
		},
	})
	out, changed, err := convert(v1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true for full v1 input")
	}
	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["depends_on"]; present {
		t.Fatal("depends_on should be removed")
	}
	if exec, _ := got["execution"].(map[string]interface{}); exec != nil {
		if _, present := exec["prepare"]; present {
			t.Fatal("execution.prepare should be removed")
		}
	}
	requires, ok := got["requires"].([]interface{})
	if !ok {
		t.Fatalf("requires should be an array, got %T", got["requires"])
	}
	wantKinds := []string{"sensor", "sensor", "tool", "env", "context", "permission", "step"}
	if len(requires) != len(wantKinds) {
		t.Fatalf("expected %d entries, got %d", len(wantKinds), len(requires))
	}
	for i, kind := range wantKinds {
		entry := requires[i].(map[string]interface{})
		if entry["kind"] != kind {
			t.Fatalf("entry %d: expected kind=%s, got %s", i, kind, entry["kind"])
		}
	}
	if got["version"] != "1.0.1" {
		t.Fatalf("expected version bump to 1.0.1, got %v", got["version"])
	}
}

func TestConvert_AlreadyV2_NoChange(t *testing.T) {
	v2, _ := json.Marshal(map[string]interface{}{
		"id":      "ex",
		"version": "1.0.0",
		"requires": []map[string]interface{}{
			{"kind": "sensor", "id": "dep-a"},
		},
	})
	out, changed, err := convert(v2)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected changed=false for already-v2 input")
	}
	if !bytes.Equal(out, v2) {
		t.Fatal("v2 input should be returned bit-identical")
	}
}

func TestConvert_PartiallyMigrated_Fails(t *testing.T) {
	mixed, _ := json.Marshal(map[string]interface{}{
		"id":         "ex",
		"version":    "1.0.0",
		"depends_on": []string{"a"},
		"requires":   []map[string]interface{}{{"kind": "sensor", "id": "b"}},
	})
	_, _, err := convert(mixed)
	if err == nil {
		t.Fatal("expected error for sensor with both depends_on and v2 requires[]")
	}
}

func TestConvert_StepDeduplicationForbidden(t *testing.T) {
	v1, _ := json.Marshal(map[string]interface{}{
		"id":      "ex",
		"version": "1.0.0",
		"execution": map[string]interface{}{
			"prepare": []map[string]interface{}{
				{"command": "echo same"},
				{"command": "echo same"},
			},
		},
	})
	out, _, err := convert(v1)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	_ = json.Unmarshal(out, &got)
	requires := got["requires"].([]interface{})
	if len(requires) != 2 {
		t.Fatalf("step entries must not dedupe, got %d", len(requires))
	}
}

func TestConvert_EmptyV1_ProducesNoRequiresOrEmptyArray(t *testing.T) {
	v1, _ := json.Marshal(map[string]interface{}{
		"id":      "ex",
		"version": "1.0.0",
	})
	out, changed, err := convert(v1)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("expected changed=false when there is nothing to migrate")
	}
	var got map[string]interface{}
	_ = json.Unmarshal(out, &got)
	if r, ok := got["requires"]; ok {
		if arr, ok := r.([]interface{}); !ok || len(arr) != 0 {
			t.Fatalf("if requires is present it must be empty array, got %v", r)
		}
	}
}

func TestConvert_EmptyDependsOnAndPrepare_ProducesEmptyArray(t *testing.T) {
	v1, _ := json.Marshal(map[string]interface{}{
		"id":         "ex",
		"version":    "1.0.0",
		"depends_on": []string{},
		"execution": map[string]interface{}{
			"prepare": []map[string]interface{}{},
		},
	})
	out, changed, err := convert(v1)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected changed=true (we stripped depends_on and prepare)")
	}
	var got map[string]interface{}
	_ = json.Unmarshal(out, &got)
	arr, ok := got["requires"].([]interface{})
	if !ok || len(arr) != 0 {
		t.Fatalf("expected requires: [], got %v", got["requires"])
	}
}

func TestBumpPatch(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"1.0.0", "1.0.1"},
		{"0.6.3", "0.6.4"},
		{"2.10.99", "2.10.100"},
	}
	for _, tc := range tests {
		got := bumpPatch(tc.in)
		if got != tc.want {
			t.Fatalf("bumpPatch(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// reflect kept for parity with other table-driven tests
var _ = reflect.DeepEqual
```

- [ ] **Step 9.3: Run the failing tests**

```
go test ./scripts/...
```

Expected: FAIL — `convert` and `bumpPatch` not defined.

- [ ] **Step 9.4: Implement `scripts/migrate-requires.go`**

Create `scripts/migrate-requires.go`:

```go
// scripts/migrate-requires.go
//
// One-shot migration tool that converts v1 sensor JSON files to the v2
// requires[] discriminated-union shape defined in schemas/sensor.json.
// Idempotent: already-v2 files are left untouched. Fail-fast on ambiguity
// (sensor mixes v1 and v2 shapes).
//
// Usage:
//
//	migrate-requires <sensor.json>...
//	migrate-requires --root <dir>
//	migrate-requires --dry-run [...]
//
// Exit codes: 0 success, 1 ambiguity / validation failure, 2 usage / I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "print diff to stdout instead of writing")
	root := flag.String("root", "", "walk this directory recursively for sensor JSON files")
	flag.Parse()

	var paths []string
	if *root != "" {
		walked, err := walkSensorJSONs(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "walk:", err)
			os.Exit(2)
		}
		paths = walked
	}
	paths = append(paths, flag.Args()...)

	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: migrate-requires [--root <dir>] [--dry-run] <sensor.json>...")
		os.Exit(2)
	}

	exit := 0
	for _, p := range paths {
		if err := migrateFile(p, *dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
			exit = 1
		}
	}
	os.Exit(exit)
}

func migrateFile(path string, dryRun bool) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changed, err := convert(body)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if dryRun {
		fmt.Printf("--- %s\n+++ %s (migrated)\n%s\n", path, path, string(out))
		return nil
	}
	return os.WriteFile(path, out, 0o644)
}

// convert returns the migrated body, whether anything changed, and any
// ambiguity error. The function is pure (no I/O).
func convert(body []byte) ([]byte, bool, error) {
	var s map[string]interface{}
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}

	hasV2Array := false
	if _, ok := s["requires"].([]interface{}); ok {
		hasV2Array = true
	}

	_, hasDepends := s["depends_on"]
	reqObj, hasReqObj := s["requires"].(map[string]interface{})
	_ = reqObj
	hasPrepare := false
	if exec, ok := s["execution"].(map[string]interface{}); ok {
		if _, ok := exec["prepare"]; ok {
			hasPrepare = true
		}
	}

	hasV1 := hasDepends || hasReqObj || hasPrepare

	if hasV2Array && hasV1 {
		return nil, false, fmt.Errorf("sensor mixes v1 and v2 shapes (refusing to guess)")
	}
	if hasV2Array {
		return body, false, nil // already v2, nothing to do
	}
	if !hasV1 {
		return body, false, nil // nothing to migrate
	}

	// Build the v2 array in stable order: sensor → tool → env → context → permission → step.
	requires := []interface{}{}

	if deps, ok := s["depends_on"].([]interface{}); ok {
		for _, d := range deps {
			if id, ok := d.(string); ok {
				requires = append(requires, map[string]interface{}{"kind": "sensor", "id": id})
			}
		}
	}
	if obj, ok := s["requires"].(map[string]interface{}); ok {
		if tools, ok := obj["tools"].([]interface{}); ok {
			for _, t := range tools {
				if name, ok := t.(string); ok {
					requires = append(requires, map[string]interface{}{"kind": "tool", "name": name})
				}
			}
		}
		if envs, ok := obj["env"].([]interface{}); ok {
			for _, e := range envs {
				em, ok := e.(map[string]interface{})
				if !ok {
					continue
				}
				entry := map[string]interface{}{"kind": "env"}
				for _, k := range []string{"name", "description", "optional"} {
					if v, present := em[k]; present {
						entry[k] = v
					}
				}
				requires = append(requires, entry)
			}
		}
		if ctxs, ok := obj["context"].([]interface{}); ok {
			for _, c := range ctxs {
				if p, ok := c.(string); ok {
					requires = append(requires, map[string]interface{}{"kind": "context", "path": p})
				}
			}
		}
		if perms, ok := obj["permissions"].([]interface{}); ok {
			for _, p := range perms {
				if scope, ok := p.(string); ok {
					requires = append(requires, map[string]interface{}{"kind": "permission", "scope": scope})
				}
			}
		}
	}
	if exec, ok := s["execution"].(map[string]interface{}); ok {
		if steps, ok := exec["prepare"].([]interface{}); ok {
			for _, st := range steps {
				sm, ok := st.(map[string]interface{})
				if !ok {
					continue
				}
				entry := map[string]interface{}{"kind": "step"}
				for _, k := range []string{"command", "timeout_ms", "exit_code_map"} {
					if v, present := sm[k]; present {
						entry[k] = v
					}
				}
				requires = append(requires, entry)
			}
		}
		delete(exec, "prepare")
	}

	delete(s, "depends_on")
	s["requires"] = requires

	if v, ok := s["version"].(string); ok {
		s["version"] = bumpPatch(v)
	}

	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, false, err
	}
	out = append(out, '\n')
	return out, true, nil
}

func bumpPatch(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return v
	}
	// Strip pre-release / build suffix from the patch component before bumping.
	patchOnly := parts[2]
	for i, r := range patchOnly {
		if r != '-' && r != '+' {
			continue
		}
		patchOnly = patchOnly[:i]
		break
	}
	n, err := strconv.Atoi(patchOnly)
	if err != nil {
		return v
	}
	parts[2] = strconv.Itoa(n + 1)
	return strings.Join(parts, ".")
}

func walkSensorJSONs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files silently in walk mode
		}
		var probe map[string]interface{}
		if err := json.Unmarshal(body, &probe); err != nil {
			return nil
		}
		if _, hasID := probe["id"]; !hasID {
			return nil
		}
		if _, hasExec := probe["execution"]; !hasExec {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}
```

- [ ] **Step 9.5: Run the tests to verify they pass**

```
go test ./scripts/... -v
```

Expected: PASS for all test cases.

- [ ] **Step 9.6: Smoke-test the CLI on the existing v1 sensor**

```
go run ./scripts/migrate-requires.go --dry-run sensors/run-with-deps-smoke.json
```

Expected: stdout shows the migrated JSON (with `requires` array containing sensor + step entries, no `depends_on`, no `execution.prepare`, version bumped to `0.1.1`). File on disk is unchanged.

- [ ] **Step 9.7: Commit**

```
git add scripts/migrate-requires.go scripts/migrate-requires_test.go
git commit -m "feat(scripts): add migrate-requires.go to convert v1 sensors to v2 (#10)

scripts/migrate-requires.go is a permanent CLI tool. Idempotent on v2
input, fails fast on mixed v1+v2 shapes, never dedupes step entries,
bumps sensor version patch on actual migrations. Supports --dry-run
and --root <dir> for batch operations. Test coverage is table-driven."
```

---

## Task 10: Run the migrator on `sensors/` (Commit 4: `chore(sensors)`)

**Goal:** Apply the migration to the 13 sensor JSONs in `sensors/`. Only `sensors/run-with-deps-smoke.json` is expected to change (it's the only sensor with v1 fields today); the other 12 should be no-ops because they have no `depends_on`, `requires` object, or `execution.prepare`.

**Files:**
- Modify: `sensors/run-with-deps-smoke.json` (expected — sole sensor with v1 fields)
- (Possibly) Modify: any other sensor the migrator touches; review the diff before committing.

- [ ] **Step 10.1: Dry-run the migrator across the whole directory**

```
go run ./scripts/migrate-requires.go --root sensors/ --dry-run
```

Expected output: a diff for `sensors/run-with-deps-smoke.json` only. If other sensors appear in the output, inspect each one — the spec assumes 12 of 13 are no-ops.

- [ ] **Step 10.2: Run the migrator for real**

```
go run ./scripts/migrate-requires.go --root sensors/
```

Expected: no stdout output, exit 0.

- [ ] **Step 10.3: Inspect the diff**

```
git diff sensors/
```

Verify `run-with-deps-smoke.json` now has:
- `requires: [ { kind: "sensor", id: "setup-touch-file" }, { kind: "step", command: "test -f /tmp/harness-smoke-sentinel", timeout_ms: 1000 } ]`
- No `depends_on` field.
- `execution` block with no `prepare` field but with `teardown` intact and `command`, `exit_code_map`, `output_parsing` intact.
- `version` bumped from `0.1.0` to `0.1.1`.

If any other sensor is modified, inspect it and decide whether to keep or revert (and fix the migrator if the change is wrong).

- [ ] **Step 10.4: Run the schema validator to confirm the migrated sensor is valid v2**

The simplest way: run the existing schema-validate-json sensor against the migrated file.

```
go run -tags=run_computational ./skills/run-sensor/scripts sensors/run-with-deps-smoke.json
```

Expected: an aggregate Signal with `verdict: pass`. (If the test environment lacks the sentinel file, the sensor's prepare step may produce `verdict: fail`. That is acceptable behaviour validation; the schema validation happens upstream.)

Alternative confirmation:

```
go test ./lib/schema/... -v
```

Expected: PASS.

- [ ] **Step 10.5: Run the full test suite**

```
go test ./...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go vet ./...
```

Expected: all green.

- [ ] **Step 10.6: Commit**

```
git add sensors/
git commit -m "chore(sensors): migrate run-with-deps-smoke to v2 requires[] (#10)

Applied scripts/migrate-requires.go to sensors/. Only run-with-deps-smoke
needed conversion (depends_on + execution.prepare → requires[kind=sensor]
+ requires[kind=step]). Version bumped 0.1.0 → 0.1.1. The other 12 sensors
in sensors/ had no v1 fields; the migrator left them untouched."
```

---

## Task 11: Hard cut — drop v1 from schema, add pre-schema sniff, remove fallback (Commit 5: `feat(schema)`)

**Goal:** Schema accepts ONLY v2 array shape. v1 fields (`depends_on`, `execution.prepare`, `requires` as object) are removed from the schema. `lib/schema/validator.go` short-circuits with an actionable message when those fields appear OR when an unknown `kind` appears in `requires[]`. `lib/sensor.Project()` loses its transitional fallback.

**Files:**
- Modify: `schemas/sensor.json`
- Modify: `lib/schema/validator.go`
- Modify: `lib/sensor/project.go`
- Modify: `lib/sensor/project_test.go` (remove the now-removed fallback tests; keep the v2-only and edge-case tests)
- Modify: `lib/schema/validator_test.go` (add cases for the new sniff)
- Modify: `lib/heal/apply.go` (update error-message strings that refer to `requires.context` / `requires.env` to the v2 vocabulary; comment-only)
- Modify: `skills/detect-sensors/SKILL.md` (prose update — v2 vocabulary)

- [ ] **Step 11.1: Write failing tests for the new sniff**

Add to `lib/schema/validator_test.go`:

```go
func TestDetectLegacyShape_DependsOn(t *testing.T) {
	fields, ok := detectLegacyShape([]byte(`{"id":"x","depends_on":["a"]}`))
	if !ok || len(fields) == 0 || fields[0] != "depends_on" {
		t.Fatalf("expected depends_on detected, got %v / ok=%v", fields, ok)
	}
}

func TestDetectLegacyShape_RequiresObject(t *testing.T) {
	fields, ok := detectLegacyShape([]byte(`{"id":"x","requires":{"tools":["docker"]}}`))
	if !ok || len(fields) == 0 || fields[0] != "requires (object)" {
		t.Fatalf("expected requires object detected, got %v / ok=%v", fields, ok)
	}
}

func TestDetectLegacyShape_ExecutionPrepare(t *testing.T) {
	fields, ok := detectLegacyShape([]byte(`{"id":"x","execution":{"prepare":[{"command":"true"}]}}`))
	if !ok || len(fields) == 0 || fields[0] != "execution.prepare" {
		t.Fatalf("expected execution.prepare detected, got %v / ok=%v", fields, ok)
	}
}

func TestDetectLegacyShape_V2Clean(t *testing.T) {
	fields, ok := detectLegacyShape([]byte(`{"id":"x","requires":[{"kind":"sensor","id":"a"}]}`))
	if ok || len(fields) > 0 {
		t.Fatalf("expected no legacy detection, got %v / ok=%v", fields, ok)
	}
}

func TestDetectUnknownKind(t *testing.T) {
	idx, kind, ok := detectUnknownKind([]byte(`{"requires":[{"kind":"foobar"}]}`))
	if !ok || idx != 0 || kind != "foobar" {
		t.Fatalf("expected unknown kind=foobar at index 0, got idx=%d kind=%q ok=%v", idx, kind, ok)
	}
}

func TestDetectUnknownKind_AllKnown(t *testing.T) {
	_, _, ok := detectUnknownKind([]byte(`{"requires":[{"kind":"sensor","id":"a"}]}`))
	if ok {
		t.Fatal("expected no unknown kind for valid input")
	}
}
```

- [ ] **Step 11.2: Run the failing tests**

```
go test ./lib/schema/... -v
```

Expected: FAIL — sniff functions not yet defined.

- [ ] **Step 11.3: Remove v1 fields from `schemas/sensor.json`**

In `schemas/sensor.json`:

1. Remove the top-level `depends_on` property entirely.
2. Replace the existing `requires` property (the `oneOf` from Task 1) with the v2-only form:

```jsonc
"requires": {
  "type": "array",
  "default": [],
  "items": { "$ref": "#/$defs/Requirement" }
}
```

3. In the `execution` block, remove the `prepare` property. The `teardown` property stays.

Keep all `$defs/Require*` and `$defs/Requirement` from Task 1.

- [ ] **Step 11.4: Add the sniff to `lib/schema/validator.go`**

Modify `lib/schema/validator.go`. Add at the bottom of the file:

```go
// detectLegacyShape returns the names of v1 schema fields present in the
// raw sensor JSON. ok is true when at least one is found. Used by Validate
// to short-circuit with an actionable migration message.
func detectLegacyShape(raw []byte) ([]string, bool) {
	var s map[string]interface{}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, false
	}
	var found []string
	if _, ok := s["depends_on"]; ok {
		found = append(found, "depends_on")
	}
	if _, ok := s["requires"].(map[string]interface{}); ok {
		found = append(found, "requires (object)")
	}
	if exec, ok := s["execution"].(map[string]interface{}); ok {
		if _, ok := exec["prepare"]; ok {
			found = append(found, "execution.prepare")
		}
	}
	return found, len(found) > 0
}

// detectUnknownKind returns the index and value of the first requires[]
// entry whose kind is not one of the six known kinds. ok is true when
// found.
func detectUnknownKind(raw []byte) (int, string, bool) {
	var s map[string]interface{}
	if err := json.Unmarshal(raw, &s); err != nil {
		return 0, "", false
	}
	arr, ok := s["requires"].([]interface{})
	if !ok {
		return 0, "", false
	}
	known := map[string]bool{
		"sensor": true, "tool": true, "env": true,
		"context": true, "permission": true, "step": true,
	}
	for i, raw := range arr {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		k, ok := item["kind"].(string)
		if !ok {
			continue
		}
		if !known[k] {
			return i, k, true
		}
	}
	return 0, "", false
}
```

Add `"encoding/json"` to the imports if not already present. Add `"strings"` if not already present.

- [ ] **Step 11.5: Wire the sniff into `Validate`**

The current `Validate` signature is:

```go
func (v *Validator) Validate(target Target, instance interface{}) error
```

`instance` is `map[string]interface{}`, not raw bytes. Adjust by adding a new method `ValidateBytes` that runs the sniff before the JSON Schema check, and have all sensor-validating callers go through it. Or — simpler — add the sniff to `Validate` when `target == TargetSensor` by re-marshaling the instance.

Use the simpler approach. Replace the `Validate` body:

```go
func (v *Validator) Validate(target Target, instance interface{}) error {
	switch target {
	case TargetSensor:
		raw, _ := json.Marshal(instance)
		if fields, ok := detectLegacyShape(raw); ok {
			id := "<unknown>"
			if m, mok := instance.(map[string]interface{}); mok {
				if s, sok := m["id"].(string); sok {
					id = s
				}
			}
			return fmt.Errorf(
				"sensor %s uses v1 schema fields (%s).\nRun `go run ./scripts/migrate-requires.go <path>` to upgrade to v2.",
				id, strings.Join(fields, ", "),
			)
		}
		if idx, k, ok := detectUnknownKind(raw); ok {
			id := "<unknown>"
			if m, mok := instance.(map[string]interface{}); mok {
				if s, sok := m["id"].(string); sok {
					id = s
				}
			}
			return fmt.Errorf(
				"sensor %s requires[%d] has unknown kind %q. Valid kinds: sensor, tool, env, context, permission, step.",
				id, idx, k,
			)
		}
		return v.sensor.Validate(instance)
	case TargetSignal:
		return v.signal.Validate(instance)
	default:
		return fmt.Errorf("unknown target %q", target)
	}
}
```

- [ ] **Step 11.6: Remove the transitional fallback from `lib/sensor/project.go`**

In `lib/sensor/project.go`:

1. Inline `asV2Array` into `Project` since synthesis is gone:

```go
func Project(sensor map[string]interface{}, kind string) []map[string]interface{} {
	arr, ok := sensor["requires"].([]interface{})
	if !ok || len(arr) == 0 {
		return nil
	}
	var out []map[string]interface{}
	for _, raw := range arr {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		k, ok := item["kind"].(string)
		if !ok || k != kind {
			continue
		}
		out = append(out, item)
	}
	return out
}
```

2. Delete `asV2Array` and `synthesizeV2` entirely.

3. Update the doc comment to drop the TRANSITIONAL note.

- [ ] **Step 11.7: Trim `lib/sensor/project_test.go`**

Delete the `TestProject_V1Fallback_*` and `TestProject_V1Fallback_CombinedDoesNotDuplicate` tests. Keep:

- `TestProject_V2Array_AllKinds`
- `TestProject_V2Array_PreservesOrder`
- `TestProject_V2Array_SkipsMalformed`
- `TestProject_EmptyAndMissing`
- `TestProject_UnknownKindReturnsNil`

The reflect blank import line can also be removed if it stops being referenced.

- [ ] **Step 11.8: Update `lib/heal/apply.go` error message strings**

In `lib/heal/apply.go`, find the four error-message strings:

```
"dir not under requires.context"
"file not under requires.context"
"var " + a.Name + " not in requires.env"
```

and the comment "requires.* surfaces."

Replace with:

```
"dir not under requires[kind=context]"
"file not under requires[kind=context]"
"var " + a.Name + " not in requires[kind=env]"
```

And update the comment "requires.* surfaces." → "requires[] surfaces (kind=env, kind=context, etc)."

These are strings, not code paths — no functional change. Adjust accordingly if the format of the messages must remain stable (search for any test that asserts exact text):

```
grep -nE 'requires\.context|requires\.env' lib/heal/apply_test.go lib/heal/plan_test.go
```

If a test asserts the exact string, update both sides.

- [ ] **Step 11.9: Update `skills/detect-sensors/SKILL.md`**

Open the file and search for every reference to the v1 vocabulary:

- `depends_on` → `requires[kind=sensor]`
- `requires.tools` → `requires[kind=tool]`
- `requires.permissions` → `requires[kind=permission]`
- `requires.context` → `requires[kind=context]`
- `requires.env` → `requires[kind=env]`
- `execution.prepare` / `prepare[]` → `requires[kind=step]`

Adjust the prose flow as needed; the goal is to remove every v1 reference. Where the document explains the previous overlap between `depends_on` and `execution.prepare`, simplify since v2 unifies them.

- [ ] **Step 11.10: Run every test**

```
go test ./...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go vet ./...
```

Expected: all green. Specifically:

- The schema rejects any sensor with `depends_on`, `requires` as object, or `execution.prepare`.
- `Project()` no longer synthesizes — every test that exercised the fallback either was deleted (project_test) or already passes because Task 8 rewrote the fixtures to v2.
- The single migrated sensor (`run-with-deps-smoke.json`) validates cleanly.
- The heal classifier behaviour is unchanged because `FailedSensor` shape stayed identical.

- [ ] **Step 11.11: Commit**

```
git add schemas/sensor.json lib/schema/validator.go lib/schema/validator_test.go \
  lib/sensor/project.go lib/sensor/project_test.go \
  lib/heal/apply.go skills/detect-sensors/SKILL.md
git commit -m "feat(schema): hard-cut v1 fields; v2 requires[] is the only shape (#10)

- schemas/sensor.json drops depends_on, execution.prepare, and the v1
  requires object branch; only the v2 array shape is accepted.
- lib/schema/validator.go gains a pre-schema sniff that emits an actionable
  migration message for v1 fields and a kind-listing message for unknown
  requires[].kind values.
- lib/sensor.Project() loses its transitional fallback (Project is now a
  thin filter over requires[]).
- lib/heal/apply.go error strings and skills/detect-sensors/SKILL.md prose
  updated to the v2 vocabulary.

This is the hard-cut commit. Sensors on disk must be v2 from this point."
```

---

## Task 12: Bump plugin version, write CHANGELOG (Commit 6: `chore(version)`)

**Goal:** Bump `.claude-plugin/plugin.json` to `1.0.0`. Create `CHANGELOG.md` documenting the breaking change, the migration command, and the v1 → v2 mapping.

**Files:**
- Modify: `.claude-plugin/plugin.json`
- Create: `CHANGELOG.md`

- [ ] **Step 12.1: Bump `.claude-plugin/plugin.json`**

Open `.claude-plugin/plugin.json` and change `"version": "0.6.0"` to `"version": "1.0.0"`. No other change.

- [ ] **Step 12.2: Write `CHANGELOG.md`**

Create `CHANGELOG.md` at the repo root with the following content:

```markdown
# Changelog

## 1.0.0 — 2026-05-11

### Breaking changes

`schemas/sensor.json` v2: `depends_on`, `requires.{tools,permissions,context,env}`, and `execution.prepare[]` are replaced by a single `requires[]` array of discriminated-union elements keyed by `kind`. Six kinds are accepted: `sensor`, `tool`, `env`, `context`, `permission`, `step`.

Refs: issue #10.

### Migration

For each project that ships sensor JSON files, run the migration tool from the harness-framework checkout:

```
go run ./scripts/migrate-requires.go --root sensors/
```

The tool is idempotent (already-v2 files are left untouched), fail-fast on ambiguity, and never dedupes step entries. It bumps each migrated sensor's `version` patch.

### v1 → v2 mapping

| v1 | v2 |
|---|---|
| `depends_on: ["id1", "id2"]` | `requires: [{kind:"sensor", id:"id1"}, {kind:"sensor", id:"id2"}]` |
| `requires.env: [{name, description, optional}]` | `requires: [{kind:"env", name, description, optional}]` |
| `requires.tools: ["docker"]` | `requires: [{kind:"tool", name:"docker"}]` |
| `requires.context: ["docs/"]` | `requires: [{kind:"context", path:"docs/"}]` |
| `requires.permissions: ["repo:read"]` | `requires: [{kind:"permission", scope:"repo:read"}]` |
| `execution.prepare: [{command, timeout_ms?, exit_code_map?}]` | `requires: [{kind:"step", command, timeout_ms?, exit_code_map?}]` |

### Runtime behaviour

Unchanged. Lifecycle phases, fail-fast semantics, cascade rules, teardown finally-semantics, exit code mapping, and Signal output all remain bit-identical. Only the schema shape and the consumer read path change. The Signal-side `metadata.lifecycle.prepare` key keeps its name (it is a phase name, not the schema field name).

### Validator

`lib/schema/validator.go` rejects v1 sensors with an actionable message naming the migration script. Unknown `requires[].kind` values produce a message listing the six valid kinds.
```

- [ ] **Step 12.3: Run the full test suite once more**

```
go test ./...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
go vet ./...
```

Expected: all green.

- [ ] **Step 12.4: Commit**

```
git add .claude-plugin/plugin.json CHANGELOG.md
git commit -m "chore: bump plugin to 1.0.0 and document the v1→v2 break (#10)

CHANGELOG documents the breaking schema change, points readers at
scripts/migrate-requires.go, and includes the v1 → v2 mapping at a glance.
plugin.json bumped 0.6.0 → 1.0.0 to signal the breaking change."
```

---

## Self-review

Run through this list before marking the plan done.

- **Spec coverage:** every acceptance-criterion checkbox in spec §14 maps to at least one task above. The schema v2 + validator integration is Task 1. The `Project()` helper is Task 2. Consumers (orchestrator/dag, lifecycle, env, hook, runner skills) are Tasks 3–7. Test fixtures are Task 8. Migration script is Task 9. Sensor migration is Task 10. Hard cut + sniff + heal error strings + SKILL.md docs are Task 11. Plugin version + CHANGELOG is Task 12. CI green at every commit is implicit because every task ends with a green test run before commit.
- **Placeholder scan:** no "TBD", no "implement later", no "similar to Task N". Every code-change step contains the actual code.
- **Type consistency:** `Project(sensor map[string]interface{}, kind string) []map[string]interface{}` signature is consistent across Tasks 2, 3, 4, 5, 6, 7. `convert(body []byte) ([]byte, bool, error)` and `bumpPatch(string) string` are consistent across Task 9. `detectLegacyShape(raw []byte) ([]string, bool)` and `detectUnknownKind(raw []byte) (int, string, bool)` are consistent across Task 11. `runPreparePhase` / `runTeardownPhase` signatures introduced in Task 4 are consistent with the call-site changes there and the indirect call from `skills/start-sensor/scripts/start.go` in Task 7.
