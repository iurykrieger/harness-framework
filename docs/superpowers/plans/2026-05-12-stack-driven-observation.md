# Stack-driven observation sensors implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move regex-against-raw-JSON out of sensor authors' heads and into a project-specific Stack artifact synthesized by `/detect-sensors`. Consolidate all framework on-disk artifacts under a single `.harness/` namespace.

**Architecture:** New entity-schema `schemas/stack.json` describes a project's languages, components, and canonical stdout shapes. New `lib/stack/` package owns load/persist/lookup/shape helpers. New `skills/detect-sensors/scripts/write-stack.go` validates and persists. Layout migration moves `<project>/sensors/` → `<project>/.harness/sensors/` and `<project>/.runtime/sensors/` → `<project>/.harness/runtime/` with no fallback. `/detect-sensors` skill prose gains a two-phase flow: Phase A discovers the stack; Phase B derives `output_parsing.patterns[]` for observation sensors from the stack's `log_shapes[]`.

**Tech Stack:** Go 1.25, JSON Schema Draft 2020-12, `github.com/santhosh-tekuri/jsonschema/v5`, the framework's own `lib/registry`, `lib/sensor`, `lib/schema` packages. No new dependencies.

**Spec:** [docs/superpowers/specs/2026-05-12-stack-driven-observation-design.md](../specs/2026-05-12-stack-driven-observation-design.md)

---

## File structure

Files created or modified, grouped by phase. Each file has one responsibility — load, persist, lookup, shape-helpers are kept as separate files so each can be held in context at once during edits.

### Phase 1 — schema + lib/stack/ + write-stack.go + write_sensor build tag

| Path | Verb | Responsibility |
|---|---|---|
| `schemas/stack.json` | Already exists (from spec PR) | The Draft 2020-12 schema for the new Stack entity. |
| `lib/stack/load.go` | Create | `LoadStackFile(path, schemasDir, stderr) (Stack, int)` — read, parse, validate. Mirrors `lib/sensor/load.go`. |
| `lib/stack/load_test.go` | Create | Table-driven tests over fixtures: valid → decodes; missing required → schema error; bad enum → schema error. |
| `lib/stack/persist.go` | Create | `ValidateAndPersist(stackJSON, projectRoot, schemasDir) (path, error)` — validate, atomic write via temp+rename. Mirrors `lib/sensor/persist.go`. |
| `lib/stack/persist_test.go` | Create | Round-trip, idempotency, atomic-write observation, 0o644 permissions. |
| `lib/stack/lookup.go` | Create | `Lookup(startDir) (Result, error)` — discover project root via `lib/registry.Discover`, return `{Path, Exists, Stack}`. |
| `lib/stack/lookup_test.go` | Create | Project with stack → Exists=true; without → Exists=false; HARNESS_REGISTRY_ROOT override honored. |
| `lib/stack/shape.go` | Create | Typed struct + accessors: `Stack.ShapesByRole`, `Stack.ShapesProducedBy`, `LogShape.FieldsByMeaning`, `LogShape.HasSeverity`. |
| `lib/stack/shape_test.go` | Create | Table-driven over a multi-shape fixture. |
| `lib/stack/testdata/golden-stack.json` | Create | Hand-written valid Stack used by load_test, persist_test, shape_test. |
| `lib/stack/testdata/invalid-missing-required.json` | Create | Same shape with a required field deleted. |
| `lib/stack/testdata/invalid-enum.json` | Create | Same shape with an out-of-enum `format` or `role`. |
| `lib/stack/testdata/invalid-produced-by-orphan.json` | Create | Stack whose `log_shapes[].produced_by[]` references a component name that does not exist. |
| `skills/detect-sensors/scripts/write-sensor.go` | Modify | Add `//go:build write_sensor` as line 1. Prerequisite for write-stack.go to coexist. |
| `skills/detect-sensors/scripts/write-stack.go` | Create | CLI: `write-stack --out=<project-root> [--schemas-dir=<dir>] <stack-payload.json>`. Validates, cross-checks produced_by integrity, persists. |
| `skills/detect-sensors/scripts/write-stack_test.go` | Create | Happy path, missing required, produced_by orphan, idempotency. |
| `skills/detect-sensors/SKILL.md` | Modify (one line) | Update `go run` invocation for `write-sensor` to pass `-tags=write_sensor`. |

### Phase 2 — Layout migration to `.harness/`

| Path | Verb | Responsibility |
|---|---|---|
| `lib/registry/root.go` | Modify | `markerDir` constant changes from `"sensors"` to `".harness"`. Doc comments update. |
| `lib/registry/paths.go` | Modify | `SensorsDir()` returns `<root>/.harness/runtime/` (the directory holding `running_sensors.json` and per-sensor subdirs). Add `SensorFile(id)` returning `<root>/.harness/sensors/<id>.json`. Add `RelativeRunDir(id, runID)` returning `.harness/runtime/<id>/<runID>` (used by orchestrator's registry-entry inserts). |
| `lib/registry/root_test.go` | Modify | Walk-up fixtures use `.harness/` directory marker (not `sensors/`). |
| `lib/registry/paths_test.go` | Modify | All path-expectation strings migrated. New tests for `SensorFile` and `RelativeRunDir`. |
| `lib/registry/lookup_test.go` | Modify | Fixtures migrate to `.harness/` layout. |
| `lib/registry/sanitize_test.go`, `state_test.go`, `held_by_test.go`, `liveness_test.go`, `lock_test.go` | Modify | Test fixtures using `.runtime/` paths migrate to `.harness/runtime/`. |
| `lib/sensor/path.go` | Modify | `ResolveByID(id, baseDir)` joins `.harness/sensors/<id>.json` instead of `sensors/<id>.json`. |
| `lib/sensor/path_test.go` | Modify | Test paths migrate. |
| `lib/orchestrator/run.go` | Modify | Doc comments update path references (`<projectRoot>/.harness/sensors/<id>.json`, `.harness/runtime/<id>/<run-id>/`). |
| `lib/orchestrator/lifecycle.go` | Modify | Line 350: replace `filepath.Join(".runtime", "sensors", envelope.SensorID, runID)` with `r.RelativeRunDir(envelope.SensorID, runID)` (call on the orchestrator's `registry.Root`). |
| `lib/orchestrator/live_deps.go` | Modify | Line 31 (doc), line 222 (same `filepath.Join` replacement as above). |
| `lib/orchestrator/cascade.go` | Modify | Line 46: replace `fmt.Sprintf("sensors/%s.json", failedID)` with `filepath.Join(".harness", "sensors", failedID+".json")` (relative path for evidence.file; not absolute). |
| `lib/orchestrator/*_test.go` | Modify | Path assertion strings migrated. |
| `hooks/error-issue-autofiler.go` | Modify | Line 190: replace `filepath.Join(res.ProjectRoot, ".runtime", "auto-issues.json")` with `filepath.Join(res.ProjectRoot, ".harness", "runtime", "auto-issues.json")`. |
| `skills/start-sensor/SKILL.md` | Modify | Frontmatter description + body prose: `.runtime/sensors/...` → `.harness/runtime/...`, `sensors/<id>.json` → `.harness/sensors/<id>.json`. |
| `skills/stop-sensor/SKILL.md` | Modify | Same pattern. |
| `skills/list-sensors/SKILL.md` | Modify | Same pattern. |
| `skills/tail-sensor/SKILL.md` | Modify | Same pattern. |
| `skills/run-sensor/SKILL.md` | Modify | Same pattern (note: `sensors/` directory mentions in dep-resolution prose too). |
| `skills/start-sensor/scripts/start_test.go`, `stop-sensor/scripts/stop_test.go`, `list-sensors/scripts/list_test.go`, `tail-sensor/scripts/tail_test.go`, `run-sensor/scripts/*_test.go` | Modify | Test fixtures migrate. |
| `.gitignore` | Modify | Replace `.runtime/` with `.harness/runtime/`. Do NOT add `.harness/sensors/` or `.harness/stack.json`. |
| `CLAUDE.md` | Modify | "Registry root discovery" section: all path references migrate; walk-up sentinel `.harness/`. "Auto issue opening" section: cache path migrates. Project rule §2 acknowledges three entity schemas (`sensor.json`, `signal.json`, `stack.json`). |
| `CHANGELOG.md` | Modify | New top entry documenting the breaking layout change with the `git mv` recipe. |
| `<repo>/sensors/` → `<repo>/.harness/sensors/` | Migrate (git mv) | Dogfood: the framework's own sensors land in the new layout. |
| `<repo>/.runtime/` | Skip if absent in worktree | If present, `git mv .runtime .harness/runtime`. Worktree is clean today, so likely no-op. |

### Phase 3 — `/detect-sensors` skill prose update

| Path | Verb | Responsibility |
|---|---|---|
| `skills/detect-sensors/SKILL.md` | Modify | Insert §0 "Stack discovery" before existing §1. Insert §0.5 degraded path. Expand §1 to also read `schemas/stack.json`. Modify §4 to branch on `kind=observation` + `output=stream` and direct the LLM to consult `.harness/stack.json#/log_shapes[]`. Modify §7 iteration loop to mention `bat .harness/stack.json` + `--refresh-stack` remediation. |

### Phase 4 — End-to-end fixture

| Path | Verb | Responsibility |
|---|---|---|
| `test/fixtures/stack-discovery/cmd/server/main.go` | Create | Tiny Go HTTP service using `go.uber.org/zap` + chi middleware. One endpoint reacting to a `?status=` query param to emit 200/400/500. |
| `test/fixtures/stack-discovery/go.mod` | Create | Standalone module for the fixture so it doesn't pollute the framework's module graph. |
| `test/fixtures/stack-discovery/go.sum` | Create | Generated by `go mod tidy`. |
| `test/fixtures/stack-discovery/expected-stdout.log` | Create | 8–10 stdout lines captured from running the fixture: boot marker + 3× 200, 2× 400, 1× 500, plus one ERROR line for completeness. |
| `test/fixtures/stack-discovery/expected-stack.json` | Create | Hand-written valid Stack that a correct Phase A would produce for this fixture: components for zap + zapcore + chi/middleware; one `log_shape` per distinct emit shape. |
| `lib/stack/e2e_fixture_test.go` | Create | Loads `expected-stack.json` via `lib/stack.LoadStackFile`, runs a deterministic helper that derives regex patterns from `log_shapes[]` (mirroring Phase B prose), and asserts the derived patterns collectively match every line in `expected-stdout.log` with the expected verdict distribution. |

---

## Conventions used throughout this plan

- **Working directory:** the worktree at `/Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/observation-signals/`. All `go test` / `go run` commands assume that cwd unless stated otherwise.
- **Branch:** `worktree-observation-signals` (already checked out).
- **Build tags:** new scripts/binaries follow the existing pattern in `skills/run-sensor/scripts/` — one `//go:build <tag>` per file. Tests inside the same dir use the same tag.
- **Commit style:** matches recent history (`type(scope): short summary` + body). Co-author trailer included. No `--no-verify`.
- **TDD:** every implementation step is preceded by a failing test step and followed by a "verify passes" step. Mechanical edits (changing string literals across files) collapse the test step into "verify existing tests still pass after the edit".

---

## Phase 1 — Schema + lib/stack/ + write-stack.go + write_sensor build tag

Goal: ship `lib/stack/` and `write-stack.go` as independently-mergeable additions. No consumers yet. Phase 1 alone must build and test green.

### Task 1.1: Add `write_sensor` build tag to existing `write-sensor.go`

This is the prerequisite for Phase 1. Without it, adding `write-stack.go` (also `package main`) breaks the default build.

**Files:**
- Modify: `skills/detect-sensors/scripts/write-sensor.go` (top of file)
- Modify: `skills/detect-sensors/SKILL.md` (one `go run` invocation line)

- [ ] **Step 1: Verify current build works**

Run:
```bash
go build ./skills/detect-sensors/scripts/...
```
Expected: builds cleanly (no errors).

- [ ] **Step 2: Add the build tag to `write-sensor.go`**

Insert `//go:build write_sensor` as the very first line of `skills/detect-sensors/scripts/write-sensor.go`, followed by a blank line. The existing file currently starts with `// Command write-sensor reads a draft sensor JSON file...`. After this change the first three lines must be:

```go
//go:build write_sensor

// Command write-sensor reads a draft sensor JSON file and persists it
```

- [ ] **Step 3: Verify untagged build no longer compiles this file**

Run:
```bash
go build ./skills/detect-sensors/scripts/...
```
Expected: succeeds, because with the tag and no peer `package main` files, Go has nothing to compile in this dir on the default tag set.

Run:
```bash
go build -tags=write_sensor ./skills/detect-sensors/scripts/...
```
Expected: succeeds; `write-sensor` binary builds.

- [ ] **Step 4: Update SKILL.md `go run` invocation**

Open `skills/detect-sensors/SKILL.md`. Find the `go run ./skills/detect-sensors/scripts ...` invocation in the body and add `-tags=write_sensor`. The line should change from:

```bash
go run ./skills/detect-sensors/scripts --out=<dir> <draft-sensor.json>
```

to:

```bash
go run -tags=write_sensor ./skills/detect-sensors/scripts --out=<dir> <draft-sensor.json>
```

If there is more than one invocation (e.g. in different examples), update each.

- [ ] **Step 5: Verify existing write-sensor tests still pass under the tag**

Run:
```bash
go test -tags=write_sensor ./skills/detect-sensors/scripts/...
```
Expected: all existing tests pass.

- [ ] **Step 6: Commit**

```bash
git add skills/detect-sensors/scripts/write-sensor.go skills/detect-sensors/SKILL.md
git commit -m "$(cat <<'EOF'
build: add write_sensor build tag to write-sensor.go

Prerequisite for landing skills/detect-sensors/scripts/write-stack.go in
the next task. Two package main files in the same directory require
mutually-exclusive build tags; otherwise Go compiles both as the default
target and fails with duplicate main.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.2: Create `lib/stack/load.go` with typed structs

**Files:**
- Create: `lib/stack/load.go`
- Create: `lib/stack/load_test.go`
- Create: `lib/stack/testdata/golden-stack.json`
- Create: `lib/stack/testdata/invalid-missing-required.json`
- Create: `lib/stack/testdata/invalid-enum.json`

- [ ] **Step 1: Create the golden Stack fixture**

Create `lib/stack/testdata/golden-stack.json`:

```json
{
  "version": "0.1.0",
  "detected_at": "2026-05-12T10:00:00Z",
  "detected_by": "manual",
  "languages": [
    { "name": "go", "version": "1.25" }
  ],
  "components": [
    {
      "role": "logger",
      "name": "go.uber.org/zap",
      "version": "1.27.0",
      "config_summary": "zap.NewProductionConfig() with EncoderConfig.LevelKey overridden to severity and TimeKey to ts_ms.",
      "evidence": [
        { "file": "cmd/server/main.go", "line_start": 42, "line_end": 50, "rationale": "zap.NewProductionConfig() call site" }
      ]
    },
    {
      "role": "http-middleware",
      "name": "github.com/go-chi/chi/middleware",
      "version": "5.0.0",
      "config_summary": "middleware.Logger registered before all routes.",
      "evidence": [
        { "file": "cmd/server/main.go", "line_start": 65, "line_end": 65, "rationale": "r.Use(middleware.Logger)" }
      ]
    }
  ],
  "log_shapes": [
    {
      "id": "zap-prod-json",
      "produced_by": ["go.uber.org/zap"],
      "format": "json",
      "fields": [
        { "key": "severity", "meaning": "severity", "example_values": ["DEBUG", "INFO", "WARN", "ERROR"] },
        { "key": "ts_ms",    "meaning": "timestamp" },
        { "key": "msg",      "meaning": "message" },
        { "key": "caller",   "meaning": "other" }
      ],
      "severity_values": ["DEBUG", "INFO", "WARN", "ERROR", "DPANIC", "PANIC", "FATAL"],
      "sample": "{\"severity\":\"INFO\",\"ts_ms\":1700000000000,\"msg\":\"server listening\",\"caller\":\"main.go:80\"}"
    },
    {
      "id": "chi-access-log",
      "produced_by": ["github.com/go-chi/chi/middleware"],
      "format": "combined-log-format",
      "sample": "127.0.0.1 - - [12/May/2026:10:00:00 +0000] \"GET /health HTTP/1.1\" 200 12 \"-\" \"curl/8.0\""
    }
  ]
}
```

- [ ] **Step 2: Create the invalid-missing-required fixture**

Create `lib/stack/testdata/invalid-missing-required.json` — identical to the golden but with the top-level `version` field deleted (a top-level required field):

```json
{
  "detected_at": "2026-05-12T10:00:00Z",
  "detected_by": "manual",
  "languages": [{ "name": "go", "version": "1.25" }],
  "components": [
    {
      "role": "logger",
      "name": "go.uber.org/zap",
      "evidence": [{ "file": "main.go", "rationale": "x" }]
    }
  ],
  "log_shapes": [
    {
      "id": "zap-prod-json",
      "produced_by": ["go.uber.org/zap"],
      "format": "json",
      "sample": "{}"
    }
  ]
}
```

- [ ] **Step 3: Create the invalid-enum fixture**

Create `lib/stack/testdata/invalid-enum.json` — golden but with `log_shapes[0].format` set to `"yaml"` (not in the enum):

```json
{
  "version": "0.1.0",
  "detected_at": "2026-05-12T10:00:00Z",
  "detected_by": "manual",
  "languages": [{ "name": "go", "version": "1.25" }],
  "components": [
    {
      "role": "logger",
      "name": "go.uber.org/zap",
      "evidence": [{ "file": "main.go", "rationale": "x" }]
    }
  ],
  "log_shapes": [
    {
      "id": "zap-prod-json",
      "produced_by": ["go.uber.org/zap"],
      "format": "yaml",
      "sample": "{}"
    }
  ]
}
```

- [ ] **Step 4: Write the failing test for `LoadStackFile`**

Create `lib/stack/load_test.go`:

```go
package stack

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestLoadStackFile(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		wantCode int
		wantSubstr string // expected fragment in stderr when wantCode != 0
	}{
		{name: "golden", fixture: "golden-stack.json", wantCode: 0},
		{name: "missing required", fixture: "invalid-missing-required.json", wantCode: 1, wantSubstr: "version"},
		{name: "bad enum", fixture: "invalid-enum.json", wantCode: 1, wantSubstr: "format"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, _, code := LoadStackFile(filepath.Join("testdata", tc.fixture), "", &stderr)
			if code != tc.wantCode {
				t.Fatalf("code = %d, want %d (stderr: %s)", code, tc.wantCode, stderr.String())
			}
			if tc.wantSubstr != "" && !bytes.Contains(stderr.Bytes(), []byte(tc.wantSubstr)) {
				t.Fatalf("stderr %q missing %q", stderr.String(), tc.wantSubstr)
			}
		})
	}
}
```

- [ ] **Step 5: Verify the test fails to compile**

Run:
```bash
go test ./lib/stack/...
```
Expected: fails with "undefined: LoadStackFile" (or similar — the package doesn't exist yet).

- [ ] **Step 6: Implement `LoadStackFile`**

Create `lib/stack/load.go`:

```go
// Package stack owns the project-level Stack artifact: the LLM-derived
// description of a project's languages, components, and canonical stdout
// shapes. Mirrors lib/sensor's load/persist/lookup pattern.
package stack

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// LoadStackFile reads, parses, and schema-validates a stack JSON file at
// path. Returns the decoded map, the resolved absolute path, and an exit
// code: 0 success, 1 schema validation failure, 2 I/O or parse failure.
func LoadStackFile(path, schemasDir string, stderr io.Writer) (map[string]interface{}, string, int) {
	if path == "" {
		fmt.Fprintln(stderr, "error: empty path")
		return nil, "", 2
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		fmt.Fprintln(stderr, "error: resolve:", err)
		return nil, "", 2
	}
	v, code := schema.LoadValidator(schemasDir, stderr)
	if code != 0 {
		return nil, "", code
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return nil, "", 2
	}
	var s map[string]interface{}
	if err := json.Unmarshal(body, &s); err != nil {
		fmt.Fprintln(stderr, "error: parse:", err)
		return nil, "", 2
	}
	if err := v.Validate(schema.TargetStack, s); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return nil, "", 1
	}
	return s, abs, 0
}
```

- [ ] **Step 7: Register the new schema target in `lib/schema`**

The `schema.TargetStack` constant doesn't exist yet. Open `lib/schema/validate.go` (or whichever file declares `TargetSensor` and `TargetSignal` — find it with `grep -rn "TargetSensor" lib/schema/`). Add `TargetStack` to the existing `Target` type/enum and the validator's compile map. Run:

```bash
grep -rn "TargetSensor" lib/schema/
```

Expected output identifies the file. Open it and add the equivalent entry for `stack.json`:

If the file has:

```go
const (
    TargetSensor Target = "sensor"
    TargetSignal Target = "signal"
)
```

change to:

```go
const (
    TargetSensor Target = "sensor"
    TargetSignal Target = "signal"
    TargetStack  Target = "stack"
)
```

Find the file-name-to-target mapping (likely a switch or map keyed by `Target` returning `"sensor.json"`, `"signal.json"`) and add the `TargetStack` → `"stack.json"` entry. Mirror exactly how the existing entries are spelled.

- [ ] **Step 8: Verify the test now passes**

Run:
```bash
go test ./lib/stack/...
```
Expected: all three subtests pass.

- [ ] **Step 9: Commit**

```bash
git add lib/stack/ lib/schema/
git commit -m "$(cat <<'EOF'
feat(stack): add LoadStackFile + register TargetStack in lib/schema

Mirror lib/sensor.LoadAndValidateSensor: read, parse, schema-validate.
Decoded value is returned as map[string]interface{} for compatibility
with downstream validators that consume generic maps.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.3: `lib/stack/persist.go` — atomic write with validation

**Files:**
- Create: `lib/stack/persist.go`
- Create: `lib/stack/persist_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/stack/persist_test.go`:

```go
package stack

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateAndPersist_Golden(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	root := t.TempDir()
	target, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	want := filepath.Join(root, ".harness", "stack.json")
	if target != want {
		t.Fatalf("target = %q, want %q", target, want)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Round-trip
	out, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var a, b map[string]interface{}
	_ = json.Unmarshal(body, &a)
	_ = json.Unmarshal(out, &b)
	if ja, _ := json.Marshal(a); !bytes.Equal(ja, mustMarshal(b)) {
		t.Fatalf("round-trip mismatch")
	}
}

func TestValidateAndPersist_Idempotent(t *testing.T) {
	body, _ := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	root := t.TempDir()
	target1, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	first, _ := os.ReadFile(target1)
	target2, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	second, _ := os.ReadFile(target2)
	if !bytes.Equal(first, second) {
		t.Fatalf("idempotency: bytes differ across writes")
	}
}

func TestValidateAndPersist_SchemaFail(t *testing.T) {
	body, _ := os.ReadFile(filepath.Join("testdata", "invalid-missing-required.json"))
	root := t.TempDir()
	_, err := ValidateAndPersist(body, root, "")
	if err == nil {
		t.Fatal("expected schema error, got nil")
	}
	// Confirm no file written
	if _, statErr := os.Stat(filepath.Join(root, ".harness", "stack.json")); statErr == nil {
		t.Fatal("expected no file on disk after validation failure")
	}
}

func TestValidateAndPersist_Permissions(t *testing.T) {
	body, _ := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	root := t.TempDir()
	target, err := ValidateAndPersist(body, root, "")
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	info, _ := os.Stat(target)
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Fatalf("perm = %o, want 0o644", perm)
	}
}

func mustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
```

- [ ] **Step 2: Verify the test fails**

Run:
```bash
go test ./lib/stack/... -run TestValidateAndPersist
```
Expected: fails with "undefined: ValidateAndPersist".

- [ ] **Step 3: Implement `ValidateAndPersist`**

Create `lib/stack/persist.go`:

```go
package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// ValidateAndPersist validates stackJSON against schemas/stack.json and,
// on success, writes a canonicalised copy (2-space indent) to
// <projectRoot>/.harness/stack.json. Returns the absolute path on success.
//
// The function is idempotent: writing the same payload twice produces a
// byte-identical file. It does NOT mutate stackJSON.
func ValidateAndPersist(stackJSON []byte, projectRoot string, schemasDir string) (string, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(stackJSON, &m); err != nil {
		return "", fmt.Errorf("parse stack JSON: %w", err)
	}

	v, err := schema.NewValidator(resolveSchemasDir(schemasDir))
	if err != nil {
		return "", fmt.Errorf("load schemas: %w", err)
	}
	if err := v.Validate(schema.TargetStack, m); err != nil {
		return "", err
	}

	outDir := filepath.Join(projectRoot, ".harness")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	target := filepath.Join(outDir, "stack.json")
	if err := writeCanonical(target, m); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	return target, nil
}

func writeCanonical(path string, v map[string]interface{}) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func resolveSchemasDir(in string) string {
	if in != "" {
		return in
	}
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

- [ ] **Step 4: Verify tests pass**

Run:
```bash
go test ./lib/stack/...
```
Expected: all `TestValidateAndPersist_*` tests pass.

- [ ] **Step 5: Commit**

```bash
git add lib/stack/persist.go lib/stack/persist_test.go
git commit -m "$(cat <<'EOF'
feat(stack): atomic write via ValidateAndPersist

Mirror lib/sensor.ValidateAndPersist: validate against stack.json,
write-temp-then-rename, 0o644, idempotent. Writes to
<projectRoot>/.harness/stack.json.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.4: `lib/stack/lookup.go` — discover and load

**Files:**
- Create: `lib/stack/lookup.go`
- Create: `lib/stack/lookup_test.go`

NOTE: Phase 1 lands `lookup.go` that resolves to `<root>/.harness/stack.json`. The `lib/registry.Lookup` it calls still walks up looking for `sensors/` — that change is Phase 2's responsibility. For Phase 1's tests, the fixture sets `HARNESS_REGISTRY_ROOT` directly to a temp dir that contains `<tmp>/sensors/` (existing marker) plus `<tmp>/.harness/stack.json`. After Phase 2 the marker is `.harness/`, so Phase 2 will adjust this test's fixture setup. Document the dependency in a code comment.

- [ ] **Step 1: Write the failing test**

Create `lib/stack/lookup_test.go`:

```go
package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// Lookup tests use HARNESS_REGISTRY_ROOT to pin the project root.
// Phase 2 changes the walk-up marker; until then, fixtures include an
// empty sensors/ subdir so registry.Discover succeeds via either path.

func TestLookup_StackPresent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	if err := os.WriteFile(filepath.Join(root, ".harness", "stack.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HARNESS_REGISTRY_ROOT", root)
	res, err := Lookup(root)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !res.Exists {
		t.Fatalf("Exists=false, want true")
	}
	wantPath := filepath.Join(root, ".harness", "stack.json")
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	if res.Stack["version"] != "0.1.0" {
		t.Fatalf("stack content not decoded; got %v", res.Stack["version"])
	}
}

func TestLookup_StackAbsent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", root)
	res, err := Lookup(root)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if res.Exists {
		t.Fatalf("Exists=true, want false")
	}
	wantPath := filepath.Join(root, ".harness", "stack.json")
	if res.Path != wantPath {
		t.Fatalf("Path = %q, want %q", res.Path, wantPath)
	}
	if res.Stack != nil {
		t.Fatalf("Stack should be nil when Exists=false, got %v", res.Stack)
	}
}
```

- [ ] **Step 2: Verify test fails**

Run:
```bash
go test ./lib/stack/... -run TestLookup
```
Expected: fails with "undefined: Lookup".

- [ ] **Step 3: Implement `Lookup`**

Create `lib/stack/lookup.go`:

```go
package stack

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// Result is the outcome of a stack Lookup.
type Result struct {
	ProjectRoot string                 // absolute path
	Path        string                 // absolute path to .harness/stack.json
	Exists      bool                   // stack.json present on disk
	Stack       map[string]interface{} // nil when Exists=false
}

// Lookup resolves the project root (via lib/registry.Discover), then
// resolves <root>/.harness/stack.json. Returns Exists=false when the
// file is absent — that is NOT an error.
//
// Schema validation runs only when the file exists.
func Lookup(startDir string) (Result, error) {
	root, _, err := registry.Discover(startDir)
	if err != nil {
		return Result{}, err
	}
	path := filepath.Join(root, ".harness", "stack.json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return Result{ProjectRoot: root, Path: path, Exists: false}, nil
		}
		return Result{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	var stderr bytes.Buffer
	s, _, code := LoadStackFile(path, "", &stderr)
	if code != 0 {
		return Result{}, &LoadError{Path: path, Code: code, Stderr: stderr.String()}
	}
	_ = body // satisfies linter; LoadStackFile already read the file
	return Result{ProjectRoot: root, Path: path, Exists: true, Stack: s}, nil
}

// LoadError carries the exit-code + stderr details from LoadStackFile.
type LoadError struct {
	Path   string
	Code   int
	Stderr string
}

func (e *LoadError) Error() string {
	return "stack load failed at " + e.Path + ": " + e.Stderr
}
```

- [ ] **Step 4: Verify tests pass**

Run:
```bash
go test ./lib/stack/...
```
Expected: both `TestLookup_*` tests pass alongside the earlier ones.

- [ ] **Step 5: Commit**

```bash
git add lib/stack/lookup.go lib/stack/lookup_test.go
git commit -m "$(cat <<'EOF'
feat(stack): Lookup resolves <root>/.harness/stack.json

Reuses lib/registry.Discover for project-root resolution. Returns
Exists=false when the stack file is absent (not an error). Validates
when present.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.5: `lib/stack/shape.go` — typed accessors

**Files:**
- Create: `lib/stack/shape.go`
- Create: `lib/stack/shape_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/stack/shape_test.go`:

```go
package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadGoldenTyped(t *testing.T) Stack {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var s Stack
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return s
}

func TestShapesByRole(t *testing.T) {
	s := loadGoldenTyped(t)
	loggers := s.ShapesByRole(RoleLogger)
	if len(loggers) != 1 || loggers[0].ID != "zap-prod-json" {
		t.Fatalf("ShapesByRole(logger) = %+v", loggers)
	}
	mw := s.ShapesByRole(RoleHTTPMiddleware)
	if len(mw) != 1 || mw[0].ID != "chi-access-log" {
		t.Fatalf("ShapesByRole(http-middleware) = %+v", mw)
	}
}

func TestShapesProducedBy(t *testing.T) {
	s := loadGoldenTyped(t)
	got := s.ShapesProducedBy("go.uber.org/zap")
	if len(got) != 1 || got[0].ID != "zap-prod-json" {
		t.Fatalf("ShapesProducedBy(zap) = %+v", got)
	}
	if len(s.ShapesProducedBy("nonexistent")) != 0 {
		t.Fatal("ShapesProducedBy(nonexistent) should be empty")
	}
}

func TestFieldsByMeaning(t *testing.T) {
	s := loadGoldenTyped(t)
	sev := s.LogShapes[0].FieldsByMeaning(MeaningSeverity)
	if len(sev) != 1 || sev[0].Key != "severity" {
		t.Fatalf("FieldsByMeaning(severity) = %+v", sev)
	}
}

func TestHasSeverity(t *testing.T) {
	s := loadGoldenTyped(t)
	if !s.LogShapes[0].HasSeverity() {
		t.Fatal("zap-prod-json should have severity")
	}
	if s.LogShapes[1].HasSeverity() {
		t.Fatal("chi-access-log should NOT have severity (combined-log-format)")
	}
}
```

- [ ] **Step 2: Verify test fails to compile**

Run:
```bash
go test ./lib/stack/... -run TestShapes
```
Expected: fails (undefined: Stack, RoleLogger, etc.).

- [ ] **Step 3: Implement typed struct + accessors**

Create `lib/stack/shape.go`:

```go
package stack

// Stack is the typed view of a stack.json file. Mirrors the schema
// shape one-to-one with JSON tags. Optional fields use pointers or
// nullable slices so absence is distinguishable from zero.
type Stack struct {
	Version     string      `json:"version"`
	DetectedAt  string      `json:"detected_at"`
	DetectedBy  string      `json:"detected_by"`
	Languages   []Language  `json:"languages"`
	Components  []Component `json:"components"`
	LogShapes   []LogShape  `json:"log_shapes"`
}

type Language struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type Component struct {
	Role          Role       `json:"role"`
	Name          string     `json:"name"`
	Version       string     `json:"version,omitempty"`
	ConfigSummary string     `json:"config_summary,omitempty"`
	Evidence      []Evidence `json:"evidence"`
}

type Evidence struct {
	File      string `json:"file"`
	LineStart *int   `json:"line_start,omitempty"`
	LineEnd   *int   `json:"line_end,omitempty"`
	Rationale string `json:"rationale"`
}

type LogShape struct {
	ID             string   `json:"id"`
	ProducedBy     []string `json:"produced_by"`
	Format         Format   `json:"format"`
	Fields         []Field  `json:"fields,omitempty"`
	SeverityValues []string `json:"severity_values,omitempty"`
	Sample         string   `json:"sample"`
}

type Field struct {
	Key           string        `json:"key"`
	Meaning       FieldMeaning  `json:"meaning"`
	ExampleValues []string      `json:"example_values,omitempty"`
}

// Role is the enum from $defs/Role in stack.json.
type Role string

const (
	RoleHTTPServer     Role = "http-server"
	RoleHTTPRouter     Role = "http-router"
	RoleHTTPMiddleware Role = "http-middleware"
	RoleLogger         Role = "logger"
	RoleLogEncoder     Role = "log-encoder"
	RoleTracer         Role = "tracer"
	RoleMetrics        Role = "metrics"
	RoleQueueConsumer  Role = "queue-consumer"
	RoleQueueProducer  Role = "queue-producer"
	RoleDBClient       Role = "db-client"
	RoleRPC            Role = "rpc"
	RoleTestRunner     Role = "test-runner"
)

// Format is the enum from $defs/Format.
type Format string

const (
	FormatJSON              Format = "json"
	FormatLogfmt            Format = "logfmt"
	FormatPlain             Format = "plain"
	FormatStackTrace        Format = "stack-trace"
	FormatCombinedLogFormat Format = "combined-log-format"
)

// FieldMeaning is the enum from $defs/FieldMeaning.
type FieldMeaning string

const (
	MeaningSeverity   FieldMeaning = "severity"
	MeaningMessage    FieldMeaning = "message"
	MeaningTimestamp  FieldMeaning = "timestamp"
	MeaningTraceID    FieldMeaning = "trace_id"
	MeaningSpanID     FieldMeaning = "span_id"
	MeaningStatusCode FieldMeaning = "status_code"
	MeaningLatencyMS  FieldMeaning = "latency_ms"
	MeaningMethod     FieldMeaning = "method"
	MeaningPath       FieldMeaning = "path"
	MeaningUserID     FieldMeaning = "user_id"
	MeaningRequestID  FieldMeaning = "request_id"
	MeaningService    FieldMeaning = "service"
	MeaningVersion    FieldMeaning = "version"
	MeaningOther      FieldMeaning = "other"
)

// ShapesByRole returns log_shapes whose produced_by[] intersects the
// names of components with the given role.
func (s Stack) ShapesByRole(role Role) []LogShape {
	names := map[string]struct{}{}
	for _, c := range s.Components {
		if c.Role == role {
			names[c.Name] = struct{}{}
		}
	}
	if len(names) == 0 {
		return nil
	}
	var out []LogShape
	for _, sh := range s.LogShapes {
		for _, n := range sh.ProducedBy {
			if _, ok := names[n]; ok {
				out = append(out, sh)
				break
			}
		}
	}
	return out
}

// ShapesProducedBy returns log_shapes whose produced_by[] contains name.
func (s Stack) ShapesProducedBy(name string) []LogShape {
	var out []LogShape
	for _, sh := range s.LogShapes {
		for _, n := range sh.ProducedBy {
			if n == name {
				out = append(out, sh)
				break
			}
		}
	}
	return out
}

// FieldsByMeaning returns fields whose meaning matches.
func (sh LogShape) FieldsByMeaning(m FieldMeaning) []Field {
	var out []Field
	for _, f := range sh.Fields {
		if f.Meaning == m {
			out = append(out, f)
		}
	}
	return out
}

// HasSeverity returns true when any field has meaning=severity.
func (sh LogShape) HasSeverity() bool {
	return len(sh.FieldsByMeaning(MeaningSeverity)) > 0
}
```

- [ ] **Step 4: Verify tests pass**

Run:
```bash
go test ./lib/stack/...
```
Expected: all tests in `lib/stack/...` pass.

- [ ] **Step 5: Commit**

```bash
git add lib/stack/shape.go lib/stack/shape_test.go
git commit -m "$(cat <<'EOF'
feat(stack): typed Stack struct + role/format/meaning enums and accessors

ShapesByRole and ShapesProducedBy power detect-sensors Phase B lookup.
HasSeverity gates the verdict-mapping branch.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.6: `write-stack.go` — CLI wrapper + cross-check

**Files:**
- Create: `skills/detect-sensors/scripts/write-stack.go`
- Create: `skills/detect-sensors/scripts/write-stack_test.go`
- Create: `lib/stack/testdata/invalid-produced-by-orphan.json`

- [ ] **Step 1: Create the produced_by-orphan fixture**

Create `lib/stack/testdata/invalid-produced-by-orphan.json`:

```json
{
  "version": "0.1.0",
  "detected_at": "2026-05-12T10:00:00Z",
  "detected_by": "manual",
  "languages": [{ "name": "go", "version": "1.25" }],
  "components": [
    {
      "role": "logger",
      "name": "go.uber.org/zap",
      "evidence": [{ "file": "main.go", "rationale": "x" }]
    }
  ],
  "log_shapes": [
    {
      "id": "ghost-shape",
      "produced_by": ["does.not.exist/ghost"],
      "format": "plain",
      "sample": "ghost line"
    }
  ]
}
```

- [ ] **Step 2: Write the failing test**

Create `skills/detect-sensors/scripts/write-stack_test.go`:

```go
//go:build write_stack

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRun_Happy(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoRoot := repoRootForTest(t)
	payload := filepath.Join(repoRoot, "lib", "stack", "testdata", "golden-stack.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out=" + tmp,
		"--schemas-dir=" + filepath.Join(repoRoot, "schemas"),
		payload,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	got := filepath.Join(tmp, ".harness", "stack.json")
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("stack.json not on disk: %v", err)
	}
}

func TestRun_SchemaFail(t *testing.T) {
	tmp := t.TempDir()
	repoRoot := repoRootForTest(t)
	payload := filepath.Join(repoRoot, "lib", "stack", "testdata", "invalid-missing-required.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out=" + tmp,
		"--schemas-dir=" + filepath.Join(repoRoot, "schemas"),
		payload,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr = %s", code, stderr.String())
	}
}

func TestRun_ProducedByOrphan(t *testing.T) {
	tmp := t.TempDir()
	repoRoot := repoRootForTest(t)
	payload := filepath.Join(repoRoot, "lib", "stack", "testdata", "invalid-produced-by-orphan.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--out=" + tmp,
		"--schemas-dir=" + filepath.Join(repoRoot, "schemas"),
		payload,
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("code = %d, want 1; stderr = %s", code, stderr.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("stack_produced_by_orphan")) {
		t.Fatalf("stderr does not mention orphan: %s", stderr.String())
	}
}

func TestRun_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	repoRoot := repoRootForTest(t)
	payload := filepath.Join(repoRoot, "lib", "stack", "testdata", "golden-stack.json")
	args := []string{
		"--out=" + tmp,
		"--schemas-dir=" + filepath.Join(repoRoot, "schemas"),
		payload,
	}
	var sb1, sb2 bytes.Buffer
	if code := run(args, &sb1, &sb1); code != 0 {
		t.Fatalf("first: code=%d, %s", code, sb1.String())
	}
	first, _ := os.ReadFile(filepath.Join(tmp, ".harness", "stack.json"))
	if code := run(args, &sb2, &sb2); code != 0 {
		t.Fatalf("second: code=%d, %s", code, sb2.String())
	}
	second, _ := os.ReadFile(filepath.Join(tmp, ".harness", "stack.json"))
	if !bytes.Equal(first, second) {
		t.Fatalf("not idempotent")
	}
	_ = json.RawMessage(first) // satisfies linter
}

// repoRootForTest walks up from cwd looking for go.mod so tests can
// reference repo-rooted fixtures regardless of where Go runs them.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
	t.Fatal("repo root not found within 8 levels")
	return ""
}
```

- [ ] **Step 3: Verify test fails to compile**

Run:
```bash
go test -tags=write_stack ./skills/detect-sensors/scripts/...
```
Expected: fails with "undefined: run" or "no Go files in ... matching tag".

- [ ] **Step 4: Implement `write-stack.go`**

Create `skills/detect-sensors/scripts/write-stack.go`:

```go
//go:build write_stack

// Command write-stack reads a draft stack JSON payload, validates it
// against schemas/stack.json, cross-checks that every
// log_shapes[].produced_by[] references an existing components[].name,
// and persists it to <project-root>/.harness/stack.json.
//
// Usage:
//
//	go run -tags=write_stack ./skills/detect-sensors/scripts \
//	  --out=<project-root> [--schemas-dir=<dir>] <stack-payload.json>
//
// Exit codes: 0 stack written, 1 schema/cross-check failure,
// 2 usage or I/O error.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-stack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var outDir, schemasDir string
	fs.StringVar(&outDir, "out", "", "project root to write .harness/stack.json into (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if outDir == "" {
		fmt.Fprintln(stderr, "error: --out is required")
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: write-stack --out=PROJECT_ROOT [--schemas-dir=DIR] <stack-payload.json>")
		return 2
	}
	payloadPath := fs.Arg(0)

	body, err := os.ReadFile(payloadPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return 2
	}

	if err := crossCheckProducedBy(body); err != nil {
		fmt.Fprintln(stderr, "error: stack_produced_by_orphan:", err)
		return 1
	}

	path, err := stack.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	fmt.Fprintln(stdout, path)
	return 0
}

// crossCheckProducedBy validates that every log_shapes[].produced_by[]
// entry matches some components[].name. Runs BEFORE schema validation
// (cheap parse, fail-fast on the most common author mistake).
func crossCheckProducedBy(body []byte) error {
	var m struct {
		Components []struct {
			Name string `json:"name"`
		} `json:"components"`
		LogShapes []struct {
			ID         string   `json:"id"`
			ProducedBy []string `json:"produced_by"`
		} `json:"log_shapes"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		// Schema validator will catch malformed JSON with a richer message.
		return nil
	}
	names := map[string]struct{}{}
	for _, c := range m.Components {
		names[c.Name] = struct{}{}
	}
	for _, sh := range m.LogShapes {
		for _, pb := range sh.ProducedBy {
			if _, ok := names[pb]; !ok {
				return fmt.Errorf("log_shape %q references unknown component %q", sh.ID, pb)
			}
		}
	}
	return nil
}
```

- [ ] **Step 5: Verify tests pass**

Run:
```bash
go test -tags=write_stack ./skills/detect-sensors/scripts/...
```
Expected: all four `TestRun_*` tests pass.

- [ ] **Step 6: Verify the write-sensor build still works alongside**

Run:
```bash
go test -tags=write_sensor ./skills/detect-sensors/scripts/...
go build -tags=write_stack ./skills/detect-sensors/scripts/...
go build -tags=write_sensor ./skills/detect-sensors/scripts/...
```
Expected: all four commands succeed.

- [ ] **Step 7: Verify default-build still has no stray `main`**

Run:
```bash
go build ./skills/detect-sensors/scripts/...
```
Expected: succeeds (no main built — both files are gated).

- [ ] **Step 8: Commit**

```bash
git add skills/detect-sensors/scripts/write-stack.go skills/detect-sensors/scripts/write-stack_test.go lib/stack/testdata/invalid-produced-by-orphan.json
git commit -m "$(cat <<'EOF'
feat(detect-sensors): add write-stack.go script

Validates stack.json payload against schemas/stack.json,
cross-checks log_shapes[].produced_by[] against components[].name,
persists atomically to <project-root>/.harness/stack.json. Mirrors
the contract of write-sensor.go. Build tag write_stack.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.7: Phase 1 final verification

- [ ] **Step 1: Run all of Phase 1's tests**

Run:
```bash
go test ./lib/stack/...
go test -tags=write_stack ./skills/detect-sensors/scripts/...
go test -tags=write_sensor ./skills/detect-sensors/scripts/...
go vet ./lib/stack/...
go vet -tags=write_stack ./skills/detect-sensors/scripts/...
go vet -tags=write_sensor ./skills/detect-sensors/scripts/...
```
Expected: all green.

- [ ] **Step 2: Confirm Phase 1 doesn't break existing tagged builds**

Run:
```bash
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go test ./lib/registry/... ./lib/sensor/... ./lib/signal/... ./lib/schema/...
```
Expected: all green. Phase 1 added new packages and a build tag; nothing else changed.

- [ ] **Step 3: Phase 1 PR boundary marker**

Phase 1 is now mergeable as a PR titled `feat(stack): introduce Stack entity + write-stack.go (phase 1 of #27)`. The commits in the PR are the six from Tasks 1.1 → 1.6.

---

## Phase 2 — Layout migration to `.harness/`

Goal: switch the walk-up sentinel to `.harness/`, rebase every path-touching call site, and dogfood the migration on the framework's own repo. **Breaking change**; no fallback. The `git mv` lands in the same PR.

### Task 2.1: Change `lib/registry/root.go` walk-up sentinel

**Files:**
- Modify: `lib/registry/root.go` (constant + doc comments)

- [ ] **Step 1: Update the constant and the directly-referenced doc strings**

In `lib/registry/root.go`:

Change line 27 from:
```go
// markerDir is the directory name Discover walks up looking for.
const markerDir = "sensors"
```
to:
```go
// markerDir is the directory name Discover walks up looking for.
// .harness/ is the consolidated framework-artifact namespace at the
// project root (contains sensors/, runtime/, stack.json).
const markerDir = ".harness"
```

Change line 20 from:
```go
	// SourceWalkUp means the sensors/ marker was found by walking up
	// from startDir.
	SourceWalkUp Source = "walk_up"
```
to:
```go
	// SourceWalkUp means the .harness/ marker was found by walking up
	// from startDir.
	SourceWalkUp Source = "walk_up"
```

Change lines 47–53 (the Discover() doc comment paragraph) from:
```go
// Discover resolves the project root using HARNESS_REGISTRY_ROOT first,
// then walking up from startDir looking for a sensors/ directory.
//
// HARNESS_REGISTRY_ROOT takes precedence because it is the operator's
// explicit override — useful when invoking skills from outside the project
// tree (CI, shell scripts). When unset, the walk-up mirrors the schema
// discovery pattern in lib/schema, but looks for sensors/ (the user-project
// tree) rather than schemas/ (the plugin tree).
```
to:
```go
// Discover resolves the project root using HARNESS_REGISTRY_ROOT first,
// then walking up from startDir looking for a .harness/ directory.
//
// HARNESS_REGISTRY_ROOT takes precedence because it is the operator's
// explicit override — useful when invoking skills from outside the project
// tree (CI, shell scripts). When unset, the walk-up looks for .harness/
// (the user-project framework namespace) — distinct from schemas/ (the
// plugin tree, resolved separately by lib/schema).
```

Change lines 113–116 (walkUpForMarker doc comment) from:
```go
// walkUpForMarker walks parent-by-parent from startDir looking for a
// directory whose sensors/ child is itself a directory (symlinks to dirs
// accepted via os.Stat; emptiness allowed). Returns the absolute path of
// the matched ancestor, or an error when the filesystem root is reached.
```
to:
```go
// walkUpForMarker walks parent-by-parent from startDir looking for a
// directory whose .harness/ child is itself a directory (symlinks to
// dirs accepted via os.Stat; emptiness allowed). Returns the absolute
// path of the matched ancestor, or an error when the filesystem root
// is reached.
```

- [ ] **Step 2: Verify the constant grep**

Run:
```bash
grep -n 'markerDir = ' lib/registry/root.go
```
Expected: prints exactly `const markerDir = ".harness"`.

(Tests are updated in a later task; expect failures until then.)

---

### Task 2.2: Rebase `lib/registry/paths.go` to `.harness/runtime/` + add new accessors

**Files:**
- Modify: `lib/registry/paths.go`

- [ ] **Step 1: Rewrite the file with updated paths**

The existing file is reproduced in the spec; the changes are mechanical:
1. `SensorsDir()` returns `<root>/.harness/runtime/` (was `<root>/.runtime/sensors/`).
2. Add `SensorFile(id)` returning `<root>/.harness/sensors/<id>.json`.
3. Add `RelativeRunDir(id, runID)` returning `.harness/runtime/<id>/<runID>` (relative).
4. Doc comments updated for the new namespace.

Replace the file contents with:

```go
// Package registry owns the .harness/runtime/ directory layout, atomic
// state-file writes, file locks, PID liveness checks, and held_by
// refcount management for blocking-sensor runs.
package registry

import "path/filepath"

// Root is the absolute path of a project root containing .harness/.
// All registry helpers are methods on Root so tests can pivot to a temp
// directory by constructing a Root around it.
type Root struct {
	projectRoot string
}

// NewRoot returns a Root anchored at the project root that owns
// <projectRoot>/.harness/.
func NewRoot(projectRoot string) Root {
	return Root{projectRoot: projectRoot}
}

// SensorsDir is the directory holding running_sensors.json and per-sensor
// subdirectories.
func (r Root) SensorsDir() string {
	return filepath.Join(r.projectRoot, ".harness", "runtime")
}

// SensorFile returns the absolute path of <root>/.harness/sensors/<id>.json.
// Replaces the hardcoded "sensors/<id>.json" construction in lib/sensor
// and lib/orchestrator.
func (r Root) SensorFile(id string) string {
	return filepath.Join(r.projectRoot, ".harness", "sensors", id+".json")
}

// RegistryFile is the absolute path to running_sensors.json.
func (r Root) RegistryFile() string {
	return filepath.Join(r.SensorsDir(), "running_sensors.json")
}

// LockFile is the sibling lock used by WithFileLock.
func (r Root) LockFile() string {
	return filepath.Join(r.SensorsDir(), "running_sensors.lock")
}

// SensorDir returns the per-sensor directory under .harness/runtime/<id>/.
func (r Root) SensorDir(id string) string {
	return filepath.Join(r.SensorsDir(), id)
}

// RawLog is the per-sensor raw subprocess output file.
func (r Root) RawLog(id string) string {
	return filepath.Join(r.SensorDir(id), "raw.log")
}

// SignalsLog is the per-sensor JSONL signals file written by the watcher.
func (r Root) SignalsLog(id string) string {
	return filepath.Join(r.SensorDir(id), "signals.log")
}

// ProjectRoot returns the absolute path of the project root that anchors
// this Root.
func (r Root) ProjectRoot() string {
	return r.projectRoot
}

// RunDir returns the per-run directory under .harness/runtime/<id>/<runID>/.
func (r Root) RunDir(id, runID string) string {
	return filepath.Join(r.SensorDir(id), runID)
}

// RelativeRunDir returns the per-run directory as a path relative to
// the project root: ".harness/runtime/<id>/<runID>". Used by callers
// that store the path for later resolution against an arbitrary
// project root (typically in registry entries).
func (r Root) RelativeRunDir(id, runID string) string {
	return filepath.Join(".harness", "runtime", id, runID)
}

// RawLogRun is the raw subprocess output file for one run.
func (r Root) RawLogRun(id, runID string) string {
	return filepath.Join(r.RunDir(id, runID), "raw.log")
}

// SignalsLogRun is the parsed JSONL signals file for one run.
func (r Root) SignalsLogRun(id, runID string) string {
	return filepath.Join(r.RunDir(id, runID), "signals.log")
}

// LegacyRawLog is the flat (pre-runID) raw.log path. Read-only fallback
// for entries migrated from before run-id-aware layouts existed.
func (r Root) LegacyRawLog(id string) string {
	return filepath.Join(r.SensorDir(id), "raw.log")
}

// LegacySignalsLog is the flat (pre-runID) signals.log path. Read-only
// fallback; mirrors LegacyRawLog.
func (r Root) LegacySignalsLog(id string) string {
	return filepath.Join(r.SensorDir(id), "signals.log")
}
```

- [ ] **Step 2: Verify package compiles**

Run:
```bash
go build ./lib/registry/...
```
Expected: succeeds (tests will fail, but the code compiles).

---

### Task 2.3: Update `lib/registry/*_test.go` fixtures to `.harness/`

**Files:**
- Modify: `lib/registry/root_test.go`, `paths_test.go`, `lookup_test.go`, and any other test file that uses the old layout

- [ ] **Step 1: Identify the test files that need fixture updates**

Run:
```bash
grep -ln 'sensors\|\.runtime' lib/registry/*_test.go
```
Expected: lists the test files touching either the walk-up marker or runtime paths.

- [ ] **Step 2: For each test file, update fixture setup**

In each file, find calls that scaffold the test directory:
- `os.MkdirAll(filepath.Join(tmp, "sensors"), 0o755)` → `os.MkdirAll(filepath.Join(tmp, ".harness"), 0o755)`
- Path assertions referencing `.runtime/sensors/...` → `.harness/runtime/...`
- Path assertions referencing `<root>/sensors/<id>.json` → `<root>/.harness/sensors/<id>.json`

Make each substitution literal. There may also be tests that currently scaffold the legacy layout to test the discovery error; rewrite them so the missing-marker test creates a tmp dir with no `.harness/` (no need to also strip a `sensors/` because it's no longer the marker).

For `paths_test.go`, add new tests for `SensorFile` and `RelativeRunDir`:

```go
func TestSensorFile(t *testing.T) {
	r := NewRoot("/project")
	got := r.SensorFile("my-sensor")
	want := filepath.Join("/project", ".harness", "sensors", "my-sensor.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRelativeRunDir(t *testing.T) {
	r := NewRoot("/project")
	got := r.RelativeRunDir("my-sensor", "12345-deadbeef")
	want := filepath.Join(".harness", "runtime", "my-sensor", "12345-deadbeef")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 3: Verify lib/registry tests pass**

Run:
```bash
go test ./lib/registry/...
```
Expected: all green.

- [ ] **Step 4: Commit (intermediate — registry only)**

```bash
git add lib/registry/
git commit -m "$(cat <<'EOF'
refactor(registry): rebase walk-up marker to .harness/ + paths to runtime/

markerDir changes from "sensors" to ".harness". Path accessors that
returned <root>/.runtime/sensors/* now return <root>/.harness/runtime/*.
Adds Root.SensorFile(id) and Root.RelativeRunDir(id,runID) so callers
in lib/sensor and lib/orchestrator can drop their hardcoded literals.

No fallback to the old layout.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.4: Migrate `lib/sensor/path.go` to use `.harness/sensors/`

**Files:**
- Modify: `lib/sensor/path.go`
- Modify: `lib/sensor/path_test.go`

- [ ] **Step 1: Update `ResolveByID` to use `.harness/sensors`**

In `lib/sensor/path.go`, change the path-join line in `ResolveByID` from:
```go
path := filepath.Join(baseDir, "sensors", id+".json")
```
to:
```go
path := filepath.Join(baseDir, ".harness", "sensors", id+".json")
```

Update the function doc comment from:
```go
// ResolveByID resolves a bare sensor id to its on-disk path under
// <baseDir>/sensors/<id>.json. The id MUST match the schema's id pattern
// to prevent path traversal via "../foo" or absolute-path inputs.
```
to:
```go
// ResolveByID resolves a bare sensor id to its on-disk path under
// <baseDir>/.harness/sensors/<id>.json. The id MUST match the schema's
// id pattern to prevent path traversal via "../foo" or absolute-path
// inputs.
```

- [ ] **Step 2: Update path_test.go fixtures**

In `lib/sensor/path_test.go`, every test that scaffolds `os.MkdirAll(filepath.Join(tmp, "sensors"), 0o755)` becomes `os.MkdirAll(filepath.Join(tmp, ".harness", "sensors"), 0o755)`. Every expected-path assertion that includes `"sensors", id+".json"` becomes `".harness", "sensors", id+".json"`.

- [ ] **Step 3: Verify tests pass**

Run:
```bash
go test ./lib/sensor/...
```
Expected: all green.

---

### Task 2.5: Migrate `lib/orchestrator/` — replace hardcoded literals with accessors

**Files:**
- Modify: `lib/orchestrator/run.go` (doc comments only)
- Modify: `lib/orchestrator/lifecycle.go:350`
- Modify: `lib/orchestrator/live_deps.go:31, :222`
- Modify: `lib/orchestrator/cascade.go:46`
- Modify: `lib/orchestrator/*_test.go` (path assertions)

- [ ] **Step 1: Update `run.go` doc comments**

In `lib/orchestrator/run.go`, update the doc comment block at top:
- Line 21: `<projectRoot>/sensors/<id>.json` → `<projectRoot>/.harness/sensors/<id>.json`
- Line 40: `.runtime/sensors/<id>/<run-id>/` → `.harness/runtime/<id>/<run-id>/`

- [ ] **Step 2: Update `lifecycle.go:350`**

Find:
```go
LogDir:     filepath.Join(".runtime", "sensors", envelope.SensorID, runID),
```

Replace with:
```go
LogDir:     root.RelativeRunDir(envelope.SensorID, runID),
```

(`root` is the `registry.Root` in scope at that call site — confirm the local variable name by reading 10 lines above the change; rename in the replacement to match.)

- [ ] **Step 3: Update `live_deps.go:31` (doc) and `:222` (call)**

In the doc comment around line 31: `<root>/sensors/<id>.json` → `<root>/.harness/sensors/<id>.json`.

At line 222, the same `filepath.Join(".runtime", "sensors", dep.ID, runID)` pattern as in lifecycle. Replace with:
```go
LogDir:     r.RelativeRunDir(dep.ID, runID),
```
(again, `r` is the local `registry.Root`; verify the variable name in scope.)

- [ ] **Step 4: Update `cascade.go:46`**

Find:
```go
"file":      fmt.Sprintf("sensors/%s.json", failedID),
```

Replace with:
```go
"file":      filepath.Join(".harness", "sensors", failedID+".json"),
```

Add `"path/filepath"` to the import block if not already imported.

- [ ] **Step 5: Run the grep invariant**

Run:
```bash
grep -rn '"\.runtime"\|"sensors/"' lib/orchestrator/
```
Expected: zero lines. The grep targets the spec's invariants: bare `".runtime"` and `"sensors/"` with trailing slash. `filepath.Join(".harness", "sensors", ...)` contains `"sensors"` without the slash and is allowed.

- [ ] **Step 6: Update orchestrator test fixtures**

Run:
```bash
grep -ln '\.runtime\|"sensors"\|sensors/<' lib/orchestrator/*_test.go
```
Then in each listed test, update fixture setup and assertions to `.harness/runtime/` / `.harness/sensors/` form.

- [ ] **Step 7: Verify orchestrator tests pass**

Run:
```bash
go test ./lib/orchestrator/...
```
Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add lib/sensor/ lib/orchestrator/
git commit -m "$(cat <<'EOF'
refactor: remove hardcoded sensors/ and .runtime/ literals

lib/sensor/path.go uses .harness/sensors/<id>.json.
lib/orchestrator/{lifecycle,live_deps}.go use Root.RelativeRunDir.
lib/orchestrator/cascade.go uses filepath.Join(".harness", "sensors", ...).

grep -rn '".runtime"|"sensors/"|"sensors", ' lib/orchestrator/ lib/sensor/
prints nothing.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.6: Migrate `hooks/error-issue-autofiler.go`

**Files:**
- Modify: `hooks/error-issue-autofiler.go:190`

- [ ] **Step 1: Update the cache path**

Find line 190:
```go
cachePath := filepath.Join(res.ProjectRoot, ".runtime", "auto-issues.json")
```

Replace with:
```go
cachePath := filepath.Join(res.ProjectRoot, ".harness", "runtime", "auto-issues.json")
```

- [ ] **Step 2: Update any test fixtures referencing the old path**

Run:
```bash
grep -ln 'auto-issues\|\.runtime' hooks/*.go
```
Update tests if listed.

- [ ] **Step 3: Verify hook builds and tests pass**

Run:
```bash
go build -tags=error_autofiler ./hooks/...
go test -tags=error_autofiler ./hooks/...
```
Expected: green.

- [ ] **Step 4: Commit**

```bash
git add hooks/error-issue-autofiler.go
git commit -m "$(cat <<'EOF'
refactor(hooks): autofile cache moves to .harness/runtime/auto-issues.json

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.7: Update the five blocking-sensor SKILL.md files

**Files:**
- Modify: `skills/start-sensor/SKILL.md`
- Modify: `skills/stop-sensor/SKILL.md`
- Modify: `skills/list-sensors/SKILL.md`
- Modify: `skills/tail-sensor/SKILL.md`
- Modify: `skills/run-sensor/SKILL.md`

- [ ] **Step 1: For each SKILL.md, run a literal substitution**

In each file's frontmatter `description:` AND body prose, replace:

- `.runtime/sensors/` → `.harness/runtime/`
- `sensors/<id>.json` → `.harness/sensors/<id>.json`
- ``` `sensors/` ``` → ``` `.harness/sensors/` ``` (bare directory mentions)
- ``` `.runtime/sensors/` ``` → ``` `.harness/runtime/` ```

Be careful: `run-sensor/SKILL.md` has at least two literal mentions of `sensors/` in the dep-resolution paragraph (`Missing deps (referenced id has no file under sensors/)`). Update that one too.

- [ ] **Step 2: Verify the grep invariant**

Run:
```bash
grep -rn '\.runtime\|<projectRoot>/sensors\|<root>/sensors\|under `sensors/`' skills/*/SKILL.md
```
Expected: zero lines.

- [ ] **Step 3: Verify skills still load (lint via Claude Code skill loader)**

This is a manual smoke test — restart Claude Code or invoke the loader's lint command if available. If no lint exists, accept the grep invariant as sufficient.

- [ ] **Step 4: Commit**

```bash
git add skills/start-sensor/SKILL.md skills/stop-sensor/SKILL.md skills/list-sensors/SKILL.md skills/tail-sensor/SKILL.md skills/run-sensor/SKILL.md
git commit -m "$(cat <<'EOF'
docs(skills): migrate SKILL.md path references to .harness/

Five blocking-sensor skills (start/stop/list/tail/run) — both
frontmatter description: lines and body prose — now reference
.harness/sensors/ and .harness/runtime/ paths.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.8: Migrate downstream skill tests

**Files:**
- Modify: `skills/start-sensor/scripts/start_test.go`
- Modify: `skills/stop-sensor/scripts/stop_test.go`
- Modify: `skills/list-sensors/scripts/list_test.go`
- Modify: `skills/tail-sensor/scripts/tail_test.go`
- Modify: `skills/run-sensor/scripts/*_test.go`

- [ ] **Step 1: Identify files needing updates**

Run:
```bash
grep -ln '"sensors"\|"\\.runtime"' skills/*/scripts/*_test.go
```
Expected: lists the test files.

- [ ] **Step 2: For each test file, substitute**

The same substitutions as in Task 2.7 but at the Go-literal level: `filepath.Join(..., "sensors", ...)` becomes `filepath.Join(..., ".harness", "sensors", ...)`; `filepath.Join(..., ".runtime", "sensors", ...)` becomes `filepath.Join(..., ".harness", "runtime", ...)`. Make each substitution one file at a time and run that file's tests to verify.

- [ ] **Step 3: Verify all downstream skill tests pass**

Run:
```bash
go test -tags=start_sensor ./skills/start-sensor/scripts/...
go test -tags=stop_sensor ./skills/stop-sensor/scripts/...
go test -tags=list_sensors ./skills/list-sensors/scripts/...
go test -tags=tail_sensor ./skills/tail-sensor/scripts/...
go test -tags=run_computational ./skills/run-sensor/scripts/...
go test -tags=run_inferential ./skills/run-sensor/scripts/...
```
(Confirm exact build tags by reading each SKILL.md's `go run` invocation if any of the above fails.)

Expected: all green.

- [ ] **Step 4: Commit**

```bash
git add skills/*/scripts/*_test.go
git commit -m "$(cat <<'EOF'
test: migrate downstream skill test fixtures to .harness/ layout

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.9: Update `.gitignore`

**Files:**
- Modify: `.gitignore`

- [ ] **Step 1: Replace the `.runtime/` line**

Open `.gitignore`. Find:
```
# Runtime
.runtime/
.test-tmp/
```

Replace with:
```
# Runtime
.harness/runtime/
.test-tmp/
```

Do NOT add `.harness/sensors/` or `.harness/stack.json` — those are committed.

- [ ] **Step 2: Verify with status**

Run:
```bash
git status --ignored | head -20
```
Expected: any old `.runtime/` content is now shown as ignored under `.harness/runtime/`. (If `.runtime/` exists in the worktree, it shows up as untracked — that's expected; Task 2.11 moves it.)

---

### Task 2.10: Update `CLAUDE.md`

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update "Registry root discovery" section**

In `CLAUDE.md`, find the section heading "### Registry root discovery". Update path references throughout:
- Line 84: `<projectRoot>/.runtime/sensors/running_sensors.json` → `<projectRoot>/.harness/runtime/running_sensors.json`.
- Line 86: `the directory that contains sensors/, not sensors/ itself` → `the directory that contains .harness/, not .harness/ itself`.
- Line 87: `Walk-up from cwd looking for sensors/` → `Walk-up from cwd looking for .harness/`; `whose sensors/ child is itself a directory` → `whose .harness/ child is itself a directory`; `Empty sensors/ is acceptable` → `Empty .harness/ is acceptable`.

Same edits for every subsequent reference within the section.

- [ ] **Step 2: Update "Auto issue opening" section**

Line 105: `<projectRoot>/.runtime/auto-issues.json` → `<projectRoot>/.harness/runtime/auto-issues.json`.

- [ ] **Step 3: Update "Project rules" §2 — three entity schemas**

In `CLAUDE.md` "Project rules" section, find rule §2. It currently reads (approximately):
```
2. **Schemas are versioned with the plugin.** They live in `schemas/` and are the source of truth for entities. ...
```

Add a parenthetical naming the three schemas:
```
2. **Schemas are versioned with the plugin.** They live in `schemas/` and are the source of truth for entities (`sensor.json`, `signal.json`, `stack.json`). ...
```

- [ ] **Step 4: Verify grep**

Run:
```bash
grep -n '\.runtime/sensors\|<projectRoot>/sensors\|walking up for `sensors/`' CLAUDE.md
```
Expected: zero lines.

---

### Task 2.11: Update `CHANGELOG.md`

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add a new top entry**

Open `CHANGELOG.md`. Above the most recent existing entry, insert:

```markdown
## [Unreleased] — Breaking: `.harness/` layout

All framework artifacts now live under `<project>/.harness/`:

- Sensor definitions: `<project>/.harness/sensors/<id>.json` (was `<project>/sensors/<id>.json`).
- Runtime state: `<project>/.harness/runtime/` (was `<project>/.runtime/sensors/`).
- Detected stack (new): `<project>/.harness/stack.json`.

To migrate an existing project:

```bash
mkdir -p .harness
git mv sensors .harness/sensors
[ -d .runtime ] && git mv .runtime .harness/runtime
# Update .gitignore: replace `/.runtime` with `/.harness/runtime`.
```

No fallback to the previous layout. `lib/registry.Discover` searches for `.harness/` only — projects with the old layout will see `registry root discovery failed: .harness/ marker not found walking up from ...`.

```

---

### Task 2.12: Dogfood the migration on the framework repo

**Files:**
- Move: `<repo>/sensors/` → `<repo>/.harness/sensors/`
- Move (if present): `<repo>/.runtime/` → `<repo>/.harness/runtime/`

- [ ] **Step 1: Confirm starting state**

Run:
```bash
ls -la sensors/ 2>/dev/null | head -5
ls -la .runtime/ 2>/dev/null | head -5
ls -la .harness/ 2>/dev/null | head -5
```
Expected: `sensors/` exists; `.runtime/` may or may not exist; `.harness/` does not exist yet (or only contains stack.json testdata fixtures — unrelated).

- [ ] **Step 2: Create `.harness/` and move sensors**

```bash
mkdir -p .harness
git mv sensors .harness/sensors
```

- [ ] **Step 3: Move `.runtime/` if it exists**

```bash
if [ -d .runtime ]; then git mv .runtime .harness/runtime; else echo "no .runtime/ to migrate"; fi
```

- [ ] **Step 4: Verify the new layout**

```bash
ls -la .harness/
git status
```
Expected: `.harness/` contains `sensors/` (and possibly `runtime/`); git status shows the renames.

- [ ] **Step 5: Run the full Phase 2 test sweep**

Run:
```bash
go test ./lib/...
go test -tags=run_computational ./...
go test -tags=run_inferential ./...
go test -tags=write_sensor ./skills/detect-sensors/scripts/...
go test -tags=write_stack ./skills/detect-sensors/scripts/...
go test -tags=start_sensor ./skills/start-sensor/scripts/...
go test -tags=stop_sensor ./skills/stop-sensor/scripts/...
go test -tags=list_sensors ./skills/list-sensors/scripts/...
go test -tags=tail_sensor ./skills/tail-sensor/scripts/...
go test -tags=error_autofiler ./hooks/...
go vet ./...
```
Expected: all green.

- [ ] **Step 6: Final grep invariants**

Run:
```bash
grep -rn '"\.runtime"\|"sensors/"' lib/ hooks/ skills/*/scripts/
```
Expected: zero matches. (The spec's invariant: bare `".runtime"` and `"sensors/"` with trailing slash. `filepath.Join(".harness", "sensors", ...)` doesn't match either.)

- [ ] **Step 7: Commit the dogfood + CHANGELOG + CLAUDE.md + .gitignore in one go**

```bash
git add CLAUDE.md CHANGELOG.md .gitignore .harness/
git commit -m "$(cat <<'EOF'
feat!: migrate to .harness/ layout (BREAKING)

Move framework artifacts under a single .harness/ namespace:
- sensors/ → .harness/sensors/
- .runtime/ → .harness/runtime/ (when present)
- new: .harness/stack.json (introduced by Phase 1)

Updates CLAUDE.md (Registry root discovery, Auto issue opening),
.gitignore (.runtime/ → .harness/runtime/), and CHANGELOG.

Closes the layout half of #27. lib/registry.Discover walks up for
.harness/ only; no fallback to the legacy sensors/ marker.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 8: Phase 2 PR boundary marker**

Phase 2 is now mergeable as a PR titled `feat!: migrate to .harness/ layout (phase 2 of #27)`. The PR's commits are Tasks 2.1 → 2.12.

---

## Phase 3 — `/detect-sensors` skill prose update

Goal: teach the LLM that runs `/detect-sensors` to do Phase A (synthesize stack) before Phase B (author sensors), and to consult `log_shapes[]` when drafting observation sensors. No Go changes.

### Task 3.1: Insert §0 "Stack discovery" into `SKILL.md`

**Files:**
- Modify: `skills/detect-sensors/SKILL.md`

- [ ] **Step 1: Locate the insertion point**

Open `skills/detect-sensors/SKILL.md`. Find the existing `### 1. Read the schema first` heading. Insert a new section IMMEDIATELY BEFORE it.

- [ ] **Step 2: Write the new §0 section**

Insert the following block before `### 1. Read the schema first`:

```markdown
### 0. Stack discovery (Phase A)

Before drafting any observation sensor, synthesize a structured description of the project's stack and persist it to `<project>/.harness/stack.json` via `schemas/stack.json`. This artifact is reused across `/detect-sensors` invocations and consumed in §4 below when drafting `kind=observation` + `output=stream` sensors.

**When to run Phase A:**
- Default: only if `<project>/.harness/stack.json` does NOT already exist. Reuse on every subsequent invocation.
- Always when the user passes `--refresh-stack`.

**What to discover:**

1. **Languages** — read `go.mod`, `package.json` engines, `pyproject.toml` requires-python, `Cargo.toml`, `pom.xml`, `build.gradle`, `Gemfile`. Capture name + version per language present.
2. **Components** — for each runtime-observable role (`logger`, `log-encoder`, `http-server`, `http-router`, `http-middleware`, `tracer`, `metrics`, `queue-consumer`, `queue-producer`, `db-client`, `rpc`, `test-runner`):
   - Identify the library actually used (Zap, Logrus, Pino, Winston, Logback, structlog, …).
   - **Open the initialization site** (`cmd/server/main.go`, `src/main.ts`, `app/__init__.py`, etc.) and read enough code to determine the CONCRETE config — not just "Zap is used" but "`zap.NewProductionConfig()` is called and `EncoderConfig.LevelKey` is overridden to `severity`".
   - Record an `evidence[]` entry pointing at the file (with line numbers when feasible) and a one-sentence `rationale`.
3. **Log shapes** — for each distinct stdout shape the components produce:
   - Pick a kebab-case `id` (e.g. `zap-prod-json`, `chi-access-log`, `panic-stack-trace`).
   - List the `produced_by[]` component names (verbatim from `components[].name`).
   - Pick the `format` enum: `json` for structured JSON loggers, `logfmt` for key=value, `combined-log-format` for Apache/Nginx-style access logs, `stack-trace` for panic dumps, `plain` for human-text fallbacks.
   - When `format` is `json` or `logfmt`, populate `fields[]` mapping literal keys to semantic `meaning` (severity, message, timestamp, trace_id, span_id, status_code, latency_ms, method, path, user_id, request_id, service, version, other). Use `meaning: "other"` as escape hatch for project-specific keys.
   - When the shape has a `severity` field, populate `severity_values[]` with the values the project actually emits — for Zap: `["DEBUG","INFO","WARN","ERROR","DPANIC","PANIC","FATAL"]`; for Pino numeric: `["10","20","30","40","50","60"]`.
   - Provide a `sample` string: one real line in this shape (capture from a CI log, test fixture, or synthesize one from the library docs + the config you observed).

**Concrete examples for the four most common stacks:**

- **Go + Zap (production config)** → one `LogShape` with `format: "json"`; `fields[]` includes `severity` (key=`level` by default, `severity` if overridden), `ts` (timestamp), `msg` (message), `caller` (other). `severity_values: ["DEBUG","INFO","WARN","ERROR","DPANIC","PANIC","FATAL"]`.
- **Node + Pino (default config)** → `format: "json"`; `fields[]` includes `level` (severity, NUMERIC), `time` (timestamp), `msg` (message), `req` and `res` (other) when `pino-http` is wired. `severity_values: ["10","20","30","40","50","60"]` (trace/debug/info/warn/error/fatal).
- **Python + structlog (JSONRenderer)** → `format: "json"`; `fields[]` keys depend on the processor chain — common defaults are `level` (severity), `timestamp`, `event` (message). `severity_values: ["debug","info","warning","error","critical"]` (Python logging level names lowercased).
- **Java + Logback (default pattern)** → `format: "plain"`; no structured fields. If the project uses `logstash-logback-encoder` for JSON output, it becomes `format: "json"` with `fields[]` for `@timestamp`, `level`, `message`, `logger_name`.

**Then call `write-stack.go`:**

```bash
go run -tags=write_stack ./skills/detect-sensors/scripts \
  --out=<project-root> \
  --schemas-dir=<plugin-root>/schemas \
  <draft-stack.json>
```

It validates against `schemas/stack.json`, cross-checks that every `log_shapes[].produced_by[]` references a known `components[].name`, and writes `<project-root>/.harness/stack.json` atomically.

### 0.5 Stack discovery — degraded path

If after a thorough search you cannot identify any logger or HTTP middleware (project is exotic, no readable manifests, no clear initialization site), persist a **minimal stack** anyway:

```json
{
  "version": "0.1.0",
  "detected_at": "<now>",
  "detected_by": "<your-model-id-or-manual>",
  "languages": [ { "name": "<best-guess>" } ],
  "components": [],
  "log_shapes": []
}
```

This is intentionally degenerate. Phase B (§4 below) will see an empty `log_shapes[]` and fall back to generic patterns (panic/error keyword matchers) annotated in the sensor's `blind_spots[]` as "stack discovery returned empty; refine patterns manually after observing real stdout".
```

- [ ] **Step 3: Verify grep**

Run:
```bash
grep -n '### 0\. Stack discovery' skills/detect-sensors/SKILL.md
grep -n '### 0\.5 Stack discovery — degraded path' skills/detect-sensors/SKILL.md
```
Expected: each prints exactly one line.

---

### Task 3.2: Update §1 to also read `schemas/stack.json`

**Files:**
- Modify: `skills/detect-sensors/SKILL.md`

- [ ] **Step 1: Extend §1**

Open `skills/detect-sensors/SKILL.md`. Find the existing `### 1. Read the schema first` section. Update the opening paragraph from (approximately):

> Always start by reading `schemas/sensor.json` and `schemas/signal.json` from this plugin so your drafts match the current shape...

to:

> Always start by reading `schemas/sensor.json`, `schemas/signal.json`, and `schemas/stack.json` from this plugin so your drafts match the current shape...

Add a one-paragraph note at the end of the section:

> The `stack.json` schema is the contract for Phase A (§0). When drafting observation sensors (§4), you'll consult the persisted `<project>/.harness/stack.json` — not the schema directly.

---

### Task 3.3: Modify §4 to branch on `kind=observation` + `output=stream`

**Files:**
- Modify: `skills/detect-sensors/SKILL.md`

- [ ] **Step 1: Locate §4**

Find `### 4. Draft each sensor` (the existing section explaining the per-sensor authoring logic — patterns, exit_code_map, blind_spots, etc.).

- [ ] **Step 2: Insert the Phase B branch**

In §4, find the bullet about `execution.output_parsing.patterns` (currently describes regex-by-hand for compilers/test runners). Just before that bullet, insert:

```markdown
- **For `kind=observation` + `output=stream` sensors (Phase B):** do NOT hand-craft regexes. Instead:
  1. Load `<project>/.harness/stack.json` (produced by §0; if missing or empty, fall through to the degraded path below).
  2. Filter `log_shapes[]` to the shapes relevant to the sensor's command. For `run-*` / `watch-*` sensors observing a running service, that typically means shapes produced by components with role `logger`, `log-encoder`, or `http-middleware`. For `tail-*` / `fetch-*` sensors against external log stores, pick the shape whose encoder matches what the store emits.
  3. For each selected shape, write 2–6 regex patterns into `execution.output_parsing.patterns[]` that map the shape's `severity_values` onto Signal verdicts:
     - `severity ∈ {ERROR, FATAL, DPANIC, PANIC}` → `verdict: fail, severity: high`.
     - `severity == WARN` AND a `status_code` field with value in `4xx/5xx` → `verdict: fail, severity: medium`.
     - `severity == WARN` (other) → `verdict: warn, severity: low`.
     - `severity == INFO` AND `message` matches a boot/ready marker → `verdict: pass, severity: info`.
  4. Anchor every drafted regex on the shape's `sample`: the regex MUST match the sample. If it doesn't, the regex is wrong.
  5. In the sensor's `description`, cite the source: e.g. *"output_parsing derived from log_shape 'zap-prod-json' in .harness/stack.json"*. This is the audit trail when patterns later fail to match real stdout.
- **Degraded path:** if `.harness/stack.json` is missing OR `log_shapes[]` is empty (Phase A failed to identify a logger), emit generic patterns matching `panic\\s*:`, `^\\s*(ERROR|FATAL)`, and similar keyword markers, AND add a `blind_spots[]` entry: *"Patterns are generic keyword markers because stack discovery did not identify a structured logger; refine after observing real stdout."*
```

- [ ] **Step 3: Verify grep**

Run:
```bash
grep -n 'For `kind=observation` + `output=stream` sensors:' skills/detect-sensors/SKILL.md
```
Expected: exactly one match.

---

### Task 3.4: Update §7 iteration loop with stack-refresh remediation

**Files:**
- Modify: `skills/detect-sensors/SKILL.md`

- [ ] **Step 1: Find §7**

Find the existing `### 7. ...iteration` section (the loop that tells authors to keep running `/run-sensor` until patterns produce informative output).

- [ ] **Step 2: Insert remediation guidance**

Add a paragraph near the end of §7:

> If a `kind=observation` + `output=stream` sensor's patterns match nothing during its first run, suspect Phase A first — not the regex. Inspect the persisted stack with `bat <project>/.harness/stack.json` (or `cat`). If the `log_shapes[].sample` no longer resembles the real stdout, rerun `/detect-sensors --refresh-stack` to regenerate. Only after the stack matches reality should you tweak the patterns themselves.

---

### Task 3.5: Phase 3 verification

- [ ] **Step 1: Grep-check all required prose**

Run:
```bash
grep -n '### 0\. Stack discovery' skills/detect-sensors/SKILL.md
grep -n '### 0\.5 Stack discovery — degraded path' skills/detect-sensors/SKILL.md
grep -n 'For `kind=observation` + `output=stream` sensors:' skills/detect-sensors/SKILL.md
grep -n '\.harness/stack\.json' skills/detect-sensors/SKILL.md
grep -n 'schemas/stack\.json' skills/detect-sensors/SKILL.md
```
Expected: each prints at least one matching line.

- [ ] **Step 2: Commit**

```bash
git add skills/detect-sensors/SKILL.md
git commit -m "$(cat <<'EOF'
docs(detect-sensors): add Phase A stack discovery + Phase B branch

§0 introduces stack discovery (synthesize <project>/.harness/stack.json
from manifests + initialization code). §0.5 documents the degraded
path. §4 branches on kind=observation + output=stream to derive
output_parsing.patterns[] from log_shapes[] instead of hand-crafting
regexes. §7 adds stack-refresh as the first remediation when patterns
match nothing.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 3: Phase 3 PR boundary**

Mergeable as `docs(detect-sensors): Phase A + Phase B prose (phase 3 of #27)`.

---

## Phase 4 — End-to-end fixture

Goal: prove that given a well-formed stack, the deterministic pipeline downstream produces patterns that work. Mini Go HTTP service + captured stdout + hand-written expected stack + Go test that derives patterns and matches.

### Task 4.1: Scaffold the fixture HTTP service

**Files:**
- Create: `test/fixtures/stack-discovery/cmd/server/main.go`
- Create: `test/fixtures/stack-discovery/go.mod`
- Create: `test/fixtures/stack-discovery/go.sum`

- [ ] **Step 1: Write `main.go`**

Create `test/fixtures/stack-discovery/cmd/server/main.go`:

```go
// Package main is a minimal HTTP service used as the stack-discovery
// fixture. Zap-Production logger + chi router + middleware.Logger.
// The /echo endpoint reacts to ?status=NNN to emit a configured status
// code so e2e tests can capture stdout shapes covering 2xx/4xx/5xx.
package main

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	r := chi.NewRouter()
	r.Use(middleware.Logger)

	r.Get("/echo", func(w http.ResponseWriter, req *http.Request) {
		status := 200
		if s := req.URL.Query().Get("status"); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				status = n
			}
		}
		w.WriteHeader(status)
		fmt.Fprintln(w, "ok")
	})

	logger.Info("server listening", zap.String("addr", ":8080"))
	if err := http.ListenAndServe(":8080", r); err != nil {
		logger.Error("listen", zap.Error(err))
	}
}
```

- [ ] **Step 2: Initialize `go.mod` for the fixture**

Run:
```bash
cd test/fixtures/stack-discovery
go mod init stack-discovery-fixture
go mod tidy
cd -
```

This creates `go.mod` and `go.sum` with `go.uber.org/zap` and `github.com/go-chi/chi/v5` resolved.

- [ ] **Step 3: Verify it builds**

```bash
cd test/fixtures/stack-discovery && go build ./cmd/server && cd -
```
Expected: a binary at `test/fixtures/stack-discovery/server` (gitignore it locally or delete after the build check).

---

### Task 4.2: Capture `expected-stdout.log`

**Files:**
- Create: `test/fixtures/stack-discovery/expected-stdout.log`

- [ ] **Step 1: Run the server, hit it, capture stdout**

Open two terminals. In terminal 1:
```bash
cd test/fixtures/stack-discovery && ./server 2>&1 | tee expected-stdout.raw
```

In terminal 2:
```bash
curl -s "http://localhost:8080/echo" >/dev/null
curl -s "http://localhost:8080/echo?status=400" >/dev/null
curl -s "http://localhost:8080/echo?status=500" >/dev/null
```

Ctrl-C the server.

- [ ] **Step 2: Trim and finalize**

Take the captured output and save the relevant lines (boot marker + 3 access-log lines) to `test/fixtures/stack-discovery/expected-stdout.log`. Strip any noise (terminal escape codes, partial lines).

The file should contain roughly:

```
{"level":"info","ts":1747130000.123,"caller":"server/main.go:30","msg":"server listening","addr":":8080"}
{"level":"info","ts":1747130001.234,"caller":"middleware/logger.go:80","msg":"\"GET http://localhost:8080/echo HTTP/1.1\" from 127.0.0.1:54321 - 200 3B in 312µs"}
{"level":"info","ts":1747130002.345,"caller":"middleware/logger.go:80","msg":"\"GET http://localhost:8080/echo?status=400 HTTP/1.1\" from 127.0.0.1:54322 - 400 3B in 250µs"}
{"level":"info","ts":1747130003.456,"caller":"middleware/logger.go:80","msg":"\"GET http://localhost:8080/echo?status=500 HTTP/1.1\" from 127.0.0.1:54323 - 500 3B in 280µs"}
```

(Exact bytes from your run will differ — that's fine; what matters is that the file is real captured stdout.)

- [ ] **Step 3: Clean up the local build**

```bash
rm -f test/fixtures/stack-discovery/server expected-stdout.raw
```

---

### Task 4.3: Hand-write `expected-stack.json`

**Files:**
- Create: `test/fixtures/stack-discovery/expected-stack.json`

- [ ] **Step 1: Author the stack**

Create `test/fixtures/stack-discovery/expected-stack.json`:

```json
{
  "version": "0.1.0",
  "detected_at": "2026-05-12T10:00:00Z",
  "detected_by": "manual",
  "languages": [
    { "name": "go", "version": "1.25" }
  ],
  "components": [
    {
      "role": "logger",
      "name": "go.uber.org/zap",
      "version": "1.27.0",
      "config_summary": "zap.NewProduction() with default EncoderConfig (LevelKey=level, TimeKey=ts, MessageKey=msg, CallerKey=caller).",
      "evidence": [
        { "file": "cmd/server/main.go", "line_start": 19, "line_end": 21, "rationale": "logger := zap.NewProduction() call site" }
      ]
    },
    {
      "role": "http-middleware",
      "name": "github.com/go-chi/chi/v5/middleware",
      "version": "5.0.0",
      "config_summary": "middleware.Logger registered before all routes; emits an INFO line per request through the configured zap logger (default logfmt-ish via fmt.Sprintf, embedded inside the JSON msg).",
      "evidence": [
        { "file": "cmd/server/main.go", "line_start": 24, "line_end": 24, "rationale": "r.Use(middleware.Logger)" }
      ]
    }
  ],
  "log_shapes": [
    {
      "id": "zap-prod-json",
      "produced_by": ["go.uber.org/zap"],
      "format": "json",
      "fields": [
        { "key": "level",  "meaning": "severity", "example_values": ["debug", "info", "warn", "error"] },
        { "key": "ts",     "meaning": "timestamp" },
        { "key": "msg",    "meaning": "message" },
        { "key": "caller", "meaning": "other" }
      ],
      "severity_values": ["debug", "info", "warn", "error", "dpanic", "panic", "fatal"],
      "sample": "{\"level\":\"info\",\"ts\":1747130000.123,\"caller\":\"server/main.go:30\",\"msg\":\"server listening\",\"addr\":\":8080\"}"
    }
  ]
}
```

Note: only ONE `log_shape` is needed for this fixture because chi's `middleware.Logger` writes through the same Zap logger, so its output is still a `zap-prod-json` line — the `msg` field happens to contain the access-log substring. A more realistic stack would split them, but for the fixture's e2e validation one shape is enough.

- [ ] **Step 2: Validate the stack against the schema**

Run:
```bash
go run -tags=write_stack ./skills/detect-sensors/scripts \
  --out=$(mktemp -d) \
  --schemas-dir=schemas \
  test/fixtures/stack-discovery/expected-stack.json
```
Expected: exit 0; prints the path to a written `stack.json`.

---

### Task 4.4: Write the e2e test

**Files:**
- Create: `lib/stack/e2e_fixture_test.go`

- [ ] **Step 1: Write the test**

Create `lib/stack/e2e_fixture_test.go`:

```go
package stack

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EFixture proves that given a well-formed Stack, a deterministic
// pattern-derivation helper produces regexes that match every line in a
// real captured stdout sample with the expected verdict distribution.
// This is the contract the LLM's Phase B prose is asked to honor.
func TestE2EFixture(t *testing.T) {
	fixtureDir := findFixtureDir(t)
	stackPath := filepath.Join(fixtureDir, "expected-stack.json")
	stdoutPath := filepath.Join(fixtureDir, "expected-stdout.log")

	body, err := os.ReadFile(stackPath)
	if err != nil {
		t.Fatalf("read expected-stack.json: %v", err)
	}
	var s Stack
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode expected-stack.json: %v", err)
	}
	if len(s.LogShapes) == 0 {
		t.Fatal("fixture stack has no log_shapes")
	}

	patterns := derivePatternsForShape(s.LogShapes[0], s)
	if len(patterns) == 0 {
		t.Fatal("derivePatternsForShape returned no patterns")
	}

	// Compile the regexes.
	type compiled struct {
		re      *regexp.Regexp
		verdict string
	}
	var ps []compiled
	for _, p := range patterns {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			t.Fatalf("compile regex %q: %v", p.Regex, err)
		}
		ps = append(ps, compiled{re: re, verdict: p.Verdict})
	}

	// Verify the sample is matched by at least one pattern (anchor invariant).
	matched := false
	for _, p := range ps {
		if p.re.MatchString(s.LogShapes[0].Sample) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("no derived pattern matches the shape sample %q", s.LogShapes[0].Sample)
	}

	// Read stdout, count verdicts.
	f, err := os.Open(stdoutPath)
	if err != nil {
		t.Fatalf("open stdout fixture: %v", err)
	}
	defer f.Close()
	counts := map[string]int{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		for _, p := range ps {
			if p.re.MatchString(line) {
				counts[p.verdict]++
				break
			}
		}
	}

	// Acceptance: every line in the captured stdout matches at least one
	// pattern (no unmatched lines). For the fixture, all four lines are
	// INFO, so we expect counts["pass"] == 4.
	totalMatched := 0
	for _, n := range counts {
		totalMatched += n
	}
	if totalMatched != 4 {
		t.Fatalf("totalMatched = %d, want 4; counts = %v", totalMatched, counts)
	}
}

// patternSpec mirrors a single output_parsing.patterns[] entry.
type patternSpec struct {
	Regex   string
	Verdict string
}

// derivePatternsForShape encodes the Phase B prose's pattern-derivation
// rules deterministically. Used by tests; not production code (Phase B
// is LLM-driven). The rules:
//
//   - severity ∈ {ERROR, FATAL, DPANIC, PANIC} → fail/high
//   - severity == WARN AND status_code ∈ 4xx/5xx → fail/medium
//   - severity == WARN (other) → warn/low
//   - severity == INFO (boot markers) → pass/info
//
// For format=json, the regex looks for `"<key>":"<value>"` patterns
// using the shape's literal field keys.
func derivePatternsForShape(sh LogShape, _ Stack) []patternSpec {
	if !sh.HasSeverity() {
		return nil
	}
	if sh.Format != FormatJSON {
		return nil
	}
	sevKey := sh.FieldsByMeaning(MeaningSeverity)[0].Key
	var out []patternSpec
	// Map case-insensitive sev tokens by category.
	highTokens := []string{}
	warnTokens := []string{}
	infoTokens := []string{}
	for _, v := range sh.SeverityValues {
		switch strings.ToUpper(v) {
		case "ERROR", "FATAL", "DPANIC", "PANIC":
			highTokens = append(highTokens, regexp.QuoteMeta(v))
		case "WARN", "WARNING":
			warnTokens = append(warnTokens, regexp.QuoteMeta(v))
		case "INFO":
			infoTokens = append(infoTokens, regexp.QuoteMeta(v))
		}
	}
	if len(highTokens) > 0 {
		out = append(out, patternSpec{
			Regex:   fmt.Sprintf(`"%s":"(?:%s)"`, regexp.QuoteMeta(sevKey), strings.Join(highTokens, "|")),
			Verdict: "fail",
		})
	}
	if len(warnTokens) > 0 {
		out = append(out, patternSpec{
			Regex:   fmt.Sprintf(`"%s":"(?:%s)"`, regexp.QuoteMeta(sevKey), strings.Join(warnTokens, "|")),
			Verdict: "warn",
		})
	}
	if len(infoTokens) > 0 {
		out = append(out, patternSpec{
			Regex:   fmt.Sprintf(`"%s":"(?:%s)"`, regexp.QuoteMeta(sevKey), strings.Join(infoTokens, "|")),
			Verdict: "pass",
		})
	}
	return out
}

func findFixtureDir(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "test", "fixtures", "stack-discovery")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("test/fixtures/stack-discovery not found")
	return ""
}
```

- [ ] **Step 2: Run the e2e test**

Run:
```bash
go test ./lib/stack/... -run TestE2EFixture -v
```
Expected: PASS with the counts log line showing all 4 fixture lines matched as `pass`.

- [ ] **Step 3: Commit**

```bash
git add test/fixtures/stack-discovery/ lib/stack/e2e_fixture_test.go
git commit -m "$(cat <<'EOF'
test(stack): add stack-discovery e2e fixture

Mini Go HTTP service (zap.NewProduction() + chi middleware.Logger),
captured expected-stdout.log, hand-written expected-stack.json, and a
deterministic pattern-derivation test that proves the schema can
describe a real service and that the Phase B prose rules produce
patterns matching every line.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4.5: Final acceptance sweep

- [ ] **Step 1: Run every test target the spec lists in §Acceptance criteria**

```bash
# Phase 1 contracts
go test ./lib/stack/...
go test -tags=write_stack ./skills/detect-sensors/scripts/...
go test -tags=write_sensor ./skills/detect-sensors/scripts/...

# Phase 2 contracts
go test ./lib/registry/... ./lib/orchestrator/... ./lib/sensor/...
go test -tags=error_autofiler ./hooks/...
go test -tags=run_computational ./...
go test -tags=run_inferential ./...

# Vet
go vet ./...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
go vet -tags=write_stack ./skills/detect-sensors/scripts/...
go vet -tags=write_sensor ./skills/detect-sensors/scripts/...
```
Expected: all green.

- [ ] **Step 2: Grep invariants (acceptance gates from the spec)**

```bash
# No legacy literals in production Go source under lib/ and hooks/.
grep -rn '"\.runtime"\|"sensors/"' lib/ hooks/

# No legacy paths in any SKILL.md.
grep -rn '\.runtime/sensors\|<projectRoot>/sensors\|under `sensors/`' skills/*/SKILL.md

# Required prose markers in detect-sensors.
grep -n '### 0\. Stack discovery' skills/detect-sensors/SKILL.md
grep -n '### 0\.5 Stack discovery — degraded path' skills/detect-sensors/SKILL.md
grep -n 'For `kind=observation` + `output=stream` sensors:' skills/detect-sensors/SKILL.md
grep -n '\.harness/stack\.json' skills/detect-sensors/SKILL.md

# Filesystem-layout invariants.
[ -d .harness/sensors ] && echo OK || echo "MISSING .harness/sensors"
[ -f .gitignore ] && grep -q '\.harness/runtime' .gitignore && echo "gitignore OK" || echo "gitignore MISSING"
[ ! -d sensors ] && echo "old sensors/ removed OK" || echo "OLD sensors/ STILL PRESENT"
```
Expected: first two greps print zero matches; remaining greps print matches; filesystem checks all print OK.

- [ ] **Step 3: Phase 4 PR boundary**

Mergeable as `test(stack): end-to-end fixture (phase 4 of #27)`. This is the final phase — after merge, issue #27 is closeable.

---

## Out-of-scope reminders

Track each of these as a separate GitHub issue when starting work:

1. `/tail-sensor --filter <gojq>` — JSONL filter at tail time.
2. `captures.metadata.*` extension to surface trace_id/status_code into `Signal.metadata`.
3. `/heal-sensor` stack-staleness detection.
4. `/detect-sensors --diff-stack`.
5. `/tail-sensor` run_id subdir read bug (issue #27 footer).
6. `/stop-sensor` aggregate `counts` zero bug (issue #27 footer).
