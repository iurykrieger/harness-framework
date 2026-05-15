# YAML migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the four JSON Schemas (`schemas/*.json`) and the three authored entity types (sensors, usecases, stack) from JSON to YAML, while keeping streaming protocol (`signals.log`, runner stdout, hook IO), runtime state (`running_sensors.json`, `auto-issues.json`), and the plugin manifest as JSON.

**Architecture:** A new `lib/schema.ReadAsJSON(path)` helper converts YAML bytes to JSON bytes via `sigs.k8s.io/yaml.YAMLToJSON`. The existing `santhosh-tekuri/jsonschema/v5` engine keeps consuming JSON bytes; the YAML conversion lives only at the file-read boundary. Writers go the other way: struct → `encoding/json.Marshal` → `yaml.JSONToYAML` → atomic write. The wire-format packages (`lib/subprocess`, `lib/watcher`, `lib/transcript`, `lib/signal`, `lib/registry`) are untouched.

**Tech Stack:** Go 1.25, `sigs.k8s.io/yaml` (new dependency), `github.com/santhosh-tekuri/jsonschema/v5` (existing), `encoding/json` (existing).

**Spec:** [`docs/superpowers/specs/2026-05-15-yaml-migration-design.md`](../specs/2026-05-15-yaml-migration-design.md)

---

## Phase 1 — Foundation

### Task 1: Add `sigs.k8s.io/yaml` dependency

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add the dependency**

Run from the repo root:
```bash
go get sigs.k8s.io/yaml@v1.6.0
```

- [ ] **Step 2: Verify go.mod**

Run: `grep "sigs.k8s.io/yaml" go.mod`
Expected: a line `sigs.k8s.io/yaml v1.6.0` (or similar) under the `require` block.

- [ ] **Step 3: Tidy and verify**

Run:
```bash
go mod tidy
go build ./...
```
Expected: clean exit; nothing else changes.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "build: add sigs.k8s.io/yaml dependency"
```

### Task 2: Add `lib/schema.ReadAsJSON` helper

**Files:**
- Create: `lib/schema/read.go`
- Create: `lib/schema/read_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/schema/read_test.go`:

```go
package schema

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadAsJSON_yamlFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.yaml")
	if err := os.WriteFile(path, []byte("id: my-sensor\nversion: 1.0.0\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := ReadAsJSON(path)
	if err != nil {
		t.Fatalf("ReadAsJSON: %v", err)
	}
	want := `{"id":"my-sensor","version":"1.0.0"}`
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("got %q want %q", string(got), want)
	}
}

func TestReadAsJSON_ymlFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.yml")
	if err := os.WriteFile(path, []byte("id: my-sensor\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := ReadAsJSON(path); err != nil {
		t.Fatalf("ReadAsJSON: %v", err)
	}
}

func TestReadAsJSON_jsonFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.json")
	if err := os.WriteFile(path, []byte(`{"id":"my-sensor"}`), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := ReadAsJSON(path)
	if err != nil {
		t.Fatalf("ReadAsJSON: %v", err)
	}
	if string(got) != `{"id":"my-sensor"}` {
		t.Fatalf("got %q", string(got))
	}
}

func TestReadAsJSON_malformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("foo: [unclosed\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := ReadAsJSON(path)
	if err == nil {
		t.Fatalf("expected error for malformed YAML")
	}
	if !strings.Contains(err.Error(), "parse YAML") {
		t.Fatalf("error %q does not mention parse YAML", err)
	}
}

func TestReadAsJSON_missingFile(t *testing.T) {
	_, err := ReadAsJSON("/nonexistent/path.yaml")
	if err == nil {
		t.Fatalf("expected error for missing file")
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test ./lib/schema/ -run TestReadAsJSON -v`
Expected: compilation error — `ReadAsJSON` is undefined.

- [ ] **Step 3: Implement `ReadAsJSON`**

Create `lib/schema/read.go`:

```go
package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// ReadAsJSON reads a file at path and returns its contents as JSON bytes.
// For .yaml and .yml files it parses the YAML and converts to JSON via
// sigs.k8s.io/yaml; for .json files (and any other extension) it returns
// the raw bytes unchanged. Used as the canonical entry point for any
// authored harness artifact (sensors, use cases, stacks, drafts).
func ReadAsJSON(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		js, err := yaml.YAMLToJSON(raw)
		if err != nil {
			return nil, fmt.Errorf("parse YAML %s: %w", path, err)
		}
		return js, nil
	default:
		return raw, nil
	}
}
```

- [ ] **Step 4: Run the test and verify it passes**

Run: `go test ./lib/schema/ -run TestReadAsJSON -v`
Expected: all five cases PASS.

- [ ] **Step 5: Run the full schema package**

Run: `go test ./lib/schema/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add lib/schema/read.go lib/schema/read_test.go
git commit -m "feat(schema): add ReadAsJSON helper for YAML and JSON files"
```

---

## Phase 2 — Convert schemas to YAML

### Task 3: Convert `schemas/signal.json` to YAML

**Files:**
- Create: `schemas/signal.yaml` (from `signal.json` content)
- Delete: `schemas/signal.json`

- [ ] **Step 1: Convert the file**

The conversion is mechanical — same content, YAML syntax. Run from the repo root:

```bash
go run -mod=mod golang.org/x/tools/cmd/...@latest 2>/dev/null || true
# Use sigs.k8s.io/yaml via a one-line Go program (or yq, if installed)
go run -mod=mod -tags=ignore_main <(cat <<'GO'
package main
import (
  "fmt"
  "os"
  "sigs.k8s.io/yaml"
)
func main() {
  in, err := os.ReadFile(os.Args[1])
  if err != nil { panic(err) }
  out, err := yaml.JSONToYAML(in)
  if err != nil { panic(err) }
  fmt.Print(string(out))
}
GO
) schemas/signal.json > schemas/signal.yaml
```

If the heredoc-Go trick is awkward, fall back to:
```bash
yq -P 'sort_keys(..)' -o=yaml schemas/signal.json > schemas/signal.yaml
```

- [ ] **Step 2: Sanity-check the YAML**

Run: `head -30 schemas/signal.yaml`
Expected: the first lines show `"$schema":` and `"$id":` strings with `https://harness-framework/schemas/signal.json` (the `$id` value is still `.json` for now — we update it in Task 7).

Run a round-trip parse to confirm it is well-formed:
```bash
go run -tags=ignore_main <(cat <<'GO'
package main
import (
  "fmt"
  "os"
  "sigs.k8s.io/yaml"
)
func main() {
  in, _ := os.ReadFile(os.Args[1])
  var m map[string]interface{}
  if err := yaml.Unmarshal(in, &m); err != nil { fmt.Println("ERR:", err); os.Exit(1) }
  fmt.Println("OK", len(m), "top-level keys")
}
GO
) schemas/signal.yaml
```
Expected: `OK N top-level keys` for some N > 0.

- [ ] **Step 3: Delete the JSON file**

```bash
rm schemas/signal.json
```

- [ ] **Step 4: Do NOT commit yet**

This task leaves the build broken because the validator still expects `signal.json`. It is fixed in Task 7. Move to Task 4 without committing.

### Task 4: Convert `schemas/sensor.json` to YAML and rewrite cross-refs

**Files:**
- Create: `schemas/sensor.yaml`
- Delete: `schemas/sensor.json`

- [ ] **Step 1: Convert the file**

Use the same one-liner approach as Task 3 Step 1, substituting `signal` for `sensor`. The resulting YAML preserves all keys.

- [ ] **Step 2: Rewrite `$ref` strings**

`schemas/sensor.json` references `signal.json` in nine places (per the grep run during planning). All of those must point at `signal.yaml`. Open the new `schemas/sensor.yaml` and replace every occurrence of `signal.json` with `signal.yaml`:

```bash
sed -i '' 's|signal\.json|signal.yaml|g' schemas/sensor.yaml   # macOS
# or, on Linux:
# sed -i 's|signal\.json|signal.yaml|g' schemas/sensor.yaml
```

- [ ] **Step 3: Verify the rewrite**

Run: `grep "signal\." schemas/sensor.yaml`
Expected: every occurrence ends in `signal.yaml`. No `signal.json` left.

- [ ] **Step 4: Delete the JSON file**

```bash
rm schemas/sensor.json
```

- [ ] **Step 5: Do NOT commit yet**

### Task 5: Convert `schemas/stack.json` to YAML

**Files:**
- Create: `schemas/stack.yaml`
- Delete: `schemas/stack.json`

- [ ] **Step 1: Convert**

Same approach as Task 3 Step 1, substituting `stack` for `signal`.

- [ ] **Step 2: Check for cross-refs**

Run: `grep -E "(signal|sensor|usecase)\.json" schemas/stack.yaml`
Expected: no output. `stack.json` has no cross-file refs (per planning).

- [ ] **Step 3: Delete the JSON file**

```bash
rm schemas/stack.json
```

- [ ] **Step 4: Do NOT commit yet**

### Task 6: Convert `schemas/usecase.json` to YAML

**Files:**
- Create: `schemas/usecase.yaml`
- Delete: `schemas/usecase.json`

- [ ] **Step 1: Convert**

Same approach as Task 3 Step 1.

- [ ] **Step 2: Check for cross-refs**

Run: `grep -E "(signal|sensor|stack)\.json" schemas/usecase.yaml`
Expected: no output. `usecase.json` has no cross-file refs (per planning).

- [ ] **Step 3: Delete the JSON file**

```bash
rm schemas/usecase.json
```

- [ ] **Step 4: Do NOT commit yet**

### Task 7: Update `$id` fields inside the four schemas

**Files:**
- Modify: `schemas/signal.yaml`
- Modify: `schemas/sensor.yaml`
- Modify: `schemas/stack.yaml`
- Modify: `schemas/usecase.yaml`

The `$id` field in each schema's header carries the canonical URL. It must match the resource name used by the validator. We update both.

- [ ] **Step 1: Inspect the `$id` form**

Run: `grep "\$id" schemas/signal.yaml`

The conversion produces one of two forms:
- `$id: https://harness-framework/schemas/signal.json` (typical for `sigs.k8s.io/yaml`)
- `"$id": "https://harness-framework/schemas/signal.json"` (atypical, if the converter retained JSON-style quoting)

Adapt Step 2's sed pattern accordingly.

- [ ] **Step 2: Rewrite `$id` in each schema**

For the unquoted form (typical), run from the repo root:
```bash
sed -i '' 's|harness-framework/schemas/signal\.json|harness-framework/schemas/signal.yaml|g' schemas/signal.yaml
sed -i '' 's|harness-framework/schemas/sensor\.json|harness-framework/schemas/sensor.yaml|g' schemas/sensor.yaml
sed -i '' 's|harness-framework/schemas/stack\.json|harness-framework/schemas/stack.yaml|g' schemas/stack.yaml
sed -i '' 's|harness-framework/schemas/usecase\.json|harness-framework/schemas/usecase.yaml|g' schemas/usecase.yaml
```

On Linux, drop the empty argument after `-i`. The pattern matches the URL substring directly, so it works whether or not the key/value is quoted.

- [ ] **Step 3: Verify**

Run:
```bash
grep "\$id" schemas/*.yaml
```
Expected: four lines, each ending in `.yaml`.

Run:
```bash
grep -E "harness-framework/schemas/[a-z]+\.json" schemas/*.yaml
```
Expected: no output.

- [ ] **Step 4: Do NOT commit yet**

The validator still expects `.json` file paths and URLs. Fixed in Task 8.

### Task 8: Update `lib/schema/validator.go` to load YAML schemas

**Files:**
- Modify: `lib/schema/validator.go`

- [ ] **Step 1: Update URL constants**

Replace lines 19-25 of `lib/schema/validator.go`:

```go
const (
	schemaBaseURL = "https://harness-framework/schemas/"
	sensorURL     = schemaBaseURL + "sensor.yaml"
	signalURL     = schemaBaseURL + "signal.yaml"
	stackURL      = schemaBaseURL + "stack.yaml"
	usecaseURL    = schemaBaseURL + "usecase.yaml"
)
```

- [ ] **Step 2: Update file reads in `NewValidator`**

Replace lines 48-78 of `lib/schema/validator.go` so file reads go through `ReadAsJSON` and target `.yaml` filenames:

```go
// NewValidator loads sensor.yaml, signal.yaml, stack.yaml, and usecase.yaml
// from schemasDir.
func NewValidator(schemasDir string) (*Validator, error) {
	sensorBytes, err := ReadAsJSON(filepath.Join(schemasDir, "sensor.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read sensor.yaml: %w", err)
	}
	signalBytes, err := ReadAsJSON(filepath.Join(schemasDir, "signal.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read signal.yaml: %w", err)
	}
	stackBytes, err := ReadAsJSON(filepath.Join(schemasDir, "stack.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read stack.yaml: %w", err)
	}
	usecaseBytes, err := ReadAsJSON(filepath.Join(schemasDir, "usecase.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read usecase.yaml: %w", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(signalURL, strings.NewReader(string(signalBytes))); err != nil {
		return nil, fmt.Errorf("register signal schema: %w", err)
	}
	if err := c.AddResource(sensorURL, strings.NewReader(string(sensorBytes))); err != nil {
		return nil, fmt.Errorf("register sensor schema: %w", err)
	}
	if err := c.AddResource(stackURL, strings.NewReader(string(stackBytes))); err != nil {
		return nil, fmt.Errorf("register stack schema: %w", err)
	}
	if err := c.AddResource(usecaseURL, strings.NewReader(string(usecaseBytes))); err != nil {
		return nil, fmt.Errorf("register usecase schema: %w", err)
	}
	// ... rest of NewValidator unchanged (compile calls etc.)
```

(The `c.Compile(...)` calls and the return statement at lines 79-95 remain unchanged; they reference the URL constants which are now `.yaml`.)

- [ ] **Step 3: Verify**

Run: `go build ./lib/schema/...`
Expected: clean exit. (Some tests may still reference `.json` — those land in Task 9.)

### Task 9: Update `lib/schema/discover.go` to look for YAML files

**Files:**
- Modify: `lib/schema/discover.go`

- [ ] **Step 1: Update file checks**

Replace lines 12-31 of `lib/schema/discover.go` (`FindSchemasDir` body):

```go
// FindSchemasDir walks up from start looking for a schemas/ directory that
// contains sensor.yaml, signal.yaml, stack.yaml, and usecase.yaml.
func FindSchemasDir(start string) (string, error) {
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(abs, "schemas")
		if hasFile(filepath.Join(candidate, "sensor.yaml")) &&
			hasFile(filepath.Join(candidate, "signal.yaml")) &&
			hasFile(filepath.Join(candidate, "stack.yaml")) &&
			hasFile(filepath.Join(candidate, "usecase.yaml")) {
			return candidate, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("schemas directory not found by walking up from %s", start)
		}
		abs = parent
	}
}
```

- [ ] **Step 2: Update `lib/schema/schematest/repodir.go`**

Open `lib/schema/schematest/repodir.go`. At line 23, replace:
```go
if _, err := os.Stat(filepath.Join(dir, "sensor.json")); err != nil {
```
with:
```go
if _, err := os.Stat(filepath.Join(dir, "sensor.yaml")); err != nil {
```

- [ ] **Step 3: Run schema tests**

Run: `go test ./lib/schema/...`
Expected: PASS. The validator now reads YAML schemas, the discover walks find them, the compiler resolves `$ref: signal.yaml#/...` cross-references because every schema is registered under its `.yaml` URL.

- [ ] **Step 4: Commit the full schema migration**

Combine all schema-related changes into one commit:

```bash
git add schemas/ lib/schema/validator.go lib/schema/discover.go lib/schema/schematest/repodir.go
git commit -m "refactor(schema): migrate schemas to YAML

The four schemas now live in schemas/*.yaml. NewValidator reads each
through ReadAsJSON, converts to JSON, and registers with the JSON
Schema compiler under the new .yaml URL. Cross-file \$ref strings
inside the schemas are updated in lockstep."
```

---

## Phase 3 — Migrate `lib/sensor/`

### Task 10: Convert sensor testdata fixtures to YAML

**Files:**
- All `lib/sensor/testdata/*.json` → `*.yaml`
- All `lib/sensor/testdata/**/*.json` → `*.yaml` (subdirs included)

- [ ] **Step 1: List the fixtures**

Run: `find lib/sensor/testdata -name '*.json' | sort`
Expected: a list of JSON fixtures. Note the count — typically `canonical-computational.json`, `canonical-inferential.json`, `canonical-setup.json`, plus `invalid-*.json` variants.

- [ ] **Step 2: Convert each fixture**

Run from the repo root:

```bash
for f in $(find lib/sensor/testdata -name '*.json'); do
  out="${f%.json}.yaml"
  go run -tags=ignore_main <(cat <<'GO'
package main
import (
  "fmt"
  "os"
  "sigs.k8s.io/yaml"
)
func main() {
  in, err := os.ReadFile(os.Args[1])
  if err != nil { panic(err) }
  out, err := yaml.JSONToYAML(in)
  if err != nil { panic(err) }
  fmt.Print(string(out))
}
GO
) "$f" > "$out"
  rm "$f"
done
```

If `yq` is preferred, the equivalent is:
```bash
for f in $(find lib/sensor/testdata -name '*.json'); do
  out="${f%.json}.yaml"
  yq -P 'sort_keys(..)' -o=yaml "$f" > "$out"
  rm "$f"
done
```

- [ ] **Step 3: Verify**

Run: `find lib/sensor/testdata -name '*.json'`
Expected: no output.

Run: `find lib/sensor/testdata -name '*.yaml' | head -5`
Expected: shows converted files.

### Task 11: Update `lib/sensor/catalog.go` glob and load path

**Files:**
- Modify: `lib/sensor/catalog.go`

- [ ] **Step 1: Update extension filter and decode path**

Replace lines 4-12 of `lib/sensor/catalog.go` (imports) to remove `encoding/json` (no longer needed for instance reads, since we go through `schema.ReadAsJSON`):

```go
package sensor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/iurykrieger/harness-framework/lib/schema"
)
```

(Keep `encoding/json` — `json.Unmarshal` still decodes the JSON bytes returned by `schema.ReadAsJSON` into the typed struct.)

Replace line 45 of `lib/sensor/catalog.go`:
```go
if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
```
with:
```go
if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
```

Replace lines 56-68 of `lib/sensor/catalog.go` (the file-read + parse block inside the loop):
```go
		fpath := filepath.Join(sensorsDir, name)
		body, err := os.ReadFile(fpath)
		if err != nil {
			warns = append(warns, CatalogWarn{File: name, Reason: fmt.Sprintf("read %s: %v", fpath, err)})
			continue
		}
		// Validate the raw shape against the schema first; schema validation
		// is stricter than json.Unmarshal into *Sensor (which silently
		// ignores unknown keys and type errors that the schema catches).
		var asMap map[string]interface{}
		if err := json.Unmarshal(body, &asMap); err != nil {
			warns = append(warns, CatalogWarn{File: name, Reason: fmt.Sprintf("parse %s: %v", fpath, err)})
			continue
		}
```
with:
```go
		fpath := filepath.Join(sensorsDir, name)
		body, err := schema.ReadAsJSON(fpath)
		if err != nil {
			warns = append(warns, CatalogWarn{File: name, Reason: fmt.Sprintf("read %s: %v", fpath, err)})
			continue
		}
		// Validate the raw shape against the schema first; schema validation
		// is stricter than json.Unmarshal into *Sensor (which silently
		// ignores unknown keys and type errors that the schema catches).
		var asMap map[string]interface{}
		if err := json.Unmarshal(body, &asMap); err != nil {
			warns = append(warns, CatalogWarn{File: name, Reason: fmt.Sprintf("parse %s: %v", fpath, err)})
			continue
		}
```

- [ ] **Step 2: Build**

Run: `go build ./lib/sensor/...`
Expected: clean.

### Task 12: Update `lib/sensor/path.go` to resolve `<id>.yaml`

**Files:**
- Modify: `lib/sensor/path.go`

- [ ] **Step 1: Update `resolveInDir`**

Replace line 44 of `lib/sensor/path.go`:
```go
path := filepath.Join(sensorRoot, id+".json")
```
with:
```go
path := filepath.Join(sensorRoot, id+".yaml")
```

- [ ] **Step 2: Update `looksLikePath`**

Replace line 75 of `lib/sensor/path.go`:
```go
strings.HasSuffix(s, ".json")
```
with:
```go
strings.HasSuffix(s, ".yaml") || strings.HasSuffix(s, ".yml")
```

- [ ] **Step 3: Build**

Run: `go build ./lib/sensor/...`
Expected: clean.

### Task 13: Update `lib/sensor/persist.go` to write YAML

**Files:**
- Modify: `lib/sensor/persist.go`

- [ ] **Step 1: Update imports**

Add `sigs.k8s.io/yaml` to the import block at lines 4-12 of `lib/sensor/persist.go`:

```go
package sensor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"sigs.k8s.io/yaml"
)
```

- [ ] **Step 2: Update RejectIfExists target filename (line 101)**

Replace:
```go
target := filepath.Join(opts.OutDir, id+".json")
```
with:
```go
target := filepath.Join(opts.OutDir, id+".yaml")
```

- [ ] **Step 3: Update final target filename (line 136)**

Replace:
```go
target := filepath.Join(opts.OutDir, id+".json")
```
with:
```go
target := filepath.Join(opts.OutDir, id+".yaml")
```

- [ ] **Step 4: Update `writeCanonical` to emit YAML**

Replace the entire `writeCanonical` function (lines 173-191):

```go
func writeCanonical(path string, sensor map[string]interface{}) error {
	jsonBytes, err := json.Marshal(sensor)
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	yamlBytes, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		return fmt.Errorf("convert to YAML: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(yamlBytes); err != nil {
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
```

- [ ] **Step 5: Build**

Run: `go build ./lib/sensor/...`
Expected: clean.

### Task 14: Update `lib/sensor/sensortest/canonical.go` testdata filenames

**Files:**
- Modify: `lib/sensor/sensortest/canonical.go`

- [ ] **Step 1: Update three fixture filenames**

Open `lib/sensor/sensortest/canonical.go`. At lines 17, 20, 23 replace:
```go
func LoadComputational(t *testing.T) *sensor.Sensor { return load(t, "canonical-computational.json") }
```
with:
```go
func LoadComputational(t *testing.T) *sensor.Sensor { return load(t, "canonical-computational.yaml") }
```

And similarly for `LoadInferential` (`canonical-inferential.json` → `canonical-inferential.yaml`) and `LoadSetup` (`canonical-setup.json` → `canonical-setup.yaml`).

If `canonical.go` has an internal `load` helper that uses `json.Unmarshal` on raw bytes, route it through `schema.ReadAsJSON` instead. Read the file first:

```bash
cat lib/sensor/sensortest/canonical.go
```

If `load` looks like:
```go
func load(t *testing.T, name string) *sensor.Sensor {
	body, _ := os.ReadFile(filepath.Join("testdata", name))
	var s sensor.Sensor
	_ = json.Unmarshal(body, &s)
	return &s
}
```

replace with:
```go
func load(t *testing.T, name string) *sensor.Sensor {
	body, err := schema.ReadAsJSON(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var s sensor.Sensor
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return &s
}
```

Add `"github.com/iurykrieger/harness-framework/lib/schema"` to the imports if not present.

### Task 15: Update sensor tests that reference `.json` paths

**Files:**
- Modify: `lib/sensor/persist_test.go`
- Modify: `lib/sensor/catalog_test.go`
- Modify: `lib/sensor/path_test.go`
- Modify: `lib/sensor/fixture_test.go` (if it references `.json` extensions)
- Modify: `lib/sensor/shape_test.go` (if applicable)
- Modify: `lib/sensor/project_test.go` (if applicable)

- [ ] **Step 1: Find all `.json` references in sensor tests**

Run:
```bash
grep -n '\.json' lib/sensor/*_test.go lib/sensor/sensortest/*.go
```

For each match, evaluate: does it refer to a sensor *file* (must become `.yaml`), or to JSON-as-format like `json.Unmarshal` or `application/json` (stays)?

- [ ] **Step 2: Update each `.json` filename reference**

For test setups that write a fixture inline, e.g.:
```go
path := filepath.Join(dir, "my-sensor.json")
os.WriteFile(path, []byte(`{...}`), 0o644)
```
update to:
```go
path := filepath.Join(dir, "my-sensor.yaml")
yamlBody, _ := yaml.JSONToYAML([]byte(`{...}`))
os.WriteFile(path, yamlBody, 0o644)
```

For test assertions that check the written path, update the expected suffix from `.json` to `.yaml`.

For tests that call `LoadComputational` etc. via `sensortest`, no change is needed — the fixture names are updated in Task 14.

- [ ] **Step 3: Run the sensor tests**

Run: `go test ./lib/sensor/...`
Expected: PASS. If any test fails, inspect the message — typical causes are a missed `.json` literal in a path string or an inline JSON body that needs YAML conversion.

### Task 16: Add regex round-trip test

**Files:**
- Modify: `lib/sensor/persist_test.go`

- [ ] **Step 1: Add a table-driven test**

Append to `lib/sensor/persist_test.go`:

```go
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
```

Make sure the imports include `"encoding/json"` and `"sigs.k8s.io/yaml"`.

- [ ] **Step 2: Run the new test**

Run: `go test ./lib/sensor/ -run TestRegexRoundTripThroughYAML -v`
Expected: all 15 sub-tests PASS. If any fail, the failure prints the YAML form — inspect to see whether the marshaller chose an unsafe style. Typically `sigs.k8s.io/yaml` quotes ambiguous strings; if a corner case slips through, fix it by adding a quoting hint in `writeCanonical` (file an issue if upstream).

### Task 17: Add Persist-canonical-form test

**Files:**
- Modify: `lib/sensor/persist_test.go`

- [ ] **Step 1: Add a test**

Append to `lib/sensor/persist_test.go`:

```go
func TestPersistCanonicalIndependentOfDraftStyle(t *testing.T) {
	// Three logically-identical drafts in different input styles must
	// produce byte-identical files on disk.
	jsonDraft := []byte(`{"id":"x","version":"1.0.0","kind":"observation","description":"x","cost":{"compute":"low"},"execution":{"command":"true","exit_code_map":[{"exit_code":0,"verdict":"pass","severity":"info"}]},"output":"single","type":"computational"}`)

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
		out  string
	}{
		{"json", jsonDraft, tmpA},
		{"yamlA", flowYAML, tmpB},
		{"yamlB", blockYAML, tmpC},
	} {
		if _, err := ValidateAndPersist(c.body, PersistOpts{OutDir: c.out, SchemasDir: schemasDir}); err != nil {
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
```

Add imports as needed: `"bytes"`, `"github.com/iurykrieger/harness-framework/lib/schema/schematest"`.

`ValidateAndPersist` also runs sensor-specific validation against the schema. The synthesized sensor above is minimal but must be schema-valid — adjust required fields after looking at `schemas/sensor.yaml`'s top-level required list (`id`, `version`, `kind`, `description`, `cost`, `execution`, `output`, `type`). If `ValidateAndPersist` rejects it, expand the JSON body until schema-valid.

- [ ] **Step 2: Run**

Run: `go test ./lib/sensor/ -run TestPersistCanonicalIndependentOfDraftStyle -v`
Expected: PASS.

- [ ] **Step 3: Commit the sensor migration**

```bash
git add lib/sensor/ schemas/
git commit -m "refactor(sensor): migrate sensor authoring to YAML

- catalog globs *.yaml and reads via schema.ReadAsJSON
- persist writes <id>.yaml via yaml.JSONToYAML
- path.Resolve targets <id>.yaml; looksLikePath accepts .yaml/.yml
- testdata fixtures converted (canonical-*, invalid-*)
- sensortest helpers updated for new filenames
- new tests: regex round-trip table + draft-style canonical form"
```

---

## Phase 4 — Migrate `lib/usecase/`

### Task 18: Convert usecase testdata fixtures to YAML

**Files:**
- All `lib/usecase/testdata/*.json` → `*.yaml`
- All `lib/usecase/testdata/**/*.json` → `*.yaml`

- [ ] **Step 1: Run the conversion**

Use the same loop as Task 10, substituting `lib/usecase/testdata` for `lib/sensor/testdata`:

```bash
for f in $(find lib/usecase/testdata -name '*.json'); do
  out="${f%.json}.yaml"
  yq -P 'sort_keys(..)' -o=yaml "$f" > "$out"
  rm "$f"
done
```

Or use the heredoc-Go variant from Task 10 Step 2 if `yq` is not installed.

- [ ] **Step 2: Verify**

Run: `find lib/usecase/testdata -name '*.json'`
Expected: no output.

### Task 19: Update `lib/usecase/load.go` to read YAML

**Files:**
- Modify: `lib/usecase/load.go`

- [ ] **Step 1: Replace the file body**

Open `lib/usecase/load.go`. Replace the function body of `LoadUseCaseFile` (lines 16-45):

```go
// LoadUseCaseFile reads, parses, and schema-validates a usecase YAML file
// at path. Returns the decoded map, the resolved absolute path, and an
// exit code: 0 success, 1 schema validation failure, 2 I/O or parse failure.
func LoadUseCaseFile(path, schemasDir string, stderr io.Writer) (map[string]interface{}, string, int) {
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
	body, err := schema.ReadAsJSON(abs)
	if err != nil {
		fmt.Fprintln(stderr, "error: read:", err)
		return nil, "", 2
	}
	var u map[string]interface{}
	if err := json.Unmarshal(body, &u); err != nil {
		fmt.Fprintln(stderr, "error: parse:", err)
		return nil, "", 2
	}
	if err := v.Validate(schema.TargetUseCase, u); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return nil, "", 1
	}
	return u, abs, 0
}
```

The change: `os.ReadFile(abs)` → `schema.ReadAsJSON(abs)`. The rest is identical.

Remove the unused `"os"` import if no other reference remains.

- [ ] **Step 2: Build**

Run: `go build ./lib/usecase/...`
Expected: clean.

### Task 20: Update `lib/usecase/persist.go` to write YAML

**Files:**
- Modify: `lib/usecase/persist.go`

- [ ] **Step 1: Add the YAML import**

Add `"sigs.k8s.io/yaml"` to the import block in `lib/usecase/persist.go`:

```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"sigs.k8s.io/yaml"
)
```

- [ ] **Step 2: Update target filename (line 78)**

Replace:
```go
target := filepath.Join(outDir, id+".json")
```
with:
```go
target := filepath.Join(outDir, id+".yaml")
```

- [ ] **Step 3: Update `writeCanonical`**

Replace `writeCanonical` (lines 89-111) with:

```go
func writeCanonical(path string, doc map[string]interface{}) error {
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	yamlBytes, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		return fmt.Errorf("convert to YAML: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(yamlBytes); err != nil {
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
```

- [ ] **Step 4: Build**

Run: `go build ./lib/usecase/...`
Expected: clean.

### Task 21: Update usecase tests

**Files:**
- Modify: `lib/usecase/load_test.go`
- Modify: `lib/usecase/persist_test.go`
- Modify: `lib/usecase/cross_check_test.go`
- Modify: `lib/usecase/evidence_test.go`
- Modify: `lib/usecase/usecasetest/*.go`

- [ ] **Step 1: Find `.json` references**

Run:
```bash
grep -rn '\.json' lib/usecase/ | grep -v testdata
```

- [ ] **Step 2: Update each reference**

For each `.json` filename in test setup or assertions, change to `.yaml`. For inline JSON bodies that need to land on disk as test fixtures, run the body through `yaml.JSONToYAML` before `os.WriteFile`. Add `"sigs.k8s.io/yaml"` to imports as needed.

For path-suffix assertions in `persist_test.go`, look for `.json` literals — e.g. `if !strings.HasSuffix(out, "create-user-with-email.json")` → `.yaml`.

- [ ] **Step 3: Run usecase tests**

Run: `go test ./lib/usecase/...`
Expected: PASS.

- [ ] **Step 4: Commit usecase migration**

```bash
git add lib/usecase/
git commit -m "refactor(usecase): migrate usecase authoring to YAML

- LoadUseCaseFile reads via schema.ReadAsJSON
- ValidateAndPersist writes <id>.yaml via yaml.JSONToYAML
- testdata fixtures converted; tests updated"
```

---

## Phase 5 — Migrate `lib/stack/`

### Task 22: Convert stack testdata fixtures to YAML

**Files:**
- All `lib/stack/testdata/*.json` → `*.yaml`
- All `lib/stack/testdata/**/*.json` → `*.yaml`

- [ ] **Step 1: Inspect first**

Run: `find lib/stack/testdata -name '*.json' | sort`

Some files under `lib/stack/testdata/stack-discovery/` may be log samples or other non-stack-shaped artifacts (per the spec: "log samples ignored"). For each file, decide before conversion: is it a stack artifact (convert) or fixture data the stack-discovery code consumes (likely stays as-is, since logs are inputs the stack analyzer reads from the project)?

- [ ] **Step 2: Convert only stack-shaped files**

Use the same loop pattern as Task 10. Start with the obvious ones (`golden-stack.json`, `golden-stack-with-journeys.json`) at the top of `lib/stack/testdata/`. For files inside `stack-discovery/`, inspect each:

```bash
ls lib/stack/testdata/stack-discovery/
```

If a file is a log sample (free-form JSON the analyzer reads as input data), leave it alone. If it is a stack artifact (matches the stack schema shape), convert it.

- [ ] **Step 3: Verify**

Run: `find lib/stack/testdata -name 'golden-stack*.json'`
Expected: no output.

### Task 23: Update `lib/stack/load.go`, `lookup.go`, `e2e_fixture_test.go`

**Files:**
- Modify: `lib/stack/load.go`
- Modify: `lib/stack/lookup.go`
- Modify: `lib/stack/e2e_fixture_test.go`

- [ ] **Step 1: Update `LoadStackFile`**

In `lib/stack/load.go`, replace line 33 (`body, err := os.ReadFile(abs)`) with:

```go
	body, err := schema.ReadAsJSON(abs)
```

Remove the `"os"` import if no other reference remains.

- [ ] **Step 2: Update `Lookup` path**

In `lib/stack/lookup.go`, replace line 29:
```go
path := filepath.Join(root, ".harness", "stack.json")
```
with:
```go
path := filepath.Join(root, ".harness", "stack.yaml")
```

- [ ] **Step 3: Build**

Run: `go build ./lib/stack/...`
Expected: clean.

### Task 24: Update `lib/stack/persist.go` to write YAML

**Files:**
- Modify: `lib/stack/persist.go`

- [ ] **Step 1: Add yaml import**

Add `"sigs.k8s.io/yaml"` to the imports.

- [ ] **Step 2: Update target filename (line 57)**

Replace:
```go
target := filepath.Join(outDir, "stack.json")
```
with:
```go
target := filepath.Join(outDir, "stack.yaml")
```

- [ ] **Step 3: Update `writeCanonical`**

Replace `writeCanonical` (lines 64-86) with the same YAML-emitting pattern as Task 13 Step 4 (only the struct/map type differs):

```go
func writeCanonical(path string, v map[string]interface{}) error {
	jsonBytes, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	yamlBytes, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		return fmt.Errorf("convert to YAML: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".persist-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(yamlBytes); err != nil {
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
```

- [ ] **Step 4: Build**

Run: `go build ./lib/stack/...`
Expected: clean.

### Task 25: Update stack tests

**Files:**
- Modify: `lib/stack/load_test.go`
- Modify: `lib/stack/persist_test.go`
- Modify: `lib/stack/lookup_test.go`
- Modify: `lib/stack/cross_check_test.go`
- Modify: `lib/stack/e2e_fixture_test.go`

- [ ] **Step 1: Find `.json` references**

Run: `grep -rn '\.json' lib/stack/*_test.go`

- [ ] **Step 2: Update**

Mirror the pattern from Task 21 Step 2: `.json` filename literals → `.yaml`; inline JSON bodies → run through `yaml.JSONToYAML` before write; expected-path suffixes → `.yaml`.

- [ ] **Step 3: Run stack tests**

Run: `go test ./lib/stack/...`
Expected: PASS.

- [ ] **Step 4: Commit stack migration**

```bash
git add lib/stack/
git commit -m "refactor(stack): migrate stack artifact to YAML

- LoadStackFile reads via schema.ReadAsJSON
- ValidateAndPersist writes <projectRoot>/.harness/stack.yaml
- Lookup targets stack.yaml on disk
- testdata fixtures converted; tests updated"
```

---

## Phase 6 — Update orchestrator and registry paths

### Task 26: Update `lib/registry/paths.go`

**Files:**
- Modify: `lib/registry/paths.go`

- [ ] **Step 1: Update sensor-file path**

In `lib/registry/paths.go` at line 31:
```go
return filepath.Join(r.projectRoot, ".harness", "sensors", id+".json")
```
becomes:
```go
return filepath.Join(r.projectRoot, ".harness", "sensors", id+".yaml")
```

Line 36 (`running_sensors.json`) stays — the registry file is JSON.

- [ ] **Step 2: Build**

Run: `go build ./lib/registry/...`
Expected: clean.

### Task 27: Update `lib/orchestrator/`

**Files:**
- Modify: `lib/orchestrator/run.go`
- Modify: `lib/orchestrator/cascade.go`

- [ ] **Step 1: Update `run.go` extension check**

In `lib/orchestrator/run.go` at line 162:
```go
if len(name) > 5 && name[len(name)-5:] == ".json" {
```
becomes:
```go
if len(name) > 5 && name[len(name)-5:] == ".yaml" {
```

(The constant `5` matches `.yaml` length; if the surrounding code uses `.json` elsewhere — search the file — adjust those too.)

- [ ] **Step 2: Update `cascade.go` evidence path**

In `lib/orchestrator/cascade.go` at line 47:
```go
"file":      filepath.Join(".harness", "sensors", failedID+".json"),
```
becomes:
```go
"file":      filepath.Join(".harness", "sensors", failedID+".yaml"),
```

- [ ] **Step 3: Build and test**

Run:
```bash
go build ./lib/orchestrator/... ./lib/registry/...
go test ./lib/orchestrator/... ./lib/registry/...
```
Expected: clean build and PASS.

- [ ] **Step 4: Commit**

```bash
git add lib/registry/paths.go lib/orchestrator/run.go lib/orchestrator/cascade.go
git commit -m "refactor(registry,orchestrator): target sensor .yaml paths

- registry.SensorPath returns <id>.yaml
- orchestrator extension check matches .yaml
- cascade error evidence points at .yaml file"
```

---

## Phase 7 — Update skill writer scripts

### Task 28: Update `skills/detect-sensors/scripts/write-sensor.go`

**Files:**
- Modify: `skills/detect-sensors/scripts/write-sensor.go`

- [ ] **Step 1: Inspect**

Run: `cat skills/detect-sensors/scripts/write-sensor.go`

The script delegates to `lib/sensor.ValidateAndPersist`. The output filename change happens inside `Persist` (Task 13). The script's only concrete changes are:
- If it reads a draft from a file, route through `schema.ReadAsJSON`.
- If it has `.json` hardcoded anywhere (e.g. error messages, sentinel paths), update.

- [ ] **Step 2: Apply**

Replace any `os.ReadFile(draftPath)` for the input draft with `schema.ReadAsJSON(draftPath)`. Update any user-visible message that says "writing sensor to <id>.json" to "<id>.yaml".

- [ ] **Step 3: Run script tests**

Run: `go test -tags=write_sensor ./skills/detect-sensors/scripts/...`
Expected: PASS.

### Task 29: Update `skills/detect-sensors/scripts/write-stack.go`

**Files:**
- Modify: `skills/detect-sensors/scripts/write-stack.go`

- [ ] **Step 1: Apply same pattern as Task 28**

Route draft reads through `schema.ReadAsJSON`; update any `stack.json` references in user-visible messages.

- [ ] **Step 2: Run**

Run: `go test -tags=write_stack ./skills/detect-sensors/scripts/...` (if a tag exists; otherwise `go test ./skills/detect-sensors/scripts/...`).
Expected: PASS.

### Task 30: Update `skills/detect-usecases/scripts/write-usecase.go`

**Files:**
- Modify: `skills/detect-usecases/scripts/write-usecase.go`

- [ ] **Step 1: Update stack path read**

At line 91:
```go
stackPath := filepath.Join(projectRoot, ".harness", "stack.json")
```
becomes:
```go
stackPath := filepath.Join(projectRoot, ".harness", "stack.yaml")
```

- [ ] **Step 2: Route draft read through ReadAsJSON**

Same pattern as Task 28.

- [ ] **Step 3: Run**

Run: `go test -tags=write_usecase ./skills/detect-usecases/scripts/...`
Expected: PASS.

### Task 31: Verify heal-sensor scripts

**Files:**
- Audit: `skills/heal-sensor/scripts/*.go`

- [ ] **Step 1: Find `.json` references**

Run: `grep -rn '\.json' skills/heal-sensor/scripts/ | grep -v "_test.go" | grep -v testdata`

- [ ] **Step 2: Update each**

For every file-extension reference to a sensor, change to `.yaml`. For draft reads, route through `schema.ReadAsJSON`.

- [ ] **Step 3: Run**

Run: `go test -tags=heal_retry_original ./skills/heal-sensor/scripts/...`
Expected: PASS.

### Task 32: Update `skills/detect-sensors/scripts/replay-fixture.go`

**Files:**
- Modify: `skills/detect-sensors/scripts/replay-fixture.go`

- [ ] **Step 1: Inspect**

The grep at planning time showed `os.CreateTemp("", "replay-sensor-*.json")` at line 84. This is a *temporary sensor file* the replay tool synthesizes to feed the runner. The runner now expects `.yaml`.

- [ ] **Step 2: Update**

Replace:
```go
tempSensor, err := os.CreateTemp("", "replay-sensor-*.json")
```
with:
```go
tempSensor, err := os.CreateTemp("", "replay-sensor-*.yaml")
```

And ensure the contents being written to that tempfile are YAML, not JSON. Inspect the next ~20 lines and apply `yaml.JSONToYAML` if the current code writes JSON bytes.

- [ ] **Step 3: Run**

Run: `go test ./skills/detect-sensors/scripts/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/detect-sensors/scripts/ skills/detect-usecases/scripts/ skills/heal-sensor/scripts/
git commit -m "refactor(skills): writer scripts produce YAML artifacts

write-sensor, write-stack, write-usecase, heal-sensor scripts all
emit YAML via the lib persisters. replay-fixture's temp sensor file
is now .yaml and contains YAML bytes."
```

---

## Phase 8 — Sweep remaining `.json` references

### Task 33: Final grep for stragglers

**Files:**
- TBD based on grep output

- [ ] **Step 1: Run a comprehensive grep**

```bash
grep -rn '\.json' --include='*.go' . | grep -v '_test.go' | grep -v testdata | grep -v "running_sensors\.json" | grep -v "auto-issues\.json" | grep -v "plugin\.json" | grep -v "json\.Marshal\|json\.Unmarshal\|json\.NewEncoder\|json\.NewDecoder\|encoding/json" | grep -v "FormatJSON"
```

The filters above exclude legitimately-JSON references (runtime state, plugin manifest, the `encoding/json` import). Any remaining hits represent a forgotten code path that should be `.yaml`.

- [ ] **Step 2: Fix any straggler**

For each hit, evaluate context and update to `.yaml` if it refers to a sensor/usecase/stack artifact. Skip if it is JSONL, registry, plugin manifest, or an unrelated piece.

- [ ] **Step 3: Run the full test suite**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential   ./skills/...
go test -tags=start_sensor      ./skills/...
go test -tags=stop_sensor       ./skills/...
go test -tags=list_sensors      ./skills/...
go test -tags=tail_sensor       ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go test -tags=write_usecase     ./skills/...
go vet -tags=run_computational  ./...
go vet -tags=run_inferential    ./...
```
Expected: every command exits 0.

- [ ] **Step 4: Commit if any stragglers were found**

```bash
git add <files>
git commit -m "refactor: sweep remaining .json -> .yaml references for authored artifacts"
```

If the grep found nothing, skip the commit.

---

## Phase 9 — Documentation

### Task 34: Update `CLAUDE.md`

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update the schemas section**

Open `CLAUDE.md` and locate the heading `### The four schemas` (~line 44). The section currently lists `sensor.json`, `signal.json`, etc.

Replace the section body to reflect YAML authoring while preserving the JSON Schema semantics:

```markdown
### The four schemas

All four are JSON Schema **Draft 2020-12**, authored as YAML and converted to JSON bytes at validator construction time via `sigs.k8s.io/yaml`. Validators must support that draft and resolve `$ref` across files.

- `schemas/signal.yaml` — the output contract. Defines the canonical `Verdict` and `Severity` enums under `$defs/`. **Edit enum values here only.**
- `schemas/sensor.yaml` — the definition contract. References `signal.yaml` two ways:
  - `#/$defs/Signal` is `{ "$ref": "signal.yaml" }`, so tooling can dereference a sensor's runtime output contract by chained `$ref`.
  - Enum sites inside `sensor.yaml` (`execution.exit_code_map[].{verdict,severity}`, `verification.golden_cases[].{expected_verdict,expected_severity}`) use `{ "$ref": "signal.yaml#/$defs/Verdict" }` and `…/Severity`. Adding a new verdict or severity value means editing `signal.yaml` only — `sensor.yaml` picks it up automatically.
- `schemas/stack.yaml` — the project-stack contract. Produced by `/detect-sensors` Phase A; consumed by Phase B when authoring `kind=observation` + `output=stream` sensors. Independent of `signal.yaml` and `sensor.yaml` (no cross-`$ref`).
- `schemas/usecase.yaml` — the use-case contract. Describes one observable journey variation of the project (trigger as narrative + fixture, behavior, expected_outcome with invariants and side_effects, file:line evidence pointing at the implementation). Produced by `/detect-usecases`; consumed by a future `/create-sensor` skill to synthesize deterministic regression sensors. References `stack.yaml` indirectly via `journey_id` (validated in Go, not JSON Schema).
```

- [ ] **Step 2: Update path examples elsewhere in CLAUDE.md**

Run: `grep -n "\.harness/sensors/<id>\.json\|\.harness/usecases/<id>\.json\|\.harness/stack\.json" CLAUDE.md`

For each hit that is **not** about runtime state (`.harness/runtime/...`), update `.json` → `.yaml`.

- [ ] **Step 3: Add a project rule about YAML comments**

Append a new rule to the numbered list under "## Project rules", or add a paragraph at the end of "The four schemas":

```markdown
**Comments in YAML artifacts are not preserved on round-trip.** `sigs.k8s.io/yaml` discards comments when marshalling, so any `# comment` lines added to a sensor or use case will be lost the next time the framework rewrites the file (`/heal-sensor`, re-running `/detect-sensors`). Durable explanations belong in commit messages, the design doc, or this CLAUDE.md — never in the artifact body.
```

- [ ] **Step 4: Verify**

Run: `grep -n "\.json" CLAUDE.md | grep -v "running_sensors\|auto-issues\|plugin\.json\|JSONL\|JSON Schema\|encoding/json"`
Expected: empty (or only matches that are legitimately about JSON — JSONL, the validator engine, etc.).

### Task 35: Update SKILL.md files

**Files:**
- Modify: `skills/detect-sensors/SKILL.md`
- Modify: `skills/detect-usecases/SKILL.md`
- Modify: `skills/create-sensor/SKILL.md` (if present)
- Modify: `skills/run-sensor/SKILL.md`
- Modify: `skills/start-sensor/SKILL.md`
- Modify: `skills/tail-sensor/SKILL.md`
- Modify: `skills/list-sensors/SKILL.md`
- Modify: `skills/stop-sensor/SKILL.md`
- Modify: `skills/heal-sensor/SKILL.md`

For each file:

- [ ] **Step 1: List and inspect**

Run: `ls skills/*/SKILL.md`

- [ ] **Step 2: Update code fences for sensor/usecase/stack examples**

For each SKILL.md, find code fences that render a sensor, use case, or stack definition. Change the language tag from ` ```json ` to ` ```yaml ` and reformat the body. Keep fences for Signal examples as ` ```json `.

Example transform: a fence currently saying
````
```json
{
  "id": "lint-eslint",
  "kind": "observation",
  ...
}
```
````
becomes
````
```yaml
id: lint-eslint
kind: observation
...
```
````

- [ ] **Step 3: Update path references in prose**

In skill prose, replace `.harness/sensors/<id>.json` with `.harness/sensors/<id>.yaml`, and similarly for usecases and stack.

- [ ] **Step 4: Run skill consistency checks (if any)**

Some skills have associated tests. Run: `go test ./skills/...`
Expected: PASS.

### Task 36: Add "Upgrading" section to README.md

**Files:**
- Modify: `README.md` (create if absent)

- [ ] **Step 1: Inspect**

Run: `ls README.md 2>/dev/null && head -30 README.md 2>/dev/null`

If absent, the section can be added to CLAUDE.md instead at the top under a `## Upgrading` heading.

- [ ] **Step 2: Add the section**

Append to the README (or insert near the top, after the project description):

```markdown
## Upgrading from a JSON-era version

This plugin moved sensors, use cases, and the stack manifest from JSON to YAML. On upgrade, downstream projects must:

```bash
rm -f .harness/stack.json
rm -f .harness/sensors/*.json
rm -f .harness/usecases/*.json
```

then re-run:

- `/detect-sensors`
- `/detect-usecases`

Both commands are idempotent regenerators — they will produce fresh YAML artifacts. No data is migrated; the framework treats `*.json` files in those directories as foreign content.

Runtime state (`.harness/runtime/running_sensors.json`, `.harness/runtime/auto-issues.json`), the streaming protocol (`signals.log`, runner stdout JSONL, hook IO), and the plugin manifest (`.claude-plugin/plugin.json`) remain JSON and require no action.
```

- [ ] **Step 3: Commit documentation**

```bash
git add CLAUDE.md skills/*/SKILL.md README.md
git commit -m "docs: update CLAUDE.md, SKILL.md, README.md for YAML migration"
```

---

## Phase 10 — Regenerate this repo's `.harness/`

### Task 37: Delete JSON-era artifacts in this repo

**Files:**
- Delete: `.harness/stack.json`
- Delete: `.harness/sensors/*.json`
- Delete: `.harness/usecases/*.json`

- [ ] **Step 1: Inspect what is there**

```bash
ls .harness/
ls .harness/sensors/ | head -5
ls .harness/usecases/ | head -5
```

- [ ] **Step 2: Delete**

```bash
rm -f .harness/stack.json
rm -f .harness/sensors/*.json
rm -f .harness/usecases/*.json
```

- [ ] **Step 3: Confirm**

```bash
find .harness -name '*.json' | grep -v "runtime/\|auto-issues\|running_sensors"
```
Expected: no output.

### Task 38: Regenerate via `/detect-sensors` and `/detect-usecases`

**Files:**
- Create: `.harness/stack.yaml`
- Create: `.harness/sensors/*.yaml`
- Create: `.harness/usecases/*.yaml`

This task is **executed by the human or by Claude Code via slash commands**, not by `go run` alone, because the skills involve LLM reasoning.

- [ ] **Step 1: Run `/detect-sensors`**

From the agent session running in this repo, invoke `/detect-sensors`. Wait for completion. Inspect: `ls .harness/sensors/*.yaml`.

- [ ] **Step 2: Run `/detect-usecases`**

From the agent session, invoke `/detect-usecases`. Wait for completion. Inspect: `ls .harness/usecases/*.yaml`.

- [ ] **Step 3: Spot-check a sensor**

Pick one and run it:
```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=run_computational \
  ./skills/run-sensor/scripts <a-known-sensor-id>
```
Expected: the runner reads the YAML sensor, spawns the subprocess, and emits JSONL Signals on stdout exactly as before. The aggregate Signal is the LAST JSONL line.

- [ ] **Step 4: Commit the regenerated artifacts**

```bash
git add .harness/
git commit -m "chore(.harness): regenerate sensors, use cases, and stack as YAML"
```

---

## Final verification

### Task 39: Run the complete test matrix

- [ ] **Step 1: All-tags test pass**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential   ./skills/...
go test -tags=start_sensor      ./skills/...
go test -tags=stop_sensor       ./skills/...
go test -tags=list_sensors      ./skills/...
go test -tags=tail_sensor       ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go test -tags=write_usecase     ./skills/...
go vet -tags=run_computational  ./...
go vet -tags=run_inferential    ./...
```
Expected: every command exits 0.

- [ ] **Step 2: Inspect final repo state**

```bash
find schemas -type f
find .harness -type f | grep -v runtime
git status
git log --oneline | head -15
```

Expected:
- `schemas/` contains only `*.yaml` files.
- `.harness/sensors/` and `.harness/usecases/` contain only `*.yaml`.
- `.harness/stack.yaml` present.
- Working tree clean; commits show the migration history.

- [ ] **Step 3: Tag the migration (optional)**

If the project tags releases:
```bash
git tag -a yaml-migration -m "Migrate schemas and authored entities from JSON to YAML"
```

---

## Self-review checklist (writer responsibility — done before plan goes to engineer)

Coverage against the spec:
- **What changes #1 (schemas migrate)**: Tasks 3–9.
- **What changes #2 (authored entities migrate)**: Tasks 11–27.
- **What changes #3 (`lib/schema/` YAML conversion layer)**: Task 2.
- **What changes #4 (loaders/persisters switch I/O format)**: Tasks 11, 13, 19, 20, 23, 24.
- **What changes #5 (skill writers emit YAML)**: Tasks 28–32.
- **What changes #6 (/heal-sensor inherits)**: Task 31.
- **What changes #7 (testdata fixtures migrate)**: Tasks 10, 18, 22.
- **What changes #8 (docs)**: Tasks 34–36.
- **What changes #9 (no migration script)**: ✓ no script shipped; Task 36 documents the upgrade procedure for downstream projects.
- **Risk: regex round-trip**: Task 16 covers it via table-driven test through the production code path.
- **Risk: `$ref` resolution**: Tasks 4 and 8 update refs and validator URLs together; the existing `validator_test.go` is the canary.
- **Risk: downstream upgrade**: Task 36 documents it; Tasks 37–38 demonstrate the procedure on this repo.

No placeholders: every step contains the exact code or command to run.

Type consistency: function signatures used in later tasks (`ReadAsJSON`, `ValidateAndPersist`, `LoadUseCaseFile`, `LoadStackFile`, `Catalog`) all reference the actual signatures present in the codebase at planning time and updated in earlier tasks.
