# Lib Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove ~360 lines of duplicated code across `start/stop/list/tail` scripts by promoting envelope construction, signal validation, bootstrap, and holder summarization to `lib/`. Delete dead code in `lib/sensor`.

**Architecture:** Two PRs. PR 1 (Tasks 1–7) is low-risk cleanup: dead code removal, sensor path API consolidation, `registry.SummarizeHolders`, `start.go` uses `sensor.BuildEnvelope`, schemas-dir discovery unified. PR 2 (Tasks 8–14) introduces `signal.Builder` + `signal.ValidateOrEmergency` + `cli.Bootstrap` and migrates all four skill scripts.

**Tech Stack:** Go 1.25, `github.com/santhosh-tekuri/jsonschema/v5`, `github.com/google/uuid`. Build-tagged main packages (`start_sensor`, `stop_sensor`, `list_sensors`, `tail_sensor`, `run_computational`, `run_inferential`).

**Spec:** `docs/superpowers/specs/2026-05-12-lib-refactor-design.md`

---

## File map

### PR 1 — Cleanup (low risk)

**Modify:**
- `lib/sensor/load.go` — delete file (dead code).
- `lib/sensor/lookup.go` — rename `FindSensorByID` → unexported `resolveInDir`; delete the file if it makes more sense to move the helper into `path.go`.
- `lib/sensor/lookup_test.go` — delete (tests moved into `path_test.go` if needed).
- `lib/sensor/path.go` — add `Resolve(idOrPath, baseDir)`, remove `ResolveSensorPath`, remove `ResolveByID` (callers migrated).
- `lib/sensor/path_test.go` — replace existing tests with `Resolve`-shaped tests.
- `lib/sensor/persist.go` — drop local `resolveSchemasDir`, call `schema.FindSchemasDir` instead.
- `lib/orchestrator/dag.go:48` — call `sensor.Resolve` instead of `sensor.FindSensorByID`.
- `lib/registry/held_by.go` — append `SummarizeHolders`.
- `lib/registry/held_by_test.go` — append tests for `SummarizeHolders`.
- `skills/stop-sensor/scripts/stop.go` — delete local `holderSummaries`, `deadHolderSummaries`; call `registry.SummarizeHolders`.
- `skills/list-sensors/scripts/list.go` — delete local `heldBySummaries`; call `registry.SummarizeHolders`.
- `skills/start-sensor/scripts/start.go:200-206` — replace literal `libsensor.Envelope{...}` with `libsensor.BuildEnvelope(sensorJSON)`.

### PR 2 — Signal builder + Bootstrap (medium risk)

**Create:**
- `lib/signal/builder.go` — fluent `Builder` for signal envelopes.
- `lib/signal/builder_test.go`.
- `lib/signal/validate.go` — `ValidateOrEmergency` helper.
- `lib/signal/validate_test.go`.
- `lib/cli/bootstrap.go` — `Bootstrap(skillName, stdout, stderr) BootstrapResult`.
- `lib/cli/bootstrap_test.go`.

**Modify (migration):**
- `skills/tail-sensor/scripts/tail.go` — replace `main` bootstrap, `validateSignal`, `simpleErrSignal`, `tailEnvelope`.
- `skills/list-sensors/scripts/list.go` — replace `main` bootstrap, `validateSignal`, `errorListSignal`, inline envelope construction.
- `skills/stop-sensor/scripts/stop.go` — replace `main` bootstrap, `validateSignal`, `simpleSignal`, `buildAggregate` (envelope shell only).
- `skills/start-sensor/scripts/start.go` — replace `main` bootstrap, `validateSignal`, `finalSignal`.

---

# PR 1 — Cleanup

## Task 1: Remove `lib/sensor/load.go` dead code

**Files:**
- Delete: `lib/sensor/load.go`

- [ ] **Step 1: Confirm zero external callers**

Run: `grep -rn "LoadAndValidateSensor\|readJSONFile" /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor/ --include='*.go'`
Expected: only matches inside `lib/sensor/load.go` itself.

- [ ] **Step 2: Delete the file**

Run: `rm /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor/lib/sensor/load.go`

- [ ] **Step 3: Verify build still passes**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go build ./...`
Expected: no errors.

- [ ] **Step 4: Run tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/sensor/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/sensor/load.go
git commit -m "refactor(sensor): remove dead LoadAndValidateSensor + readJSONFile

Zero external callers — vestigial helper from an older migration."
```

---

## Task 2: Introduce `sensor.Resolve` unified API

**Files:**
- Modify: `lib/sensor/path.go`
- Create: `lib/sensor/resolve_test.go`

- [ ] **Step 1: Write failing test for `Resolve`**

Replace `lib/sensor/path_test.go` content with:

```go
package sensor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestResolve_ByID(t *testing.T) {
	dir := t.TempDir()
	sensorsDir := filepath.Join(dir, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(sensorsDir, "watch-logs.json")
	if err := os.WriteFile(want, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sensor.Resolve("watch-logs", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolve_ByPath(t *testing.T) {
	dir := t.TempDir()
	sensorsDir := filepath.Join(dir, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(sensorsDir, "x.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		arg  string
	}{
		{"@-prefix relative", "@sensors/x.json"},
		{"relative", "sensors/x.json"},
		{"absolute", target},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := sensor.Resolve(tc.arg, dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != target {
				t.Fatalf("got %s, want %s", got, target)
			}
		})
	}
}

func TestResolve_Empty(t *testing.T) {
	if _, err := sensor.Resolve("", "/tmp"); err == nil {
		t.Fatal("expected error on empty")
	}
}

func TestResolve_BadID(t *testing.T) {
	if _, err := sensor.Resolve("Bad_ID", "/tmp"); err == nil {
		t.Fatal("expected error on uppercase id")
	}
}

func TestResolve_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := sensor.Resolve("nope", dir); err == nil {
		t.Fatal("expected error when file missing")
	}
}

func TestResolve_PathTraversal(t *testing.T) {
	if _, err := sensor.Resolve("../etc/passwd", "/tmp"); err == nil {
		t.Fatal("expected error on path-like id")
	}
}
```

- [ ] **Step 2: Verify test fails (function not yet present)**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/sensor/...`
Expected: FAIL with "undefined: sensor.Resolve".

- [ ] **Step 3: Replace `lib/sensor/path.go` with unified API**

Overwrite `lib/sensor/path.go`:

```go
package sensor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// idRegex matches the sensor.id shape required by schemas/sensor.json.
var idRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Resolve devolve o path absoluto canônico para um sensor identificado por
// id puro ("my-sensor"), por path prefixado ("@sensors/my.json"), ou por
// path relativo/absoluto. Quando idOrPath bate o regex de id, é resolvido
// como <baseDir>/sensors/<id>.json; caso contrário é tratado como path
// (com @ removido, e relativos resolvidos contra baseDir).
//
// Retorna erro descritivo para entrada vazia, id mal formado, path
// traversal e arquivo inexistente.
func Resolve(idOrPath, baseDir string) (string, error) {
	if idOrPath == "" {
		return "", errors.New("empty sensor reference")
	}
	if looksLikePath(idOrPath) {
		return resolvePath(idOrPath, baseDir)
	}
	if !idRegex.MatchString(idOrPath) {
		return "", fmt.Errorf("sensor id %q does not match ^[a-z][a-z0-9-]*$", idOrPath)
	}
	return resolveInDir(idOrPath, filepath.Join(baseDir, "sensors"))
}

// resolveInDir é o helper interno usado pelo orchestrator: assume que
// sensorRoot já é o diretório que contém <id>.json.
func resolveInDir(id, sensorRoot string) (string, error) {
	if strings.ContainsAny(id, "/\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid sensor id %q (no path separators)", id)
	}
	path := filepath.Join(sensorRoot, id+".json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("sensor %q not found at %s: %w", id, path, err)
	}
	return path, nil
}

func resolvePath(arg, baseDir string) (string, error) {
	arg = strings.TrimPrefix(arg, "@")
	if arg == "" {
		return "", errors.New("empty path after trimming @")
	}
	if !filepath.IsAbs(arg) {
		arg = filepath.Join(baseDir, arg)
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return abs, nil
}

func looksLikePath(s string) bool {
	return strings.HasPrefix(s, "@") ||
		strings.ContainsAny(s, "/\\") ||
		strings.HasSuffix(s, ".json")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/sensor/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/sensor/path.go lib/sensor/path_test.go
git commit -m "refactor(sensor): unify path resolution under sensor.Resolve

Replaces ResolveByID + ResolveSensorPath with a single API that
detects id vs path heuristically. Path-like inputs (contain /, start
with @, end with .json) take the path branch; bare id strings go
through the sensors/<id>.json branch with regex validation."
```

---

## Task 3: Migrate callers from `ResolveByID` / `FindSensorByID` to `sensor.Resolve`

**Files:**
- Modify: `lib/orchestrator/dag.go:48`
- Modify: `skills/run-sensor/scripts/run-computational.go:53`
- Modify: `skills/run-sensor/scripts/run-inferential.go:70`
- Modify: `skills/start-sensor/scripts/start.go:70`
- Modify: `skills/stop-sensor/scripts/stop.go:212`
- Delete: `lib/sensor/lookup.go`
- Delete: `lib/sensor/lookup_test.go`

- [ ] **Step 1: Update orchestrator/dag.go**

In `lib/orchestrator/dag.go`, find:
```go
path, err := sensor.FindSensorByID(id, root)
```
Replace with:
```go
path, err := sensor.Resolve(id, filepath.Dir(root))
```

Note: `root` here is the directory containing sensor JSONs (e.g. `<projectRoot>/sensors`). `filepath.Dir(root)` recovers `<projectRoot>`, which is what `Resolve` expects. Verify by reading `lib/orchestrator/dag.go` first to confirm what `root` is bound to in context.

- [ ] **Step 2: Update run-computational.go**

In `skills/run-sensor/scripts/run-computational.go`, find:
```go
sensorPath, err := sensor.ResolveByID(id, projectRoot)
```
Replace with:
```go
sensorPath, err := sensor.Resolve(id, projectRoot)
```

- [ ] **Step 3: Update run-inferential.go**

Same change in `skills/run-sensor/scripts/run-inferential.go`:
```go
sensorAbsPath, err := sensor.Resolve(id, projectRoot)
```

- [ ] **Step 4: Update start.go**

In `skills/start-sensor/scripts/start.go`, find:
```go
path, err := libsensor.ResolveByID(id, projectRoot)
```
Replace with:
```go
path, err := libsensor.Resolve(id, projectRoot)
```

- [ ] **Step 5: Update stop.go**

In `skills/stop-sensor/scripts/stop.go`, find:
```go
path, err := libsensor.ResolveByID(id, projectRoot)
```
Replace with:
```go
path, err := libsensor.Resolve(id, projectRoot)
```

- [ ] **Step 6: Delete `lib/sensor/lookup.go` and `lookup_test.go`**

Run:
```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor
rm lib/sensor/lookup.go lib/sensor/lookup_test.go
```

- [ ] **Step 7: Run full test suite**

Run:
```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor
go test ./lib/... && \
go test -tags=run_computational ./skills/... && \
go test -tags=run_inferential ./skills/... && \
go test -tags=start_sensor ./skills/... && \
go test -tags=stop_sensor ./skills/... && \
go test -tags=list_sensors ./skills/... && \
go test -tags=tail_sensor ./skills/... && \
go test -tags=detect_sensors ./skills/... && \
go test -tags=heal_sensor ./skills/...
```
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor(sensor): migrate callers to sensor.Resolve; remove lookup.go

All call sites of ResolveByID and FindSensorByID now go through the
single sensor.Resolve entry point. lookup.go is no longer reachable
and is deleted."
```

---

## Task 4: Add `registry.SummarizeHolders`

**Files:**
- Modify: `lib/registry/held_by.go`
- Modify: `lib/registry/held_by_test.go`

- [ ] **Step 1: Write failing test**

Append to `lib/registry/held_by_test.go`:

```go
func TestSummarizeHolders_All(t *testing.T) {
	holders := []HeldByEntry{
		{Kind: "manual", AttachedAt: "2026-05-12T00:00:00Z"},
		{Kind: "sensor", ID: "foo", PID: 1, AttachedAt: "2026-05-12T00:00:01Z"},
	}
	out := SummarizeHolders(holders, SummarizeOpts{})
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(out))
	}
	m0 := out[0].(map[string]interface{})
	if m0["kind"] != "manual" || m0["attached_at"] != "2026-05-12T00:00:00Z" {
		t.Fatalf("entry 0 mismatch: %v", m0)
	}
	m1 := out[1].(map[string]interface{})
	if m1["kind"] != "sensor" || m1["id"] != "foo" || m1["pid"] != 1 {
		t.Fatalf("entry 1 mismatch: %v", m1)
	}
	if _, ok := m1["pid_alive"]; !ok {
		t.Fatal("sensor entry missing pid_alive")
	}
}

func TestSummarizeHolders_DeadOnly(t *testing.T) {
	holders := []HeldByEntry{
		{Kind: "manual", AttachedAt: "2026-05-12T00:00:00Z"},
		{Kind: "sensor", ID: "live", PID: os.Getpid(), AttachedAt: "2026-05-12T00:00:01Z"},
		{Kind: "sensor", ID: "dead", PID: 1, AttachedAt: "2026-05-12T00:00:02Z"},
	}
	out := SummarizeHolders(holders, SummarizeOpts{DeadOnly: true})
	if len(out) != 1 {
		t.Fatalf("expected 1 dead entry, got %d", len(out))
	}
	m := out[0].(map[string]interface{})
	if m["id"] != "dead" {
		t.Fatalf("expected dead sensor, got %v", m)
	}
}

func TestSummarizeHolders_EmptyReturnsNonNil(t *testing.T) {
	out := SummarizeHolders(nil, SummarizeOpts{})
	if out == nil {
		t.Fatal("expected non-nil empty slice")
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(out))
	}
}
```

Add to the top imports if not present:
```go
import (
	"os"
	"testing"
)
```

- [ ] **Step 2: Verify test fails**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/registry/...`
Expected: FAIL with "undefined: SummarizeHolders".

- [ ] **Step 3: Implement `SummarizeHolders`**

Append to `lib/registry/held_by.go`:

```go
// SummarizeOpts controls SummarizeHolders output.
type SummarizeOpts struct {
	// DeadOnly restricts output to kind=sensor holders whose PID is no
	// longer alive (manual holders are excluded). Useful for surfacing
	// dead_holders evidence in /stop-sensor.
	DeadOnly bool
}

// SummarizeHolders converts holders into a JSON-serializable representation
// suitable for embedding in Signal metadata. For kind=sensor entries it
// annotates the entry with pid_alive computed at call time. Returns a
// non-nil slice even when empty (callers may type-assert without a nil
// check).
func SummarizeHolders(holders []HeldByEntry, opts SummarizeOpts) []interface{} {
	out := make([]interface{}, 0, len(holders))
	for _, h := range holders {
		if opts.DeadOnly {
			if h.Kind != "sensor" || IsPIDAlive(h.PID) {
				continue
			}
		}
		entry := map[string]interface{}{
			"kind":        h.Kind,
			"attached_at": h.AttachedAt,
		}
		if h.Kind == "sensor" {
			entry["id"] = h.ID
			entry["pid"] = h.PID
			if !opts.DeadOnly {
				entry["pid_alive"] = IsPIDAlive(h.PID)
			}
		}
		out = append(out, entry)
	}
	return out
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/registry/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/held_by.go lib/registry/held_by_test.go
git commit -m "feat(registry): add SummarizeHolders helper

Centralizes the holder summarization logic duplicated in stop-sensor
and list-sensors scripts. Supports a DeadOnly mode for the
dead_holders evidence carried by /stop-sensor."
```

---

## Task 5: Migrate `stop.go` and `list.go` to `registry.SummarizeHolders`

**Files:**
- Modify: `skills/stop-sensor/scripts/stop.go`
- Modify: `skills/list-sensors/scripts/list.go`

- [ ] **Step 1: Replace in `stop.go`**

In `skills/stop-sensor/scripts/stop.go`, find and replace:
- Lines 100–109 (the block setting `holders`, `dead_holders`, `reaped_holders` inside the `IsHeld(entry)` branch). Replace `holderSummaries(entry.HeldBy)` with `registry.SummarizeHolders(entry.HeldBy, registry.SummarizeOpts{})`, `deadHolderSummaries(entry.HeldBy)` with `registry.SummarizeHolders(entry.HeldBy, registry.SummarizeOpts{DeadOnly: true})`, and `holderSummaries(reaped)` with `registry.SummarizeHolders(reaped, registry.SummarizeOpts{})`.
- Line 292 (`md["reaped_holders"] = holderSummaries(reaped)`) → same replacement.

Then delete the local helpers (functions `deadHolderSummaries` and `holderSummaries`, lines ~312–349).

- [ ] **Step 2: Replace in `list.go`**

In `skills/list-sensors/scripts/list.go:95`, replace:
```go
"held_by": heldBySummaries(e.HeldBy),
```
with:
```go
"held_by": registry.SummarizeHolders(e.HeldBy, registry.SummarizeOpts{}),
```

Delete the local `heldBySummaries` function (lines 127–139).

- [ ] **Step 3: Run skill tests**

Run:
```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor
go test -tags=stop_sensor ./skills/stop-sensor/... && \
go test -tags=list_sensors ./skills/list-sensors/...
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/stop-sensor/scripts/stop.go skills/list-sensors/scripts/list.go
git commit -m "refactor(skills): use registry.SummarizeHolders in stop/list

Removes the three local copies of holderSummaries/deadHolderSummaries
in favor of the centralized helper."
```

---

## Task 6: `start.go` uses `sensor.BuildEnvelope`

**Files:**
- Modify: `skills/start-sensor/scripts/start.go:200-213`

- [ ] **Step 1: Replace literal Envelope construction**

In `skills/start-sensor/scripts/start.go`, find lines ~200–206:
```go
envelope := libsensor.Envelope{
    SensorID:   id,
    Version:    stringField(sensorJSON, "version"),
    RunID:      uuid.NewString(),
    StartedAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
    SensorType: stringField(sensorJSON, "type"),
}
```
Replace with:
```go
envelope, eerr := libsensor.BuildEnvelope(sensorJSON)
if eerr != nil {
    return fmt.Errorf("envelope: %w", eerr)
}
```

- [ ] **Step 2: Check unused imports**

After the change, the `uuid` and `time` imports in `start.go` may still be needed elsewhere — verify with:
```bash
grep -n "uuid\.\|time\." /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor/skills/start-sensor/scripts/start.go
```
If unused, remove from import block. (Note: `start.go` uses `time.Now()` elsewhere — likely both stay.)

- [ ] **Step 3: Run start-sensor tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test -tags=start_sensor ./skills/start-sensor/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/start-sensor/scripts/start.go
git commit -m "refactor(start-sensor): use libsensor.BuildEnvelope

Replaces the hand-rolled Envelope literal with the canonical builder,
making envelope-shape changes a single-place edit."
```

---

## Task 7: Consolidate schemas-dir discovery

**Files:**
- Modify: `lib/sensor/persist.go`

- [ ] **Step 1: Update persist.go to call `schema.FindSchemasDir`**

In `lib/sensor/persist.go`, find the line:
```go
v, err := schema.NewValidator(resolveSchemasDir(schemasDir))
```
Replace with:
```go
dir := schemasDir
if dir == "" {
    cwd, _ := os.Getwd()
    found, ferr := schema.FindSchemasDir(cwd)
    if ferr != nil {
        return "", fmt.Errorf("locate schemas: %w", ferr)
    }
    dir = found
}
v, err := schema.NewValidator(dir)
```

Then delete the local `resolveSchemasDir` function (lines ~79–100).

- [ ] **Step 2: Run sensor lib tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/sensor/...`
Expected: PASS.

- [ ] **Step 3: Run full test suite as a final PR 1 gate**

Run:
```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor
go vet -tags=run_computational ./... && \
go vet -tags=run_inferential ./... && \
go test ./lib/... && \
go test -tags=run_computational ./skills/... && \
go test -tags=run_inferential ./skills/... && \
go test -tags=start_sensor ./skills/... && \
go test -tags=stop_sensor ./skills/... && \
go test -tags=list_sensors ./skills/... && \
go test -tags=tail_sensor ./skills/... && \
go test -tags=detect_sensors ./skills/... && \
go test -tags=heal_sensor ./skills/...
```
Expected: all PASS, zero vet warnings.

- [ ] **Step 4: Commit and open PR 1**

```bash
git add lib/sensor/persist.go
git commit -m "refactor(sensor): drop local resolveSchemasDir; use schema.FindSchemasDir

Consolidates schemas-dir discovery in lib/schema, the single source
of truth used by every validator-bootstrapping skill."
```

Open PR 1 with title "Refactor: cleanup duplicate APIs in lib/sensor and lib/registry":

```bash
gh pr create --title "Refactor: cleanup duplicate APIs in lib/sensor and lib/registry" --body "$(cat <<'EOF'
## Summary
- Removes dead code in lib/sensor (LoadAndValidateSensor, readJSONFile, lookup.go).
- Consolidates three sensor-resolution functions into sensor.Resolve.
- Adds registry.SummarizeHolders; deletes three local copies.
- start.go uses sensor.BuildEnvelope.
- schemas-dir discovery unified in lib/schema.

Spec: docs/superpowers/specs/2026-05-12-lib-refactor-design.md

## Test plan
- [ ] All build tags pass: run_computational, run_inferential, start_sensor, stop_sensor, list_sensors, tail_sensor, detect_sensors, heal_sensor
- [ ] go vet -tags=run_computational ./...
- [ ] go vet -tags=run_inferential ./...

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

# PR 2 — Signal builder + Bootstrap

## Task 8: Implement `signal.Builder`

**Files:**
- Create: `lib/signal/builder.go`
- Create: `lib/signal/builder_test.go`

- [ ] **Step 1: Write failing tests**

Create `lib/signal/builder_test.go`:

```go
package signal_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/signal"
)

func TestBuilder_DefaultsAreSignalValid(t *testing.T) {
	sig := signal.NewBuilder("my-sensor", "1.0.0").
		WithVerdict("pass", "info").
		WithKind("started").
		WithRationale("ok").
		Build()

	if sig["sensor_id"] != "my-sensor" {
		t.Fatalf("sensor_id: %v", sig["sensor_id"])
	}
	if sig["version"] != "1.0.0" {
		t.Fatalf("version: %v", sig["version"])
	}
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict: %v", sig["verdict"])
	}
	if sig["severity"] != "info" {
		t.Fatalf("severity: %v", sig["severity"])
	}
	if sig["confidence"] != 1.0 {
		t.Fatalf("confidence: %v", sig["confidence"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "started" {
		t.Fatalf("metadata.kind: %v", md["kind"])
	}
	if sig["run_id"] == "" {
		t.Fatal("run_id empty")
	}
	if sig["started_at"] == "" || sig["finished_at"] == "" {
		t.Fatal("timestamps empty")
	}
	cost := sig["cost_actual"].(map[string]interface{})
	if cost["latency_ms"] != 0 {
		t.Fatalf("latency: %v", cost["latency_ms"])
	}
	ev := sig["evidence"].([]interface{})
	if len(ev) != 1 {
		t.Fatalf("expected single evidence, got %d", len(ev))
	}
	if ev[0].(map[string]interface{})["rationale"] != "ok" {
		t.Fatalf("rationale: %v", ev[0])
	}
}

func TestBuilder_MissingVersionFallsBackToZero(t *testing.T) {
	sig := signal.NewBuilder("x", "").
		WithVerdict("error", "high").
		WithKind("failed").
		Build()
	if sig["version"] != "0.0.0" {
		t.Fatalf("expected 0.0.0 fallback, got %v", sig["version"])
	}
}

func TestBuilder_WithMetadataMergesAndKindWins(t *testing.T) {
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("aggregate").
		WithMetadata(map[string]interface{}{
			"counts":    map[string]interface{}{"pass": 3.0},
			"exit_code": 0,
		}).
		Build()
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "aggregate" {
		t.Fatalf("kind: %v", md["kind"])
	}
	if md["exit_code"] != 0 {
		t.Fatalf("exit_code: %v", md["exit_code"])
	}
}

func TestBuilder_WithDiagnoseMergesAfter(t *testing.T) {
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("list").
		WithDiagnose(map[string]interface{}{
			"registry_path": "/x/.runtime/sensors/running_sensors.json",
		}).
		Build()
	md := sig["metadata"].(map[string]interface{})
	if md["registry_path"] != "/x/.runtime/sensors/running_sensors.json" {
		t.Fatalf("diagnose not merged: %v", md)
	}
}

func TestBuilder_WithRunIDOverridesDefaults(t *testing.T) {
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("started").
		WithRunID("RUN-1", "2026-05-12T10:00:00Z", "2026-05-12T10:00:01Z").
		Build()
	if sig["run_id"] != "RUN-1" {
		t.Fatalf("run_id: %v", sig["run_id"])
	}
	if sig["started_at"] != "2026-05-12T10:00:00Z" {
		t.Fatalf("started_at: %v", sig["started_at"])
	}
	if sig["finished_at"] != "2026-05-12T10:00:01Z" {
		t.Fatalf("finished_at: %v", sig["finished_at"])
	}
}

func TestBuilder_WithEvidenceWinsOverRationale(t *testing.T) {
	custom := []interface{}{
		map[string]interface{}{"rationale": "A"},
		map[string]interface{}{"rationale": "B"},
	}
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("k").
		WithRationale("ignored").
		WithEvidence(custom).
		Build()
	ev := sig["evidence"].([]interface{})
	if len(ev) != 2 {
		t.Fatalf("expected 2 evidence entries, got %d", len(ev))
	}
}

func TestBuilder_WithLatencyMS(t *testing.T) {
	sig := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("k").
		WithLatencyMS(123).
		Build()
	cost := sig["cost_actual"].(map[string]interface{})
	if cost["latency_ms"] != 123 {
		t.Fatalf("latency: %v", cost["latency_ms"])
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/signal/...`
Expected: FAIL.

- [ ] **Step 3: Implement `Builder`**

Create `lib/signal/builder.go`:

```go
package signal

import (
	"time"

	"github.com/google/uuid"
)

// Builder constrói o envelope canônico de um Signal acumulando os campos
// via método fluente. Aplica defaults seguros em Build() (run_id, started/
// finished timestamps, confidence, evidence) para que cada caller só
// precise especificar o que muda.
type Builder struct {
	sensorID   string
	version    string
	verdict    string
	severity   string
	kind       string
	rationale  string
	evidence   []interface{}
	latencyMS  int
	metadata   map[string]interface{}
	diagnose   map[string]interface{}
	runID      string
	startedAt  string
	finishedAt string
}

// NewBuilder cria um Builder com sensor id e version pré-amarrados. Quando
// version é vazia, Build() emite "0.0.0" (compatível com signals de
// pré-validação como discovery_error e bootstrap_failed).
func NewBuilder(sensorID, version string) *Builder {
	return &Builder{sensorID: sensorID, version: version}
}

func (b *Builder) WithVerdict(verdict, severity string) *Builder {
	b.verdict = verdict
	b.severity = severity
	return b
}

func (b *Builder) WithKind(kind string) *Builder {
	b.kind = kind
	return b
}

func (b *Builder) WithRationale(s string) *Builder {
	b.rationale = s
	return b
}

func (b *Builder) WithEvidence(ev []interface{}) *Builder {
	b.evidence = ev
	return b
}

func (b *Builder) WithMetadata(extra map[string]interface{}) *Builder {
	if b.metadata == nil {
		b.metadata = map[string]interface{}{}
	}
	for k, v := range extra {
		b.metadata[k] = v
	}
	return b
}

func (b *Builder) WithDiagnose(diagnose map[string]interface{}) *Builder {
	b.diagnose = diagnose
	return b
}

func (b *Builder) WithLatencyMS(ms int) *Builder {
	b.latencyMS = ms
	return b
}

// WithRunID sobrescreve run_id, started_at e finished_at. Use quando o
// signal pertence a um envelope pré-existente (ex.: aggregate Signal
// derivado de um run em andamento).
func (b *Builder) WithRunID(runID, startedAt, finishedAt string) *Builder {
	b.runID = runID
	b.startedAt = startedAt
	b.finishedAt = finishedAt
	return b
}

// Build emite o signal final como map[string]interface{} pronto para
// json.NewEncoder(...).Encode(). Não valida contra schemas/signal.json —
// use signal.ValidateOrEmergency para isso.
func (b *Builder) Build() map[string]interface{} {
	version := b.version
	if version == "" {
		version = "0.0.0"
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	runID := b.runID
	if runID == "" {
		runID = uuid.NewString()
	}
	startedAt := b.startedAt
	if startedAt == "" {
		startedAt = now
	}
	finishedAt := b.finishedAt
	if finishedAt == "" {
		finishedAt = now
	}

	md := map[string]interface{}{}
	for k, v := range b.metadata {
		md[k] = v
	}
	for k, v := range b.diagnose {
		md[k] = v
	}
	if b.kind != "" {
		md["kind"] = b.kind
	}

	evidence := b.evidence
	if evidence == nil && b.rationale != "" {
		evidence = []interface{}{
			map[string]interface{}{"rationale": b.rationale},
		}
	}

	return map[string]interface{}{
		"sensor_id":   b.sensorID,
		"version":     version,
		"run_id":      runID,
		"started_at":  startedAt,
		"finished_at": finishedAt,
		"verdict":     b.verdict,
		"severity":    b.severity,
		"confidence":  1.0,
		"evidence":    evidence,
		"cost_actual": map[string]interface{}{"latency_ms": b.latencyMS},
		"metadata":    md,
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/signal/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/signal/builder.go lib/signal/builder_test.go
git commit -m "feat(signal): add fluent Builder for Signal envelopes

Centralizes envelope construction (run_id, timestamps, confidence,
evidence, cost_actual, metadata) so changes propagate to every skill
without per-script edits."
```

---

## Task 9: Implement `signal.ValidateOrEmergency`

**Files:**
- Create: `lib/signal/validate.go`
- Create: `lib/signal/validate_test.go`

- [ ] **Step 1: Write failing tests**

Create `lib/signal/validate_test.go`:

```go
package signal_test

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func loadTestValidator(t *testing.T) *schema.Validator {
	t.Helper()
	_, here, _, _ := runtime.Caller(0)
	// lib/signal/validate_test.go → walk up to repo root, then schemas/
	root := filepath.Dir(filepath.Dir(filepath.Dir(here)))
	v, err := schema.NewValidator(filepath.Join(root, "schemas"))
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	return v
}

func TestValidateOrEmergency_PassThroughWhenValid(t *testing.T) {
	v := loadTestValidator(t)
	good := signal.NewBuilder("x", "1.0").
		WithVerdict("pass", "info").
		WithKind("started").
		WithRationale("ok").
		Build()
	var buf bytes.Buffer
	out := signal.ValidateOrEmergency(v, good, "x", &buf)
	if out["verdict"] != "pass" {
		t.Fatalf("expected original sig, got %v", out)
	}
	if buf.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", buf.String())
	}
}

func TestValidateOrEmergency_EmitsEmergencyOnInvalid(t *testing.T) {
	v := loadTestValidator(t)
	bad := map[string]interface{}{"sensor_id": "x"} // missing required fields
	var buf bytes.Buffer
	out := signal.ValidateOrEmergency(v, bad, "x", &buf)
	if out["verdict"] != "error" {
		t.Fatalf("expected emergency verdict=error, got %v", out["verdict"])
	}
	md := out["metadata"].(map[string]interface{})
	if md["kind"] != "signal_validation_failed" {
		t.Fatalf("kind: %v", md["kind"])
	}
	if !strings.Contains(buf.String(), "BUG: emitted signal failed signal.json validation") {
		t.Fatalf("stderr should contain BUG message, got %q", buf.String())
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/signal/...`
Expected: FAIL.

- [ ] **Step 3: Implement `ValidateOrEmergency`**

Create `lib/signal/validate.go`:

```go
package signal

import (
	"fmt"
	"io"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// ValidateOrEmergency valida sig contra schemas/signal.json. Se a
// validação falhar, loga o erro em stderr e devolve um signal de
// emergência (verdict=error, metadata.kind=signal_validation_failed)
// para que o bug suba sem recursão. Em sucesso devolve sig sem cópia.
//
// fallbackID é usado como sensor_id do signal de emergência quando o
// signal original não tem id válido (ex.: bug que produziu o sig
// inválido também perdeu o sensor_id).
func ValidateOrEmergency(v *schema.Validator, sig map[string]interface{}, fallbackID string, stderr io.Writer) map[string]interface{} {
	if v == nil {
		return sig
	}
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(stderr, "BUG: emitted signal failed signal.json validation: %v\n", err)
		return NewBuilder(fallbackID, "0.0.0").
			WithVerdict("error", "high").
			WithKind("signal_validation_failed").
			WithRationale(fmt.Sprintf("signal_validation_failed: %v", err)).
			Build()
	}
	return sig
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/signal/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/signal/validate.go lib/signal/validate_test.go
git commit -m "feat(signal): add ValidateOrEmergency helper

Centralizes the validate-or-fallback-to-emergency-signal pattern that
each skill script reimplements identically. Uses Builder under the
hood for the emergency envelope."
```

---

## Task 10: Implement `cli.Bootstrap`

**Files:**
- Create: `lib/cli/bootstrap.go`
- Create: `lib/cli/bootstrap_test.go`

- [ ] **Step 1: Write failing tests**

Create `lib/cli/bootstrap_test.go`:

```go
package cli_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/cli"
)

func TestBootstrap_HappyPathInProjectRoot(t *testing.T) {
	tmp := t.TempDir()
	// fake a project root: must contain a sensors/ dir for registry.Lookup
	if err := os.MkdirAll(filepath.Join(tmp, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", tmp)

	chdir(t, tmp)
	var out, errBuf bytes.Buffer
	res := cli.Bootstrap("my-skill", &out, &errBuf)
	if res.ExitCode != 0 {
		t.Fatalf("exit %d, stderr=%q", res.ExitCode, errBuf.String())
	}
	if res.Validator == nil {
		t.Fatal("validator nil")
	}
	if res.Diagnose["registry_path"] == "" {
		t.Fatalf("diagnose: %v", res.Diagnose)
	}
}

func TestBootstrap_DiscoveryFailureEmitsSignalAndExits(t *testing.T) {
	tmp := t.TempDir() // no sensors/ subdir
	chdir(t, tmp)
	var out, errBuf bytes.Buffer
	res := cli.Bootstrap("my-skill", &out, &errBuf)
	if res.ExitCode == 0 {
		t.Fatal("expected non-zero exit")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(out.Bytes(), &sig); err != nil {
		t.Fatalf("decode emitted signal: %v (bytes=%q)", err, out.String())
	}
	if sig["verdict"] != "error" {
		t.Fatalf("verdict: %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "registry_discovery_failed" {
		t.Fatalf("kind: %v", md["kind"])
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Verify tests fail**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/cli/...`
Expected: FAIL.

- [ ] **Step 3: Implement `Bootstrap`**

Create `lib/cli/bootstrap.go`:

```go
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

// BootstrapResult é o retorno padronizado de Bootstrap. Quando ExitCode é
// != 0, signals já foram emitidos em stdout — o caller deve sair com esse
// código imediatamente.
type BootstrapResult struct {
	Res       registry.Result
	Validator *schema.Validator
	Diagnose  map[string]interface{}
	ExitCode  int
}

// Bootstrap executa o setup padrão das skills que tocam o registry de
// sensores: resolve cwd, descobre a raiz do registry (com sanitização),
// emite signals de discovery_error e registry_migrated quando aplicável,
// e inicializa o schema validator.
//
// O caller usa o BootstrapResult retornado para acessar registry.Result,
// validator e diagnose pré-construído. Se ExitCode != 0, o caller chama
// os.Exit(ExitCode) sem emitir mais nada (signals já foram emitidos).
func Bootstrap(skillName string, stdout, stderr io.Writer) BootstrapResult {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s: cwd: %v\n", skillName, err)
		return BootstrapResult{ExitCode: 2}
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(registry.DiscoveryErrorSignal(err, skillName))
		return BootstrapResult{ExitCode: 1}
	}
	if len(reports) > 0 {
		_ = json.NewEncoder(stdout).Encode(registry.RegistryMigratedSignal(res, reports, skillName))
	}
	v, code := schema.LoadValidator("", stderr)
	if code != 0 {
		return BootstrapResult{Res: res, ExitCode: code, Diagnose: registry.DiagnoseMetadata(res)}
	}
	return BootstrapResult{
		Res:       res,
		Validator: v,
		Diagnose:  registry.DiagnoseMetadata(res),
		ExitCode:  0,
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test ./lib/cli/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/cli/bootstrap.go lib/cli/bootstrap_test.go
git commit -m "feat(cli): add Bootstrap for registry-aware skill entry points

Wraps cwd + LookupSanitized + discovery/migrated signal emission +
schema validator init. Replaces the ~18-line block that every
registry-touching skill (/start, /stop, /list, /tail) reimplements."
```

---

## Task 11: Migrate `tail.go` to `Builder` + `Bootstrap`

**Files:**
- Modify: `skills/tail-sensor/scripts/tail.go`

- [ ] **Step 1: Rewrite `tail.go`**

Overwrite `skills/tail-sensor/scripts/tail.go`:

```go
//go:build tail_sensor

// tail returns Signals from a blocking sensor's signals.log starting
// from a 1-based line cursor, plus a final tail-envelope Signal that
// carries metadata.next_cursor for the agent to use on the next call.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	b := cli.Bootstrap("tail-sensor", os.Stdout, os.Stderr)
	if b.ExitCode != 0 {
		os.Exit(b.ExitCode)
	}
	os.Exit(runTail(b, os.Args[1:], os.Stdout, os.Stderr))
}

func runTail(b cli.BootstrapResult, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder("tail", "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_invalid_args").
					WithRationale("expected <sensor.id> <cursor>").
					WithDiagnose(b.Diagnose).
					Build(),
				"tail", stderr))
		return 2
	}
	id := args[0]
	cursor, err := strconv.Atoi(args[1])
	if err != nil || cursor < 0 {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(id, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_invalid_cursor").
					WithRationale(fmt.Sprintf("cursor must be a non-negative integer, got %q", args[1])).
					WithDiagnose(b.Diagnose).
					Build(),
				id, stderr))
		return 1
	}

	if !b.Res.Exists {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(id, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_no_registry").
					WithRationale(fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", b.Res.Root.RegistryFile())).
					WithDiagnose(b.Diagnose).
					Build(),
				id, stderr))
		return 1
	}

	r := b.Res.Root
	if b.Res.State.FindEntry(id) == nil {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(id, "0.0.0").
					WithVerdict("error", "high").
					WithKind("not_running").
					WithRationale(fmt.Sprintf("no live entry for %q", id)).
					WithDiagnose(b.Diagnose).
					Build(),
				id, stderr))
		return 1
	}

	f, err := os.Open(r.SignalsLog(id))
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(
			signal.ValidateOrEmergency(b.Validator,
				signal.NewBuilder(id, "0.0.0").
					WithVerdict("error", "high").
					WithKind("tail_failed").
					WithRationale(fmt.Sprintf("open signals.log: %v", err)).
					WithDiagnose(b.Diagnose).
					Build(),
				id, stderr))
		return 1
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	current := 0
	for sc.Scan() {
		current++
		if current <= cursor {
			continue
		}
		fmt.Fprintln(stdout, sc.Text())
	}

	envelope := signal.NewBuilder(id, "0.0.0").
		WithVerdict("pass", "info").
		WithKind("envelope").
		WithEvidence([]interface{}{map[string]interface{}{"rationale": "tail envelope"}}).
		WithMetadata(map[string]interface{}{
			"next_cursor": current,
			"sensor_id":   id, // legacy field, do not remove
		}).
		WithDiagnose(b.Diagnose).
		Build()
	_ = json.NewEncoder(stdout).Encode(signal.ValidateOrEmergency(b.Validator, envelope, id, stderr))
	return 0
}
```

- [ ] **Step 2: Compile-check**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go build -tags=tail_sensor ./skills/tail-sensor/scripts/`
Expected: compiles cleanly.

- [ ] **Step 3: Run tail tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test -tags=tail_sensor ./skills/tail-sensor/...`
Expected: PASS. Tests may need a small adaptation: the signature of `runTail` changed (now takes `cli.BootstrapResult` instead of `registry.Result`). Adapt `tail_test.go` callers minimally — wrap with `cli.BootstrapResult{Res: res, Validator: v, Diagnose: registry.DiagnoseMetadata(res)}`.

- [ ] **Step 4: Adapt `tail_test.go` if it fails to compile**

Read the test file first to understand the call sites. The most common pattern is:
```go
exit := runTail(res, args, &out, &errBuf)
```
Replace with:
```go
v, _ := schema.LoadValidator("", &errBuf)
exit := runTail(cli.BootstrapResult{Res: res, Validator: v, Diagnose: registry.DiagnoseMetadata(res)}, args, &out, &errBuf)
```

Add imports as needed.

- [ ] **Step 5: Run tail tests again**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test -tags=tail_sensor ./skills/tail-sensor/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add skills/tail-sensor/scripts/tail.go skills/tail-sensor/scripts/tail_test.go
git commit -m "refactor(tail-sensor): use cli.Bootstrap + signal.Builder

Removes ~80 lines of hand-rolled main, validateSignal, simpleErrSignal,
tailEnvelope. Behavior preserved: same signal shapes, same exit codes."
```

---

## Task 12: Migrate `list.go` to `Builder` + `Bootstrap`

**Files:**
- Modify: `skills/list-sensors/scripts/list.go`
- Modify: `skills/list-sensors/scripts/list_test.go` (test signature adaptation)

- [ ] **Step 1: Rewrite `list.go`**

Overwrite `skills/list-sensors/scripts/list.go`:

```go
//go:build list_sensors

// list reads the registry (resolved via cli.Bootstrap), annotates each
// entry with PID liveness, and emits one Signal verdict=pass /
// metadata.kind=list. When the registry file does not exist, emits
// verdict=warn pointing at HARNESS_REGISTRY_ROOT.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	b := cli.Bootstrap("list-sensors", os.Stdout, os.Stderr)
	if b.ExitCode != 0 {
		os.Exit(b.ExitCode)
	}
	os.Exit(runList(b, os.Stdout, os.Stderr))
}

func runList(b cli.BootstrapResult, stdout, stderr io.Writer) int {
	r := b.Res.Root
	rs := b.Res.State

	if !b.Res.Exists {
		sig := signal.NewBuilder("list-sensors", "0.0.0").
			WithVerdict("warn", "info").
			WithKind("list").
			WithRationale(fmt.Sprintf(
				"registry not found at %s. /start-sensor was likely run from a different cwd, or this project has no live blocking sensors. "+
					"If you expect sensors to be live, set HARNESS_REGISTRY_ROOT to the project root used at start time, or rerun /list-sensors from within that project.",
				r.RegistryFile(),
			)).
			WithMetadata(map[string]interface{}{"entries": []interface{}{}}).
			WithDiagnose(b.Diagnose).
			Build()
		_ = json.NewEncoder(stdout).Encode(signal.ValidateOrEmergency(b.Validator, sig, "list-sensors", stderr))
		return 0
	}

	entries := make([]interface{}, 0, len(rs.Entries))
	for _, e := range rs.Entries {
		pidAlive := registry.IsPIDAlive(e.PID)
		watcherAlive := registry.IsPIDAlive(e.WatcherPID)
		state := "running"
		if !pidAlive {
			state = "orphan"
		}
		entries = append(entries, map[string]interface{}{
			"sensor_id":        e.SensorID,
			"pid":              e.PID,
			"pid_alive":        pidAlive,
			"watcher_pid":      e.WatcherPID,
			"watcher_alive":    watcherAlive,
			"started_at":       e.StartedAt,
			"command":          e.Command,
			"held_by":          registry.SummarizeHolders(e.HeldBy, registry.SummarizeOpts{}),
			"signals_log_path": r.SignalsLog(e.SensorID),
			"state":            state,
		})
	}
	sig := signal.NewBuilder("list-sensors", "0.0.0").
		WithVerdict("pass", "info").
		WithKind("list").
		WithRationale(fmt.Sprintf("%d running sensor(s)", len(entries))).
		WithMetadata(map[string]interface{}{"entries": entries}).
		WithDiagnose(b.Diagnose).
		Build()
	_ = json.NewEncoder(stdout).Encode(signal.ValidateOrEmergency(b.Validator, sig, "list-sensors", stderr))
	return 0
}
```

- [ ] **Step 2: Adapt `list_test.go`**

Read `skills/list-sensors/scripts/list_test.go`. Wherever the test calls `runList(res, reports, ...)`, replace with the new signature. Pattern:

```go
v, _ := schema.LoadValidator("", &errBuf)
exit := runList(cli.BootstrapResult{Res: res, Validator: v, Diagnose: registry.DiagnoseMetadata(res)}, &out, &errBuf)
```

(If the test relies on the old `reports` parameter to drive a migrated signal, set up the registry state with reports beforehand and trust `cli.Bootstrap` to emit them — but since tests inject `runList` directly, they probably bypass `Bootstrap`. In that case, drop the assertion about migrated signals or move it to a separate test that calls `cli.Bootstrap` end-to-end.)

- [ ] **Step 3: Run list tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test -tags=list_sensors ./skills/list-sensors/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/list-sensors/scripts/list.go skills/list-sensors/scripts/list_test.go
git commit -m "refactor(list-sensors): use cli.Bootstrap + signal.Builder

Removes ~90 lines of hand-rolled main, validateSignal, errorListSignal,
listMetadata, heldBySummaries. Behavior preserved."
```

---

## Task 13: Migrate `stop.go` to `Builder` + `Bootstrap`

**Files:**
- Modify: `skills/stop-sensor/scripts/stop.go`
- Modify: `skills/stop-sensor/scripts/stop_test.go`

- [ ] **Step 1: Rewrite the signal-emitting paths in `stop.go`**

This is the most involved migration because `stop.go` has many error branches. Strategy: keep `runStop` returning `(int, map[string]interface{})` so tests inspect the signal directly. Replace internal helpers:

- Delete local `validateSignal` (lines ~359–381).
- Delete local `simpleSignal` (lines ~383–400).
- Delete local `stringField` (lines ~351–357).

Replace every `validateSignal(v, simpleSignal(res, id, verdict, severity, kind, rationale), id)` call site with:

```go
signal.ValidateOrEmergency(v, signal.NewBuilder(id, "0.0.0").
    WithVerdict(verdict, severity).
    WithKind(kind).
    WithRationale(rationale).
    WithDiagnose(registry.DiagnoseMetadata(res)).
    Build(), id, os.Stderr)
```

In `buildAggregate` (lines 274–310), replace the `map[string]interface{}{...}` literal at the end with a `signal.NewBuilder(...)` invocation. The `metadata` map (`md`) is already pre-built — pass it via `WithMetadata`. Final `started_at` uses `entry.StartedAt`, so call `WithRunID(uuid.NewString(), entry.StartedAt, now)`.

Adapt `main()` to use `cli.Bootstrap`:

```go
func main() {
    var reap bool
    fs := flag.NewFlagSet("stop", flag.ContinueOnError)
    fs.BoolVar(&reap, "reap-dead-holders", false, "drop kind=sensor holders whose PID is dead before deciding whether to stop")
    if err := fs.Parse(os.Args[1:]); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(2)
    }
    b := cli.Bootstrap("stop-sensor", os.Stdout, os.Stderr)
    if b.ExitCode != 0 {
        os.Exit(b.ExitCode)
    }
    exit, sig := runStop(b, fs.Args(), reap)
    if sig != nil {
        _ = json.NewEncoder(os.Stdout).Encode(sig)
    }
    os.Exit(exit)
}
```

Change `runStop` signature from `(res registry.Result, args []string, reap bool)` to `(b cli.BootstrapResult, args []string, reap bool)`. Replace `res` references with `b.Res`, `v` references with `b.Validator`.

For local helpers that need `Diagnose`, use `b.Diagnose` instead of `registry.DiagnoseMetadata(res)`.

- [ ] **Step 2: Adapt `stop_test.go`**

Same pattern as Task 11/12: tests likely call `runStop(res, args, reap)`. Wrap:

```go
v, _ := schema.LoadValidator("", &errBuf)
exit, sig := runStop(cli.BootstrapResult{Res: res, Validator: v, Diagnose: registry.DiagnoseMetadata(res)}, args, reap)
```

- [ ] **Step 3: Run stop tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test -tags=stop_sensor ./skills/stop-sensor/...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/stop-sensor/scripts/stop.go skills/stop-sensor/scripts/stop_test.go
git commit -m "refactor(stop-sensor): use cli.Bootstrap + signal.Builder

Removes ~120 lines of hand-rolled main, validateSignal, simpleSignal,
stringField, buildAggregate envelope literal. Behavior preserved."
```

---

## Task 14: Migrate `start.go` to `Builder` + `Bootstrap`

**Files:**
- Modify: `skills/start-sensor/scripts/start.go`
- Modify: `skills/start-sensor/scripts/start_test.go`

- [ ] **Step 1: Rewrite signal-emitting paths in `start.go`**

Delete local helpers: `finalSignal` (lines ~371–419), `validateSignal` (lines ~425–444), `stringField` (lines ~342–345).

Replace `finalSignal(id, sensorJSON, kind, cause, aux, rationale, diagnose)` call sites with `signal.NewBuilder(...)`:

```go
func startSignal(id, version, kind, cause, rationale string, aux, diagnose map[string]interface{}) map[string]interface{} {
    verdict, severity := "error", "high"
    if kind == "started" {
        verdict, severity = "pass", "info"
    }
    md := map[string]interface{}{}
    if cause != "" && kind == "failed" {
        md["cause"] = cause
    }
    for k, v := range aux {
        md[k] = v
    }
    return signal.NewBuilder(id, version).
        WithVerdict(verdict, severity).
        WithKind(kind).
        WithRationale(rationale).
        WithMetadata(md).
        WithDiagnose(diagnose).
        Build()
}
```

Wrap every signal emission with `signal.ValidateOrEmergency(b.Validator, sig, id, os.Stderr)`.

Replace `main()` with `cli.Bootstrap`:

```go
func main() {
    b := cli.Bootstrap("start-sensor", os.Stdout, os.Stderr)
    if b.ExitCode != 0 {
        os.Exit(b.ExitCode)
    }
    exit, sig := runStart(b, os.Args[1:])
    if sig != nil {
        _ = json.NewEncoder(os.Stdout).Encode(sig)
    }
    os.Exit(exit)
}
```

Change `runStart(res registry.Result, args []string)` → `runStart(b cli.BootstrapResult, args []string)`.

- [ ] **Step 2: Adapt `start_test.go`**

Same pattern: wrap `runStart(res, args)` with `cli.BootstrapResult{...}`.

- [ ] **Step 3: Run start tests**

Run: `cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor && go test -tags=start_sensor ./skills/start-sensor/...`
Expected: PASS.

- [ ] **Step 4: Run all build tags as final PR 2 gate**

Run:
```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor
go vet -tags=run_computational ./... && \
go vet -tags=run_inferential ./... && \
go test ./lib/... && \
go test -tags=run_computational ./skills/... && \
go test -tags=run_inferential ./skills/... && \
go test -tags=start_sensor ./skills/... && \
go test -tags=stop_sensor ./skills/... && \
go test -tags=list_sensors ./skills/... && \
go test -tags=tail_sensor ./skills/... && \
go test -tags=detect_sensors ./skills/... && \
go test -tags=heal_sensor ./skills/...
```
Expected: all PASS.

- [ ] **Step 5: Smoke test end-to-end**

Pick any computational sensor in `sensors/` and run it:
```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/refactor
ls sensors/
go run -tags=run_computational ./skills/run-sensor/scripts <sensor-id>
```
Expected: JSONL output, last line is the aggregate Signal that validates against `schemas/signal.json`.

- [ ] **Step 6: Commit and open PR 2**

```bash
git add skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_test.go
git commit -m "refactor(start-sensor): use cli.Bootstrap + signal.Builder

Final skill migrated. Removes ~100 lines of hand-rolled finalSignal,
validateSignal, stringField. Behavior preserved."

gh pr create --title "Refactor: signal.Builder + cli.Bootstrap unify skill scripts" --body "$(cat <<'EOF'
## Summary
- Introduces signal.Builder, signal.ValidateOrEmergency, cli.Bootstrap.
- Migrates start-sensor, stop-sensor, list-sensors, tail-sensor to use the new helpers.
- Removes ~400 lines of duplicated bootstrap, validation, and envelope construction.

Spec: docs/superpowers/specs/2026-05-12-lib-refactor-design.md
Builds on: PR 1 (lib/sensor + lib/registry cleanup).

## Test plan
- [ ] go test ./lib/... (signal, cli)
- [ ] All skill build tags: start_sensor, stop_sensor, list_sensors, tail_sensor, run_computational, run_inferential, detect_sensors, heal_sensor
- [ ] Smoke test: run a computational sensor end-to-end, verify aggregate Signal validates against signal.json

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Final verification (post-merge)

After both PRs merge, run a final audit:

```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework
# Count LoC delta — should be ~360 lines net removed
git log main..HEAD --stat | tail -20

# Confirm dead/duplicate symbols are gone
! grep -rn "LoadAndValidateSensor\|FindSensorByID\|ResolveSensorPath\|ResolveByID" --include='*.go' . && echo "OK: dead APIs removed"
! grep -rn "func validateSignal\b" skills/ && echo "OK: per-skill validateSignal removed"
! grep -rn "func simpleSignal\|func simpleErrSignal\|func errorListSignal\|func finalSignal" skills/ && echo "OK: hand-rolled envelope builders removed"
! grep -rn "func holderSummaries\|func deadHolderSummaries\|func heldBySummaries" skills/ && echo "OK: holder summary helpers removed"
! grep -rn "func resolveSchemasDir" lib/sensor/ && echo "OK: duplicate schemas-dir helper removed"

echo "Final: $(find lib skills -name '*.go' | xargs wc -l | tail -1)"
```
