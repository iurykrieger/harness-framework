# /create-sensor Skill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new Claude Code skill `/create-sensor` that takes a single requirement prompt and produces one targeted assertion sensor at `<project>/.harness/sensors/<id>.json`, composing existing sensors as `depends_on` / `requires[kind=sensor]` based on each dep's `execution.blocking` flag.

**Architecture:** New skill at `skills/create-sensor/` with `SKILL.md` orchestrating a 7-phase clarification dialogue, plus three skill-local Go scripts (`catalog-sensors.go`, `write-fixture.go`, `write-sensor.go`) — each its own `package main` with a build tag, each with a `_test.go` sibling. Schema validation and atomic persistence reuse `lib/sensor.ValidateAndPersist`. No changes to `lib/`, schemas, or other skills.

**Tech Stack:** Go 1.25 (module `github.com/iurykrieger/harness-framework`), standard library `testing`, `github.com/santhosh-tekuri/jsonschema/v5` (transitive via `lib/schema`), `lib/sensor`, `lib/schema`, `lib/registry`. Skill body is Markdown prose.

---

## File Structure

```
skills/create-sensor/
├── SKILL.md                                # frontmatter + 7-phase orchestration body
└── scripts/
    ├── catalog-sensors.go                  # //go:build catalog_sensors
    ├── catalog-sensors_test.go
    ├── write-fixture.go                    # //go:build write_fixture
    ├── write-fixture_test.go
    ├── write-sensor.go                     # //go:build write_sensor
    ├── write-sensor_test.go
    └── testdata/
        ├── catalog-sensors/
        │   ├── empty/                      # (empty directory)
        │   ├── one-sensor/<id>.json
        │   ├── multi-sensor/<id>.json × 3
        │   ├── malformed/                  # mix of valid + broken JSON
        │   └── schema-invalid/             # mix of valid + schema-invalid
        ├── write-fixture/                  # (no static fixtures; tests use t.TempDir)
        └── write-sensor/                   # (no static fixtures; tests use t.TempDir + canonical-computational)
```

Each file has one clear responsibility:

| File | Responsibility |
|---|---|
| `SKILL.md` | Prose orchestration for the LLM: parse → catalog → classify → draft → clarify → fixtures → persist |
| `catalog-sensors.go` | Read-only enumeration of `<project>/.harness/sensors/*.json` → JSONL digest |
| `write-fixture.go` | Atomic write of one fixture file under `<project>/.harness/sensors/fixtures/` |
| `write-sensor.go` | Pre-checks (fixture existence, id collision) + delegate to `lib/sensor.ValidateAndPersist` |

---

## Task 1: Scaffold skill directory and SKILL.md frontmatter

**Files:**
- Create: `skills/create-sensor/SKILL.md`

The frontmatter alone is enough to make the skill discoverable by Claude Code's loader. The full 7-phase body is written in Task 5 after all scripts exist (so the body can reference real, working CLI flags). For Task 1 the body is a one-line placeholder.

- [ ] **Step 1.1: Create the skill directory and SKILL.md with frontmatter and a placeholder body**

```bash
mkdir -p skills/create-sensor/scripts/testdata
```

Create `skills/create-sensor/SKILL.md`:

```markdown
---
name: create-sensor
description: Use when the user invokes /create-sensor or asks to create a single sensor that validates a specific acceptance criterion, functional requirement, or use case. Takes a free-text requirement as input, runs an interactive clarification dialogue, composes existing sensors as dependencies, synthesizes fixtures, and persists one new assertion sensor to <project>/.harness/sensors/<id>.json via the schema validator. Distinct from /detect-sensors, which sweeps the whole project; /create-sensor produces exactly one targeted sensor per invocation.
---

# create-sensor

(Body is written in Task 5 once the scripts exist.)
```

- [ ] **Step 1.2: Commit**

```bash
git add skills/create-sensor/SKILL.md
git commit -m "feat(create-sensor): scaffold skill directory with frontmatter"
```

---

## Task 2: `catalog-sensors.go` — read-only sensor digest enumeration

**Files:**
- Create: `skills/create-sensor/scripts/catalog-sensors.go`
- Create: `skills/create-sensor/scripts/catalog-sensors_test.go`
- Create: `skills/create-sensor/scripts/testdata/catalog-sensors/empty/.gitkeep`
- Create: `skills/create-sensor/scripts/testdata/catalog-sensors/one-sensor/smoke-comp.json`
- Create: `skills/create-sensor/scripts/testdata/catalog-sensors/multi-sensor/{a,b,c}.json` (3 valid sensors)
- Create: `skills/create-sensor/scripts/testdata/catalog-sensors/malformed/{good.json,broken.json}`
- Create: `skills/create-sensor/scripts/testdata/catalog-sensors/schema-invalid/{good.json,missing-field.json}`

The script reads `<sensorsDir>/*.json`, parses each, and emits one JSONL digest line per sensor on stdout. Malformed JSON or schema-invalid sensors (when `--schemas-dir` is set) produce a warn Signal envelope on stdout and are skipped.

### Digest shape

Each stdout line is a JSON object:

```json
{"id":"...","kind":"...","type":"...","output":"...","blocking":true,"description":"...","path":".harness/sensors/<id>.json"}
```

- `blocking` is `false` when `execution.blocking` is absent or false.
- `path` is **relative to `<projectRoot>`** for log-friendliness. The script computes it by joining `.harness/sensors/<id>.json`.

### CLI contract

```
catalog-sensors [--sensors-dir <dir>] [--schemas-dir <dir>]
```

- `--sensors-dir`: defaults to `<projectRoot>/.harness/sensors/` resolved via `registry.Lookup(cwd)`. When `HARNESS_REGISTRY_ROOT` is set, that wins; otherwise walks up looking for `.harness/`. If discovery fails, the script emits `registry.DiscoveryErrorSignal(...)` on stdout and exits 0 (the catalog is just empty — not an error condition for downstream callers).
- `--schemas-dir`: when set, each sensor JSON is schema-validated; schema-invalid sensors are skipped with a warn envelope.

### Exit codes

- `0` — normal completion (digest written, even if empty)
- `2` — usage error (unknown flag, etc.)

The script does NOT exit non-zero on per-file parse/validation failures; those produce warn envelopes interleaved with valid digest lines.

### Testdata layout

Use the `lib/sensor/testdata/canonical-computational.json` as the seed for all test sensor JSON files. For `multi-sensor`, copy that file three times with different `id` fields (`a`, `b`, `c` — and adjust to `smoke-comp` → other ids so the JSON remains schema-valid). The `malformed/broken.json` contains `not-json`. The `schema-invalid/missing-field.json` is canonical-computational with the `regulation` field deleted.

- [ ] **Step 2.1: Write a failing test for the empty-directory case**

Create `skills/create-sensor/scripts/catalog-sensors_test.go`:

```go
//go:build catalog_sensors

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the absolute path to the repo root, derived from this
// test file's location. catalog-sensors_test.go lives at
// skills/create-sensor/scripts/, three levels deep.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

func TestRun_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
}

func TestRun_MissingDir(t *testing.T) {
	parent := t.TempDir()
	missing := filepath.Join(parent, "does-not-exist")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", missing}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("expected empty stdout, got %q", stdout.String())
	}
}

// helper: copy a sensor JSON from lib/sensor/testdata into a temp dir, with id rewritten.
func copyCanonical(t *testing.T, dstDir, newID string) string {
	t.Helper()
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.json")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.Replace(string(body), `"smoke-comp"`, `"`+newID+`"`, 1)
	dst := filepath.Join(dstDir, newID+".json")
	if err := os.WriteFile(dst, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}
```

- [ ] **Step 2.2: Run the tests to confirm they fail with `run undefined`**

```bash
GOWORK=off go test -tags=catalog_sensors ./skills/create-sensor/scripts/...
```

Expected: compile error `undefined: run` (and other helpers). This confirms the test file is wired up; the next step writes the script skeleton.

- [ ] **Step 2.3: Create the script skeleton with flag parsing and an empty-directory path**

Create `skills/create-sensor/scripts/catalog-sensors.go`:

```go
//go:build catalog_sensors

// Command catalog-sensors emits a JSONL digest of every sensor JSON file
// under <projectRoot>/.harness/sensors/, plus warn envelopes for files
// that fail to parse or fail schema validation. Used by /create-sensor
// to seed the clarification dialogue with the user's existing sensor
// inventory.
//
// Usage:
//
//	catalog-sensors [--sensors-dir <dir>] [--schemas-dir <dir>]
//
// Exit codes: 0 normal completion, 2 usage error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("catalog-sensors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sensorsDir, schemasDir string
	fs.StringVar(&sensorsDir, "sensors-dir", "", "directory to scan (default: <projectRoot>/.harness/sensors/)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory; when set, each sensor is schema-validated")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: catalog-sensors [--sensors-dir DIR] [--schemas-dir DIR]")
		return 2
	}

	// Resolve sensorsDir via registry discovery if not explicit.
	projectRoot := ""
	if sensorsDir == "" {
		cwd, _ := os.Getwd()
		res, err := registry.Lookup(cwd)
		if err != nil {
			// Discovery failed; emit the canonical signal and exit 0 (empty catalog).
			emitJSON(stdout, registry.DiscoveryErrorSignal(err, "catalog-sensors"))
			return 0
		}
		projectRoot = res.ProjectRoot
		sensorsDir = filepath.Join(projectRoot, ".harness", "sensors")
	} else {
		// When the dir is explicit, derive projectRoot from it for the "path" field.
		projectRoot = deriveProjectRoot(sensorsDir)
	}

	entries, err := os.ReadDir(sensorsDir)
	if err != nil {
		// Missing directory: empty catalog, exit 0.
		if os.IsNotExist(err) {
			return 0
		}
		fmt.Fprintln(stderr, "error: read dir:", err)
		return 2
	}

	// Build a stable order for deterministic output.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var validator *schema.Validator
	if schemasDir != "" {
		v, vErr := schema.NewValidator(schemasDir)
		if vErr != nil {
			fmt.Fprintln(stderr, "error: load schemas:", vErr)
			return 2
		}
		validator = v
	}

	for _, name := range names {
		path := filepath.Join(sensorsDir, name)
		body, err := os.ReadFile(path)
		if err != nil {
			emitJSON(stdout, warnSignal(name, fmt.Sprintf("read %s: %v", path, err)))
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(body, &m); err != nil {
			emitJSON(stdout, warnSignal(name, fmt.Sprintf("parse %s: %v", path, err)))
			continue
		}
		if validator != nil {
			if err := validator.Validate(schema.TargetSensor, m); err != nil {
				emitJSON(stdout, warnSignal(name, fmt.Sprintf("schema-invalid %s: %v", path, err)))
				continue
			}
		}
		emitJSON(stdout, digest(m, projectRoot))
	}
	return 0
}

// digest projects the fields /create-sensor consumes from the sensor JSON.
func digest(m map[string]interface{}, projectRoot string) map[string]interface{} {
	id, _ := m["id"].(string)
	blocking := false
	if exec, ok := m["execution"].(map[string]interface{}); ok {
		if b, ok := exec["blocking"].(bool); ok {
			blocking = b
		}
	}
	relPath := filepath.Join(".harness", "sensors", id+".json")
	out := map[string]interface{}{
		"id":          id,
		"kind":        m["kind"],
		"type":        m["type"],
		"output":      m["output"],
		"blocking":    blocking,
		"description": m["description"],
		"path":        relPath,
	}
	return out
}

func warnSignal(file, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "catalog-sensors",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "warn",
		"severity":    "low",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale, "file": file}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "catalog_entry_skipped"},
	}
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}

// deriveProjectRoot strips trailing .harness/sensors/ from an explicit
// sensorsDir to recover the project root used in digest path fields.
// When the input does not match that suffix, the function returns "".
func deriveProjectRoot(sensorsDir string) string {
	abs, err := filepath.Abs(sensorsDir)
	if err != nil {
		return ""
	}
	clean := filepath.Clean(abs)
	parent := filepath.Dir(clean)
	if filepath.Base(parent) == ".harness" {
		return filepath.Dir(parent)
	}
	return ""
}
```

- [ ] **Step 2.4: Run tests; the empty-dir and missing-dir tests should now pass**

```bash
GOWORK=off go test -tags=catalog_sensors -run TestRun_EmptyDir ./skills/create-sensor/scripts/...
GOWORK=off go test -tags=catalog_sensors -run TestRun_MissingDir ./skills/create-sensor/scripts/...
```

Expected: both PASS.

- [ ] **Step 2.5: Add tests for single-sensor and multi-sensor cases**

Append to `skills/create-sensor/scripts/catalog-sensors_test.go`:

```go
func TestRun_OneSensor(t *testing.T) {
	dir := t.TempDir()
	copyCanonical(t, dir, "alpha")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := splitNonEmpty(stdout.String())
	if len(lines) != 1 {
		t.Fatalf("expected 1 JSONL line, got %d: %q", len(lines), stdout.String())
	}
	if !strings.Contains(lines[0], `"id":"alpha"`) {
		t.Fatalf("line missing id=alpha: %q", lines[0])
	}
	if !strings.Contains(lines[0], `"kind":"assertion"`) {
		t.Fatalf("line missing kind=assertion: %q", lines[0])
	}
	if !strings.Contains(lines[0], `"blocking":false`) {
		t.Fatalf("line missing blocking=false: %q", lines[0])
	}
}

func TestRun_MultipleSensors_SortedByID(t *testing.T) {
	dir := t.TempDir()
	copyCanonical(t, dir, "charlie")
	copyCanonical(t, dir, "alpha")
	copyCanonical(t, dir, "bravo")

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := splitNonEmpty(stdout.String())
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}
	for i, want := range []string{"alpha", "bravo", "charlie"} {
		needle := `"id":"` + want + `"`
		if !strings.Contains(lines[i], needle) {
			t.Fatalf("line %d expected %s, got %q", i, needle, lines[i])
		}
	}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
```

- [ ] **Step 2.6: Run the new tests; expect PASS**

```bash
GOWORK=off go test -tags=catalog_sensors ./skills/create-sensor/scripts/...
```

Expected: all four tests PASS.

- [ ] **Step 2.7: Add a test for the malformed-JSON path**

Append:

```go
func TestRun_MalformedJSON_EmitsWarn(t *testing.T) {
	dir := t.TempDir()
	copyCanonical(t, dir, "ok-sensor")
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := splitNonEmpty(stdout.String())
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (1 valid + 1 warn), got %d: %q", len(lines), stdout.String())
	}
	hasWarn, hasValid := false, false
	for _, line := range lines {
		if strings.Contains(line, `"verdict":"warn"`) {
			hasWarn = true
		}
		if strings.Contains(line, `"id":"ok-sensor"`) {
			hasValid = true
		}
	}
	if !hasWarn || !hasValid {
		t.Fatalf("expected one warn and one valid digest; got %q", stdout.String())
	}
}
```

- [ ] **Step 2.8: Run; expect PASS (script already handles parse errors)**

```bash
GOWORK=off go test -tags=catalog_sensors -run TestRun_MalformedJSON ./skills/create-sensor/scripts/...
```

Expected: PASS. If FAIL, inspect the script — it likely already passes because the malformed-JSON code path was implemented in Step 2.3.

- [ ] **Step 2.9: Add a test for schema validation rejection**

Append:

```go
func TestRun_SchemaInvalid_EmitsWarn(t *testing.T) {
	dir := t.TempDir()
	copyCanonical(t, dir, "valid")
	// Build a schema-invalid sensor: canonical with "regulation" deleted.
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.json")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = "broken-schema"
	delete(m, "regulation")
	bad, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "broken-schema.json"), bad, 0o644); err != nil {
		t.Fatal(err)
	}

	schemasDir := filepath.Join(repoRoot(t), "schemas")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir, "--schemas-dir", schemasDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	lines := splitNonEmpty(stdout.String())
	hasWarn, hasValid := false, false
	for _, line := range lines {
		if strings.Contains(line, `"verdict":"warn"`) && strings.Contains(line, "broken-schema") {
			hasWarn = true
		}
		if strings.Contains(line, `"id":"valid"`) {
			hasValid = true
		}
	}
	if !hasWarn || !hasValid {
		t.Fatalf("expected warn for broken-schema and valid digest for 'valid'; got %q", stdout.String())
	}
}
```

Also add the import at the top of the test file: `"encoding/json"`.

- [ ] **Step 2.10: Run; expect PASS**

```bash
GOWORK=off go test -tags=catalog_sensors ./skills/create-sensor/scripts/...
```

- [ ] **Step 2.11: Add a test for the `blocking: true` digest derivation**

Append:

```go
func TestRun_BlockingDerivation(t *testing.T) {
	dir := t.TempDir()
	// Load canonical and set execution.blocking = true.
	src := filepath.Join(repoRoot(t), "lib", "sensor", "testdata", "canonical-computational.json")
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = "blocking-one"
	exec := m["execution"].(map[string]interface{})
	exec["blocking"] = true
	exec["graceful_timeout_ms"] = float64(5000)
	delete(m["cost"].(map[string]interface{})["latency"].(map[string]interface{}), "timeout_ms")
	out, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "blocking-one.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--sensors-dir", dir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"blocking":true`) {
		t.Fatalf("expected blocking=true in digest, got %q", stdout.String())
	}
}
```

Note: setting `blocking=true` + `graceful_timeout_ms` while removing `latency.timeout_ms` keeps the canonical sensor schema-valid for blocking lifecycle. The test does not pass `--schemas-dir` so this trade-off does not need to be perfect; the digest path runs regardless.

- [ ] **Step 2.12: Run; expect PASS**

```bash
GOWORK=off go test -tags=catalog_sensors ./skills/create-sensor/scripts/...
```

- [ ] **Step 2.13: Run `go vet` to catch unused imports / dead code**

```bash
GOWORK=off go vet -tags=catalog_sensors ./skills/create-sensor/scripts/...
```

Expected: no output (clean).

- [ ] **Step 2.14: Commit**

```bash
git add skills/create-sensor/scripts/catalog-sensors.go \
        skills/create-sensor/scripts/catalog-sensors_test.go
git commit -m "feat(create-sensor): catalog-sensors.go enumerates sensor digests as JSONL"
```

---

## Task 3: `write-fixture.go` — atomic fixture file write

**Files:**
- Create: `skills/create-sensor/scripts/write-fixture.go`
- Create: `skills/create-sensor/scripts/write-fixture_test.go`

The script writes one fixture payload atomically to `<projectRoot>/.harness/sensors/fixtures/<sensor-id>/<case>.<ext>`. The target path is rejected if, after cleaning, it does not have `<projectRoot>/.harness/sensors/fixtures/` as a prefix.

### CLI contract

```
write-fixture [--from-file <src>] <target-relative-path>
```

- positional 1: target path **relative to `<projectRoot>`**, e.g. `.harness/sensors/fixtures/assert-x/pass.txt`.
- `--from-file <src>`: optional flag; when set, payload is read from `<src>`. When omitted, payload is read from stdin.

### Exit codes

- `0` — fixture written; emits `verdict=pass` Signal envelope on stdout.
- `2` — usage error, path escape, I/O failure; emits `verdict=error` Signal envelope on stdout.

- [ ] **Step 3.1: Write failing tests for the happy path and path-escape rejection**

Create `skills/create-sensor/scripts/write-fixture_test.go`:

```go
//go:build write_fixture

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withProjectRoot sets HARNESS_REGISTRY_ROOT to a fresh temp dir
// containing a .harness/ marker, restores the previous value on cleanup,
// and returns the absolute project root.
func withProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, hadPrev := os.LookupEnv("HARNESS_REGISTRY_ROOT")
	os.Setenv("HARNESS_REGISTRY_ROOT", root)
	t.Cleanup(func() {
		if hadPrev {
			os.Setenv("HARNESS_REGISTRY_ROOT", prev)
		} else {
			os.Unsetenv("HARNESS_REGISTRY_ROOT")
		}
	})
	return root
}

func TestRun_HappyPath_Stdin(t *testing.T) {
	root := withProjectRoot(t)
	rel := ".harness/sensors/fixtures/assert-x/pass.txt"

	var stdout, stderr bytes.Buffer
	stdin := bytes.NewBufferString("200\n")
	code := runWithStdin([]string{rel}, stdin, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("fixture not written: %v", err)
	}
	if string(body) != "200\n" {
		t.Fatalf("payload=%q want %q", body, "200\n")
	}
	if !strings.Contains(stdout.String(), `"verdict":"pass"`) {
		t.Fatalf("missing pass signal: %q", stdout.String())
	}
}

func TestRun_PathEscape_Rejected(t *testing.T) {
	withProjectRoot(t)
	for _, bad := range []string{
		".harness/sensors/fixtures/../escape.txt",
		".harness/sensors/escape.txt",
		"/etc/passwd",
		"../outside.txt",
	} {
		var stdout, stderr bytes.Buffer
		stdin := bytes.NewBufferString("payload")
		code := runWithStdin([]string{bad}, stdin, &stdout, &stderr)
		if code == 0 {
			t.Fatalf("expected non-zero exit for %q, got 0", bad)
		}
		if !strings.Contains(stdout.String(), "fixture_path_escape") {
			t.Fatalf("missing fixture_path_escape for %q: %q", bad, stdout.String())
		}
	}
}
```

- [ ] **Step 3.2: Run; expect compile error (`runWithStdin undefined`)**

```bash
GOWORK=off go test -tags=write_fixture ./skills/create-sensor/scripts/...
```

Expected: compile error.

- [ ] **Step 3.3: Write the script**

Create `skills/create-sensor/scripts/write-fixture.go`:

```go
//go:build write_fixture

// Command write-fixture writes a fixture payload atomically to a path
// under <projectRoot>/.harness/sensors/fixtures/. Used by /create-sensor
// to materialize the fixture files referenced by a sensor's
// verification.golden_cases[].
//
// Usage:
//
//	write-fixture [--from-file <src>] <target-relative-path>
//
// Exit codes: 0 success, 2 usage / path escape / I/O failure.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func main() {
	os.Exit(runWithStdin(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runWithStdin(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("write-fixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var fromFile string
	fs.StringVar(&fromFile, "from-file", "", "read payload from this file (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: write-fixture [--from-file SRC] <target-relative-path>")
		return 2
	}
	relPath := fs.Arg(0)

	cwd, _ := os.Getwd()
	res, err := registry.Lookup(cwd)
	if err != nil {
		emitJSON(stdout, registry.DiscoveryErrorSignal(err, "write-fixture"))
		return 2
	}
	projectRoot := res.ProjectRoot

	fixturesRoot := filepath.Join(projectRoot, ".harness", "sensors", "fixtures")
	target := filepath.Clean(filepath.Join(projectRoot, relPath))
	if !strings.HasPrefix(target+string(os.PathSeparator), fixturesRoot+string(os.PathSeparator)) {
		emitJSON(stdout, errorSignal("fixture_path_escape", fmt.Sprintf("path %q resolves outside %s", relPath, fixturesRoot)))
		return 2
	}

	var payload []byte
	if fromFile != "" {
		payload, err = os.ReadFile(fromFile)
	} else {
		payload, err = io.ReadAll(stdin)
	}
	if err != nil {
		emitJSON(stdout, errorSignal("read_payload", err.Error()))
		return 2
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		emitJSON(stdout, errorSignal("mkdir", err.Error()))
		return 2
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-fixture-*")
	if err != nil {
		emitJSON(stdout, errorSignal("create_tmp", err.Error()))
		return 2
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		emitJSON(stdout, errorSignal("write_tmp", err.Error()))
		return 2
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		emitJSON(stdout, errorSignal("sync_tmp", err.Error()))
		return 2
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		emitJSON(stdout, errorSignal("close_tmp", err.Error()))
		return 2
	}
	if err := os.Rename(tmpPath, target); err != nil {
		os.Remove(tmpPath)
		emitJSON(stdout, errorSignal("rename", err.Error()))
		return 2
	}

	emitJSON(stdout, passSignal(target))
	return 0
}

func passSignal(target string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "write-fixture",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "fixture written"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "fixture_written", "path": target},
	}
}

func errorSignal(kind, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "write-fixture",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": kind},
	}
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}
```

- [ ] **Step 3.4: Run the two existing tests; expect PASS**

```bash
GOWORK=off go test -tags=write_fixture ./skills/create-sensor/scripts/...
```

- [ ] **Step 3.5: Add tests for `--from-file`, parent-dir creation, and idempotency**

Append to `skills/create-sensor/scripts/write-fixture_test.go`:

```go
func TestRun_FromFile(t *testing.T) {
	root := withProjectRoot(t)
	src := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(src, []byte("404\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel := ".harness/sensors/fixtures/assert-y/fail.txt"

	var stdout, stderr bytes.Buffer
	code := runWithStdin([]string{"--from-file", src, rel}, bytes.NewBuffer(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "404\n" {
		t.Fatalf("body=%q", body)
	}
}

func TestRun_CreatesNestedParents(t *testing.T) {
	root := withProjectRoot(t)
	rel := ".harness/sensors/fixtures/deeply/nested/case/pass.txt"

	var stdout, stderr bytes.Buffer
	code := runWithStdin([]string{rel}, bytes.NewBufferString("ok"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Fatalf("nested fixture not written: %v", err)
	}
}

func TestRun_IdempotentRewrite(t *testing.T) {
	root := withProjectRoot(t)
	rel := ".harness/sensors/fixtures/assert-z/pass.txt"

	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		code := runWithStdin([]string{rel}, bytes.NewBufferString("same"), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("iter %d: exit=%d stderr=%s", i, code, stderr.String())
		}
	}
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "same" {
		t.Fatalf("body=%q want %q", body, "same")
	}
}
```

- [ ] **Step 3.6: Run; expect PASS**

```bash
GOWORK=off go test -tags=write_fixture ./skills/create-sensor/scripts/...
```

- [ ] **Step 3.7: Run vet**

```bash
GOWORK=off go vet -tags=write_fixture ./skills/create-sensor/scripts/...
```

- [ ] **Step 3.8: Commit**

```bash
git add skills/create-sensor/scripts/write-fixture.go \
        skills/create-sensor/scripts/write-fixture_test.go
git commit -m "feat(create-sensor): write-fixture.go atomic fixture writer with path-escape guard"
```

---

## Task 4: `write-sensor.go` — pre-checks + delegate to `lib/sensor.ValidateAndPersist`

**Files:**
- Create: `skills/create-sensor/scripts/write-sensor.go`
- Create: `skills/create-sensor/scripts/write-sensor_test.go`

This is the strict counterpart to `skills/detect-sensors/scripts/write-sensor.go`. The detect-sensors version overwrites and does not check fixtures; this version:

1. Rejects if any `verification.golden_cases[].fixture` path is missing on disk.
2. Rejects if `<id>.json` already exists at the target.
3. Delegates the schema validation + atomic write to `lib/sensor.ValidateAndPersist`.

### CLI contract

```
write-sensor --out <dir> [--schemas-dir <dir>] <draft.json>
```

- `--out`: required. Target directory (e.g. `<projectRoot>/.harness/sensors/`).
- `--schemas-dir`: optional. Forwarded to `ValidateAndPersist`.
- positional 1: path to a draft sensor JSON file.

### Exit codes

- `0` — sensor written; stdout carries the `verdict=pass` Signal envelope (single line) followed by the absolute path on a second line.
- `1` — schema validation failed.
- `2` — usage / I/O / pre-check failure (missing fixture, id collision, draft missing).

In all non-zero cases, stdout carries a `verdict=error` Signal envelope with `metadata.kind` set to one of: `missing_fixture`, `sensor_already_exists`, `schema_invalid`, `read_draft`, `usage`, `registry_discovery_failed`.

### Fixture path resolution

Each `verification.golden_cases[].fixture` is relative to the project root. The script resolves the project root via `registry.Lookup(cwd)`. If discovery fails, the script emits the canonical discovery-failure signal and exits 2.

- [ ] **Step 4.1: Write failing tests for the happy path, schema-invalid, id-collision, and missing-fixture cases**

Create `skills/create-sensor/scripts/write-sensor_test.go`:

```go
//go:build write_sensor

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/sensor/sensortest"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

// withProjectRoot creates a fresh project root with a .harness/ marker,
// .harness/sensors/, and .harness/sensors/fixtures/. Returns absolute root.
func withProjectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, sub := range []string{".harness", ".harness/sensors", ".harness/sensors/fixtures"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prev, had := os.LookupEnv("HARNESS_REGISTRY_ROOT")
	os.Setenv("HARNESS_REGISTRY_ROOT", root)
	t.Cleanup(func() {
		if had {
			os.Setenv("HARNESS_REGISTRY_ROOT", prev)
		} else {
			os.Unsetenv("HARNESS_REGISTRY_ROOT")
		}
	})
	return root
}

// writeDraftWithFixture builds a canonical-computational sensor with id=newID,
// optionally writes the fixture file referenced by golden_cases[0], and returns
// the draft path.
func writeDraftWithFixture(t *testing.T, root, newID string, writeFixture bool) string {
	t.Helper()
	s := sensortest.LoadComputational(t)
	s.ID = newID
	fixtureRel := ".harness/sensors/fixtures/" + newID + "/pass.txt"
	s.Verification.GoldenCases = []sensor.GoldenCase{
		{
			Fixture:          fixtureRel,
			ExpectedVerdict:  "pass",
			ExpectedSeverity: "info",
		},
	}
	if writeFixture {
		full := filepath.Join(root, fixtureRel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("ok"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	draftDir := t.TempDir()
	draftPath := filepath.Join(draftDir, "draft.json")
	body, err := json.Marshal(s.AsMap())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(draftPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return draftPath
}

func TestRun_HappyPath(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")
	draft := writeDraftWithFixture(t, root, "alpha", true)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "alpha.json")); err != nil {
		t.Fatalf("alpha.json not written: %v", err)
	}
	if !strings.Contains(stdout.String(), `"verdict":"pass"`) {
		t.Fatalf("missing pass signal: %s", stdout.String())
	}
}

func TestRun_MissingFixture(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")
	draft := writeDraftWithFixture(t, root, "beta", false) // fixture NOT written

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d (want 2) stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "missing_fixture") {
		t.Fatalf("missing_fixture not in stdout: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "beta.json")); !os.IsNotExist(err) {
		t.Fatalf("beta.json should not exist, err=%v", err)
	}
}

func TestRun_SensorAlreadyExists(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")

	// Pre-existing target file with stale content.
	target := filepath.Join(outDir, "gamma.json")
	if err := os.WriteFile(target, []byte(`{"id":"gamma"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	draft := writeDraftWithFixture(t, root, "gamma", true)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draft}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit=%d (want 2) stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "sensor_already_exists") {
		t.Fatalf("expected sensor_already_exists; got %s", stdout.String())
	}
	// Verify the stale content survived.
	body, _ := os.ReadFile(target)
	if !strings.Contains(string(body), `"id":"gamma"`) || strings.Contains(string(body), `"smoke-comp"`) {
		t.Fatalf("stale content should be untouched; got %s", body)
	}
}

func TestRun_SchemaInvalid(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	outDir := filepath.Join(root, ".harness", "sensors")

	// Build a draft missing the required "regulation" field.
	s := sensortest.LoadComputational(t).AsMap()
	s["id"] = "delta"
	delete(s, "regulation")
	// Even bad drafts need a fixture for pre-checks; we ensure the
	// schema check is what trips, not the missing-fixture check.
	fixtureRel := ".harness/sensors/fixtures/delta/pass.txt"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(fixtureRel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, fixtureRel), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	s["verification"] = map[string]interface{}{
		"golden_cases": []interface{}{
			map[string]interface{}{
				"fixture":           fixtureRel,
				"expected_verdict":  "pass",
				"expected_severity": "info",
			},
		},
	}
	draftDir := t.TempDir()
	draftPath := filepath.Join(draftDir, "draft.json")
	body, _ := json.Marshal(s)
	if err := os.WriteFile(draftPath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--out", outDir, "--schemas-dir", schemasDir, draftPath}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit=%d (want 1) stdout=%s", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "schema_invalid") {
		t.Fatalf("missing schema_invalid in stdout: %s", stdout.String())
	}
}

func TestRun_MissingOut(t *testing.T) {
	withProjectRoot(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"some.json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit=%d (want 2)", code)
	}
}

func TestRun_DraftMissing(t *testing.T) {
	root := withProjectRoot(t)
	schemasDir := schematest.RepoSchemasDir(t)
	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"--out", filepath.Join(root, ".harness", "sensors"), "--schemas-dir", schemasDir, "/nonexistent/x.json"},
		&stdout, &stderr,
	)
	if code != 2 {
		t.Fatalf("exit=%d (want 2)", code)
	}
	if !strings.Contains(stdout.String(), "read_draft") {
		t.Fatalf("expected read_draft kind: %s", stdout.String())
	}
}
```

- [ ] **Step 4.2: Run; expect compile error (`run undefined`)**

```bash
GOWORK=off go test -tags=write_sensor ./skills/create-sensor/scripts/...
```

- [ ] **Step 4.3: Write the script**

Create `skills/create-sensor/scripts/write-sensor.go`:

```go
//go:build write_sensor

// Command write-sensor persists a draft sensor JSON to
// <projectRoot>/.harness/sensors/<id>.json. Strict mode: refuses if any
// golden_cases[].fixture is missing on disk and refuses to overwrite an
// existing <id>.json. Schema validation and atomic write are delegated
// to lib/sensor.ValidateAndPersist.
//
// Usage:
//
//	write-sensor --out <dir> [--schemas-dir <dir>] <draft.json>
//
// Exit codes: 0 written, 1 schema-invalid, 2 usage / I/O / pre-check.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/iurykrieger/harness-framework/lib/registry"
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
		emitJSON(stdout, errorSignal("usage", err.Error()))
		return 2
	}
	if outDir == "" {
		emitJSON(stdout, errorSignal("usage", "--out is required"))
		return 2
	}
	if fs.NArg() != 1 {
		emitJSON(stdout, errorSignal("usage", "exactly one positional draft path required"))
		return 2
	}
	draftPath := fs.Arg(0)

	body, err := os.ReadFile(draftPath)
	if err != nil {
		emitJSON(stdout, errorSignal("read_draft", err.Error()))
		return 2
	}

	// Parse for pre-checks; we re-pass body unchanged to ValidateAndPersist.
	var draft map[string]interface{}
	if err := json.Unmarshal(body, &draft); err != nil {
		emitJSON(stdout, errorSignal("read_draft", "parse draft JSON: "+err.Error()))
		return 2
	}

	// Resolve project root for fixture existence checks.
	cwd, _ := os.Getwd()
	res, err := registry.Lookup(cwd)
	if err != nil {
		emitJSON(stdout, registry.DiscoveryErrorSignal(err, "write-sensor"))
		return 2
	}
	projectRoot := res.ProjectRoot

	if code := checkFixtures(stdout, draft, projectRoot); code != 0 {
		return code
	}

	id, _ := draft["id"].(string)
	if id == "" {
		emitJSON(stdout, errorSignal("usage", "sensor.id missing or empty"))
		return 2
	}
	target := filepath.Join(outDir, id+".json")
	if _, statErr := os.Stat(target); statErr == nil {
		emitJSON(stdout, sensorAlreadyExistsSignal(target))
		return 2
	}

	path, err := sensor.ValidateAndPersist(body, outDir, schemasDir)
	if err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			emitJSON(stdout, errorSignal("schema_invalid", err.Error()))
			return 1
		}
		emitJSON(stdout, errorSignal("persist_failed", err.Error()))
		return 2
	}
	emitJSON(stdout, passSignal(path, id, draft))
	fmt.Fprintln(stdout, path)
	return 0
}

func checkFixtures(stdout io.Writer, draft map[string]interface{}, projectRoot string) int {
	ver, ok := draft["verification"].(map[string]interface{})
	if !ok {
		return 0 // schema will catch this later
	}
	cases, ok := ver["golden_cases"].([]interface{})
	if !ok {
		return 0
	}
	for _, raw := range cases {
		gc, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		rel, _ := gc["fixture"].(string)
		if rel == "" {
			continue
		}
		full := filepath.Join(projectRoot, rel)
		if _, err := os.Stat(full); err != nil {
			emitJSON(stdout, errorSignal("missing_fixture", fmt.Sprintf("fixture %q not found at %s", rel, full)))
			return 2
		}
	}
	return 0
}

func passSignal(path, id string, draft map[string]interface{}) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "write-sensor",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "sensor persisted"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind": "sensor_persisted",
			"path": path,
			"id":   id,
			"kind_attr": draft["kind"],
			"type_attr": draft["type"],
		},
	}
}

func sensorAlreadyExistsSignal(target string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "write-sensor",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "sensor file already exists; refusing to overwrite", "path": target}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "sensor_already_exists", "path": target},
	}
}

func errorSignal(kind, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "write-sensor",
		"version":     "0.1.0",
		"run_id":      sensor.NewUUIDv4(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": kind},
	}
}

func emitJSON(w io.Writer, m map[string]interface{}) {
	body, _ := json.Marshal(m)
	fmt.Fprintln(w, string(body))
}
```

- [ ] **Step 4.4: Run; expect all six tests PASS**

```bash
GOWORK=off go test -tags=write_sensor ./skills/create-sensor/scripts/...
```

Expected:
- TestRun_HappyPath PASS
- TestRun_MissingFixture PASS
- TestRun_SensorAlreadyExists PASS
- TestRun_SchemaInvalid PASS
- TestRun_MissingOut PASS
- TestRun_DraftMissing PASS

- [ ] **Step 4.5: Run vet**

```bash
GOWORK=off go vet -tags=write_sensor ./skills/create-sensor/scripts/...
```

- [ ] **Step 4.6: Commit**

```bash
git add skills/create-sensor/scripts/write-sensor.go \
        skills/create-sensor/scripts/write-sensor_test.go
git commit -m "feat(create-sensor): write-sensor.go with fixture/collision pre-checks"
```

---

## Task 5: SKILL.md body — 7-phase orchestration

**Files:**
- Modify: `skills/create-sensor/SKILL.md` (replace placeholder body with full orchestration)

The body is procedural prose addressed to the LLM. It does NOT contain temporal references (rule 10): no PR numbers, no migration notes, no "after version X" annotations. Examples are in en-US.

- [ ] **Step 5.1: Replace the SKILL.md body**

Rewrite `skills/create-sensor/SKILL.md` to:

````markdown
---
name: create-sensor
description: Use when the user invokes /create-sensor or asks to create a single sensor that validates a specific acceptance criterion, functional requirement, or use case. Takes a free-text requirement as input, runs an interactive clarification dialogue, composes existing sensors as dependencies, synthesizes fixtures, and persists one new assertion sensor to <project>/.harness/sensors/<id>.json via the schema validator. Distinct from /detect-sensors, which sweeps the whole project; /create-sensor produces exactly one targeted sensor per invocation.
---

# create-sensor

Take a single requirement (acceptance criterion, functional requirement, use case) as a free-text prompt and produce one targeted assertion sensor that validates it deterministically. Compose existing sensors as dependencies when relevant. Persist the result to `<project>/.harness/sensors/<id>.json` after schema validation.

This skill produces **exactly one sensor per invocation** and only of kind `assertion`. For project-wide bootstrapping or for `observation` / `setup` sensors, refer the user to `/detect-sensors`.

## Invocation

```
/create-sensor "<requirement-as-text>"
```

If the user supplies no requirement-string argument, ask for one in plain prose before proceeding:

> What is the requirement / acceptance criterion / use case you want this sensor to validate? Paste it as free text.

Block until the user replies.

## Procedure

### Phase 1: Parse invocation

Read the user-supplied requirement string into a working draft. Do not start drafting JSON yet — Phase 2's catalog data feeds the draft.

### Phase 2: Catalog existing sensors + read stack

Invoke `catalog-sensors.go` to enumerate the existing sensor inventory:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=catalog_sensors \
  ./skills/create-sensor/scripts \
  --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas"
```

Each stdout line is either a sensor digest (`{id, kind, type, output, blocking, description, path}`) or a `verdict=warn` Signal envelope describing a malformed entry that was skipped. Surface the warns to the user inline if any appeared so they know to clean up later, then proceed with the valid digests.

If `<projectRoot>/.harness/stack.json` exists, read it as additional context. The stack's `components[]` and `log_shapes[]` are not consumed by assertion sensors directly, but they help the LLM reason about which logger / HTTP framework the project uses when the requirement implies log observation as part of the check. When the file is absent or schema-invalid, ignore it.

### Phase 3: Classify

Decide the three discriminators:

- **`kind`** — always `assertion`. If the user's requirement is shaped like an observation (*"watch the logs while X runs"*) or a setup (*"install dependencies before Y"*), explain the boundary and recommend `/detect-sensors` instead. Do not proceed.
- **`type`** — `computational` by default. Escalate to `inferential` only when no deterministic shell command can verify the requirement (examples: *"the API response should be semantically equivalent to the spec example"*, *"the log line should not contain personally identifiable information"*). Inferential adds a top-level `calibration` block (`confidence_threshold: 0.8`, `calibration_set: ""`, `calibration_size: 0`, `calibration_date: <today>`) plus a `blind_spots[]` entry noting the calibration set is empty pending real samples.
- **`output`** — `stream` when the underlying tool naturally emits one independently-actionable observation per line (multi-record verification, batch tests). `single` otherwise.

### Phase 4: Draft v0

Produce a first-pass JSON in memory containing:

- `id` — kebab-case prefixed `assert-`. Derive from the requirement (e.g. `"POST /users/:id returns 200"` → `assert-post-users-id-200`).
- `version: "0.1.0"`.
- `name`, `description` — one-sentence summaries. The description cites the requirement and notes it was authored via `/create-sensor`.
- `kind: "assertion"`, `type` from Phase 3, `output` from Phase 3.
- `regulation: "behaviour"` for behavioral assertions; `"architecture-fitness"` only when the requirement is structural (file presence, dependency boundary).
- `phase: "on-demand"` by default; `"pre-merge"` when the requirement is gating a PR.
- `determinism: "high"` for computational; `"medium"` for inferential.
- `cost.class` — `cheap` for HTTP probes / file checks; `medium` for multi-step setups; `expensive` for inferential.
- `cost.latency` — sensible p50/p95/timeout for the inferred check.
- `triggers: [{"on": "manual"}]` by default.
- `requires[kind=env]` — one entry per env var the command references (auth tokens, base URLs, target ids). Each entry must have a `name` and a `description`.
- **Deps from the catalog** (the composability heart of this skill):
  - For each sensor in the catalog the LLM judges relevant to the requirement:
    - If its `blocking` field is `true`, encode it as `requires[kind=sensor]` (the orchestrator brings it up live and holds it during this sensor's run).
    - Otherwise, encode it as `depends_on` (the orchestrator runs it to completion before this sensor and propagates failures).
  - This mapping is mechanical: `blocking → requires[kind=sensor]`, `not-blocking → depends_on`. Always apply it consistently.
- `execution.command` — the shell invocation. Prefer commands available in most environments (`curl`, `jq`, `test`, `grep`).
- `execution.exit_code_map` — `[{"exit_code": 0, "verdict": "pass", "severity": "info"}, {"exit_code": "*", "verdict": "fail", "severity": "high"}]` by default. Adjust when the requirement implies severity tiers.
- `execution.output_parsing.patterns` — only when `output: "stream"`. One pattern per actionable verdict; anchor each regex to the kind of line the command emits.
- `verification.golden_cases` — list one case per declared verdict (at minimum `pass`; usually also `fail`). Populate `fixture` paths now (e.g. `.harness/sensors/fixtures/<id>/pass.txt`); the fixture *files* themselves are written in Phase 6.

### Phase 5: Interactive clarification loop

Identify gaps the LLM cannot resolve from the requirement + catalog + stack alone. **Ask one question per turn.** Common gap categories:

| Gap | Sample question |
|---|---|
| Command target | What URL, file path, or process should the check target? If localhost, which port? |
| Auth | Does the check need auth? If yes, which env var holds the token, and what header format does the service expect? |
| Inputs | What inputs does the command need (request body, query params, CLI flags)? Are these stable values, or should we template them via env vars? |
| Expected output | What does success look like in the command's output? What does failure look like? |
| Deps | The catalog lists [X, Y]. Does this sensor need [X] to bring the system up first? |
| Failure modes | Beyond the obvious exit code, are there other failure signals worth catching (timeouts, specific error messages)? |

After each user reply, update the in-memory draft and re-evaluate remaining gaps. Stop when no more gaps remain, or when the user signals *"that's enough, generate it"*.

### Phase 6: Fixture synthesis

Each `golden_case` needs a real fixture file on disk before `write-sensor.go` will persist the JSON.

For each verdict the sensor declares:

1. If the user's requirement or clarification answers contained an explicit sample output for that verdict, use it verbatim as the fixture content.
2. Otherwise, synthesize a plausible fixture from the requirement and the command. Examples:
   - `curl -w '%{http_code}'` → `pass.txt: "200\n"`, `fail-404.txt: "404\n"`.
   - `jq '.status' response.json` → `pass.txt: "\"ok\"\n"`, `fail.txt: "\"degraded\"\n"`.
   - `test -f /var/log/app.log` → `pass.txt: ""`, `fail.txt: "test: /var/log/app.log: No such file or directory\n"`.

Write each fixture via `write-fixture.go`, piping the payload through stdin (POSIX-safe):

```bash
printf '%s' "<payload>" | \
  HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_fixture \
  ./skills/create-sensor/scripts \
  ".harness/sensors/fixtures/<id>/<case>.txt"
```

The output is a Signal envelope on stdout. On `verdict=error`, surface the rationale to the user and stop — do not attempt the final persist with missing fixtures.

### Phase 7: Persist + report

Serialize the draft to a temp file (use Bash `mktemp` or write to `/tmp/create-sensor-draft-<id>.json`), then invoke `write-sensor.go`:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=write_sensor \
  ./skills/create-sensor/scripts \
  --out ".harness/sensors" \
  --schemas-dir "${CLAUDE_PLUGIN_ROOT}/schemas" \
  /tmp/create-sensor-draft-<id>.json
```

Outcomes:

- **`verdict=pass`** — sensor persisted. Emit a final summary to the user:

  > Created sensor `<id>` at `.harness/sensors/<id>.json`.
  > Dependencies wired: `<dep-id-1>` (via `requires[kind=sensor]`, blocking), `<dep-id-2>` (via `depends_on`).
  > Fixtures: `pass.txt`, `fail-404.txt`.
  > Next: run `/run-sensor <id>` to exercise the sensor.

- **`verdict=error, metadata.kind=schema_invalid`** — the draft failed schema validation. Read the rationale, identify the violated field, patch the draft, re-serialize, and retry the persist. Offer the user *"shall I attempt to fix and retry, or hand the draft over for manual editing?"* before iterating.

- **`verdict=error, metadata.kind=sensor_already_exists`** — the user already has a sensor with this id. Ask the user whether to pick a new id (and re-run Phase 7) or to abort and resolve the collision manually. Do not delete the existing file.

- **`verdict=error, metadata.kind=missing_fixture`** — one of the fixture files Phase 6 was supposed to write is absent. Re-run Phase 6 for the missing file before retrying.

## What this skill does NOT do

- It does not exercise the sensor end-to-end after creation. Run `/run-sensor <id>` yourself to confirm the assertion holds against the current state.
- It does not split multi-AC prompts into multiple sensors. One requirement, one sensor. For multiple ACs, invoke `/create-sensor` once per AC.
- It does not produce `kind: observation` or `kind: setup` sensors. For those, use `/detect-sensors`.
- It does not modify existing sensors. Sensor id collisions are rejected and surfaced to the user.
````

- [ ] **Step 5.2: Verify the skill loads by listing it in the user's available skills**

There is no programmatic verification for SKILL.md loadability beyond the frontmatter being well-formed YAML and including the `name:` and `description:` fields. Verify by inspection:

```bash
head -3 skills/create-sensor/SKILL.md
```

Expected: `---`, `name: create-sensor`, `description: ...`.

- [ ] **Step 5.3: Commit**

```bash
git add skills/create-sensor/SKILL.md
git commit -m "feat(create-sensor): SKILL.md body with 7-phase orchestration"
```

---

## Task 6: End-to-end manual acceptance

**Files:**
- None modified; this task verifies the working system.

This task confirms the skill works against a fixture project. The acceptance criteria mirror the spec's "SKILL.md acceptance" section.

- [ ] **Step 6.1: Build a minimal fixture project to exercise the skill**

```bash
TMPDIR_PROJECT=$(mktemp -d)
cd "$TMPDIR_PROJECT"
mkdir -p .harness/sensors
# Copy a blocking-style sensor (canonical-computational with execution.blocking=true) into the fixture project.
# This is the "existing sensor" that /create-sensor's catalog will pick up.
cat > .harness/sensors/run-fake-server.json <<'EOF'
{
  "id": "run-fake-server",
  "version": "0.1.0",
  "name": "Run fake server",
  "description": "Boots a fake HTTP server on port 8080 for testing /create-sensor end-to-end.",
  "kind": "observation",
  "type": "computational",
  "regulation": "behaviour",
  "phase": "on-demand",
  "determinism": "high",
  "output": "stream",
  "cost": {"class": "cheap", "latency": {"p50_ms": 1000, "p95_ms": 3000}, "compute": {"cpu": "low", "memory_mb": 64}},
  "triggers": [{"on": "manual"}],
  "execution": {
    "command": "python3 -m http.server 8080",
    "blocking": true,
    "graceful_timeout_ms": 5000,
    "exit_code_map": [
      {"exit_code": 0, "verdict": "pass", "severity": "info"},
      {"exit_code": "*", "verdict": "fail", "severity": "high"}
    ],
    "output_parsing": {"patterns": [{"regex": "Serving HTTP on", "verdict": "pass", "severity": "info"}]}
  },
  "verification": {"golden_cases": [{"fixture": ".harness/sensors/fixtures/run-fake-server/clean-boot.txt", "expected_verdict": "pass", "expected_severity": "info"}]}
}
EOF
mkdir -p .harness/sensors/fixtures/run-fake-server
echo "Serving HTTP on 0.0.0.0 port 8080 (http://0.0.0.0:8080/) ..." > .harness/sensors/fixtures/run-fake-server/clean-boot.txt
echo "Project: $TMPDIR_PROJECT"
```

- [ ] **Step 6.2: Verify the catalog script picks up the seed sensor**

From the fixture project root:

```bash
cd "$TMPDIR_PROJECT"
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=catalog_sensors \
  /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/create-sensor/skills/create-sensor/scripts \
  --schemas-dir "/Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/create-sensor/schemas"
```

(Adjust the absolute path if running from a different worktree. In a real Claude Code session, `${CLAUDE_PLUGIN_ROOT}` is set automatically.)

Expected stdout: one JSON line containing `"id":"run-fake-server"` and `"blocking":true`.

- [ ] **Step 6.3: Manually invoke /create-sensor in a Claude Code session**

From the fixture project root, in a Claude Code session that has the plugin loaded:

```
/create-sensor "GET http://localhost:8080/ returns HTTP 200"
```

Walk through Phase 5 by answering the clarification questions (the LLM will probably ask whether auth is needed and whether `run-fake-server` should be a dependency).

- [ ] **Step 6.4: Verify the persisted artifacts**

After the skill completes:

```bash
cd "$TMPDIR_PROJECT"
ls .harness/sensors/                           # expect: run-fake-server.json + new assert-*.json
ls .harness/sensors/fixtures/                  # expect: run-fake-server/ + new assert-*/ dir(s)
cat .harness/sensors/assert-*.json | jq '.requires'
```

Expected: the new sensor's `requires[]` contains an entry with `{"kind": "sensor", "id": "run-fake-server"}` (because the seed is blocking).

- [ ] **Step 6.5: Verify schema validity by re-running the catalog with `--schemas-dir`**

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=catalog_sensors \
  /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/create-sensor/skills/create-sensor/scripts \
  --schemas-dir "/Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/create-sensor/schemas"
```

Expected: both sensors appear as valid digest lines (no `verdict=warn`).

- [ ] **Step 6.6: Verify the id-collision behavior**

Re-invoke the skill with the same requirement:

```
/create-sensor "GET http://localhost:8080/ returns HTTP 200"
```

The skill should reach Phase 7 and surface a `verdict=error, metadata.kind=sensor_already_exists` from `write-sensor.go`. Verify it does NOT silently overwrite the prior file.

- [ ] **Step 6.7: Clean up the fixture project**

```bash
rm -rf "$TMPDIR_PROJECT"
```

- [ ] **Step 6.8: Run the full test suite one more time to catch any cross-script regressions**

From the worktree root:

```bash
GOWORK=off go test -tags=catalog_sensors ./skills/create-sensor/scripts/...
GOWORK=off go test -tags=write_fixture   ./skills/create-sensor/scripts/...
GOWORK=off go test -tags=write_sensor    ./skills/create-sensor/scripts/...
GOWORK=off go vet  -tags=catalog_sensors ./skills/create-sensor/scripts/...
GOWORK=off go vet  -tags=write_fixture   ./skills/create-sensor/scripts/...
GOWORK=off go vet  -tags=write_sensor    ./skills/create-sensor/scripts/...
```

Expected: all PASS, vet clean.

- [ ] **Step 6.9: Commit a brief note in the repo's CHANGELOG if one exists**

Check whether `CHANGELOG.md` is part of the project release workflow:

```bash
head -5 CHANGELOG.md
```

If yes, prepend a line under the next unreleased section noting the addition of `/create-sensor`. If `CHANGELOG.md` does not exist or is not in active use, skip this step.

```bash
git status
```

If the CHANGELOG was updated:

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): record /create-sensor skill addition"
```

- [ ] **Step 6.10: Open a draft PR for review**

```bash
git push -u origin feat/create-sensor-skill
gh pr create --title "feat: add /create-sensor skill" --body "$(cat <<'EOF'
## Summary

- New skill `/create-sensor` for converting a single requirement into a targeted assertion sensor.
- Three skill-local Go scripts: `catalog-sensors.go`, `write-fixture.go`, `write-sensor.go`.
- SKILL.md orchestrates a 7-phase clarification dialogue; deps composed from the existing sensor catalog.

## Test plan

- [x] `go test -tags=catalog_sensors ./skills/create-sensor/scripts/...`
- [x] `go test -tags=write_fixture ./skills/create-sensor/scripts/...`
- [x] `go test -tags=write_sensor ./skills/create-sensor/scripts/...`
- [x] `go vet` clean under all three build tags
- [x] End-to-end manual acceptance against a fixture project

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review

This is a checklist for the engineer executing the plan to run against the spec after completing all tasks. Read each item; if any is unresolved, return to the relevant task.

**Spec coverage:**

- [x] §What changes #1 (new skill directory) → Task 1
- [x] §What changes #2 (catalog-sensors.go) → Task 2
- [x] §What changes #3 (write-fixture.go) → Task 3
- [x] §What changes #4 (write-sensor.go) → Task 4
- [x] §What changes #5 (SKILL.md 7-phase body) → Task 5
- [x] §What changes #6 (no changes to lib/, schemas, other skills) → enforced by absence; verify `git diff --stat` shows only files under `skills/create-sensor/` and `docs/superpowers/`
- [x] §Acceptance criteria #1-#8 → tasks 1, 2, 3, 4, 5, 6
- [x] §Anti-scope items → enforced by *omission* of corresponding features; verify no `--force`, no auto-heal loop, no end-to-end run invocation in any task

**Placeholder scan:** no TODOs, no "implement later", no "similar to Task N" without repeated content, no references to undefined functions or methods.

**Type consistency:**

- `run(args []string, stdout, stderr io.Writer) int` — same signature in all three scripts.
- `runWithStdin(args []string, stdin io.Reader, stdout, stderr io.Writer) int` — only in `write-fixture.go`.
- `emitJSON(w io.Writer, m map[string]interface{})` — duplicated across all three scripts (each is `package main` and cannot share helpers, per the build-tag-per-file pattern). Names match.
- `errorSignal(kind, rationale string) map[string]interface{}` — same shape in all three scripts (small payload variations: `write-sensor`'s pass signal carries extra metadata).
- `sensor.NewUUIDv4()` — public function in `lib/sensor/envelope.go`, used for run_id in all three scripts.
- `registry.Lookup(cwd)` — returns `registry.Result` with `.ProjectRoot string` field; used in `catalog-sensors.go` and `write-sensor.go` and `write-fixture.go`.
- `sensor.ValidateAndPersist(body []byte, outDir, schemasDir string) (string, error)` — used only by `write-sensor.go`.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-13-create-sensor.md`. Two execution options:

**1. Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
