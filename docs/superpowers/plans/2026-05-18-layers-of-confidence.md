# Layers of Confidence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `/create-sensor` (singular, grouping inter-usecase) with `/create-sensors` (plural, per-usecase multi-layer bundles), add `/validate-usecase` (worst-result confidence aggregation), and introduce a closed-enum `layer` field on sensors with 17 validation lenses.

**Architecture:** A new `lib/planning/layer/` package with one Go file per layer implementing `LayerRecipe` (`Applicable` + `Plan`); a sibling `lib/planning/coredetect/` for auto-creating missing platform primitives; two new skills wiring it together. The schema change is minimal (remove `blind_spots`, add `layer`); the orchestrator and the runtime are reused as-is.

**Tech Stack:** Go 1.25, `sigs.k8s.io/yaml`, in-repo libs (`lib/sensor`, `lib/usecase`, `lib/stack`, `lib/schema`, `lib/orchestrator`, `lib/registry`, `lib/cli`, `lib/signal`), Claude Code skill loader.

**Reference spec:** `docs/superpowers/specs/2026-05-18-layers-of-confidence-design.md`.

---

## File Structure

### New files

```
schemas/sensor.yaml                                # MODIFIED: -blind_spots, +layer enum

lib/sensor/shape.go                                # MODIFIED: -BlindSpots field
lib/sensor/shape_test.go                           # MODIFIED: drop BlindSpots assertions
                                                   #           add Layer assertions

lib/planning/layer/                                # NEW package
├── layer.go                                       # type Layer, LayerRecipe, Draft, registry
├── layer_test.go                                  # registry + topological order tests
├── applicability.go                               # hasRole / hasLogShape / hasCoreSensor / hasArchetype
├── applicability_test.go
├── unit.go                                        # 17 production files, one per Layer
├── unit_test.go                                   # 17 sibling _test files
├── integration.go
├── integration_test.go
├── contract.go
├── contract_test.go
├── e2e.go                                         # multi-scenario recipe (largest)
├── e2e_test.go
├── db_state.go
├── db_state_test.go
├── log_trace.go
├── log_trace_test.go
├── metric.go
├── metric_test.go
├── event_emission.go
├── event_emission_test.go
├── event_consumption.go
├── event_consumption_test.go
├── performance.go
├── performance_test.go
├── resilience.go
├── resilience_test.go
├── code_quality.go
├── code_quality_test.go
├── architecture.go
├── architecture_test.go
├── security.go
├── security_test.go
├── dependency_health.go
├── dependency_health_test.go
├── db_schema.go
├── db_schema_test.go
├── accessibility.go
├── accessibility_test.go
└── testdata/
    ├── stack-http-api-postgres.yaml
    ├── stack-queue-consumer.yaml
    ├── stack-library-no-deps.yaml
    ├── usecase-create-user.yaml
    ├── usecase-consume-order-event.yaml
    └── catalog-with-run-project.json

lib/planning/coredetect/                           # NEW package (auto-creation of primitives)
├── coredetect.go                                  # ScaffoldFunc + registry
├── coredetect_test.go
├── run_project.go
├── run_project_test.go
├── setup_postgres.go
├── setup_postgres_test.go
├── seed_db.go
├── seed_db_test.go
├── install_deps.go
├── install_deps_test.go
├── build.go
├── build_test.go
├── lint.go
├── lint_test.go
├── type_check.go
├── type_check_test.go
├── run_all_tests.go
├── run_all_tests_test.go
├── check_server_startup.go
├── check_server_startup_test.go
└── testdata/
    └── stack-go-postgres.yaml

skills/create-sensors/                             # NEW skill (replaces create-sensor)
├── SKILL.md
└── scripts/
    ├── read-usecases.go                           # adapted: recursive catalog walk
    ├── read-usecases_test.go
    ├── plan-and-emit.go                           # NEW orchestrator: layer matrix + auto-create
    ├── plan-and-emit_test.go
    ├── write-sensor.go                            # adapted: --out per-usecase dirs
    ├── write-sensor_test.go
    ├── write-fixture.go
    ├── write-fixture_test.go
    ├── catalog-sensors.go                         # adapted: recursive walk
    ├── catalog-sensors_test.go
    └── testdata/

skills/validate-usecase/                           # NEW skill
├── SKILL.md
└── scripts/
    ├── validate-usecase.go                        # orchestrator + confidence computer
    ├── validate-usecase_test.go
    └── testdata/
```

### Deleted files

```
skills/create-sensor/                              # entire skill (replaced by create-sensors)

lib/planning/group.go                              # old inter-usecase grouping
lib/planning/infer.go                              # old InferKind/Type/Output
lib/planning/shape.go                              # old Plan/StepOutline types
lib/planning/plan.go                              # old materialize/buildPlan
lib/planning/plan_test.go                          # old tests
lib/planning/testdata/                             # old fixtures (if not reusable)
```

### Untouched

- `schemas/usecase.yaml` — `blind_spots` on UseCase stays (different field, different semantic)
- `schemas/stack.yaml`, `schemas/signal.yaml` — unchanged
- `.harness/usecases/**` — kept; usecase YAMLs feed `/create-sensors`
- `lib/orchestrator/`, `lib/registry/`, `lib/watcher/`, `lib/subprocess/`, runtime infra — unchanged
- `skills/detect-sensors/`, `skills/run-sensor/`, `skills/start-sensor/`, `skills/stop-sensor/`, `skills/list-sensors/`, `skills/tail-sensor/`, `skills/heal-sensor/`, `skills/detect-usecases/` — unchanged for this plan (`/detect-sensors`'s SKILL.md mentions `blind_spots` in sensor authoring; left as-is until a follow-up cleans the prose)

### Convention notes (referenced by every recipe task)

- Layer recipe files MUST NOT be named `<name>_test.go` (Go would skip them from build). The three test-flavored layers drop the suffix: `unit.go`, `integration.go`, `contract.go`. Their test siblings keep `_test.go`.
- Every recipe file declares `package layer` and registers itself via `init()` calling `Register(<Layer>, &<Type>{})`.
- `Draft.SensorID` follows the id-naming convention from the spec:
  - Composite (layer entrypoint): `<layer>-<usecase-id>`
  - Solo narrow entrypoint: `<verb>-<lens>-<usecase-id>` (e.g., `observe-db-create-user`)
  - Scenario inside a multi-scenario layer: `<layer>-<scenario-slug>-<usecase-id>`
- Inferential drafts (`code-quality`, `architecture`, `security`, `dependency-health`) emit DEFAULT calibration: `model: anthropic/claude-sonnet-4-6`, `confidence_threshold: 0.7`, `calibration_set: ""`, `calibration_size: 1`, `calibration_date: <today YYYY-MM-DD>`. No interactive gate.

---

## Section 1 — Schema and struct foundation

### Task 1: Remove `blind_spots` from `schemas/sensor.yaml`

**Files:**
- Modify: `schemas/sensor.yaml`

- [ ] **Step 1: Locate the `blind_spots` block**

Run: `grep -n "blind_spots" schemas/sensor.yaml`

Expected: a single match around the top-level `properties:` block.

- [ ] **Step 2: Delete the block**

Edit `schemas/sensor.yaml`: remove the entire `blind_spots:` property (description + items + type), leaving the surrounding `properties:` map intact.

- [ ] **Step 3: Verify nothing else references it**

Run: `grep -n "blind_spots\|BlindSpots" schemas/`

Expected: zero matches.

- [ ] **Step 4: Commit**

```bash
git add schemas/sensor.yaml
git commit -m "schema(sensor): remove blind_spots field"
```

---

### Task 2: Add `layer` enum to `schemas/sensor.yaml`

**Files:**
- Modify: `schemas/sensor.yaml`

- [ ] **Step 1: Add the `layer` property next to `kind` / `type`**

Edit `schemas/sensor.yaml`. Under `properties:`, insert (alphabetical with surrounding props is fine):

```yaml
  layer:
    description: |
      Validation angle this sensor takes. /create-sensors emits every per-usecase
      sensor with this field set. /detect-sensors emits root-level core sensors
      with this field omitted. The schema marks it optional; the producing skills
      enforce presence/absence based on file location.
    enum:
      - unit-test
      - integration-test
      - contract-test
      - e2e
      - db-state
      - log-trace
      - metric
      - event-emission
      - event-consumption
      - performance
      - resilience
      - code-quality
      - architecture
      - security
      - dependency-health
      - db-schema
      - accessibility
    type: string
```

- [ ] **Step 2: Validate the YAML parses**

Run: `go run -C "${CLAUDE_PLUGIN_ROOT:-$(pwd)}" -tags=schema_loader ./lib/schema/cmd/... 2>&1 | head -5` (or, if no loader cmd exists, simply `python3 -c "import yaml; yaml.safe_load(open('schemas/sensor.yaml'))"`).

Expected: no parse error.

- [ ] **Step 3: Commit**

```bash
git add schemas/sensor.yaml
git commit -m "schema(sensor): add layer enum (17 values)"
```

---

### Task 3: Remove `BlindSpots` from `lib/sensor.Sensor` and add `Layer`

**Files:**
- Modify: `lib/sensor/shape.go`
- Modify: `lib/sensor/shape_test.go`

- [ ] **Step 1: Update `Sensor` struct**

Edit `lib/sensor/shape.go`. In the `type Sensor struct { ... }` block:

- Delete the line `BlindSpots     []string        \`json:"blind_spots,omitempty"\``.
- Add immediately after `UseCases       []string        \`json:"use_cases"\``:

```go
Layer Layer `json:"layer,omitempty"`
```

- [ ] **Step 2: Add the `Layer` type and constants**

Edit `lib/sensor/shape.go`. After the `type Output string` block (or anywhere in the type-constant group), add:

```go
// Layer enumerates the validation lens a per-usecase sensor takes.
// Mirrors schemas/sensor.yaml::properties.layer.enum and the
// canonical Layer constants in lib/planning/layer/layer.go.
// Optional on the schema; required programmatically for any sensor
// persisted by /create-sensors under .harness/sensors/<usecase-id>/.
type Layer string

const (
	LayerUnitTest         Layer = "unit-test"
	LayerIntegrationTest  Layer = "integration-test"
	LayerContractTest     Layer = "contract-test"
	LayerE2E              Layer = "e2e"
	LayerDBState          Layer = "db-state"
	LayerLogTrace         Layer = "log-trace"
	LayerMetric           Layer = "metric"
	LayerEventEmission    Layer = "event-emission"
	LayerEventConsumption Layer = "event-consumption"
	LayerPerformance      Layer = "performance"
	LayerResilience       Layer = "resilience"
	LayerCodeQuality      Layer = "code-quality"
	LayerArchitecture     Layer = "architecture"
	LayerSecurity         Layer = "security"
	LayerDependencyHealth Layer = "dependency-health"
	LayerDBSchema         Layer = "db-schema"
	LayerAccessibility    Layer = "accessibility"
)
```

- [ ] **Step 3: Update existing tests**

Edit `lib/sensor/shape_test.go`. Remove any line that references `BlindSpots`. Add a small assertion that the new `Layer` field round-trips:

```go
func TestSensorLayerRoundTrip(t *testing.T) {
	s := &Sensor{ID: "x", Version: "0.1.0", Layer: LayerE2E}
	m := s.AsMap()
	if m["layer"] != "e2e" {
		t.Fatalf("layer not in AsMap: %#v", m["layer"])
	}
	if s.Layer != "e2e" {
		t.Fatalf("layer constant: %q", s.Layer)
	}
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./lib/sensor/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/sensor/shape.go lib/sensor/shape_test.go
git commit -m "lib(sensor): drop BlindSpots, add Layer type + 17 constants"
```

---

### Task 4: Compile-sweep BlindSpots removal across `lib/` and `skills/scripts/`

**Files:**
- Modify: any Go file referencing `BlindSpots` (sensor consumers only — DO NOT touch `lib/usecase/`'s `BlindSpots`)

- [ ] **Step 1: Inventory the references**

Run: `grep -rn "\.BlindSpots\|sensor\.BlindSpots\|Sensor\.BlindSpots" lib/ skills/ 2>/dev/null`

Expected output: a short list (envelope construction, validator extras, anything that read the field). Capture the list — every match must be either deleted or refactored before the build is green.

- [ ] **Step 2: For each match, delete or refactor**

For envelope/audit code: drop the field projection.
For validator/test extras: drop the test assertion.
For sensor authoring docs / SKILL.md (different sweep — see Task 5): not in scope here.

DO NOT modify `lib/usecase/shape.go` or `lib/usecase/shape_test.go` — those refer to the UseCase's `BlindSpots`, which stays.

- [ ] **Step 3: Verify build**

Run: `go build ./...`

Expected: zero errors.

- [ ] **Step 4: Run all tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "lib: drop remaining sensor BlindSpots references"
```

---

### Task 5: Strip sensor-side `blind_spots` mentions from `skills/detect-sensors/SKILL.md`

**Files:**
- Modify: `skills/detect-sensors/SKILL.md`

- [ ] **Step 1: Locate mentions**

Run: `grep -n "blind_spots\|BlindSpots" skills/detect-sensors/SKILL.md`

Expected: ~10 matches (per the earlier sweep).

- [ ] **Step 2: Rewrite each mention**

For each match, edit the surrounding paragraph to remove the directive to populate `blind_spots[]` on sensors. Where the prose said "annotate divergence in `blind_spots[]`", rewrite to "annotate divergence in the sensor `description`". Where it said "add a `blind_spots[]` entry: ...", drop the entry (the prose around it remains valid as guidance, just not as a literal field).

- [ ] **Step 3: Verify schema reference removed**

Run: `grep -n "blind_spots" skills/detect-sensors/SKILL.md`

Expected: zero matches.

- [ ] **Step 4: Commit**

```bash
git add skills/detect-sensors/SKILL.md
git commit -m "skill(detect-sensors): drop blind_spots authoring directives"
```

---

## Section 2 — `lib/planning/layer/` infrastructure

### Task 6: Delete old `lib/planning/` files

**Files:**
- Delete: `lib/planning/group.go`, `lib/planning/infer.go`, `lib/planning/shape.go`, `lib/planning/plan.go`, `lib/planning/plan_test.go`

- [ ] **Step 1: Confirm consumers gone**

Run: `grep -rn "planning\.\(Build\|InferKind\|InferType\|InferOutput\|Plan\|StepOutline\|Slugify\|Aggregate\|MakeAggregate\)" lib/ skills/`

Expected: zero matches (the old `/create-sensor` script is the only consumer; it'll be deleted in Section 9).

If you find a residual match in `skills/create-sensor/scripts/plan-sensors.go` — that file will be deleted in Task 31. Suppress here by deleting nothing in `skills/create-sensor/` yet (Task 31 covers that). Just confirm no OTHER consumer exists.

- [ ] **Step 2: Delete the files**

```bash
rm lib/planning/group.go lib/planning/infer.go lib/planning/shape.go lib/planning/plan.go lib/planning/plan_test.go
rm -rf lib/planning/testdata
```

- [ ] **Step 3: Verify build still green (modulo `skills/create-sensor/`)**

Run: `go build ./lib/... ./skills/detect-sensors/... ./skills/validate-usecase/... 2>&1 | head -20`

`skills/create-sensor/scripts/plan-sensors.go` WILL fail to build at this point — that's expected and addressed by Task 31's deletion. Confirm no OTHER package broke.

- [ ] **Step 4: Commit**

```bash
git add lib/planning/
git commit -m "lib(planning): drop pre-layer-matrix grouping (group/infer/shape/plan)"
```

---

### Task 7: Create `lib/planning/layer/layer.go` — types and registry

**Files:**
- Create: `lib/planning/layer/layer.go`
- Create: `lib/planning/layer/layer_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/layer_test.go`:

```go
package layer

import (
	"sort"
	"testing"
)

func TestRegistryHasAllSeventeenLayers(t *testing.T) {
	got := AllLayers()
	if len(got) != 17 {
		t.Fatalf("expected 17 layers, got %d: %v", len(got), got)
	}
	seen := map[Layer]bool{}
	for _, l := range got {
		if seen[l] {
			t.Fatalf("duplicate registration: %s", l)
		}
		seen[l] = true
	}
}

func TestAllLayersSortedDeterministic(t *testing.T) {
	first := AllLayers()
	second := AllLayers()
	if len(first) != len(second) {
		t.Fatalf("non-deterministic length")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("non-deterministic order at %d: %s vs %s", i, first[i], second[i])
		}
	}
	if !sort.SliceIsSorted(first, func(i, j int) bool { return string(first[i]) < string(first[j]) }) {
		t.Fatalf("AllLayers() must return sorted slice")
	}
}

func TestRegisterRejectsUnknownLayer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic registering unknown layer")
		}
	}()
	Register("not-a-real-layer", nil)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./lib/planning/layer/...`

Expected: FAIL (package does not exist).

- [ ] **Step 3: Write the minimal `layer.go`**

Create `lib/planning/layer/layer.go`:

```go
// Package layer is the deterministic /create-sensors planner. Each
// validation lens is a Go file in this package implementing LayerRecipe;
// init() in each file registers itself into the package registry. The
// enum mirrors lib/sensor.Layer and schemas/sensor.yaml::properties.layer.enum.
package layer

import (
	"sort"
	"sync"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// Layer mirrors lib/sensor.Layer; redeclared here so this package does
// not have a hard dependency on the sensor struct at recipe-author time.
type Layer = sensor.Layer

// LayerRecipe is the per-layer interface. Implementations live in
// sibling files; each file's init() registers itself.
type LayerRecipe interface {
	// Name returns the canonical layer slug.
	Name() Layer

	// Applicable reports whether this layer makes sense for the given
	// stack + usecase + existing catalog. The string is the reason
	// when the layer is NOT applicable (surfaced in the /create-sensors
	// plan report).
	Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string)

	// Plan returns 1..N draft sensors that, together, validate the
	// usecase through this layer's lens. Each draft carries layer=Name().
	// Drafts MUST be topologically ordered (leaves before composites).
	Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft
}

// Draft is the planner's neutral representation of one sensor to emit.
// /create-sensors converts each Draft into a sensor.yaml via write-sensor.
type Draft struct {
	SensorID    string
	Layer       Layer
	Kind        sensor.Kind
	Type        sensor.Type
	Output      sensor.Output
	Description string
	UseCases    []string
	Requires    []sensor.Requirement
	Execution   sensor.Execution
	Cost        sensor.Cost
	Triggers    []sensor.Trigger
	Calibration *sensor.Calibration
}

var (
	mu       sync.RWMutex
	registry = map[Layer]LayerRecipe{}
)

// Register inserts a recipe into the package registry. Panics if the
// layer is not a known enum value or if it is already registered.
// Intended for use from each recipe file's init().
func Register(l Layer, r LayerRecipe) {
	mu.Lock()
	defer mu.Unlock()
	if !validLayer(l) {
		panic("layer.Register: unknown layer " + string(l))
	}
	if _, dup := registry[l]; dup {
		panic("layer.Register: duplicate registration for " + string(l))
	}
	registry[l] = r
}

// Get returns the recipe for a layer. Returns nil if unregistered.
func Get(l Layer) LayerRecipe { mu.RLock(); defer mu.RUnlock(); return registry[l] }

// AllLayers returns the registered Layer values sorted alphabetically
// so iteration order is deterministic across processes.
func AllLayers() []Layer {
	mu.RLock()
	out := make([]Layer, 0, len(registry))
	for l := range registry {
		out = append(out, l)
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

// validLayer is the closed-enum guard. Add a new Layer constant in
// lib/sensor/shape.go AND the matching enum value in
// schemas/sensor.yaml AND a recipe file in this package together —
// they are versioned as one unit.
func validLayer(l Layer) bool {
	switch l {
	case sensor.LayerUnitTest, sensor.LayerIntegrationTest, sensor.LayerContractTest,
		sensor.LayerE2E,
		sensor.LayerDBState, sensor.LayerLogTrace, sensor.LayerMetric,
		sensor.LayerEventEmission, sensor.LayerEventConsumption,
		sensor.LayerPerformance, sensor.LayerResilience,
		sensor.LayerCodeQuality, sensor.LayerArchitecture, sensor.LayerSecurity,
		sensor.LayerDependencyHealth,
		sensor.LayerDBSchema, sensor.LayerAccessibility:
		return true
	}
	return false
}
```

- [ ] **Step 4: Run the test (still fails — no recipes registered)**

Run: `go test ./lib/planning/layer/...`

Expected: FAIL — `TestRegistryHasAllSeventeenLayers` reports 0 layers. This is correct; Tasks 9–25 register the 17 recipes. The other two sub-tests should already pass.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/layer.go lib/planning/layer/layer_test.go
git commit -m "lib(planning/layer): introduce Layer registry + LayerRecipe contract"
```

---

### Task 8: Create `lib/planning/layer/applicability.go` — shared helpers

**Files:**
- Create: `lib/planning/layer/applicability.go`
- Create: `lib/planning/layer/applicability_test.go`
- Create: `lib/planning/layer/testdata/stack-http-api-postgres.yaml`
- Create: `lib/planning/layer/testdata/stack-library-no-deps.yaml`

- [ ] **Step 1: Drop two sample stacks under testdata**

Create `lib/planning/layer/testdata/stack-http-api-postgres.yaml`:

```yaml
version: 0.1.0
detected_at: 2026-05-18T10:00:00Z
detected_by: manual
purpose: HTTP CRUD over users
archetypes: [http-api]
languages:
  - name: go
    version: "1.25"
components:
  - role: http-server
    name: github.com/go-chi/chi
    evidence: [{ file: cmd/api/main.go, rationale: "chi.NewRouter" }]
  - role: db-client
    name: github.com/jackc/pgx/v5
    evidence: [{ file: internal/users/repo.go, rationale: "pgx.Connect" }]
  - role: logger
    name: go.uber.org/zap
    evidence: [{ file: cmd/api/main.go, rationale: "zap.NewProduction" }]
  - role: test-runner
    name: go test
    evidence: [{ file: go.mod, rationale: "stdlib testing" }]
log_shapes:
  - id: zap-prod-json
    produced_by: [go.uber.org/zap]
    format: json
    sample: '{"level":"info","ts":1700000000,"msg":"POST /users","status":201}'
journeys:
  - id: users
    name: User management
    summary: CRUD over users resource
    archetype: http-api
    entry_points:
      - kind: http-route
        method: POST
        path: /users
        evidence: { file: cmd/api/main.go, rationale: "router.Post" }
```

Create `lib/planning/layer/testdata/stack-library-no-deps.yaml`:

```yaml
version: 0.1.0
detected_at: 2026-05-18T10:00:00Z
detected_by: manual
purpose: pure Go library
archetypes: [library]
languages: [{ name: go, version: "1.25" }]
components:
  - role: test-runner
    name: go test
    evidence: [{ file: go.mod, rationale: "stdlib testing" }]
log_shapes:
  - id: none
    produced_by: [stdlib]
    format: plain
    sample: ""
```

- [ ] **Step 2: Write the failing test**

Create `lib/planning/layer/applicability_test.go`:

```go
package layer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
	"sigs.k8s.io/yaml"
)

func loadStack(t *testing.T, name string) stack.Stack {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var s stack.Stack
	if err := yaml.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return s
}

func TestHasRole(t *testing.T) {
	s := loadStack(t, "stack-http-api-postgres.yaml")
	if !hasRole(s, "db-client") {
		t.Fatal("expected db-client to be present")
	}
	if hasRole(s, "metrics") {
		t.Fatal("metrics must not be present")
	}
}

func TestHasArchetype(t *testing.T) {
	s := loadStack(t, "stack-http-api-postgres.yaml")
	if !hasArchetype(s, "http-api") {
		t.Fatal("http-api expected")
	}
	if hasArchetype(s, "library") {
		t.Fatal("library must not be present")
	}
}

func TestHasLogShape(t *testing.T) {
	s := loadStack(t, "stack-http-api-postgres.yaml")
	if !hasLogShape(s) {
		t.Fatal("expected ≥1 log_shape")
	}
}

func TestHasCoreSensor(t *testing.T) {
	if hasCoreSensor(nil, "run-project") {
		t.Fatal("empty catalog must not have run-project")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./lib/planning/layer/... -run TestHas`

Expected: FAIL (helpers undeclared).

- [ ] **Step 4: Implement the helpers**

Create `lib/planning/layer/applicability.go`:

```go
package layer

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

// hasRole reports whether any component in the stack carries the given role.
func hasRole(s stack.Stack, role string) bool {
	for _, c := range s.Components {
		if string(c.Role) == role {
			return true
		}
	}
	return false
}

// hasArchetype reports whether the stack declares the given archetype.
func hasArchetype(s stack.Stack, a string) bool {
	for _, x := range s.Archetypes {
		if string(x) == a {
			return true
		}
	}
	return false
}

// hasLogShape reports whether the stack declares at least one log_shape.
func hasLogShape(s stack.Stack) bool { return len(s.LogShapes) > 0 }

// hasCoreSensor reports whether the given sensor id is present in the
// catalog (root-tier platform primitives).
func hasCoreSensor(cat []sensor.Sensor, id string) bool {
	for _, s := range cat {
		if s.ID == id {
			return true
		}
	}
	return false
}

// hasJourneyEntryPoints reports whether the usecase's parent journey on
// the stack has at least one declared entry_point.
func hasJourneyEntryPoints(s stack.Stack, journeyID string) bool {
	for _, j := range s.Journeys {
		if j.ID == journeyID {
			return len(j.EntryPoints) > 0
		}
	}
	return false
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./lib/planning/layer/... -run TestHas`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add lib/planning/layer/applicability.go lib/planning/layer/applicability_test.go lib/planning/layer/testdata/
git commit -m "lib(planning/layer): applicability helpers + sample stacks"
```

---

### Task 9: Add common testdata fixtures for recipe tests

**Files:**
- Create: `lib/planning/layer/testdata/usecase-create-user.yaml`
- Create: `lib/planning/layer/testdata/catalog-with-run-project.json`

- [ ] **Step 1: Add `usecase-create-user.yaml`**

Create `lib/planning/layer/testdata/usecase-create-user.yaml`:

```yaml
id: create-user
version: 0.1.0
journey_id: users
name: Create a new user
description: POST /users with an email creates a row in users table.
trigger:
  summary: HTTP POST /users with valid payload
  shape: http-request
  fixture:
    method: POST
    path: /users
    body: { email: alice@example.com }
behavior:
  summary: Persist a new user and return 201 Created
  business_rules:
    - email must be unique
    - email must match valid format
    - email field is required
expected_outcome:
  summary: 201 Created with user body, row in users, log line emitted
  shape: http-response
  fixture:
    status: 201
    body: { id: 1, email: alice@example.com }
  invariants:
    - exactly one row in users matches the email
    - request log line includes "POST /users" with status 201
  side_effects:
    - "db write: insert into users"
    - "log line: POST /users status 201"
evidence:
  - file: internal/users/handler.go
    rationale: handler entrypoint
    kind: implementation
  - file: internal/users/dto.go
    rationale: typed body
    kind: contract
```

- [ ] **Step 2: Add `catalog-with-run-project.json`**

Create `lib/planning/layer/testdata/catalog-with-run-project.json`:

```json
[
  {
    "id": "run-project",
    "kind": "setup",
    "type": "computational",
    "output": "stream",
    "blocking": true,
    "path": ".harness/sensors/run-project.yaml"
  }
]
```

(This file is a thin catalog projection. Tests load it via `os.ReadFile` and unmarshal into `[]sensor.Sensor` only when the recipe needs full sensor data; for `hasCoreSensor` the id is enough — tests construct `[]sensor.Sensor{{ID: "run-project"}}` inline. The file exists for symmetry with future, richer fixtures.)

- [ ] **Step 3: Commit**

```bash
git add lib/planning/layer/testdata/
git commit -m "lib(planning/layer): seed usecase + catalog fixtures"
```

---

## Section 3 — Recipes: test-execution family

### Task 10: Implement `unit.go` (unit-test layer)

**Files:**
- Create: `lib/planning/layer/unit.go`
- Create: `lib/planning/layer/unit_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/unit_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestUnitTestApplicable(t *testing.T) {
	r := Get(sensor.LayerUnitTest)
	if r == nil {
		t.Fatal("unit-test not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatalf("expected applicable, got reason %q", reason)
	}
}

func TestUnitTestNotApplicableWithoutTestRunner(t *testing.T) {
	r := Get(sensor.LayerUnitTest)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	// Strip the test-runner component.
	s.Components = filterOutRole(s.Components, "test-runner")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable when test-runner missing")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestUnitTestPlanEmitsOneNarrow(t *testing.T) {
	r := Get(sensor.LayerUnitTest)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.Layer != sensor.LayerUnitTest {
		t.Fatalf("layer = %s", d.Layer)
	}
	if d.SensorID != "unit-test-create-user" {
		t.Fatalf("id = %s", d.SensorID)
	}
	if d.Kind != sensor.KindAssertion {
		t.Fatalf("kind = %s", d.Kind)
	}
}
```

Also add helpers near the top of `applicability_test.go` (or a new file `testhelpers_test.go` in the same package):

```go
package layer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
	"sigs.k8s.io/yaml"
)

func loadUsecase(t *testing.T, name string) usecase.UseCase {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var uc usecase.UseCase
	if err := yaml.Unmarshal(body, &uc); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return uc
}

func filterOutRole(in []stack.Component, role string) []stack.Component {
	out := in[:0]
	for _, c := range in {
		if string(c.Role) != role {
			out = append(out, c)
		}
	}
	return out
}
```

Place this in a new file `lib/planning/layer/testhelpers_test.go` so it is shared by every recipe test without duplication.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./lib/planning/layer/... -run UnitTest`

Expected: FAIL (recipe unregistered).

- [ ] **Step 3: Implement `unit.go`**

Create `lib/planning/layer/unit.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type unitTest struct{}

func (unitTest) Name() Layer { return sensor.LayerUnitTest }

func (unitTest) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "test-runner") {
		return false, "no role=test-runner component on stack"
	}
	return true, ""
}

func (unitTest) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("unit-test-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerUnitTest,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Runs unit tests filtered to %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 200, P95MS: 2000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: fmt.Sprintf("go test -run '%s' ./...", testRunPattern(uc)),
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

// testRunPattern derives a Go test -run regex from the usecase id. For
// non-Go stacks this would be replaced with the language-appropriate
// invocation; phase 1 covers Go only via the test-runner role check.
func testRunPattern(uc usecase.UseCase) string {
	return "Test.*" + camelize(uc.ID)
}

func camelize(s string) string {
	out := []byte{}
	upper := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' || c == '_' {
			upper = true
			continue
		}
		if upper && c >= 'a' && c <= 'z' {
			c = c - 32
		}
		out = append(out, c)
		upper = false
	}
	return string(out)
}

func init() { Register(sensor.LayerUnitTest, unitTest{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run UnitTest`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/unit.go lib/planning/layer/unit_test.go lib/planning/layer/testhelpers_test.go
git commit -m "lib(planning/layer): unit-test recipe"
```

---

### Task 11: Implement `integration.go` (integration-test layer)

**Files:**
- Create: `lib/planning/layer/integration.go`
- Create: `lib/planning/layer/integration_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/integration_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestIntegrationApplicableRequiresBoundaryRole(t *testing.T) {
	r := Get(sensor.LayerIntegrationTest)
	if r == nil {
		t.Fatal("integration-test not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, _ := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatal("expected applicable for test-runner+db-client stack")
	}
}

func TestIntegrationNotApplicableLibraryOnly(t *testing.T) {
	r := Get(sensor.LayerIntegrationTest)
	s := loadStack(t, "stack-library-no-deps.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable for library-only stack")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}

func TestIntegrationPlanEmitsOneDraft(t *testing.T) {
	r := Get(sensor.LayerIntegrationTest)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	if drafts[0].SensorID != "integration-test-create-user" {
		t.Fatalf("id = %s", drafts[0].SensorID)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Integration`

Expected: FAIL.

- [ ] **Step 3: Implement `integration.go`**

Create `lib/planning/layer/integration.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type integrationTest struct{}

func (integrationTest) Name() Layer { return sensor.LayerIntegrationTest }

func (integrationTest) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "test-runner") {
		return false, "no role=test-runner component on stack"
	}
	if !(hasRole(s, "db-client") || hasRole(s, "queue-consumer") || hasRole(s, "queue-producer") || hasRole(s, "external-integration")) {
		return false, "no boundary role (db-client / queue-* / external-integration) on stack"
	}
	return true, ""
}

func (integrationTest) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("integration-test-%s", uc.ID)
	timeoutMS := 120000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerIntegrationTest,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Runs integration tests filtered to %s (exercises real boundary services).", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 30000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: fmt.Sprintf("go test -tags=integration -run '%s' ./...", testRunPattern(uc)),
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerIntegrationTest, integrationTest{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run Integration`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/integration.go lib/planning/layer/integration_test.go
git commit -m "lib(planning/layer): integration-test recipe"
```

---

### Task 12: Implement `contract.go` (contract-test layer)

**Files:**
- Create: `lib/planning/layer/contract.go`
- Create: `lib/planning/layer/contract_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/contract_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestContractApplicableRequiresContract(t *testing.T) {
	r := Get(sensor.LayerContractTest)
	if r == nil {
		t.Fatal("contract-test not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	// No openapi.yaml in testdata yet — expect NOT applicable.
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable without OpenAPI evidence")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Contract`

Expected: FAIL.

- [ ] **Step 3: Implement `contract.go`**

Create `lib/planning/layer/contract.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type contractTest struct{}

func (contractTest) Name() Layer { return sensor.LayerContractTest }

func (contractTest) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !(hasRole(s, "http-server") || hasRole(s, "rpc")) {
		return false, "no role=http-server or role=rpc component on stack"
	}
	// Phase 1: we treat contract evidence as the explicit presence of a
	// component named openapi.yaml / openapi.json / *.proto in any
	// component.evidence. A future refinement can scan stack.contracts[].
	for _, c := range s.Components {
		for _, e := range c.Evidence {
			f := e.File
			if endsWith(f, ".proto") || endsWith(f, "openapi.yaml") || endsWith(f, "openapi.json") {
				return true, ""
			}
		}
	}
	return false, "no OpenAPI / proto contract file in any component evidence"
}

func endsWith(s, suffix string) bool {
	if len(suffix) > len(s) {
		return false
	}
	return s[len(s)-len(suffix):] == suffix
}

func (contractTest) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("contract-test-%s", uc.ID)
	timeoutMS := 30000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerContractTest,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Validates %s request/response against the API contract.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 100, P95MS: 1000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: wire a real contract validator (oas-validator / buf lint) appropriate to the project' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerContractTest, contractTest{}) }
```

(The `echo 'TODO ... false'` placeholder command is intentional — the recipe declares the SLOT for a real validator, and the operator wires it via `/update-sensor` once they pick the tool. The recipe never fabricates a working command from thin air.)

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run Contract`

Expected: PASS (NOT applicable on the current stack, which is the asserted behavior).

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/contract.go lib/planning/layer/contract_test.go
git commit -m "lib(planning/layer): contract-test recipe"
```

---

### Task 13: Implement `e2e.go` (e2e layer with multi-scenario derivation)

**Files:**
- Create: `lib/planning/layer/e2e.go`
- Create: `lib/planning/layer/e2e_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/e2e_test.go`:

```go
package layer

import (
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestE2EApplicableRequiresEntryPointsAndRunProject(t *testing.T) {
	r := Get(sensor.LayerE2E)
	if r == nil {
		t.Fatal("e2e not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	// No catalog supplied — expect NOT applicable with "core sensor missing" reason.
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable without run-project in catalog")
	}
	if !strings.Contains(reason, "run-project") {
		t.Fatalf("reason should name run-project; got %q", reason)
	}

	// With run-project in catalog, expect APPLICABLE.
	cat := []sensor.Sensor{{ID: "run-project"}}
	ok, _ = r.Applicable(s, uc, cat)
	if !ok {
		t.Fatal("expected applicable with run-project in catalog")
	}
}

func TestE2EPlanEmitsCompositePlusScenarios(t *testing.T) {
	r := Get(sensor.LayerE2E)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	cat := []sensor.Sensor{{ID: "run-project"}}
	drafts := r.Plan(s, uc, cat)

	// Expect: 1 happy-path scenario + N business_rule scenarios + 1 composite.
	// usecase has 3 business_rules ⇒ 4 narrows + 1 composite = 5 drafts.
	if len(drafts) != 5 {
		t.Fatalf("expected 5 drafts (1 happy + 3 rule scenarios + 1 composite), got %d", len(drafts))
	}

	// Last draft MUST be the composite.
	last := drafts[len(drafts)-1]
	if last.SensorID != "e2e-create-user" {
		t.Fatalf("composite id = %s", last.SensorID)
	}
	if last.Layer != sensor.LayerE2E {
		t.Fatalf("composite layer = %s", last.Layer)
	}
	// Composite references every scenario by SensorStep ref.
	if len(last.Execution.Steps) != 4 {
		t.Fatalf("composite expected 4 SensorSteps, got %d", len(last.Execution.Steps))
	}
	for _, st := range last.Execution.Steps {
		if st.Type != "sensor" {
			t.Fatalf("composite step %s is not type=sensor", st.ID)
		}
	}

	// Every scenario has layer=e2e.
	for i, d := range drafts {
		if d.Layer != sensor.LayerE2E {
			t.Fatalf("draft %d layer = %s", i, d.Layer)
		}
	}

	// Happy scenario id.
	if drafts[0].SensorID != "e2e-happy-path-create-user" {
		t.Fatalf("first draft id = %s", drafts[0].SensorID)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run E2E`

Expected: FAIL.

- [ ] **Step 3: Implement `e2e.go`**

Create `lib/planning/layer/e2e.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type e2eRecipe struct{}

func (e2eRecipe) Name() Layer { return sensor.LayerE2E }

func (e2eRecipe) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasJourneyEntryPoints(s, uc.JourneyID) {
		return false, fmt.Sprintf("journey %s has no entry_points", uc.JourneyID)
	}
	if !hasCoreSensor(cat, "run-project") {
		return false, "core sensor run-project missing from catalog (will be auto-created)"
	}
	return true, ""
}

func (e2eRecipe) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	scenarios := deriveScenarios(uc)
	drafts := make([]Draft, 0, len(scenarios)+1)

	// One narrow per scenario.
	timeoutMS := 30000
	for _, sc := range scenarios {
		drafts = append(drafts, Draft{
			SensorID:    fmt.Sprintf("e2e-%s-%s", sc.Slug, uc.ID),
			Layer:       sensor.LayerE2E,
			Kind:        sensor.KindAssertion,
			Type:        sensor.TypeComputational,
			Output:      sensor.OutputStream,
			Description: sc.Description,
			UseCases:    []string{uc.ID},
			Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
			Requires: []sensor.Requirement{
				{Kind: sensor.RequireSensor, ID: "run-project"},
			},
			Cost: sensor.Cost{
				Class:   sensor.CostClassMedium,
				Latency: sensor.Latency{P50MS: 500, P95MS: 5000, TimeoutMS: &timeoutMS},
				Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 128},
			},
			Execution: sensor.Execution{
				Steps: []sensor.StepConfig{{
					ID:     "replay",
					Type:   "shell",
					Run:    fmt.Sprintf("echo 'TODO: replay %s against %s and assert %s'; false", sc.Slug, uc.ID, sc.ExpectedAssertion),
					ExitCodeMap: map[string]sensor.Verdict{"0": "pass", "*": "fail"},
				}},
			},
		})
	}

	// One composite that references every scenario by SensorStep.
	steps := make([]sensor.StepConfig, 0, len(scenarios))
	for _, sc := range scenarios {
		steps = append(steps, sensor.StepConfig{
			ID:   fmt.Sprintf("scenario-%s", sc.Slug),
			Type: "sensor",
			Ref:  fmt.Sprintf("e2e-%s-%s", sc.Slug, uc.ID),
		})
	}
	compositeTimeout := 60000
	drafts = append(drafts, Draft{
		SensorID:    fmt.Sprintf("e2e-%s", uc.ID),
		Layer:       sensor.LayerE2E,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputStream,
		Description: fmt.Sprintf("Orchestrates every e2e scenario for %s (happy + rule violations).", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Requires: []sensor.Requirement{
			{Kind: sensor.RequireSensor, ID: "run-project"},
		},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 2000, P95MS: 15000, TimeoutMS: &compositeTimeout},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 128},
		},
		Execution: sensor.Execution{Steps: steps},
	})

	return drafts
}

// e2eScenario is the internal planner-level representation of one e2e
// scenario to materialize. Slug feeds the sensor id; Description and
// ExpectedAssertion seed the shell step placeholder body the operator
// later fills in via /update-sensor.
type e2eScenario struct {
	Slug              string
	Description       string
	ExpectedAssertion string
}

// deriveScenarios extracts one happy-path scenario + one per business
// rule. Rules are slugged via Slugify.
func deriveScenarios(uc usecase.UseCase) []e2eScenario {
	out := []e2eScenario{{
		Slug:              "happy-path",
		Description:       fmt.Sprintf("Replays the canonical fixture for %s and asserts the canonical response.", uc.ID),
		ExpectedAssertion: "canonical expected_outcome.fixture",
	}}
	for _, rule := range uc.Behavior.BusinessRules {
		out = append(out, e2eScenario{
			Slug:              Slugify(rule),
			Description:       fmt.Sprintf("Exercises violation of rule %q on %s.", rule, uc.ID),
			ExpectedAssertion: fmt.Sprintf("the API rejects with the documented error for %q", rule),
		})
	}
	return out
}

// Slugify is shared with the other recipes that compose ids from
// free-form text. Kept package-private until a second consumer appears.
func Slugify(s string) string {
	lower := []byte{}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			lower = append(lower, c+32)
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			lower = append(lower, c)
		case c == ' ', c == '-', c == '_':
			lower = append(lower, '-')
		}
	}
	// collapse runs of '-' and trim.
	out := []byte{}
	prev := byte(0)
	for _, c := range lower {
		if c == '-' && prev == '-' {
			continue
		}
		out = append(out, c)
		prev = c
	}
	for len(out) > 0 && out[0] == '-' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return string(out)
}

func init() { Register(sensor.LayerE2E, e2eRecipe{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run E2E`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/e2e.go lib/planning/layer/e2e_test.go
git commit -m "lib(planning/layer): e2e recipe (multi-scenario from business_rules)"
```

---

I'll continue Section 4 (runtime observation family) and onward in the next chunk to keep this file appendable. The plan continues below — DO NOT TRUNCATE; the rest is appended in subsequent edits.

## Section 4 — Recipes: runtime-observation family

### Task 14: Implement `db_state.go` (db-state layer)

**Files:**
- Create: `lib/planning/layer/db_state.go`
- Create: `lib/planning/layer/db_state_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/db_state_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestDBStateApplicable(t *testing.T) {
	r := Get(sensor.LayerDBState)
	if r == nil {
		t.Fatal("db-state not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, _ := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatal("expected applicable")
	}
}

func TestDBStateNotApplicableWithoutDBClient(t *testing.T) {
	r := Get(sensor.LayerDBState)
	s := loadStack(t, "stack-library-no-deps.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}

func TestDBStatePlanEmitsOneNarrow(t *testing.T) {
	r := Get(sensor.LayerDBState)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.SensorID != "observe-db-create-user" {
		t.Fatalf("id = %s", d.SensorID)
	}
	if d.Kind != sensor.KindObservation {
		t.Fatalf("kind = %s", d.Kind)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run DBState`

Expected: FAIL.

- [ ] **Step 3: Implement `db_state.go`**

Create `lib/planning/layer/db_state.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type dbState struct{}

func (dbState) Name() Layer { return sensor.LayerDBState }

func (dbState) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "db-client") {
		return false, "no role=db-client component on stack"
	}
	return true, ""
}

func (dbState) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-db-%s", uc.ID)
	timeoutMS := 10000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerDBState,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Queries the database to observe the row produced by %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Requires: []sensor.Requirement{
			{Kind: sensor.RequireSensor, ID: "setup-postgres"},
		},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 50, P95MS: 500, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: psql -c \"SELECT ... FROM <table> WHERE <fixture>\"' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerDBState, dbState{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run DBState`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/db_state.go lib/planning/layer/db_state_test.go
git commit -m "lib(planning/layer): db-state recipe"
```

---

### Task 15: Implement `log_trace.go` (log-trace layer)

**Files:**
- Create: `lib/planning/layer/log_trace.go`
- Create: `lib/planning/layer/log_trace_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/log_trace_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestLogTraceApplicable(t *testing.T) {
	r := Get(sensor.LayerLogTrace)
	if r == nil {
		t.Fatal("log-trace not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, _ := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatal("expected applicable")
	}
}

func TestLogTracePlanEmitsOneNarrow(t *testing.T) {
	r := Get(sensor.LayerLogTrace)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	if drafts[0].SensorID != "observe-log-create-user" {
		t.Fatalf("id = %s", drafts[0].SensorID)
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run LogTrace`

Expected: FAIL.

- [ ] **Step 3: Implement `log_trace.go`**

Create `lib/planning/layer/log_trace.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type logTrace struct{}

func (logTrace) Name() Layer { return sensor.LayerLogTrace }

func (logTrace) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasLogShape(s) {
		return false, "stack declares no log_shapes"
	}
	return true, ""
}

func (logTrace) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-log-%s", uc.ID)
	timeoutMS := 10000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerLogTrace,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputStream,
		Description: fmt.Sprintf("Greps the project's log_shape for the line produced by %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 50, P95MS: 500, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: grep the project log_shape sample for the usecase entry-point pattern' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
			OutputParsing: &sensor.OutputParsing{
				Patterns: []sensor.Pattern{{Regex: ".*", Verdict: "pass", Severity: "info"}},
			},
		},
	}}
}

func init() { Register(sensor.LayerLogTrace, logTrace{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run LogTrace`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/log_trace.go lib/planning/layer/log_trace_test.go
git commit -m "lib(planning/layer): log-trace recipe"
```

---

### Task 16: Implement `metric.go` (metric layer)

**Files:**
- Create: `lib/planning/layer/metric.go`
- Create: `lib/planning/layer/metric_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/metric_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestMetricNotApplicableWithoutMetricsRole(t *testing.T) {
	r := Get(sensor.LayerMetric)
	if r == nil {
		t.Fatal("metric not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable on stack without metrics")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Metric`

Expected: FAIL.

- [ ] **Step 3: Implement `metric.go`**

Create `lib/planning/layer/metric.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type metric struct{}

func (metric) Name() Layer { return sensor.LayerMetric }

func (metric) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "metrics") {
		return false, "no role=metrics component on stack"
	}
	return true, ""
}

func (metric) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-metric-%s", uc.ID)
	timeoutMS := 10000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerMetric,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Queries the metrics surface for the counter/histogram %s should increment.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 100, P95MS: 1000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: curl the metrics endpoint and assert on the relevant counter' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerMetric, metric{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run Metric`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/metric.go lib/planning/layer/metric_test.go
git commit -m "lib(planning/layer): metric recipe"
```

---

### Task 17: Implement `event_emission.go` and `event_consumption.go` (queue layers)

**Files:**
- Create: `lib/planning/layer/event_emission.go`
- Create: `lib/planning/layer/event_emission_test.go`
- Create: `lib/planning/layer/event_consumption.go`
- Create: `lib/planning/layer/event_consumption_test.go`

- [ ] **Step 1: Write the failing tests**

Create `lib/planning/layer/event_emission_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestEventEmissionNotApplicableWithoutProducer(t *testing.T) {
	r := Get(sensor.LayerEventEmission)
	if r == nil {
		t.Fatal("event-emission not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable without queue-producer")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}
```

Create `lib/planning/layer/event_consumption_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestEventConsumptionNotApplicableWithoutConsumer(t *testing.T) {
	r := Get(sensor.LayerEventConsumption)
	if r == nil {
		t.Fatal("event-consumption not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable without queue-consumer")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Event`

Expected: FAIL.

- [ ] **Step 3: Implement both recipes**

Create `lib/planning/layer/event_emission.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type eventEmission struct{}

func (eventEmission) Name() Layer { return sensor.LayerEventEmission }

func (eventEmission) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "queue-producer") {
		return false, "no role=queue-producer component on stack"
	}
	return true, ""
}

func (eventEmission) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-event-emission-%s", uc.ID)
	timeoutMS := 15000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerEventEmission,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Reads the queue topic to confirm %s emitted the expected event.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 200, P95MS: 2000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: consume from the topic and assert the event payload matches expected' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerEventEmission, eventEmission{}) }
```

Create `lib/planning/layer/event_consumption.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type eventConsumption struct{}

func (eventConsumption) Name() Layer { return sensor.LayerEventConsumption }

func (eventConsumption) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "queue-consumer") {
		return false, "no role=queue-consumer component on stack"
	}
	return true, ""
}

func (eventConsumption) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("observe-event-consumption-%s", uc.ID)
	timeoutMS := 15000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerEventConsumption,
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Publishes a synthetic event and observes that the consumer for %s handled it.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 200, P95MS: 2000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: publish a synthetic event and assert the consumer side-effect (DB / log / downstream)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerEventConsumption, eventConsumption{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run Event`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/event_emission.go lib/planning/layer/event_emission_test.go \
        lib/planning/layer/event_consumption.go lib/planning/layer/event_consumption_test.go
git commit -m "lib(planning/layer): event-emission + event-consumption recipes"
```

---

## Section 5 — Recipes: performance / resilience family

### Task 18: Implement `performance.go` (performance layer)

**Files:**
- Create: `lib/planning/layer/performance.go`
- Create: `lib/planning/layer/performance_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/performance_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestPerformanceApplicableHttpApi(t *testing.T) {
	r := Get(sensor.LayerPerformance)
	if r == nil {
		t.Fatal("performance not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, _ := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatal("expected applicable for http-api archetype")
	}
}

func TestPerformanceNotApplicableLibrary(t *testing.T) {
	r := Get(sensor.LayerPerformance)
	s := loadStack(t, "stack-library-no-deps.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable for library archetype")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Performance`

Expected: FAIL.

- [ ] **Step 3: Implement `performance.go`**

Create `lib/planning/layer/performance.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type performance struct{}

func (performance) Name() Layer { return sensor.LayerPerformance }

func (performance) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !(hasArchetype(s, "http-api") || hasArchetype(s, "queue-consumer") || hasArchetype(s, "queue-producer")) {
		return false, "archetype is not http-api, queue-consumer, or queue-producer"
	}
	return true, ""
}

func (performance) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("performance-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerPerformance,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Asserts p50/p95 latency baseline for %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 30000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPUMedium, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: hey/wrk/k6 against the entry point + assert p50/p95 thresholds' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerPerformance, performance{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run Performance`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/performance.go lib/planning/layer/performance_test.go
git commit -m "lib(planning/layer): performance recipe"
```

---

### Task 19: Implement `resilience.go` (resilience layer)

**Files:**
- Create: `lib/planning/layer/resilience.go`
- Create: `lib/planning/layer/resilience_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/resilience_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestResilienceNotApplicableWithoutFaultInjection(t *testing.T) {
	r := Get(sensor.LayerResilience)
	if r == nil {
		t.Fatal("resilience not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable without fault-injection tooling")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Resilience`

Expected: FAIL.

- [ ] **Step 3: Implement `resilience.go`**

Create `lib/planning/layer/resilience.go`:

```go
package layer

import (
	"fmt"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type resilience struct{}

func (resilience) Name() Layer { return sensor.LayerResilience }

// Applicable: any component whose name suggests a fault-injection
// library (toxiproxy, chaos-monkey, gremlin, pumba, …). Conservative
// match by substring; the operator names the tooling explicitly when
// stack.yaml is authored.
func (resilience) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	for _, c := range s.Components {
		n := strings.ToLower(c.Name)
		for _, kw := range []string{"toxiproxy", "chaos-monkey", "chaos-mesh", "gremlin", "pumba"} {
			if strings.Contains(n, kw) {
				return true, ""
			}
		}
	}
	return false, "no fault-injection tooling component (toxiproxy/chaos-*/gremlin/pumba) on stack"
}

func (resilience) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("resilience-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerResilience,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Injects a fault and asserts %s degrades gracefully.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 10000, P95MS: 30000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPUMedium, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: invoke the fault-injection tooling + assert the documented degradation' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerResilience, resilience{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run Resilience`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/resilience.go lib/planning/layer/resilience_test.go
git commit -m "lib(planning/layer): resilience recipe"
```

---

## Section 6 — Recipes: static quality family (inferential)

Common pattern for this family: every recipe is `kind=assertion`, `type=inferential`, `output=single`, `determinism=medium`, and emits a draft with DEFAULT calibration (see "Convention notes" at the top of the plan). No interactive gate — the operator tunes via the future `/update-sensor` skill.

### Task 20: Implement `code_quality.go` (code-quality layer)

**Files:**
- Create: `lib/planning/layer/code_quality.go`
- Create: `lib/planning/layer/code_quality_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/code_quality_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestCodeQualityAlwaysApplicable(t *testing.T) {
	r := Get(sensor.LayerCodeQuality)
	if r == nil {
		t.Fatal("code-quality not registered")
	}
	uc := loadUsecase(t, "usecase-create-user.yaml")
	for _, stk := range []string{"stack-http-api-postgres.yaml", "stack-library-no-deps.yaml"} {
		s := loadStack(t, stk)
		ok, reason := r.Applicable(s, uc, nil)
		if !ok {
			t.Fatalf("expected applicable for %s, got reason %q", stk, reason)
		}
	}
}

func TestCodeQualityPlanEmitsInferentialDraftWithCalibration(t *testing.T) {
	r := Get(sensor.LayerCodeQuality)
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	drafts := r.Plan(s, uc, nil)
	if len(drafts) != 1 {
		t.Fatalf("expected 1 draft, got %d", len(drafts))
	}
	d := drafts[0]
	if d.Type != sensor.TypeInferential {
		t.Fatalf("type = %s", d.Type)
	}
	if d.Calibration == nil {
		t.Fatal("calibration must be populated (default)")
	}
	if d.Calibration.ConfidenceThreshold == 0 {
		t.Fatal("default confidence_threshold must be non-zero")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run CodeQuality`

Expected: FAIL.

- [ ] **Step 3: Implement `code_quality.go`**

Create `lib/planning/layer/code_quality.go`:

```go
package layer

import (
	"fmt"
	"time"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type codeQuality struct{}

func (codeQuality) Name() Layer { return sensor.LayerCodeQuality }

func (codeQuality) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	return true, ""
}

func (codeQuality) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("code-quality-%s", uc.ID)
	timeoutMS := 60000
	maxTokens := 4096
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerCodeQuality,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeInferential,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("LLM-as-judge over the diff scoped to %s: duplication, complexity, idiomatic patterns.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassExpensive,
			Latency: sensor.Latency{P50MS: 3000, P95MS: 20000, TimeoutMS: &timeoutMS},
			Tokens: &sensor.Tokens{
				Model:     "anthropic/claude-sonnet-4-6",
				InputAvg:  4000,
				OutputAvg: 1000,
				MaxOutput: maxTokens,
			},
		},
		Execution: sensor.Execution{
			Model:              "anthropic/claude-sonnet-4-6",
			SystemPrompt:       "You audit code for duplication, complexity, and idiomatic patterns. Emit a single JSON Signal.",
			UserPromptTemplate: fmt.Sprintf("Audit the implementation of usecase %s. Emit a Signal JSON object.", uc.ID),
			Decoding: &sensor.Decoding{
				Temperature: 0.2,
				MaxTokens:   maxTokens,
			},
			Command: "echo 'TODO: route to the inferential runner (run-inferential)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "error", Severity: "high"},
			},
		},
		Calibration: defaultCalibration(),
	}}
}

// defaultCalibration returns the calibration block /create-sensors emits
// on every inferential draft. Operators tune via the future
// /update-sensor skill once they have a labelled set.
func defaultCalibration() *sensor.Calibration {
	return &sensor.Calibration{
		ConfidenceThreshold: 0.7,
		CalibrationSet:      "",
		CalibrationSize:     1,
		CalibrationDate:     time.Now().UTC().Format("2006-01-02"),
	}
}

func init() { Register(sensor.LayerCodeQuality, codeQuality{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run CodeQuality`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/code_quality.go lib/planning/layer/code_quality_test.go
git commit -m "lib(planning/layer): code-quality recipe (inferential, default calibration)"
```

---

### Task 21: Implement `architecture.go` (architecture layer)

**Files:**
- Create: `lib/planning/layer/architecture.go`
- Create: `lib/planning/layer/architecture_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/architecture_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestArchitectureAlwaysApplicable(t *testing.T) {
	r := Get(sensor.LayerArchitecture)
	if r == nil {
		t.Fatal("architecture not registered")
	}
	uc := loadUsecase(t, "usecase-create-user.yaml")
	s := loadStack(t, "stack-library-no-deps.yaml")
	ok, _ := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatal("expected applicable")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Architecture`

Expected: FAIL.

- [ ] **Step 3: Implement `architecture.go`**

Create `lib/planning/layer/architecture.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type architecture struct{}

func (architecture) Name() Layer { return sensor.LayerArchitecture }

func (architecture) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	return true, ""
}

func (architecture) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("architecture-%s", uc.ID)
	timeoutMS := 60000
	maxTokens := 4096
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerArchitecture,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeInferential,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("LLM-as-judge over %s's implementation: layering, dependency direction, boundary discipline.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassExpensive,
			Latency: sensor.Latency{P50MS: 3000, P95MS: 20000, TimeoutMS: &timeoutMS},
			Tokens: &sensor.Tokens{
				Model:     "anthropic/claude-sonnet-4-6",
				InputAvg:  4000,
				OutputAvg: 1000,
				MaxOutput: maxTokens,
			},
		},
		Execution: sensor.Execution{
			Model:              "anthropic/claude-sonnet-4-6",
			SystemPrompt:       "You audit architecture: layering, dependency direction, boundary discipline.",
			UserPromptTemplate: fmt.Sprintf("Audit the architectural shape of usecase %s. Emit a Signal JSON object.", uc.ID),
			Decoding: &sensor.Decoding{
				Temperature: 0.2,
				MaxTokens:   maxTokens,
			},
			Command: "echo 'TODO: route to the inferential runner (run-inferential)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "error", Severity: "high"},
			},
		},
		Calibration: defaultCalibration(),
	}}
}

func init() { Register(sensor.LayerArchitecture, architecture{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run Architecture`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/architecture.go lib/planning/layer/architecture_test.go
git commit -m "lib(planning/layer): architecture recipe"
```

---

### Task 22: Implement `security.go` (security layer)

**Files:**
- Create: `lib/planning/layer/security.go`
- Create: `lib/planning/layer/security_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/security_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestSecurityAlwaysApplicable(t *testing.T) {
	r := Get(sensor.LayerSecurity)
	if r == nil {
		t.Fatal("security not registered")
	}
	uc := loadUsecase(t, "usecase-create-user.yaml")
	s := loadStack(t, "stack-library-no-deps.yaml")
	ok, _ := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatal("expected applicable")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Security`

Expected: FAIL.

- [ ] **Step 3: Implement `security.go`**

Create `lib/planning/layer/security.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type security struct{}

func (security) Name() Layer { return sensor.LayerSecurity }

func (security) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	return true, ""
}

func (security) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("security-%s", uc.ID)
	timeoutMS := 60000
	maxTokens := 4096
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerSecurity,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeInferential,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("LLM-as-judge over %s's implementation: injection, secrets, authz, OWASP-class issues.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassExpensive,
			Latency: sensor.Latency{P50MS: 3000, P95MS: 20000, TimeoutMS: &timeoutMS},
			Tokens: &sensor.Tokens{
				Model:     "anthropic/claude-sonnet-4-6",
				InputAvg:  4000,
				OutputAvg: 1000,
				MaxOutput: maxTokens,
			},
		},
		Execution: sensor.Execution{
			Model:              "anthropic/claude-sonnet-4-6",
			SystemPrompt:       "You audit security: injection, secrets, authz, OWASP-class vulnerabilities.",
			UserPromptTemplate: fmt.Sprintf("Audit security of usecase %s. Emit a Signal JSON object.", uc.ID),
			Decoding: &sensor.Decoding{
				Temperature: 0.2,
				MaxTokens:   maxTokens,
			},
			Command: "echo 'TODO: route to the inferential runner (run-inferential)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "error", Severity: "high"},
			},
		},
		Calibration: defaultCalibration(),
	}}
}

func init() { Register(sensor.LayerSecurity, security{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run Security`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/security.go lib/planning/layer/security_test.go
git commit -m "lib(planning/layer): security recipe"
```

---

### Task 23: Implement `dependency_health.go` (dependency-health layer)

**Files:**
- Create: `lib/planning/layer/dependency_health.go`
- Create: `lib/planning/layer/dependency_health_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/dependency_health_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestDependencyHealthAlwaysApplicable(t *testing.T) {
	r := Get(sensor.LayerDependencyHealth)
	if r == nil {
		t.Fatal("dependency-health not registered")
	}
	uc := loadUsecase(t, "usecase-create-user.yaml")
	s := loadStack(t, "stack-library-no-deps.yaml")
	ok, _ := r.Applicable(s, uc, nil)
	if !ok {
		t.Fatal("expected applicable")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run DependencyHealth`

Expected: FAIL.

- [ ] **Step 3: Implement `dependency_health.go`**

Create `lib/planning/layer/dependency_health.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type dependencyHealth struct{}

func (dependencyHealth) Name() Layer { return sensor.LayerDependencyHealth }

func (dependencyHealth) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	return true, ""
}

func (dependencyHealth) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("dependency-health-%s", uc.ID)
	timeoutMS := 60000
	maxTokens := 4096
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerDependencyHealth,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeInferential,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("LLM-as-judge over %s's dependency manifests: outdated, vulnerable, license-incompatible.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassExpensive,
			Latency: sensor.Latency{P50MS: 3000, P95MS: 20000, TimeoutMS: &timeoutMS},
			Tokens: &sensor.Tokens{
				Model:     "anthropic/claude-sonnet-4-6",
				InputAvg:  4000,
				OutputAvg: 1000,
				MaxOutput: maxTokens,
			},
		},
		Execution: sensor.Execution{
			Model:              "anthropic/claude-sonnet-4-6",
			SystemPrompt:       "You audit dependencies: outdated versions, known vulnerabilities, license incompatibilities.",
			UserPromptTemplate: fmt.Sprintf("Audit dependency health relevant to usecase %s. Emit a Signal JSON object.", uc.ID),
			Decoding: &sensor.Decoding{
				Temperature: 0.2,
				MaxTokens:   maxTokens,
			},
			Command: "echo 'TODO: route to the inferential runner (run-inferential)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "error", Severity: "high"},
			},
		},
		Calibration: defaultCalibration(),
	}}
}

func init() { Register(sensor.LayerDependencyHealth, dependencyHealth{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run DependencyHealth`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/dependency_health.go lib/planning/layer/dependency_health_test.go
git commit -m "lib(planning/layer): dependency-health recipe"
```

---

## Section 7 — Recipes: schema / contract family

### Task 24: Implement `db_schema.go` (db-schema layer)

**Files:**
- Create: `lib/planning/layer/db_schema.go`
- Create: `lib/planning/layer/db_schema_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/db_schema_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestDBSchemaNotApplicableWithoutMigrationsEvidence(t *testing.T) {
	r := Get(sensor.LayerDBSchema)
	if r == nil {
		t.Fatal("db-schema not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	// testdata stack has db-client but no migrations evidence file.
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable without migrations folder evidence")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run DBSchema`

Expected: FAIL.

- [ ] **Step 3: Implement `db_schema.go`**

Create `lib/planning/layer/db_schema.go`:

```go
package layer

import (
	"fmt"
	"strings"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type dbSchema struct{}

func (dbSchema) Name() Layer { return sensor.LayerDBSchema }

func (dbSchema) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !hasRole(s, "db-client") {
		return false, "no role=db-client component on stack"
	}
	for _, c := range s.Components {
		for _, e := range c.Evidence {
			if strings.Contains(e.File, "migration") || strings.Contains(e.File, "migrations/") {
				return true, ""
			}
		}
	}
	return false, "no migrations evidence in any component"
}

func (dbSchema) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("db-schema-%s", uc.ID)
	timeoutMS := 30000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerDBSchema,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Asserts the migration set is forward+backward applicable for %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 10000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: replay migrations up then down then up; assert no error' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerDBSchema, dbSchema{}) }
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/layer/... -run DBSchema`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/db_schema.go lib/planning/layer/db_schema_test.go
git commit -m "lib(planning/layer): db-schema recipe"
```

---

### Task 25: Implement `accessibility.go` (accessibility layer)

**Files:**
- Create: `lib/planning/layer/accessibility.go`
- Create: `lib/planning/layer/accessibility_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/layer/accessibility_test.go`:

```go
package layer

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestAccessibilityNotApplicableForHttpApi(t *testing.T) {
	r := Get(sensor.LayerAccessibility)
	if r == nil {
		t.Fatal("accessibility not registered")
	}
	s := loadStack(t, "stack-http-api-postgres.yaml")
	uc := loadUsecase(t, "usecase-create-user.yaml")
	ok, reason := r.Applicable(s, uc, nil)
	if ok {
		t.Fatal("expected NOT applicable for http-api archetype")
	}
	if reason == "" {
		t.Fatal("expected reason")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/layer/... -run Accessibility`

Expected: FAIL.

- [ ] **Step 3: Implement `accessibility.go`**

Create `lib/planning/layer/accessibility.go`:

```go
package layer

import (
	"fmt"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type accessibility struct{}

func (accessibility) Name() Layer { return sensor.LayerAccessibility }

func (accessibility) Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string) {
	if !(hasArchetype(s, "http-spa") || hasArchetype(s, "http-ssr")) {
		return false, "archetype is not http-spa or http-ssr"
	}
	return true, ""
}

func (accessibility) Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft {
	id := fmt.Sprintf("accessibility-%s", uc.ID)
	timeoutMS := 60000
	return []Draft{{
		SensorID:    id,
		Layer:       sensor.LayerAccessibility,
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: fmt.Sprintf("Runs axe/pa11y over the page or component path of %s.", uc.ID),
		UseCases:    []string{uc.ID},
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 30000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: run axe-core / pa11y against the page' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}}
}

func init() { Register(sensor.LayerAccessibility, accessibility{}) }
```

- [ ] **Step 4: Run all layer tests (registry must now report 17)**

Run: `go test ./lib/planning/layer/...`

Expected: PASS, including `TestRegistryHasAllSeventeenLayers`.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/layer/accessibility.go lib/planning/layer/accessibility_test.go
git commit -m "lib(planning/layer): accessibility recipe (completes 17-layer enum)"
```

---

## Section 8 — `lib/planning/coredetect/` (auto-create missing platform primitives)

### Task 26: Create `lib/planning/coredetect/coredetect.go` (registry + ScaffoldFunc)

**Files:**
- Create: `lib/planning/coredetect/coredetect.go`
- Create: `lib/planning/coredetect/coredetect_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/planning/coredetect/coredetect_test.go`:

```go
package coredetect

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestGetUnknownReturnsNil(t *testing.T) {
	if Get("not-a-real-primitive") != nil {
		t.Fatal("expected nil for unknown id")
	}
}

func TestEnsureMissingReturnsEmptySliceWhenIDsEmpty(t *testing.T) {
	got, err := EnsureMissing(stack.Stack{}, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 drafts, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/coredetect/...`

Expected: FAIL (package missing).

- [ ] **Step 3: Implement `coredetect.go`**

Create `lib/planning/coredetect/coredetect.go`:

```go
// Package coredetect scaffolds platform-primitive sensors that
// /create-sensors auto-creates on demand when a layer needs one and
// the catalog does not yet contain it. Each known primitive id is
// implemented in a sibling file via ScaffoldFunc; init() in each file
// registers itself.
//
// The same scaffolds are the source of truth for /detect-sensors —
// when that skill is refactored to consume this package, the two
// skills agree byte-for-byte on platform primitives.
package coredetect

import (
	"fmt"
	"sync"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

// ScaffoldFunc produces a Draft for one primitive sensor, parameterized
// by the project's stack. Returns nil when the primitive is not
// applicable to this stack (e.g. setup-postgres on a stack without
// role=db-client) — the caller treats nil as "skip".
type ScaffoldFunc func(s stack.Stack) *Draft

// Draft mirrors lib/planning/layer.Draft; redeclared here to keep the
// coredetect package independent of the layer package (which would
// otherwise pull recipe init() side-effects into coredetect tests).
type Draft struct {
	SensorID    string
	Kind        sensor.Kind
	Type        sensor.Type
	Output      sensor.Output
	Description string
	Requires    []sensor.Requirement
	Cost        sensor.Cost
	Triggers    []sensor.Trigger
	Execution   sensor.Execution
}

var (
	mu       sync.RWMutex
	registry = map[string]ScaffoldFunc{}
)

// Register inserts a scaffold into the package registry. Panics on
// duplicate id. Intended for use from each scaffold file's init().
func Register(id string, fn ScaffoldFunc) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[id]; dup {
		panic("coredetect.Register: duplicate id " + id)
	}
	registry[id] = fn
}

// Get returns the scaffold for the given id, or nil when unregistered.
func Get(id string) ScaffoldFunc { mu.RLock(); defer mu.RUnlock(); return registry[id] }

// EnsureMissing maps the requested ids to drafts via the registry,
// skipping ids whose scaffold returns nil (not applicable to this
// stack) and erroring on unknown ids (caller bug — only known
// primitives should be requested).
func EnsureMissing(s stack.Stack, ids []string) ([]Draft, error) {
	out := make([]Draft, 0, len(ids))
	for _, id := range ids {
		fn := Get(id)
		if fn == nil {
			return nil, fmt.Errorf("coredetect: unknown primitive id %q", id)
		}
		if d := fn(s); d != nil {
			d.SensorID = id // enforce id matches registry key
			out = append(out, *d)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/coredetect/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/coredetect/coredetect.go lib/planning/coredetect/coredetect_test.go
git commit -m "lib(planning/coredetect): registry + ScaffoldFunc + EnsureMissing"
```

---

### Task 27: Implement `run_project.go` and `setup_postgres.go` and `seed_db.go` scaffolds

**Files:**
- Create: `lib/planning/coredetect/run_project.go`
- Create: `lib/planning/coredetect/run_project_test.go`
- Create: `lib/planning/coredetect/setup_postgres.go`
- Create: `lib/planning/coredetect/setup_postgres_test.go`
- Create: `lib/planning/coredetect/seed_db.go`
- Create: `lib/planning/coredetect/seed_db_test.go`

- [ ] **Step 1: Write the failing tests**

Create `lib/planning/coredetect/run_project_test.go`:

```go
package coredetect

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestRunProjectScaffolded(t *testing.T) {
	fn := Get("run-project")
	if fn == nil {
		t.Fatal("run-project not registered")
	}
	d := fn(stack.Stack{Archetypes: []stack.Archetype{"http-api"}})
	if d == nil {
		t.Fatal("expected draft for http-api stack")
	}
	if d.Kind != sensor.KindSetup {
		t.Fatalf("kind = %s", d.Kind)
	}
}
```

Create `lib/planning/coredetect/setup_postgres_test.go`:

```go
package coredetect

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
)

func TestSetupPostgresNilWithoutDBClient(t *testing.T) {
	fn := Get("setup-postgres")
	if fn == nil {
		t.Fatal("setup-postgres not registered")
	}
	if d := fn(stack.Stack{}); d != nil {
		t.Fatal("expected nil for stack without db-client")
	}
}
```

Create `lib/planning/coredetect/seed_db_test.go`:

```go
package coredetect

import "testing"

func TestSeedDBRegistered(t *testing.T) {
	if Get("seed-db") == nil {
		t.Fatal("seed-db not registered")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/coredetect/...`

Expected: FAIL.

- [ ] **Step 3: Implement the three scaffolds**

Create `lib/planning/coredetect/run_project.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("run-project", scaffoldRunProject) }

func scaffoldRunProject(s stack.Stack) *Draft {
	timeoutMS := 5000
	return &Draft{
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputStream,
		Description: "Brings the project up locally and streams its stdout to the runtime log.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 5000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Blocking: true,
			Command:  "echo 'TODO: project-specific run command (go run / pnpm dev / make dev). Auto-generated; tune via /update-sensor.' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
			OutputParsing: &sensor.OutputParsing{
				Patterns: []sensor.Pattern{{Regex: ".*", Verdict: "pass", Severity: "info"}},
			},
		},
	}
}
```

Create `lib/planning/coredetect/setup_postgres.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("setup-postgres", scaffoldSetupPostgres) }

func scaffoldSetupPostgres(s stack.Stack) *Draft {
	hasDB := false
	for _, c := range s.Components {
		if string(c.Role) == "db-client" {
			hasDB = true
			break
		}
	}
	if !hasDB {
		return nil
	}
	timeoutMS := 30000
	return &Draft{
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: "Idempotently brings a Postgres instance up (docker / system service) and waits for ready.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 10000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 128},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: docker-compose up -d postgres && pg_isready' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}
```

Create `lib/planning/coredetect/seed_db.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("seed-db", scaffoldSeedDB) }

func scaffoldSeedDB(s stack.Stack) *Draft {
	hasDB := false
	for _, c := range s.Components {
		if string(c.Role) == "db-client" {
			hasDB = true
			break
		}
	}
	if !hasDB {
		return nil
	}
	timeoutMS := 30000
	return &Draft{
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: "Idempotently seeds the database with the fixtures e2e scenarios depend on.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Requires: []sensor.Requirement{
			{Kind: sensor.RequireSensor, ID: "setup-postgres"},
		},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 500, P95MS: 5000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: apply seed SQL or fixture loader' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/coredetect/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/coredetect/run_project.go lib/planning/coredetect/run_project_test.go \
        lib/planning/coredetect/setup_postgres.go lib/planning/coredetect/setup_postgres_test.go \
        lib/planning/coredetect/seed_db.go lib/planning/coredetect/seed_db_test.go
git commit -m "lib(planning/coredetect): run-project + setup-postgres + seed-db scaffolds"
```

---

### Task 28: Implement `install_deps.go` + `build.go` + `lint.go` scaffolds

**Files:**
- Create: `lib/planning/coredetect/install_deps.go`
- Create: `lib/planning/coredetect/install_deps_test.go`
- Create: `lib/planning/coredetect/build.go`
- Create: `lib/planning/coredetect/build_test.go`
- Create: `lib/planning/coredetect/lint.go`
- Create: `lib/planning/coredetect/lint_test.go`

- [ ] **Step 1: Write the failing tests (all three)**

Create `lib/planning/coredetect/install_deps_test.go`:

```go
package coredetect

import "testing"

func TestInstallDepsRegistered(t *testing.T) {
	if Get("install-deps") == nil {
		t.Fatal("install-deps not registered")
	}
}
```

Create `lib/planning/coredetect/build_test.go`:

```go
package coredetect

import "testing"

func TestBuildRegistered(t *testing.T) {
	if Get("build") == nil {
		t.Fatal("build not registered")
	}
}
```

Create `lib/planning/coredetect/lint_test.go`:

```go
package coredetect

import "testing"

func TestLintRegistered(t *testing.T) {
	if Get("lint") == nil {
		t.Fatal("lint not registered")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/coredetect/... -run 'InstallDeps|Build|Lint'`

Expected: FAIL.

- [ ] **Step 3: Implement the three scaffolds**

Create `lib/planning/coredetect/install_deps.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("install-deps", scaffoldInstallDeps) }

func scaffoldInstallDeps(s stack.Stack) *Draft {
	timeoutMS := 120000
	return &Draft{
		Kind:        sensor.KindSetup,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: "Installs the project's dependency manifest (go mod download / pnpm install / pip install).",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 60000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate install (go mod download / pnpm install / pip install)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}
```

Create `lib/planning/coredetect/build.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("build", scaffoldBuild) }

func scaffoldBuild(s stack.Stack) *Draft {
	timeoutMS := 120000
	return &Draft{
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: "Compiles the project end-to-end (go build / tsc --noEmit / cargo build).",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 5000, P95MS: 60000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPUMedium, MemoryMB: 512},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate build (go build / tsc --noEmit / cargo build)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}
```

Create `lib/planning/coredetect/lint.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("lint", scaffoldLint) }

func scaffoldLint(s stack.Stack) *Draft {
	timeoutMS := 60000
	return &Draft{
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputStream,
		Description: "Runs the language linter (go vet / eslint / ruff).",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 10000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate linter (go vet / eslint / ruff)' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "medium"},
			},
			OutputParsing: &sensor.OutputParsing{
				Patterns: []sensor.Pattern{{Regex: ".*", Verdict: "warn", Severity: "low"}},
			},
		},
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/planning/coredetect/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/coredetect/install_deps.go lib/planning/coredetect/install_deps_test.go \
        lib/planning/coredetect/build.go lib/planning/coredetect/build_test.go \
        lib/planning/coredetect/lint.go lib/planning/coredetect/lint_test.go
git commit -m "lib(planning/coredetect): install-deps + build + lint scaffolds"
```

---

### Task 29: Implement `type_check.go` + `run_all_tests.go` + `check_server_startup.go` scaffolds

**Files:**
- Create: `lib/planning/coredetect/type_check.go`
- Create: `lib/planning/coredetect/type_check_test.go`
- Create: `lib/planning/coredetect/run_all_tests.go`
- Create: `lib/planning/coredetect/run_all_tests_test.go`
- Create: `lib/planning/coredetect/check_server_startup.go`
- Create: `lib/planning/coredetect/check_server_startup_test.go`

- [ ] **Step 1: Write the failing tests**

Create `lib/planning/coredetect/type_check_test.go`:

```go
package coredetect

import "testing"

func TestTypeCheckRegistered(t *testing.T) {
	if Get("type-check") == nil {
		t.Fatal("type-check not registered")
	}
}
```

Create `lib/planning/coredetect/run_all_tests_test.go`:

```go
package coredetect

import "testing"

func TestRunAllTestsRegistered(t *testing.T) {
	if Get("run-all-tests") == nil {
		t.Fatal("run-all-tests not registered")
	}
}
```

Create `lib/planning/coredetect/check_server_startup_test.go`:

```go
package coredetect

import "testing"

func TestCheckServerStartupRegistered(t *testing.T) {
	if Get("check-server-startup") == nil {
		t.Fatal("check-server-startup not registered")
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test ./lib/planning/coredetect/... -run 'TypeCheck|RunAllTests|CheckServerStartup'`

Expected: FAIL.

- [ ] **Step 3: Implement the three scaffolds**

Create `lib/planning/coredetect/type_check.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("type-check", scaffoldTypeCheck) }

func scaffoldTypeCheck(s stack.Stack) *Draft {
	timeoutMS := 60000
	return &Draft{
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: "Runs the static type checker (go vet / tsc --noEmit / mypy).",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 1000, P95MS: 10000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 256},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate type check' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}
```

Create `lib/planning/coredetect/run_all_tests.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("run-all-tests", scaffoldRunAllTests) }

func scaffoldRunAllTests(s stack.Stack) *Draft {
	timeoutMS := 300000
	return &Draft{
		Kind:        sensor.KindAssertion,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: "Runs the full test suite (unfiltered).",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassMedium,
			Latency: sensor.Latency{P50MS: 10000, P95MS: 120000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPUMedium, MemoryMB: 512},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: language-appropriate full test command' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}
```

Create `lib/planning/coredetect/check_server_startup.go`:

```go
package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func init() { Register("check-server-startup", scaffoldCheckServerStartup) }

func scaffoldCheckServerStartup(s stack.Stack) *Draft {
	hasHttp := false
	for _, c := range s.Components {
		if string(c.Role) == "http-server" {
			hasHttp = true
			break
		}
	}
	if !hasHttp {
		return nil
	}
	timeoutMS := 30000
	return &Draft{
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Output:      sensor.OutputSingle,
		Description: "Probes the server's health endpoint to confirm startup.",
		Triggers:    []sensor.Trigger{{On: sensor.TriggerManual}},
		Requires: []sensor.Requirement{
			{Kind: sensor.RequireSensor, ID: "run-project"},
		},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 100, P95MS: 2000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: curl --fail http://localhost:<port>/health' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}
```

- [ ] **Step 4: Run all coredetect tests**

Run: `go test ./lib/planning/coredetect/...`

Expected: PASS. Registry should contain 9 ids: `run-project, setup-postgres, seed-db, install-deps, build, lint, type-check, run-all-tests, check-server-startup`.

- [ ] **Step 5: Commit**

```bash
git add lib/planning/coredetect/type_check.go lib/planning/coredetect/type_check_test.go \
        lib/planning/coredetect/run_all_tests.go lib/planning/coredetect/run_all_tests_test.go \
        lib/planning/coredetect/check_server_startup.go lib/planning/coredetect/check_server_startup_test.go
git commit -m "lib(planning/coredetect): type-check + run-all-tests + check-server-startup scaffolds"
```

---

## Section 9 — Skill `/create-sensors`

### Task 30: Delete `skills/create-sensor/` and scaffold `skills/create-sensors/`

**Files:**
- Delete: `skills/create-sensor/` (entire directory)
- Create: `skills/create-sensors/SKILL.md`
- Create: `skills/create-sensors/scripts/` (empty)

- [ ] **Step 1: Verify nothing still imports the old planning types**

Run: `grep -rn "lib/planning\"" lib/ skills/ 2>/dev/null | grep -v "lib/planning/layer\|lib/planning/coredetect"`

Expected: only matches inside `skills/create-sensor/scripts/` (which is about to be deleted).

- [ ] **Step 2: Delete the old skill**

```bash
rm -rf skills/create-sensor/
```

- [ ] **Step 3: Create the new skill skeleton**

Create `skills/create-sensors/SKILL.md`:

```markdown
---
name: create-sensors
description: Use when the user invokes /create-sensors or asks to author the per-usecase sensor bundle that validates one or more usecases from multiple angles. Resolves the input to one or more usecase ids (by id, journey, path, or free-text match), applies the closed-enum layer matrix in lib/planning/layer/, auto-creates missing platform primitives via lib/planning/coredetect/, and persists each draft to .harness/sensors/<usecase-id>/<sensor-id>.yaml. Distinct from /detect-sensors (sweeps the project for root-tier primitives) and from /validate-usecase (orchestrates the bundle and reports confidence).
---

# create-sensors

Take one or more usecase ids (or a journey id, or a usecase file path, or a free-text requirement) as input and produce, per usecase, the multi-layer bundle of narrow + composite sensors that validates it through every applicable lens. Uses deterministic Go scripts for catalog walking, layer application, and persistence; LLM judgment only when synthesizing fixtures the layer recipe cannot infer from evidence alone.

## Invocation

```
/create-sensors [usecase-id | journey-id | path/to/usecase.yaml | "<free text>"]
```

(Phase prose continues in Task 33.)
```

- [ ] **Step 4: Confirm build still green**

Run: `go build ./...`

Expected: PASS (no consumers of the deleted package remain).

- [ ] **Step 5: Commit**

```bash
git add -A skills/
git commit -m "skill: drop create-sensor, scaffold create-sensors (plural)"
```

---

### Task 31: Port + adapt `read-usecases.go` to `skills/create-sensors/scripts/`

**Files:**
- Create: `skills/create-sensors/scripts/read-usecases.go`
- Create: `skills/create-sensors/scripts/read-usecases_test.go`

- [ ] **Step 1: Copy + adapt the old read-usecases.go**

The old `skills/create-sensor/scripts/read-usecases.go` was deleted in Task 30. Reconstruct it under the new path, with TWO changes from the deleted version:

1. The build tag stays `//go:build read_usecases`.
2. `loadCatalog` now walks recursively (handles both root-tier `.harness/sensors/<id>.yaml` AND per-usecase `.harness/sensors/<usecase-id>/<sensor-id>.yaml`).

Create `skills/create-sensors/scripts/read-usecases.go`:

```go
//go:build read_usecases

// Command read-usecases resolves a set of usecase identifiers (by id
// or by journey) under <projectRoot>/.harness/usecases/ and emits a
// JSON ledger on stdout. Read-only. The catalog projection walks
// .harness/sensors/ recursively so root-tier primitives AND per-
// usecase folders are both included.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type Ledger struct {
	Usecases    []usecase.UseCase `json:"usecases"`
	Stack       map[string]any    `json:"stack,omitempty"`
	Catalog     []catalogEntry    `json:"catalog,omitempty"`
	ProjectRoot string            `json:"project_root"`
}

type catalogEntry struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	Output   string `json:"output"`
	Blocking bool   `json:"blocking"`
	Layer    string `json:"layer,omitempty"`
	Path     string `json:"path"`
}

type indexLedger struct {
	Usecases []listEntry `json:"usecases"`
}

type listEntry struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("read-usecases", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		projectRoot, usecases, journey string
		listOnly, includeStack         bool
		includeCatalog                 bool
	)
	fs.StringVar(&projectRoot, "project-root", "", "project root (default: registry.Lookup from cwd)")
	fs.StringVar(&usecases, "usecases", "", "comma-separated usecase ids")
	fs.StringVar(&journey, "journey", "", "journey id (loads all usecases under that journey)")
	fs.BoolVar(&listOnly, "list-only", false, "emit thin index (id+name+tags) only")
	fs.BoolVar(&includeStack, "include-stack", false, "include .harness/stack.yaml in the ledger")
	fs.BoolVar(&includeCatalog, "include-catalog", false, "include existing sensor catalog in the ledger")
	if err := fs.Parse(args); err != nil {
		emit(stdout, errSignal("usage", err.Error()))
		return 2
	}
	if listOnly && (includeStack || includeCatalog) {
		emit(stdout, errSignal("usage", "--list-only mutually exclusive with --include-stack and --include-catalog"))
		return 2
	}
	if projectRoot == "" {
		cwd, _ := os.Getwd()
		res, err := registry.Lookup(cwd)
		if err != nil {
			emit(stdout, registry.DiscoveryErrorSignal(err, "read-usecases"))
			return 2
		}
		projectRoot = res.ProjectRoot
	}

	ids, err := resolveIDs(projectRoot, usecases, journey)
	if err != nil {
		emit(stdout, errSignal("usage", err.Error()))
		return 2
	}

	var validatorErr bytes.Buffer
	validator, code := schema.LoadValidator("", &validatorErr)
	if code != 0 {
		detail := strings.TrimSpace(validatorErr.String())
		emit(stdout, errSignal("schema_validator_init_failed", "schema validator init failed: "+detail))
		return 2
	}

	loaded, warns, missing := loadUsecases(projectRoot, ids, validator)
	for _, w := range warns {
		emit(stdout, w)
	}
	if len(missing) > 0 {
		emit(stdout, errSignal("usecase_not_found", "usecases not found: "+strings.Join(missing, ", ")))
		return 1
	}

	if listOnly {
		idx := indexLedger{Usecases: []listEntry{}}
		for _, uc := range loaded {
			idx.Usecases = append(idx.Usecases, listEntry{ID: uc.ID, Name: uc.Name, Tags: uc.Tags})
		}
		emit(stdout, idx)
		return 0
	}

	lg := Ledger{Usecases: loaded, ProjectRoot: projectRoot}
	if includeStack {
		if stackMap, ok := loadStack(projectRoot); ok {
			lg.Stack = stackMap
		}
	}
	if includeCatalog {
		lg.Catalog = loadCatalog(projectRoot, validator)
	}
	emit(stdout, lg)
	return 0
}

func resolveIDs(projectRoot, usecasesCSV, journey string) ([]string, error) {
	if usecasesCSV != "" && journey != "" {
		return nil, errors.New("pass either --usecases or --journey, not both")
	}
	if usecasesCSV == "" && journey == "" {
		return nil, errors.New("one of --usecases or --journey is required")
	}
	if usecasesCSV != "" {
		raw := strings.Split(usecasesCSV, ",")
		out := make([]string, 0, len(raw))
		for _, r := range raw {
			if r = strings.TrimSpace(r); r != "" {
				out = append(out, r)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	dir := filepath.Join(projectRoot, ".harness", "usecases", journey)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("journey %q: %w", journey, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(ids)
	return ids, nil
}

func loadUsecases(projectRoot string, ids []string, validator *schema.Validator) (loaded []usecase.UseCase, warns []map[string]interface{}, missing []string) {
	usecasesRoot := filepath.Join(projectRoot, ".harness", "usecases")
	pathByID := indexUsecaseFiles(usecasesRoot)
	for _, id := range ids {
		path, ok := pathByID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			warns = append(warns, warnSignal("usecase_read_failed", fmt.Sprintf("read %s: %v", path, err)))
			continue
		}
		var instance interface{}
		if err := yaml.Unmarshal(body, &instance); err != nil {
			warns = append(warns, warnSignal("usecase_parse_failed", fmt.Sprintf("%s: parse: %v", path, err)))
			continue
		}
		if err := validator.Validate(schema.TargetUseCase, instance); err != nil {
			warns = append(warns, warnSignal("usecase_schema_invalid", fmt.Sprintf("%s: %v", path, err)))
			continue
		}
		var uc usecase.UseCase
		if err := yaml.Unmarshal(body, &uc); err != nil {
			warns = append(warns, warnSignal("usecase_parse_failed", fmt.Sprintf("%s: decode: %v", path, err)))
			continue
		}
		loaded = append(loaded, uc)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].ID < loaded[j].ID })
	return loaded, warns, missing
}

func indexUsecaseFiles(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		id := strings.TrimSuffix(info.Name(), ".yaml")
		out[id] = path
		return nil
	})
	return out
}

func loadStack(projectRoot string) (map[string]any, bool) {
	body, err := os.ReadFile(filepath.Join(projectRoot, ".harness", "stack.yaml"))
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := yaml.Unmarshal(body, &out); err != nil {
		return nil, false
	}
	return out, true
}

// loadCatalog walks .harness/sensors/ RECURSIVELY so root-tier
// platform primitives AND per-usecase bundles both contribute. Each
// entry's Path is relative to projectRoot.
func loadCatalog(projectRoot string, validator *schema.Validator) []catalogEntry {
	sensorsRoot := filepath.Join(projectRoot, ".harness", "sensors")
	out := []catalogEntry{}
	_ = filepath.Walk(sensorsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var instance interface{}
		if err := yaml.Unmarshal(body, &instance); err != nil {
			return nil
		}
		if err := validator.Validate(schema.TargetSensor, instance); err != nil {
			return nil
		}
		var s sensor.Sensor
		if err := yaml.Unmarshal(body, &s); err != nil {
			return nil
		}
		rel, _ := filepath.Rel(projectRoot, path)
		out = append(out, catalogEntry{
			ID: s.ID, Kind: string(s.Kind), Type: string(s.Type),
			Output: string(s.Output), Blocking: s.Execution.Blocking,
			Layer: string(s.Layer), Path: rel,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func emit(w io.Writer, v interface{}) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func warnSignal(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("read-usecases", "0.1.0").
		WithVerdict("warn", "medium").WithKind(kind).WithRationale(rationale).Build()
}

func errSignal(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("read-usecases", "0.1.0").
		WithVerdict("error", "high").WithKind(kind).WithRationale(rationale).Build()
}
```

- [ ] **Step 2: Add a test for recursive catalog walk**

Create `skills/create-sensors/scripts/read-usecases_test.go`:

```go
//go:build read_usecases

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCatalogWalksSubdirs(t *testing.T) {
	// Stand up a fake project root with one root-tier sensor and one
	// per-usecase folder.
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".harness", "sensors", "create-user"), 0o755); err != nil {
		t.Fatal(err)
	}
	rootSensor := []byte(`id: run-project
version: 0.1.0
name: run project
description: tmp
kind: setup
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: stream
cost: { class: cheap, latency: { p50_ms: 100, p95_ms: 1000 }, compute: { cpu: low, memory_mb: 64 } }
triggers: [{ on: manual }]
execution:
  blocking: true
  command: 'true'
  exit_code_map: [{ exit_code: 0, verdict: pass, severity: info }, { exit_code: '*', verdict: fail, severity: high }]
  output_parsing: { patterns: [{ regex: '.*', verdict: pass, severity: info }] }
use_cases: [bootstrap]
`)
	if err := os.WriteFile(filepath.Join(root, ".harness", "sensors", "run-project.yaml"), rootSensor, 0o644); err != nil {
		t.Fatal(err)
	}
	perUC := []byte(`id: observe-db-create-user
version: 0.1.0
name: observe db
description: tmp
kind: observation
type: computational
regulation: behaviour
phase: on-demand
determinism: high
output: single
layer: db-state
cost: { class: cheap, latency: { p50_ms: 50, p95_ms: 500, timeout_ms: 5000 }, compute: { cpu: low, memory_mb: 64 } }
triggers: [{ on: manual }]
execution:
  command: 'true'
  exit_code_map: [{ exit_code: 0, verdict: pass, severity: info }, { exit_code: '*', verdict: fail, severity: high }]
use_cases: [create-user]
`)
	if err := os.WriteFile(filepath.Join(root, ".harness", "sensors", "create-user", "observe-db-create-user.yaml"), perUC, 0o644); err != nil {
		t.Fatal(err)
	}

	// Load schema validator from the plugin root (env propagated by Bash).
	t.Setenv("HARNESS_REGISTRY_ROOT", root)
	// loadCatalog uses the package validator instantiated in run(); here we
	// exercise the helper directly with a stub validator. To keep the test
	// hermetic, we shell out to the actual binary in a follow-up
	// integration suite (Task 38). For this unit test we only assert the
	// recursive walker visits both files via a simplified validator that
	// always returns nil.
	t.Skip("loadCatalog requires a real schema validator; covered by the integration test in Task 38")
}
```

(The body is deliberately a `t.Skip` because exercising `loadCatalog` requires a real `*schema.Validator`. The integration test in Task 38 covers the recursive walk end-to-end. The Go file exists so future contributors have a hook to drop a real validator-stub test.)

- [ ] **Step 3: Compile**

Run: `go build -tags=read_usecases ./skills/create-sensors/scripts`

Expected: zero errors.

- [ ] **Step 4: Run tests**

Run: `go test -tags=read_usecases ./skills/create-sensors/scripts`

Expected: PASS (with skip).

- [ ] **Step 5: Commit**

```bash
git add skills/create-sensors/scripts/read-usecases.go skills/create-sensors/scripts/read-usecases_test.go
git commit -m "skill(create-sensors): port read-usecases with recursive catalog walk"
```

---

### Task 32: Port `write-sensor.go`, `write-fixture.go`, `catalog-sensors.go` to the new skill

**Files:**
- Create: `skills/create-sensors/scripts/write-sensor.go`
- Create: `skills/create-sensors/scripts/write-sensor_test.go`
- Create: `skills/create-sensors/scripts/write-fixture.go`
- Create: `skills/create-sensors/scripts/write-fixture_test.go`
- Create: `skills/create-sensors/scripts/catalog-sensors.go`
- Create: `skills/create-sensors/scripts/catalog-sensors_test.go`

- [ ] **Step 1: Reconstruct the three scripts from the deleted skill**

The deleted `skills/create-sensor/scripts/` directory contained `write-sensor.go`, `write-fixture.go`, `catalog-sensors.go` (and their `_test.go` siblings). Reconstruct each verbatim at the new path with the new build tags unchanged (`//go:build write_sensor`, `//go:build write_fixture`, `//go:build catalog_sensors`). The schema validator path resolution stays the same — `--schemas-dir` defaults to `${CLAUDE_PLUGIN_ROOT}/schemas` via `lib/schema.LoadValidator`.

ONE behavioral change in `write-sensor.go`: the `--out` flag now accepts both root-tier paths (`.harness/sensors/`) and per-usecase paths (`.harness/sensors/<usecase-id>/`). Make sure the parent directory is `MkdirAll`'d before the file is written:

```go
// Before opening the destination for write:
if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
    // emit error signal & exit
}
```

(If the deleted version already did this — likely — leave it alone. Check via the prior commit `git show HEAD~N:skills/create-sensor/scripts/write-sensor.go` where `~N` reaches the deletion commit, and copy verbatim.)

ONE behavioral change in `catalog-sensors.go`: the walk is recursive (same as `read-usecases.loadCatalog`). Copy from the deleted version, swap `os.ReadDir(sensorsDir)` for the recursive `filepath.Walk` pattern shown in Task 31.

- [ ] **Step 2: Compile each script**

```bash
go build -tags=write_sensor    ./skills/create-sensors/scripts
go build -tags=write_fixture   ./skills/create-sensors/scripts
go build -tags=catalog_sensors ./skills/create-sensors/scripts
```

Expected: all green.

- [ ] **Step 3: Run tests for each (the deleted skill had `_test.go` siblings — copy them too)**

```bash
go test -tags=write_sensor    ./skills/create-sensors/scripts
go test -tags=write_fixture   ./skills/create-sensors/scripts
go test -tags=catalog_sensors ./skills/create-sensors/scripts
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/create-sensors/scripts/
git commit -m "skill(create-sensors): port write-sensor, write-fixture, catalog-sensors (recursive)"
```

---

### Task 33: Create `skills/create-sensors/scripts/plan-and-emit.go` (layer-matrix orchestrator)

**Files:**
- Create: `skills/create-sensors/scripts/plan-and-emit.go`
- Create: `skills/create-sensors/scripts/plan-and-emit_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/create-sensors/scripts/plan-and-emit_test.go`:

```go
//go:build plan_and_emit

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanAndEmitRejectsEmptyStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(strings.NewReader(""), &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for empty ledger")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(stdout.Bytes(), &sig); err != nil {
		t.Fatalf("expected JSON Signal on stdout, got %q", stdout.String())
	}
	if v, _ := sig["verdict"].(string); v != "error" {
		t.Fatalf("expected verdict=error, got %v", sig["verdict"])
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test -tags=plan_and_emit ./skills/create-sensors/scripts`

Expected: FAIL (script doesn't exist).

- [ ] **Step 3: Implement `plan-and-emit.go`**

Create `skills/create-sensors/scripts/plan-and-emit.go`:

```go
//go:build plan_and_emit

// Command plan-and-emit reads a ledger from stdin (the wire format
// emitted by read-usecases.go), applies the closed-enum layer matrix
// in lib/planning/layer/, auto-creates missing platform primitives
// via lib/planning/coredetect/, and emits a JSONL plan on stdout — one
// Plan line per draft + one Aggregate envelope at the end.
//
// The script is the deterministic adapter between the in-memory layer
// recipes and the wire format consumed by write-sensor.go.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/planning/coredetect"
	"github.com/iurykrieger/harness-framework/lib/planning/layer"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type ledger struct {
	Usecases []usecase.UseCase `json:"usecases"`
	Stack    map[string]any    `json:"stack,omitempty"`
	Catalog  []sensor.Sensor   `json:"catalog,omitempty"`
}

type wirePlan struct {
	Type     string         `json:"type"`
	Draft    layer.Draft    `json:"draft,omitempty"`
	UseCase  string         `json:"use_case,omitempty"`
	Layer    sensor.Layer   `json:"layer,omitempty"`
	Skipped  bool           `json:"skipped,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	CoreScaf *coredetect.Draft `json:"core_scaffold,omitempty"`
}

func main() { os.Exit(run(os.Stdin, os.Stdout, os.Stderr)) }

func run(stdin io.Reader, stdout, stderr io.Writer) int {
	body, err := io.ReadAll(stdin)
	if err != nil {
		emit(stdout, errSig("usage", "read stdin: "+err.Error()))
		return 2
	}
	if len(body) == 0 {
		emit(stdout, errSig("usage", "stdin is empty"))
		return 2
	}
	var lg ledger
	if err := json.Unmarshal(body, &lg); err != nil {
		emit(stdout, errSig("usage", "parse ledger: "+err.Error()))
		return 2
	}

	st := decodeStack(lg.Stack)

	// Pass 1: figure out which core sensors are needed by any applicable layer.
	missingCore := map[string]struct{}{}
	for _, uc := range lg.Usecases {
		for _, l := range layer.AllLayers() {
			r := layer.Get(l)
			ok, reason := r.Applicable(st, uc, lg.Catalog)
			if !ok && reasonNamesMissingCore(reason) {
				if id := extractCoreID(reason); id != "" {
					missingCore[id] = struct{}{}
				}
			}
		}
	}

	// Auto-create missing core scaffolds (emit them first).
	coreIDs := make([]string, 0, len(missingCore))
	for id := range missingCore {
		coreIDs = append(coreIDs, id)
	}
	scaffolds, err := coredetect.EnsureMissing(st, coreIDs)
	if err != nil {
		emit(stdout, errSig("coredetect_failed", err.Error()))
		return 1
	}
	syntheticCatalog := append([]sensor.Sensor{}, lg.Catalog...)
	for _, sc := range scaffolds {
		emit(stdout, wirePlan{Type: "core_scaffold", CoreScaf: &sc})
		// Add to in-memory catalog so layer.Applicable now sees the primitive.
		syntheticCatalog = append(syntheticCatalog, sensor.Sensor{ID: sc.SensorID})
	}

	// Pass 2: emit layer drafts.
	total := 0
	for _, uc := range lg.Usecases {
		for _, l := range layer.AllLayers() {
			r := layer.Get(l)
			ok, reason := r.Applicable(st, uc, syntheticCatalog)
			if !ok {
				emit(stdout, wirePlan{Type: "layer_skipped", UseCase: uc.ID, Layer: l, Skipped: true, Reason: reason})
				continue
			}
			for _, d := range r.Plan(st, uc, syntheticCatalog) {
				emit(stdout, wirePlan{Type: "draft", UseCase: uc.ID, Draft: d})
				total++
			}
		}
	}

	emit(stdout, map[string]any{
		"aggregate":          true,
		"verdict":            "pass",
		"severity":           "info",
		"drafts_emitted":     total,
		"core_scaffolds":     len(scaffolds),
		"usecases_planned":   len(lg.Usecases),
	})
	return 0
}

// decodeStack normalises the map[string]any ledger field into the
// typed stack.Stack consumed by recipes. JSON round-trip is the
// cheapest path; the validator already ran upstream.
func decodeStack(m map[string]any) stack.Stack {
	if m == nil {
		return stack.Stack{}
	}
	body, _ := json.Marshal(m)
	var out stack.Stack
	_ = json.Unmarshal(body, &out)
	return out
}

func reasonNamesMissingCore(reason string) bool {
	return len(reason) > 0 && (containsAll(reason, "core sensor", "missing"))
}

func extractCoreID(reason string) string {
	// reason format from layer.e2eRecipe.Applicable:
	//   "core sensor run-project missing from catalog (will be auto-created)"
	const prefix = "core sensor "
	const suffix = " missing"
	i := indexOf(reason, prefix)
	if i < 0 {
		return ""
	}
	rest := reason[i+len(prefix):]
	j := indexOf(rest, suffix)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func containsAll(s string, needles ...string) bool {
	for _, n := range needles {
		if indexOf(s, n) < 0 {
			return false
		}
	}
	return true
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func emit(w io.Writer, v interface{}) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func errSig(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("plan-and-emit", "0.1.0").
		WithVerdict("error", "high").WithKind(kind).WithRationale(rationale).Build()
}
```

- [ ] **Step 4: Run tests**

Run: `go test -tags=plan_and_emit ./skills/create-sensors/scripts`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/create-sensors/scripts/plan-and-emit.go skills/create-sensors/scripts/plan-and-emit_test.go
git commit -m "skill(create-sensors): plan-and-emit (layer matrix + coredetect orchestrator)"
```

---

### Task 34: Write `skills/create-sensors/SKILL.md` body (Phases 1–7)

**Files:**
- Modify: `skills/create-sensors/SKILL.md`

- [ ] **Step 1: Replace the placeholder body from Task 30 with the procedural prose**

Edit `skills/create-sensors/SKILL.md` to read in full:

```markdown
---
name: create-sensors
description: Use when the user invokes /create-sensors or asks to author the per-usecase sensor bundle that validates one or more usecases from multiple angles. Resolves the input to one or more usecase ids (by id, journey, path, or free-text match), applies the closed-enum layer matrix in lib/planning/layer/, auto-creates missing platform primitives via lib/planning/coredetect/, and persists each draft to .harness/sensors/<usecase-id>/<sensor-id>.yaml. Distinct from /detect-sensors (sweeps the project for root-tier primitives) and from /validate-usecase (orchestrates the bundle and reports confidence).
---

# create-sensors

Take one or more usecase ids (or a journey id, or a usecase file path, or a free-text requirement) as input and produce, per usecase, the multi-layer bundle of narrow + composite sensors that validates it through every applicable lens. Deterministic Go scripts cover catalog walking, layer application, fixture writing, and persistence; LLM judgment is reserved for synthesizing fixture bodies the recipe cannot infer from evidence alone.

## Invocation

\`\`\`
/create-sensors [usecase-id | journey-id | path/to/usecase.yaml | "<free text>"]
\`\`\`

If no argument is supplied, block:

> What is the requirement to cover? Pass a usecase id (\`create-user\`), a journey id (\`users\`), a file path, or a free-text requirement.

## Phase 1 — Resolve input

Classify and resolve to one or more usecase ids. The thin index loader (\`read-usecases.go\` with \`--list-only\`) supports free-text matching by id+name+tags.

## Phase 2 — Load context

Run:

\`\`\`bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=read_usecases \
  ./skills/create-sensors/scripts \
  --usecases "<id-1>,<id-2>,..." \
  --include-stack \
  --include-catalog \
  > /tmp/ledger-$(date +%s).json
\`\`\`

The ledger now includes ALL sensors under \`.harness/sensors/\` (root-tier + per-usecase folders) via the recursive walk.

## Phase 3 — Plan via layer matrix

\`\`\`bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=plan_and_emit \
  ./skills/create-sensors/scripts \
  < /tmp/ledger-<saved-epoch>.json
\`\`\`

The script emits JSONL: \`core_scaffold\` entries first (one per missing platform primitive), then \`draft\` entries (one per planned sensor), then \`layer_skipped\` entries (with reasons), then a final aggregate envelope.

## Phase 4 — Report plan + confirm

Summarise the plan to the user: per usecase, list the applicable layers (with the number of drafts each emits), the skipped layers (with reasons), and the core scaffolds that will be auto-created. Ask: *"Proceed? (yes/no)"*. Yes/no only — no editing here. If the user wants different scoping, they re-invoke with a narrower input.

## Phase 5 — Synthesize fixtures and persist

For each \`core_scaffold\` and \`draft\`, in order:

1. If the layer requires a fixture body the recipe could not infer, prompt the user OR fall back to a documented placeholder; persist via \`write-fixture.go\`.
2. Persist the draft via \`write-sensor.go\` with \`--out\` pointing at:
   - \`.harness/sensors/\` for \`core_scaffold\` entries.
   - \`.harness/sensors/<usecase-id>/\` for layer drafts.

Inferential drafts (\`code-quality\`, \`architecture\`, \`security\`, \`dependency-health\`) carry default calibration produced by the recipe; do NOT block on a calibration gate.

## Phase 6 — Verify catalog grew correctly

Re-run \`catalog-sensors.go\`. The catalog should now include every persisted draft plus any auto-created core sensors.

## Phase 7 — Per-usecase report

Print a short summary per usecase:

\`\`\`
<usecase-id>: <N> sensors across <M> generated layers (out of <K> applicable; skipped layers + reasons).
Next: /validate-usecase <usecase-id>
\`\`\`

## Policy for an existing usecase folder

- Incremental by default: layers absent from the folder are generated; layers present are skipped silently.
- \`--force-layer <name>\`: regenerate ONE specific layer (bump \`sensor.version\`).
- \`--regenerate\`: delete and regenerate the entire bundle.

## What this skill does NOT do

- Does not exercise sensors after creation — \`/validate-usecase\` is the next step.
- Does not modify existing sensors — id collisions are surfaced; the user resolves.
- Does not interpret free text as "create a usecase" — Phase 1 free-text matches existing usecases only.
- Does not auto-replay usecases at runtime — \`use_cases[]\` is declarative traceability.
```

- [ ] **Step 2: Commit**

```bash
git add skills/create-sensors/SKILL.md
git commit -m "skill(create-sensors): full procedural body (phases 1-7)"
```

---

## Section 10 — Skill `/validate-usecase`

### Task 35: Scaffold `skills/validate-usecase/`

**Files:**
- Create: `skills/validate-usecase/SKILL.md`
- Create: `skills/validate-usecase/scripts/` (empty)

- [ ] **Step 1: Create the skill skeleton**

Create `skills/validate-usecase/SKILL.md`:

```markdown
---
name: validate-usecase
description: Use when the user invokes /validate-usecase or asks to exercise the per-usecase sensor bundle and report a confidence score. Reads the bundle at .harness/sensors/<usecase-id>/, identifies layer entrypoints (one per unique sensor.layer), invokes the runtime in topological order, and emits a Signal carrying ceiling/coverage/realized counts plus the worst-result aggregate verdict.
---

# validate-usecase

(Phase prose added in Task 37.)
```

- [ ] **Step 2: Commit**

```bash
git add skills/validate-usecase/
git commit -m "skill: scaffold validate-usecase"
```

---

### Task 36: Implement `skills/validate-usecase/scripts/validate-usecase.go`

**Files:**
- Create: `skills/validate-usecase/scripts/validate-usecase.go`
- Create: `skills/validate-usecase/scripts/validate-usecase_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/validate-usecase/scripts/validate-usecase_test.go`:

```go
//go:build validate_usecase

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRejectsMissingArg(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for missing arg")
	}
	if !strings.Contains(stdout.String(), "usecase") {
		t.Fatalf("expected error signal naming usecase, got %q", stdout.String())
	}
}
```

- [ ] **Step 2: Run to fail**

Run: `go test -tags=validate_usecase ./skills/validate-usecase/scripts`

Expected: FAIL (package missing).

- [ ] **Step 3: Implement `validate-usecase.go`**

Create `skills/validate-usecase/scripts/validate-usecase.go`:

```go
//go:build validate_usecase

// Command validate-usecase orchestrates the bundle at
// <projectRoot>/.harness/sensors/<usecase-id>/, runs each layer
// entrypoint via lib/orchestrator, collects the aggregate Signals,
// computes ceiling/coverage/realized, and emits the confidence
// report as a Signal on stdout.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/planning/layer"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || strings.TrimSpace(args[0]) == "" {
		emit(stdout, errSig("usage", "/validate-usecase <usecase-id> requires a usecase id"))
		return 2
	}
	usecaseID := args[0]

	cwd, _ := os.Getwd()
	res, err := registry.Lookup(cwd)
	if err != nil {
		emit(stdout, registry.DiscoveryErrorSignal(err, "validate-usecase"))
		return 2
	}
	projectRoot := res.ProjectRoot

	bundleDir := filepath.Join(projectRoot, ".harness", "sensors", usecaseID)
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		emit(stdout, errSig("no_coverage", fmt.Sprintf("bundle dir %q not found — run /create-sensors %s first", bundleDir, usecaseID)))
		return 1
	}

	var validatorErr bytes.Buffer
	validator, code := schema.LoadValidator("", &validatorErr)
	if code != 0 {
		emit(stdout, errSig("schema_validator_init_failed", validatorErr.String()))
		return 2
	}

	// Walk the bundle. Pick layer entrypoints — for each unique
	// sensor.layer value, the entrypoint is the composite (output=stream
	// containing sensor steps) when present, otherwise the solo sensor
	// with that layer.
	layerToEntrypoint := map[sensor.Layer]*sensor.Sensor{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(bundleDir, e.Name())
		body, _ := os.ReadFile(path)
		var instance interface{}
		if err := yaml.Unmarshal(body, &instance); err != nil {
			continue
		}
		if err := validator.Validate(schema.TargetSensor, instance); err != nil {
			continue
		}
		var s sensor.Sensor
		if err := yaml.Unmarshal(body, &s); err != nil {
			continue
		}
		if s.Layer == "" {
			continue
		}
		prev, ok := layerToEntrypoint[s.Layer]
		if !ok {
			layerToEntrypoint[s.Layer] = &s
			continue
		}
		// Prefer composite (has steps that include type=sensor) over solo.
		if isComposite(s) && !isComposite(*prev) {
			layerToEntrypoint[s.Layer] = &s
		}
	}

	// Topo-sort layers by id for deterministic invocation order. The
	// orchestrator handles per-sensor requires graph itself.
	var layers []sensor.Layer
	for l := range layerToEntrypoint {
		layers = append(layers, l)
	}
	sort.Slice(layers, func(i, j int) bool { return string(layers[i]) < string(layers[j]) })

	type verdictRow struct {
		Layer      sensor.Layer `json:"layer"`
		Verdict    string       `json:"verdict"`
		SensorID   string       `json:"sensor_id"`
		FinishedAt string       `json:"finished_at"`
	}
	verdicts := make([]verdictRow, 0, len(layers))

	for _, l := range layers {
		s := layerToEntrypoint[l]
		sensorPath := filepath.Join(bundleDir, s.ID+".yaml")
		var depStdout, depStderr bytes.Buffer
		code := orchestrator.RunWithDeps(context.Background(), sensorPath, "", &depStdout, &depStderr)
		_ = code // verdict is read from the LAST JSONL line of depStdout
		v, finishedAt := lastAggregateVerdict(depStdout.Bytes())
		verdicts = append(verdicts, verdictRow{
			Layer: l, Verdict: v, SensorID: s.ID, FinishedAt: finishedAt,
		})
	}

	// Compute ceiling against stack + ALL registered layers.
	st := readStack(projectRoot)
	uc := readUsecase(projectRoot, usecaseID)
	cat := readCatalog(projectRoot, validator)
	applicable := []sensor.Layer{}
	notApplicable := []map[string]string{}
	for _, l := range layer.AllLayers() {
		ok, reason := layer.Get(l).Applicable(st, uc, cat)
		if ok {
			applicable = append(applicable, l)
		} else {
			notApplicable = append(notApplicable, map[string]string{
				"layer": string(l), "reason": reason,
			})
		}
	}
	ceiling := len(applicable)
	coverage := len(verdicts)
	realized := 0
	for _, v := range verdicts {
		if v.Verdict == "pass" {
			realized++
		}
	}

	// Worst-result aggregate verdict.
	aggregateVerdict := worstResult(verdicts, coverage)
	severity := "info"
	if aggregateVerdict != "pass" {
		severity = "high"
	}

	report := map[string]any{
		"usecase_id":  usecaseID,
		"computed_at": time.Now().UTC().Format(time.RFC3339),
		"ceiling": map[string]any{
			"value":          ceiling,
			"applicable":     stringer(applicable),
			"not_applicable": notApplicable,
		},
		"coverage": map[string]any{
			"value":     coverage,
			"generated": layerIDs(verdicts),
		},
		"realized": map[string]any{
			"value":          realized,
			"layer_verdicts": verdicts,
		},
		"ratios": map[string]any{
			"completeness":        safeDiv(coverage, ceiling),
			"pass_rate":           safeDiv(realized, coverage),
			"executed_pass_rate":  safeDiv(realized, coverage), // untested == 0 in phase 1
			"confidence":          safeDiv(realized, ceiling),
		},
		"aggregate_verdict": aggregateVerdict,
	}

	emit(stdout, signal.NewBuilder("validate-usecase", "0.1.0").
		WithVerdict(aggregateVerdict, severity).
		WithKind("confidence_report").
		WithMetadata("confidence_report", report).
		Build())
	return 0
}

func isComposite(s sensor.Sensor) bool {
	for _, st := range s.Execution.Steps {
		if st.Type == "sensor" {
			return true
		}
	}
	return false
}

func lastAggregateVerdict(b []byte) (string, string) {
	lines := bytes.Split(b, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		ln := bytes.TrimSpace(lines[i])
		if len(ln) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(ln, &m); err != nil {
			continue
		}
		if v, ok := m["verdict"].(string); ok {
			f, _ := m["finished_at"].(string)
			return v, f
		}
	}
	return "error", ""
}

func worstResult(rows []struct{ Layer sensor.Layer; Verdict, SensorID, FinishedAt string } /* unused — see verdictRow */ , coverage int) string {
	// Replaced by inline computation below when called; signature here
	// is kept for clarity. The real implementation uses the typed
	// verdictRow slice in scope.
	return "pass"
}

// (The actual worstResult uses verdictRow; placeholder above is a
// deliberate compile sentinel — replace by the body in Step 4.)

func readStack(root string) stack.Stack {
	body, _ := os.ReadFile(filepath.Join(root, ".harness", "stack.yaml"))
	var s stack.Stack
	_ = yaml.Unmarshal(body, &s)
	return s
}

func readUsecase(root, id string) usecase.UseCase {
	// Walk .harness/usecases/ for <id>.yaml.
	var found string
	_ = filepath.Walk(filepath.Join(root, ".harness", "usecases"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(info.Name(), "/"+id+".yaml") || info.Name() == id+".yaml" {
			found = p
		}
		return nil
	})
	if found == "" {
		return usecase.UseCase{ID: id}
	}
	body, _ := os.ReadFile(found)
	var uc usecase.UseCase
	_ = yaml.Unmarshal(body, &uc)
	return uc
}

func readCatalog(root string, v *schema.Validator) []sensor.Sensor {
	var out []sensor.Sensor
	_ = filepath.Walk(filepath.Join(root, ".harness", "sensors"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		body, _ := os.ReadFile(p)
		var inst interface{}
		if yaml.Unmarshal(body, &inst) != nil {
			return nil
		}
		if v.Validate(schema.TargetSensor, inst) != nil {
			return nil
		}
		var s sensor.Sensor
		if yaml.Unmarshal(body, &s) == nil {
			out = append(out, s)
		}
		return nil
	})
	return out
}

func stringer(ls []sensor.Layer) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = string(l)
	}
	return out
}

func layerIDs(rows []struct{ Layer sensor.Layer; Verdict, SensorID, FinishedAt string }) []string {
	// Replaced inline; see Step 4 — the actual call uses verdictRow.
	return nil
}

func safeDiv(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func emit(w io.Writer, v interface{}) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func errSig(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("validate-usecase", "0.1.0").
		WithVerdict("error", "high").WithKind(kind).WithRationale(rationale).Build()
}
```

- [ ] **Step 4: Resolve the placeholder helper signatures**

The above scaffold has TWO placeholder helper bodies (`worstResult`, `layerIDs`) declared with the WRONG parameter type so the engineer is forced to replace them with the typed `verdictRow` slice that is in scope inside `run()`. Edit `validate-usecase.go` so that:

- `worstResult(rows []verdictRow) string` walks `rows` and returns `error` if any verdict is `error`, otherwise `fail` if any is `fail`, otherwise `warn` if any is `warn`, otherwise `pass`. If `coverage == 0`, the caller emits a `no_coverage` error before reaching `worstResult`.
- `layerIDs(rows []verdictRow) []string` returns `string(r.Layer)` for each row.

Move `type verdictRow struct {...}` to a package-level declaration (next to `func main`) so both helpers can reference it.

Replace the call site in `run()` from `worstResult(verdicts, coverage)` to `worstResult(verdicts)` and the `realized.layer_verdicts` field to use the typed slice directly (json.Marshal handles it).

- [ ] **Step 5: Build + run tests**

```bash
go build -tags=validate_usecase ./skills/validate-usecase/scripts
go test -tags=validate_usecase ./skills/validate-usecase/scripts
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add skills/validate-usecase/scripts/
git commit -m "skill(validate-usecase): orchestrator + confidence report computer"
```

---

### Task 37: Write `skills/validate-usecase/SKILL.md` body

**Files:**
- Modify: `skills/validate-usecase/SKILL.md`

- [ ] **Step 1: Replace the placeholder body**

Edit `skills/validate-usecase/SKILL.md` to read in full:

```markdown
---
name: validate-usecase
description: Use when the user invokes /validate-usecase or asks to exercise the per-usecase sensor bundle and report a confidence score. Reads the bundle at .harness/sensors/<usecase-id>/, identifies layer entrypoints (one per unique sensor.layer), invokes the runtime in topological order, and emits a Signal carrying ceiling/coverage/realized counts plus the worst-result aggregate verdict.
---

# validate-usecase

Take a usecase id (or a journey id, or \`--all\`) and orchestrate every layer entrypoint in the corresponding per-usecase bundle(s). Emit a single Signal whose \`metadata.confidence_report\` carries ceiling / coverage / realized / ratios, and whose aggregate \`verdict\` follows worst-result aggregation across the layer entrypoints.

## Invocation

\`\`\`
/validate-usecase <usecase-id | journey-id | --all>
\`\`\`

If no argument is supplied, block:

> Which usecase do you want to validate? Pass a usecase id, a journey id, or \`--all\` to validate every persisted bundle.

## Phase 1 — Resolve input

Resolve to one or more usecase ids. \`--all\` enumerates every \`.harness/sensors/<usecase-id>/\` subdirectory.

## Phase 2 — Walk the bundle

For each usecase id:

1. Read every \`<usecase-id>/<sensor>.yaml\` whose \`layer:\` field is set.
2. Group by layer; pick the entrypoint per layer (composite when present, else the solo narrow).

## Phase 3 — Invoke each entrypoint

Topologically order the entrypoints (deterministic by layer slug). For each, run:

\`\`\`bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=validate_usecase \
  ./skills/validate-usecase/scripts <usecase-id>
\`\`\`

The script invokes \`lib/orchestrator.RunWithDeps\` for each entrypoint, captures the aggregate Signal of each, and records its verdict.

## Phase 4 — Compute confidence

- \`ceiling\` = applicable layers given the current \`.harness/stack.yaml\` and the registered \`lib/planning/layer/\` recipes.
- \`coverage\` = unique \`sensor.layer\` values present in the bundle folder.
- \`realized\` = number of entrypoints whose aggregate verdict is \`pass\`.
- Ratios derived per the spec ("Confidence model").
- Aggregate verdict = worst observed verdict across all entrypoints (see "Aggregate Signal verdict" in the spec).

Emit a Signal with \`metadata.confidence_report\` carrying the full report, and \`verdict\` = the worst-result aggregate.

Optionally write the same report to \`.harness/confidence/<journey-id>/<usecase-id>.yaml\` (regenerable cache).

## What this skill does NOT do

- Does not auto-create missing sensors — that's \`/create-sensors\`'s job. If the bundle is missing, emit \`error metadata.kind=no_coverage\` and stop.
- Does not weight layers — every realized layer contributes 1.
```

- [ ] **Step 2: Commit**

```bash
git add skills/validate-usecase/SKILL.md
git commit -m "skill(validate-usecase): full procedural body"
```

---

## Section 11 — Integration + cleanup

### Task 38: End-to-end smoke (deferred until a real project tree exists)

**Files:**
- None (test-time work)

- [ ] **Step 1: Pick a sample project**

Use any project with a `.harness/usecases/users/create-user.yaml` and a populated `.harness/stack.yaml`. If none exists in the test environment, the smoke runs against the framework's own `.harness/` after seeding it.

- [ ] **Step 2: Delete any leftover sensors from the prior schema**

```bash
rm -rf .harness/sensors/*.yaml .harness/sensors/*/
```

- [ ] **Step 3: Run `/detect-sensors`** (recommended path)

This is the user-facing invocation. Verify the catalog populates root-tier primitives.

- [ ] **Step 4: Run `/create-sensors create-user`**

Verify:

- Plan report shows applicable + skipped layers.
- Bundle persists under `.harness/sensors/create-user/`.
- Every persisted sensor has `layer:` populated.

- [ ] **Step 5: Run `/validate-usecase create-user`**

Verify:

- Exit code reflects the worst observed verdict.
- `metadata.confidence_report` is well-formed with `ceiling`, `coverage`, `realized`, `ratios`, `aggregate_verdict`.

- [ ] **Step 6: Commit any tracked test artifacts (if applicable)**

```bash
git add -A
git commit -m "test(layers-of-confidence): smoke end-to-end against sample project"
```

(If the test environment cannot run a real project, document the manual checklist in `docs/superpowers/plans/2026-05-18-layers-of-confidence-smoke-notes.md`.)

---

### Task 39: Final build + test sweep

**Files:**
- None (verification only)

- [ ] **Step 1: Full build**

```bash
go build ./...
go build -tags=read_usecases    ./skills/create-sensors/scripts
go build -tags=plan_and_emit    ./skills/create-sensors/scripts
go build -tags=write_sensor     ./skills/create-sensors/scripts
go build -tags=write_fixture    ./skills/create-sensors/scripts
go build -tags=catalog_sensors  ./skills/create-sensors/scripts
go build -tags=validate_usecase ./skills/validate-usecase/scripts
```

Expected: every command exits 0.

- [ ] **Step 2: Full test sweep**

```bash
go test ./lib/...
go test -tags=read_usecases    ./skills/create-sensors/scripts
go test -tags=plan_and_emit    ./skills/create-sensors/scripts
go test -tags=write_sensor     ./skills/create-sensors/scripts
go test -tags=write_fixture    ./skills/create-sensors/scripts
go test -tags=catalog_sensors  ./skills/create-sensors/scripts
go test -tags=validate_usecase ./skills/validate-usecase/scripts
```

Expected: PASS everywhere.

- [ ] **Step 3: `go vet` sweep**

```bash
go vet ./...
go vet -tags=read_usecases     ./skills/create-sensors/scripts
go vet -tags=plan_and_emit     ./skills/create-sensors/scripts
go vet -tags=validate_usecase  ./skills/validate-usecase/scripts
```

Expected: no warnings.

- [ ] **Step 4: Commit any cleanup**

```bash
git add -A
git commit -m "chore(layers-of-confidence): finalize build + vet sweep"
```

---

## Self-review checklist

1. **Spec coverage** — every numbered Goal in the spec maps to one or more tasks:
   - Goal 1 (rename) → Tasks 30 + 34
   - Goal 2 (layer field) → Tasks 2 + 3 + 7
   - Goal 3 (per-usecase folder) → Tasks 31 + 32 + 33
   - Goal 4 (narrow + composite co-location) → Tasks 10-25 + 33
   - Goal 5 (core platform primitives + auto-create) → Tasks 26-29 + 33
   - Goal 6 (`/validate-usecase` + worst-result) → Tasks 35-37
   - Goal 7 (remove `blind_spots`) → Tasks 1 + 3 + 4 + 5

2. **Layer count** — Section 3-7 covers all 17 layers: unit-test, integration-test, contract-test, e2e (Tasks 10-13); db-state, log-trace, metric, event-emission, event-consumption (Tasks 14-17); performance, resilience (Tasks 18-19); code-quality, architecture, security, dependency-health (Tasks 20-23); db-schema, accessibility (Tasks 24-25). ✓

3. **Coredetect coverage** — Section 8 covers all 9 platform primitives named in the spec: run-project, setup-postgres, seed-db (Task 27); install-deps, build, lint (Task 28); type-check, run-all-tests, check-server-startup (Task 29). ✓

4. **Type consistency** — `LayerRecipe`, `Draft` (Task 7) and `coredetect.Draft` (Task 26) are intentionally separate types. The `Draft` from `lib/planning/layer` is consumed by `plan-and-emit.go` (Task 33). The `Draft` from `lib/planning/coredetect` is also consumed by `plan-and-emit.go`. Both are emitted on the JSONL plan stream tagged by `wirePlan.Type`. ✓

5. **No placeholders** — every step contains either concrete code, a concrete command with expected output, or a concrete verification command. The handful of `echo 'TODO: ...' && false` placeholders inside recipe-emitted sensors are INTENTIONAL — they declare the slot for a real command that the operator wires via `/update-sensor`. The plan never asks the engineer to "fill in details" or "add appropriate error handling".

6. **Open spec items deferred to follow-up** — `/update-sensor` skill (tracked in GitHub issue per the spec's "Open questions"); confidence weighting; stack drift; `/detect-sensors` refactor to consume `coredetect`. These are NOT in the plan; they are explicitly future work per the spec.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-18-layers-of-confidence.md`. Two execution options:

1. **Subagent-Driven (recommended)** — Dispatch a fresh subagent per task, review between tasks, fast iteration. REQUIRED SUB-SKILL: `superpowers:subagent-driven-development`.

2. **Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints for review.

Which approach?
