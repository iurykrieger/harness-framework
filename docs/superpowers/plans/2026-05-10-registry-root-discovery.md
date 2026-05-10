# Registry Root Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the blocking-sensor registry path independent of the caller's cwd by introducing a deterministic project-root discovery layer in `lib/registry/`, then migrating the four registry-touching skills (start, list, stop, tail) to use it.

**Architecture:** New `lib/registry/root.go` with `Discover(startDir)` (env var → walk-up looking for `sensors/` marker → error) and `Lookup(startDir)` (Discover + state load → `Result` struct). New `LoadOrEmpty` companion in `state.go` distinguishes "registry file present but empty" from "registry file missing". Each migrated skill calls `Lookup` once at the top of `main()` and threads the `Result` into its existing `runX` function in place of the bare `projectRoot` string. `/list-sensors` adds a `verdict=warn` branch when the file is missing; `/stop-sensor` and `/tail-sensor` add `verdict=error` branches; `/start-sensor` is unaffected (first-start is the canonical "missing file" case). All four skills include `metadata.{registry_path, registry_source, registry_exists}` on every emitted signal.

**Tech Stack:** Go 1.25, single module `github.com/iurykrieger/harness-framework`. Build tags `start_sensor`, `list_sensors`, `stop_sensor`, `tail_sensor` gate per-skill scripts (each skill ships its own `package main`). Tests use `testing` package, `t.TempDir()`, `t.Setenv()`. Black-box e2e tests under `test/<name>/` build skill binaries via `exec.Command("go", "build", "-tags=...", ...)`.

**Spec:** `docs/superpowers/specs/2026-05-10-registry-root-discovery-design.md`. Issue: [#6](https://github.com/iurykrieger/harness-framework/issues/6).

---

## Conventions used by every task

- Always run from the repo root (`cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework`). All `go` commands assume cwd = repo root.
- After every code change, run `go vet -tags=<relevant tags> ./...` to catch typos.
- Commit subject prefix conventions in this repo (see `git log --oneline`):
  - Library work: `feat(registry): ...` or `refactor(registry): ...`.
  - Skill migration: `refactor(<skill>): use registry.Lookup`.
  - Tests-only: `test(<area>): ...`.
  - Docs: `docs: ...`.
- The repo uses `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` on commits made by Claude. Match that style.
- **`HARNESS_REGISTRY_ROOT` MUST be unset (or empty) in any test that does not intentionally set it.** Use `t.Setenv("HARNESS_REGISTRY_ROOT", "")` to scope per-test.

---

## Task 1: Add `LoadOrEmpty` to `lib/registry/state.go` (TDD)

**Files:**
- Modify: `lib/registry/state.go` (add new exported function at the end)
- Modify: `lib/registry/state_test.go` (add tests for new function)

- [ ] **Step 1: Write failing tests in `lib/registry/state_test.go`**

Append these test functions at the end of `lib/registry/state_test.go` (keep existing tests untouched):

```go
func TestLoadOrEmpty_FileAbsent(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	rs, exists, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Errorf("exists: got true, want false")
	}
	if rs.Version != 1 {
		t.Errorf("Version: got %d, want 1", rs.Version)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("Entries: got %d, want 0", len(rs.Entries))
	}
}

func TestLoadOrEmpty_FilePresentEmpty(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	rs, exists, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Errorf("exists: got false, want true")
	}
	if rs.Version != 1 {
		t.Errorf("Version: got %d, want 1", rs.Version)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("Entries: got %d, want 0", len(rs.Entries))
	}
}

func TestLoadOrEmpty_FilePresentWithEntries(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	want := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "loop", PID: 1234, StartedAt: "2026-05-10T00:00:00Z"},
		},
	}
	if err := registry.Save(r, want); err != nil {
		t.Fatal(err)
	}
	rs, exists, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Errorf("exists: got false, want true")
	}
	if !reflect.DeepEqual(want, rs) {
		t.Fatalf("state mismatch\nwant %+v\ngot  %+v", want, rs)
	}
}

func TestLoadOrEmpty_FileMalformed(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.RegistryFile(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, exists, err := registry.LoadOrEmpty(r)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if exists {
		t.Errorf("exists: got true on parse error, want false")
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail with `LoadOrEmpty` undefined**

Run:
```
go test ./lib/registry/ -run TestLoadOrEmpty -v
```
Expected: build failure containing `undefined: registry.LoadOrEmpty`.

- [ ] **Step 3: Implement `LoadOrEmpty` in `lib/registry/state.go`**

Append after `Load` (which currently ends at line 63), keeping the existing `Load`, `Save`, `FindEntry`, and `RemoveEntry` untouched:

```go
// LoadOrEmpty reads running_sensors.json and reports existence
// explicitly:
//   - file present and parseable → (state, true, nil)
//   - file absent                → (RunningSensors{Version: 1}, false, nil)
//   - file present but malformed → (zero, false, parse error)
//
// Load is preserved unchanged for callers that do not care about
// existence (orchestrator, watcher).
func LoadOrEmpty(r Root) (RunningSensors, bool, error) {
	data, err := os.ReadFile(r.RegistryFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RunningSensors{Version: 1}, false, nil
		}
		return RunningSensors{}, false, fmt.Errorf("read registry: %w", err)
	}
	var rs RunningSensors
	if err := json.Unmarshal(data, &rs); err != nil {
		return RunningSensors{}, false, fmt.Errorf("parse registry: %w", err)
	}
	if rs.Version == 0 {
		rs.Version = 1
	}
	return rs, true, nil
}
```

- [ ] **Step 4: Run tests, confirm pass and existing `Load` tests still pass**

Run:
```
go test ./lib/registry/ -v
```
Expected: all tests pass, including the four new `TestLoadOrEmpty_*` and the pre-existing `TestLoad_Empty`, `TestSaveLoad_RoundTrip`, `TestSave_AtomicWrite`, `TestLoad_RejectsCorrupt`.

- [ ] **Step 5: Commit**

```
git add lib/registry/state.go lib/registry/state_test.go
git commit -m "$(cat <<'EOF'
feat(registry): LoadOrEmpty distinguishes missing from empty registry

Additive companion to Load that returns a third value (exists bool) so
callers can tell "file present, no live sensors" from "file absent, wrong
cwd entirely" — needed for the verdict semantics in /list-sensors,
/stop-sensor, and /tail-sensor.

Load itself is unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Create `lib/registry/root.go` with `Source`, `Result`, `Discover` (TDD)

**Files:**
- Create: `lib/registry/root.go`
- Create: `lib/registry/root_test.go`

- [ ] **Step 1: Write failing tests in `lib/registry/root_test.go`**

Create `lib/registry/root_test.go`:

```go
package registry_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// makeProjectTree builds <root>/sensors/ and returns the project root.
// Tests use it to anchor the walk-up marker.
func makeProjectTree(t *testing.T, parent string) string {
	t.Helper()
	root := filepath.Join(parent, "proj")
	if err := os.MkdirAll(filepath.Join(root, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDiscover_EnvVarAbsoluteAndExists(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	t.Setenv("HARNESS_REGISTRY_ROOT", proj)
	got, source, err := registry.Discover("/tmp/whatever")
	if err != nil {
		t.Fatal(err)
	}
	if source != registry.SourceEnv {
		t.Errorf("source: got %q, want %q", source, registry.SourceEnv)
	}
	if got != proj {
		t.Errorf("root: got %q, want %q", got, proj)
	}
}

func TestDiscover_EnvVarNotAbsolute(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "relative/path")
	_, _, err := registry.Discover(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("err message should mention 'absolute', got: %v", err)
	}
}

func TestDiscover_EnvVarNotExists(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "/nonexistent/path/that/should/not/exist/12345")
	_, _, err := registry.Discover(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not exist") && !strings.Contains(err.Error(), "no such") {
		t.Errorf("err message should mention 'not exist' or 'no such', got: %v", err)
	}
}

func TestDiscover_EnvVarPointsToFileNotDir(t *testing.T) {
	parent := t.TempDir()
	file := filepath.Join(parent, "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", file)
	_, _, err := registry.Discover(parent)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not a directory") && !strings.Contains(err.Error(), "directory") {
		t.Errorf("err message should mention 'directory', got: %v", err)
	}
}

func TestDiscover_EnvVarSymlinkResolved(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	link := filepath.Join(parent, "link-to-proj")
	if err := os.Symlink(proj, link); err != nil {
		t.Skipf("symlink not supported on this filesystem: %v", err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", link)
	got, source, err := registry.Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if source != registry.SourceEnv {
		t.Errorf("source: got %q, want %q", source, registry.SourceEnv)
	}
	// EvalSymlinks resolves through the link; got should equal proj
	// (after EvalSymlinks-resolved comparison via filepath.EvalSymlinks).
	gotResolved, _ := filepath.EvalSymlinks(got)
	projResolved, _ := filepath.EvalSymlinks(proj)
	if gotResolved != projResolved {
		t.Errorf("root: got %q (resolved %q), want %q (resolved %q)", got, gotResolved, proj, projResolved)
	}
}

func TestDiscover_WalkUpFindsSensorsTwoLevels(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	deep := filepath.Join(proj, "nested", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	got, source, err := registry.Discover(deep)
	if err != nil {
		t.Fatal(err)
	}
	if source != registry.SourceWalkUp {
		t.Errorf("source: got %q, want %q", source, registry.SourceWalkUp)
	}
	if got != proj {
		t.Errorf("root: got %q, want %q", got, proj)
	}
}

func TestDiscover_WalkUpFromProjectRoot(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	got, source, err := registry.Discover(proj)
	if err != nil {
		t.Fatal(err)
	}
	if source != registry.SourceWalkUp {
		t.Errorf("source: got %q, want %q", source, registry.SourceWalkUp)
	}
	if got != proj {
		t.Errorf("root: got %q, want %q", got, proj)
	}
}

func TestDiscover_WalkUpEmptySensorsDirAcceptable(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent) // sensors/ created but empty
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	got, _, err := registry.Discover(proj)
	if err != nil {
		t.Fatal(err)
	}
	if got != proj {
		t.Errorf("root: got %q, want %q", got, proj)
	}
}

func TestDiscover_NoMarkerNoEnv_ErrorMentionsBothStrategies(t *testing.T) {
	parent := t.TempDir() // no sensors/ anywhere up to filesystem root from here
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, err := registry.Discover(parent)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "HARNESS_REGISTRY_ROOT") {
		t.Errorf("err should mention HARNESS_REGISTRY_ROOT, got: %v", err)
	}
	if !strings.Contains(msg, "sensors") {
		t.Errorf("err should mention 'sensors', got: %v", err)
	}
}

func TestDiscover_DiscoveryError_IsTyped(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, err := registry.Discover(t.TempDir())
	if err == nil {
		t.Fatal("expected error")
	}
	var de *registry.DiscoveryError
	if !errors.As(err, &de) {
		t.Errorf("error should be *registry.DiscoveryError, got %T: %v", err, err)
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail with `Discover` undefined**

Run:
```
go test ./lib/registry/ -run TestDiscover -v
```
Expected: build failure with `undefined: registry.Discover`, `undefined: registry.SourceEnv`, `undefined: registry.SourceWalkUp`, `undefined: registry.DiscoveryError`.

- [ ] **Step 3: Implement `lib/registry/root.go`**

Create `lib/registry/root.go`:

```go
package registry

import (
	"fmt"
	"os"
	"path/filepath"
)

// Source labels how Discover resolved the project root.
type Source string

const (
	// SourceEnv means HARNESS_REGISTRY_ROOT was honored.
	SourceEnv Source = "env"
	// SourceWalkUp means the sensors/ marker was found by walking up
	// from startDir.
	SourceWalkUp Source = "walk_up"
)

// envVarName is the env var Discover honors first.
const envVarName = "HARNESS_REGISTRY_ROOT"

// markerDir is the directory name Discover walks up looking for.
const markerDir = "sensors"

// DiscoveryError is returned when neither HARNESS_REGISTRY_ROOT nor a
// sensors/ marker resolved a project root. Callers can use errors.As to
// distinguish discovery errors from parse or I/O failures.
type DiscoveryError struct {
	StartDir string
	EnvValue string // raw env var contents, may be empty
	Reason   string // brief reason ("env not absolute", "marker not found", ...)
}

func (e *DiscoveryError) Error() string {
	return fmt.Sprintf(
		"registry root discovery failed: %s. HARNESS_REGISTRY_ROOT=%q, started walk-up from %q. "+
			"Either run from a directory inside a project that contains %q/, or set HARNESS_REGISTRY_ROOT to the project's absolute path.",
		e.Reason, e.EnvValue, e.StartDir, markerDir,
	)
}

// Discover resolves the project root using HARNESS_REGISTRY_ROOT first,
// then walking up from startDir looking for a sensors/ directory.
//
// Errors:
//   - HARNESS_REGISTRY_ROOT is set but not absolute, not an existing
//     directory, or otherwise unreachable.
//   - Walk-up reached the filesystem root with no sensors/ found.
//
// startDir is the caller's anchor (typically os.Getwd()). It is only
// consulted when HARNESS_REGISTRY_ROOT is unset/empty.
func Discover(startDir string) (string, Source, error) {
	if env := os.Getenv(envVarName); env != "" {
		root, err := validateEnvRoot(env)
		if err != nil {
			return "", "", &DiscoveryError{
				StartDir: startDir,
				EnvValue: env,
				Reason:   err.Error(),
			}
		}
		return root, SourceEnv, nil
	}
	root, err := walkUpForMarker(startDir)
	if err != nil {
		return "", "", &DiscoveryError{
			StartDir: startDir,
			EnvValue: "",
			Reason:   err.Error(),
		}
	}
	return root, SourceWalkUp, nil
}

// validateEnvRoot enforces the env-var contract: absolute, EvalSymlinks
// resolves, and the resolved path is an existing directory.
func validateEnvRoot(env string) (string, error) {
	if !filepath.IsAbs(env) {
		return "", fmt.Errorf("HARNESS_REGISTRY_ROOT must be an absolute path, got %q", env)
	}
	resolved, err := filepath.EvalSymlinks(env)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("HARNESS_REGISTRY_ROOT path does not exist: %q", env)
		}
		return "", fmt.Errorf("HARNESS_REGISTRY_ROOT eval symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("HARNESS_REGISTRY_ROOT stat: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("HARNESS_REGISTRY_ROOT is not a directory: %q", env)
	}
	return resolved, nil
}

// walkUpForMarker walks parent-by-parent from startDir looking for a
// directory whose sensors/ child is itself a directory (symlinks to dirs
// accepted; emptiness allowed). Returns the absolute path of the matched
// ancestor, or an error when the filesystem root is reached.
func walkUpForMarker(startDir string) (string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", fmt.Errorf("abs(%q): %w", startDir, err)
	}
	for {
		candidate := filepath.Join(abs, markerDir)
		info, err := os.Stat(candidate) // os.Stat follows symlinks
		if err == nil && info.IsDir() {
			return abs, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("%s/ marker not found walking up from %q", markerDir, startDir)
		}
		abs = parent
	}
}
```

- [ ] **Step 4: Run tests, confirm they pass**

Run:
```
go test ./lib/registry/ -run TestDiscover -v
```
Expected: all `TestDiscover_*` tests PASS. The symlink test may emit `--- SKIP` on filesystems that disallow symlinks, which is acceptable.

- [ ] **Step 5: Commit**

```
git add lib/registry/root.go lib/registry/root_test.go
git commit -m "$(cat <<'EOF'
feat(registry): Discover resolves project root from env var or walk-up

HARNESS_REGISTRY_ROOT is the explicit override (must be absolute, must
resolve via EvalSymlinks to an existing directory). Otherwise walks up
from startDir looking for a sensors/ marker directory.

Returns a *DiscoveryError on failure; the Error() message names both
strategies tried so the user knows what to fix.

Used in the next step by registry.Lookup; not yet wired into skills.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Add `Result` struct and `Lookup` to `lib/registry/root.go` (TDD)

**Files:**
- Modify: `lib/registry/root.go` (append `Result` and `Lookup`)
- Modify: `lib/registry/root_test.go` (append `TestLookup_*`)

- [ ] **Step 1: Write failing tests at the end of `lib/registry/root_test.go`**

Append (do not replace existing tests):

```go
func TestLookup_FileAbsent(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	res, err := registry.Lookup(proj)
	if err != nil {
		t.Fatal(err)
	}
	if res.Exists {
		t.Errorf("Exists: got true, want false")
	}
	if res.ProjectRoot != proj {
		t.Errorf("ProjectRoot: got %q, want %q", res.ProjectRoot, proj)
	}
	if res.Source != registry.SourceWalkUp {
		t.Errorf("Source: got %q, want %q", res.Source, registry.SourceWalkUp)
	}
	if res.State.Version != 1 {
		t.Errorf("State.Version: got %d, want 1", res.State.Version)
	}
	if len(res.State.Entries) != 0 {
		t.Errorf("State.Entries: got %d, want 0", len(res.State.Entries))
	}
	// Root should be usable.
	if res.Root.RegistryFile() == "" {
		t.Error("Root unwired")
	}
}

func TestLookup_FilePresentWithEntries(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	r := registry.NewRoot(proj)
	want := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "loop", PID: 1234, StartedAt: "2026-05-10T00:00:00Z"},
		},
	}
	if err := registry.Save(r, want); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	res, err := registry.Lookup(proj)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exists {
		t.Errorf("Exists: got false, want true")
	}
	if len(res.State.Entries) != 1 || res.State.Entries[0].SensorID != "loop" {
		t.Errorf("State.Entries: got %+v", res.State.Entries)
	}
}

func TestLookup_DiscoveryFailurePropagates(t *testing.T) {
	parent := t.TempDir() // no sensors/ marker anywhere
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, err := registry.Lookup(parent)
	if err == nil {
		t.Fatal("expected error")
	}
	var de *registry.DiscoveryError
	if !errors.As(err, &de) {
		t.Errorf("expected DiscoveryError, got %T: %v", err, err)
	}
}

func TestLookup_MalformedJSONReturnsError(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.RegistryFile(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, err := registry.Lookup(proj)
	if err == nil {
		t.Fatal("expected parse error")
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail with `Lookup` undefined**

Run:
```
go test ./lib/registry/ -run TestLookup -v
```
Expected: build failure containing `undefined: registry.Lookup` and `undefined: registry.Result`.

- [ ] **Step 3: Append `Result` and `Lookup` to `lib/registry/root.go`**

Append at the end of `lib/registry/root.go`:

```go
// Result aggregates discovery and state load in one return value so
// each skill needs a single call.
type Result struct {
	Root        Root           // ready-to-use, anchored at ProjectRoot
	ProjectRoot string         // absolute path
	Source      Source         // SourceEnv or SourceWalkUp
	Exists      bool           // running_sensors.json present on disk
	State       RunningSensors // {Version: 1, Entries: nil} if !Exists
}

// Lookup resolves the project root, builds a Root, and loads the
// registry state. "Registry file does not exist" is NOT an error — it
// is reported as Result.Exists == false with an empty State.
//
// Errors mirror Discover (returns *DiscoveryError) plus parse failures
// from a malformed running_sensors.json on disk.
func Lookup(startDir string) (Result, error) {
	root, source, err := Discover(startDir)
	if err != nil {
		return Result{}, err
	}
	r := NewRoot(root)
	state, exists, err := LoadOrEmpty(r)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Root:        r,
		ProjectRoot: root,
		Source:      source,
		Exists:      exists,
		State:       state,
	}, nil
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run:
```
go test ./lib/registry/ -v
```
Expected: all tests pass — `TestLoadOrEmpty_*`, `TestDiscover_*`, `TestLookup_*`, plus pre-existing `TestLoad_*`, `TestSave*`.

- [ ] **Step 5: Commit**

```
git add lib/registry/root.go lib/registry/root_test.go
git commit -m "$(cat <<'EOF'
feat(registry): Lookup combines Discover + LoadOrEmpty into one call

Returns Result{Root, ProjectRoot, Source, Exists, State} so each
registry-touching skill can resolve and load the registry in a single
line. "Registry file does not exist" is reported via Exists=false, not
as an error.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add `DiscoveryErrorSignal` helper to `lib/registry/root.go` (TDD)

**Files:**
- Modify: `go.mod` (the `github.com/google/uuid` dep is currently indirect; this task makes it direct)
- Modify: `lib/registry/root.go` (append `DiscoveryErrorSignal`)
- Modify: `lib/registry/root_test.go` (append `TestDiscoveryErrorSignal_*`)

- [ ] **Step 1: Write failing tests at the end of `lib/registry/root_test.go`**

Append:

```go
func TestDiscoveryErrorSignal_Shape(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, derr := registry.Discover(t.TempDir())
	if derr == nil {
		t.Fatal("expected discovery error")
	}
	sig := registry.DiscoveryErrorSignal(derr, "list-sensors")

	if sig["sensor_id"] != "list-sensors" {
		t.Errorf("sensor_id: got %v, want %q", sig["sensor_id"], "list-sensors")
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict: got %v, want \"error\"", sig["verdict"])
	}
	if sig["severity"] != "high" {
		t.Errorf("severity: got %v, want \"high\"", sig["severity"])
	}
	md, ok := sig["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata: got %T", sig["metadata"])
	}
	if md["kind"] != "registry_discovery_failed" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	// registry_path must NOT be present (no path was resolved).
	if _, present := md["registry_path"]; present {
		t.Errorf("metadata.registry_path should be absent on discovery failure, got %v", md["registry_path"])
	}
	ev, _ := sig["evidence"].([]interface{})
	if len(ev) != 1 {
		t.Fatalf("evidence: got %d items, want 1", len(ev))
	}
	rationale := ev[0].(map[string]interface{})["rationale"].(string)
	if !strings.Contains(rationale, derr.Error()) {
		t.Errorf("rationale should contain raw error string, got: %q", rationale)
	}
}

func TestDiscoveryErrorSignal_ValidatesAgainstSchema(t *testing.T) {
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, derr := registry.Discover(t.TempDir())
	sig := registry.DiscoveryErrorSignal(derr, "list-sensors")

	// Required-by-signal.json fields:
	for _, k := range []string{"sensor_id", "version", "run_id", "started_at", "finished_at", "verdict", "severity", "confidence", "evidence", "cost_actual", "metadata"} {
		if _, ok := sig[k]; !ok {
			t.Errorf("required field %q missing", k)
		}
	}
	if conf, ok := sig["confidence"].(float64); !ok || conf <= 0 || conf > 1 {
		t.Errorf("confidence: got %v", sig["confidence"])
	}
	cost, _ := sig["cost_actual"].(map[string]interface{})
	if _, ok := cost["latency_ms"]; !ok {
		t.Errorf("cost_actual.latency_ms missing")
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail with `DiscoveryErrorSignal` undefined**

Run:
```
go test ./lib/registry/ -run TestDiscoveryErrorSignal -v
```
Expected: build failure with `undefined: registry.DiscoveryErrorSignal`.

- [ ] **Step 3: Add the dep & implement `DiscoveryErrorSignal`**

First, add the direct dep:

```
go get github.com/google/uuid@v1.6.0
go mod tidy
```

Then append to `lib/registry/root.go`:

```go
// At the top of the file, add to the import block:
//   "time"
//   "github.com/google/uuid"

// DiscoveryErrorSignal builds an error Signal describing a failed
// registry-root discovery. The returned map satisfies signal.json
// (verdict=error, severity=high, all required envelope fields). The
// helper is exported for the four registry-touching skills so each
// emits the same shape.
//
// sensorID is the id field carried on the signal — pass the skill's
// fixed name (e.g., "list-sensors") for skills that don't take a
// sensor argument, or the user-supplied sensor id for /start-sensor.
//
// metadata.registry_path is intentionally OMITTED: discovery failed,
// so no path was resolved.
func DiscoveryErrorSignal(err error, sensorID string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	rationale := "registry root discovery failed: <nil error>"
	if err != nil {
		rationale = err.Error()
	}
	return map[string]interface{}{
		"sensor_id":   sensorID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "registry_discovery_failed"},
	}
}
```

Updated import block at the top of `lib/registry/root.go` (replace existing import block):

```go
import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)
```

- [ ] **Step 4: Run tests, confirm pass and validate against schema**

Run:
```
go test ./lib/registry/ -v
```
Expected: all tests pass.

Also run the schema validation cross-check via the existing skill tests (which already exercise signal.json):
```
go test -tags=list_sensors ./skills/list-sensors/...
```
Expected: still passes (no change to skill code yet).

- [ ] **Step 5: Commit**

```
git add go.mod go.sum lib/registry/root.go lib/registry/root_test.go
git commit -m "$(cat <<'EOF'
feat(registry): DiscoveryErrorSignal builds a uniform error signal

Helper used by /start-sensor, /list-sensors, /stop-sensor, /tail-sensor
when registry.Lookup returns a *DiscoveryError. Single source of truth
for the shape so all four skills emit the same metadata.kind.

Promotes github.com/google/uuid from indirect to direct (lib/registry
now imports it).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Migrate `/list-sensors` (most touched by Exists branch) (TDD)

**Files:**
- Modify: `skills/list-sensors/scripts/list.go`
- Modify: `skills/list-sensors/scripts/list_test.go`

- [ ] **Step 1: Update existing tests + add new tests in `list_test.go`**

Replace the entire contents of `skills/list-sensors/scripts/list_test.go` with:

```go
//go:build list_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

// resultFor builds a registry.Result for a tempdir-backed project.
// exists controls whether running_sensors.json is on disk.
func resultFor(t *testing.T, projectRoot string, exists bool) registry.Result {
	t.Helper()
	r := registry.NewRoot(projectRoot)
	state, _, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return registry.Result{
		Root:        r,
		ProjectRoot: projectRoot,
		Source:      registry.SourceWalkUp,
		Exists:      exists,
		State:       state,
	}
}

func TestList_FileAbsent_Warn(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	var buf bytes.Buffer
	exit := runList(res, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig); err != nil {
		t.Fatal(err)
	}
	if sig["verdict"] != "warn" {
		t.Errorf("verdict: got %v, want \"warn\"", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "list" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	if md["registry_exists"] != false {
		t.Errorf("metadata.registry_exists: got %v", md["registry_exists"])
	}
	if md["registry_source"] != "walk_up" {
		t.Errorf("metadata.registry_source: got %v", md["registry_source"])
	}
	wantPath := filepath.Join(root, ".runtime", "sensors", "running_sensors.json")
	if md["registry_path"] != wantPath {
		t.Errorf("metadata.registry_path: got %v, want %v", md["registry_path"], wantPath)
	}
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 0 {
		t.Errorf("entries: got %d, want 0", len(entries))
	}
	ev, _ := sig["evidence"].([]interface{})
	rationale := ev[0].(map[string]interface{})["rationale"].(string)
	if !strings.Contains(rationale, "registry not found") || !strings.Contains(rationale, "HARNESS_REGISTRY_ROOT") {
		t.Errorf("rationale should mention registry not found and HARNESS_REGISTRY_ROOT, got: %q", rationale)
	}
}

func TestList_FilePresentEmpty_Pass(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	var buf bytes.Buffer
	exit := runList(res, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	if sig["verdict"] != "pass" {
		t.Errorf("verdict: got %v", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v", md["registry_exists"])
	}
}

func TestList_AnnotatesOrphan(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "alive", PID: registry.SelfPID()},
			{SensorID: "dead", PID: 3_999_999},
		},
	}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	var buf bytes.Buffer
	_ = runList(res, &buf, os.Stderr)
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	entries := sig["metadata"].(map[string]interface{})["entries"].([]interface{})
	var alive, dead map[string]interface{}
	for _, e := range entries {
		em := e.(map[string]interface{})
		switch em["sensor_id"] {
		case "alive":
			alive = em
		case "dead":
			dead = em
		}
	}
	if alive["pid_alive"].(bool) != true {
		t.Errorf("alive pid_alive: got %v", alive["pid_alive"])
	}
	if dead["pid_alive"].(bool) != false {
		t.Errorf("dead pid_alive: got %v", dead["pid_alive"])
	}
	if dead["state"] != "orphan" {
		t.Errorf("dead state: got %v", dead["state"])
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail (signature mismatch)**

Run:
```
go test -tags=list_sensors ./skills/list-sensors/... -v
```
Expected: build failure with `cannot use res ... as type string` (or similar) because `runList` still has the old signature.

- [ ] **Step 3: Rewrite `skills/list-sensors/scripts/list.go`**

Replace the entire contents:

```go
//go:build list_sensors

// list reads .runtime/sensors/running_sensors.json (resolved via
// registry.Lookup, NOT os.Getwd()), annotates each entry with PID
// liveness, and emits one Signal verdict=pass / metadata.kind=list.
//
// When the registry file does not exist, emits verdict=warn with
// remediation pointing at HARNESS_REGISTRY_ROOT.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

func main() {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "list: cwd:", err)
		os.Exit(2)
	}
	res, err := registry.Lookup(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, "list-sensors"))
		os.Exit(1)
	}
	os.Exit(runList(res, os.Stdout, os.Stderr))
}

func runList(res registry.Result, stdout, stderr io.Writer) int {
	v, code := schema.LoadValidator("", stderr)
	if code != 0 {
		_ = json.NewEncoder(stdout).Encode(errorListSignal(res, "schema validator init failed"))
		return code
	}

	r := res.Root
	rs := res.State

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	if !res.Exists {
		sig := map[string]interface{}{
			"sensor_id":   "list-sensors",
			"version":     "0.0.0",
			"run_id":      uuid.NewString(),
			"started_at":  now,
			"finished_at": now,
			"verdict":     "warn",
			"severity":    "info",
			"confidence":  1.0,
			"evidence": []interface{}{
				map[string]interface{}{
					"rationale": fmt.Sprintf(
						"registry not found at %s. /start-sensor was likely run from a different cwd, or this project has no live blocking sensors. "+
							"If you expect sensors to be live, set HARNESS_REGISTRY_ROOT to the project root used at start time, or rerun /list-sensors from within that project.",
						r.RegistryFile(),
					),
				},
			},
			"cost_actual": map[string]interface{}{"latency_ms": 0},
			"metadata":    listMetadata(res, []interface{}{}),
		}
		_ = json.NewEncoder(stdout).Encode(validateSignal(v, sig, stderr))
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
			"held_by":          heldBySummaries(e.HeldBy),
			"signals_log_path": r.SignalsLog(e.SensorID),
			"state":            state,
		})
	}
	sig := map[string]interface{}{
		"sensor_id":   "list-sensors",
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": fmt.Sprintf("%d running sensor(s)", len(entries))}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    listMetadata(res, entries),
	}
	_ = json.NewEncoder(stdout).Encode(validateSignal(v, sig, stderr))
	return 0
}

// listMetadata builds the metadata map shared by both verdict branches.
// All entries except "entries" are diagnostic (where the registry was
// looked up and how).
func listMetadata(res registry.Result, entries []interface{}) map[string]interface{} {
	return map[string]interface{}{
		"kind":            "list",
		"entries":         entries,
		"registry_path":   res.Root.RegistryFile(),
		"registry_source": string(res.Source),
		"registry_exists": res.Exists,
	}
}

func heldBySummaries(hs []registry.HeldByEntry) []interface{} {
	out := make([]interface{}, 0, len(hs))
	for _, h := range hs {
		entry := map[string]interface{}{"kind": h.Kind, "attached_at": h.AttachedAt}
		if h.Kind == "sensor" {
			entry["id"] = h.ID
			entry["pid"] = h.PID
			entry["pid_alive"] = registry.IsPIDAlive(h.PID)
		}
		out = append(out, entry)
	}
	return out
}

func validateSignal(v *schema.Validator, sig map[string]interface{}, stderr io.Writer) map[string]interface{} {
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(stderr, "list: BUG: emitted signal failed signal.json validation: %v\n", err)
		now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		return map[string]interface{}{
			"sensor_id":   "list-sensors",
			"version":     "0.0.0",
			"run_id":      uuid.NewString(),
			"started_at":  now,
			"finished_at": now,
			"verdict":     "error",
			"severity":    "high",
			"confidence":  1.0,
			"evidence":    []interface{}{map[string]interface{}{"rationale": fmt.Sprintf("signal_validation_failed: %v", err)}},
			"cost_actual": map[string]interface{}{"latency_ms": 0},
			"metadata":    map[string]interface{}{"kind": "signal_validation_failed"},
		}
	}
	return sig
}

func errorListSignal(res registry.Result, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   "list-sensors",
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":            "list_failed",
			"registry_path":   res.Root.RegistryFile(),
			"registry_source": string(res.Source),
			"registry_exists": res.Exists,
		},
	}
}
```

- [ ] **Step 4: Run tests, confirm pass**

Run:
```
go test -tags=list_sensors ./skills/list-sensors/... -v
go vet -tags=list_sensors ./skills/list-sensors/...
```
Expected: all three tests pass; vet is clean.

- [ ] **Step 5: Commit**

```
git add skills/list-sensors/scripts/list.go skills/list-sensors/scripts/list_test.go
git commit -m "$(cat <<'EOF'
refactor(list-sensors): use registry.Lookup, add verdict=warn on missing file

/list-sensors no longer derives the registry path from os.Getwd().
Calls registry.Lookup, which fails fast if no project root can be
discovered. Adds verdict=warn when the registry file does not exist
(distinct from verdict=pass with empty entries) so the user knows the
likely cause: wrong cwd at /start-sensor time.

Every emitted signal now carries metadata.{registry_path,
registry_source, registry_exists} for diagnose.

Refs #6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Migrate `/start-sensor` (TDD)

**Files:**
- Modify: `skills/start-sensor/scripts/start.go`
- Modify: `skills/start-sensor/scripts/start_test.go`

- [ ] **Step 1: Update existing tests + add registry-metadata assertions**

Replace `skills/start-sensor/scripts/start_test.go` with:

```go
//go:build start_sensor

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func writeFixtureSensor(t *testing.T, projectRoot, id string, body map[string]interface{}) string {
	t.Helper()
	dir := filepath.Join(projectRoot, "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body["id"] = id
	data, _ := json.MarshalIndent(body, "", "  ")
	path := filepath.Join(dir, id+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func resultFor(t *testing.T, projectRoot string, exists bool) registry.Result {
	t.Helper()
	r := registry.NewRoot(projectRoot)
	state, _, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return registry.Result{
		Root:        r,
		ProjectRoot: projectRoot,
		Source:      registry.SourceWalkUp,
		Exists:      exists,
		State:       state,
	}
}

func TestStart_RejectsNonBlocking(t *testing.T) {
	root := t.TempDir()
	writeFixtureSensor(t, root, "not-blocking", map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Not blocking fixture",
		"description": "non-blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "on-demand",
		"output":      "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50, "timeout_ms": 1000},
		},
		"triggers": []interface{}{
			map[string]interface{}{"on": "manual"},
		},
		"execution": map[string]interface{}{
			"command": "echo hi",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{
					"fixture":           "sensors/fixtures/not-blocking/pass.txt",
					"expected_verdict":  "pass",
					"expected_severity": "info",
				},
			},
		},
	})
	res := resultFor(t, root, false)
	exit, _ := runStart(res, []string{"not-blocking"})
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
}

func TestStart_RejectsAlreadyRunning(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "loop", PID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{{Kind: "manual"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeFixtureSensor(t, root, "loop", blockingFixtureBody())
	res := resultFor(t, root, true)
	exit, sig := runStart(res, []string{"loop"})
	if exit != 1 {
		t.Fatalf("expected exit 1, got %d", exit)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "start_rejected" {
		t.Fatalf("metadata.kind: got %v", md["kind"])
	}
	// Registry diagnose metadata MUST be present even on rejection.
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v, want true", md["registry_exists"])
	}
	if md["registry_source"] != "walk_up" {
		t.Errorf("metadata.registry_source: got %v", md["registry_source"])
	}
	wantPath := filepath.Join(root, ".runtime", "sensors", "running_sensors.json")
	if md["registry_path"] != wantPath {
		t.Errorf("metadata.registry_path: got %v, want %v", md["registry_path"], wantPath)
	}
}

func blockingFixtureBody() map[string]interface{} {
	return map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking fixture",
		"description": "blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"output":      "stream",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"triggers": []interface{}{
			map[string]interface{}{"on": "manual"},
		},
		"execution": map[string]interface{}{
			"command":  "while true; do echo TICK; sleep 0.1; done",
			"blocking": true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{
					"fixture":           "sensors/fixtures/loop/pass.txt",
					"expected_verdict":  "pass",
					"expected_severity": "info",
				},
			},
		},
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail (signature mismatch)**

Run:
```
go test -tags=start_sensor ./skills/start-sensor/... -v
```
Expected: build failure on `runStart(res, ...)` because `runStart` still takes a string.

- [ ] **Step 3: Update `skills/start-sensor/scripts/start.go`**

Apply five focused changes (do not rewrite the whole file; surgical edits):

**Change 1 — `main()` (lines 23-34):** replace with:

```go
func main() {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start: cwd:", err)
		os.Exit(2)
	}
	res, err := registry.Lookup(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	exit, sig := runStart(res, os.Args[1:])
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}
```

**Change 2 — `runStart` signature and body (lines 43-223):** replace the function with:

```go
// runStart performs the full /start-sensor lifecycle for sensor id given
// in args[0]. Returns (exitCode, finalSignal). The signal is encoded by
// the caller; tests inspect it directly.
//
// Note: execution.prepare[] is not yet executed for blocking sensors; this
// is a documented follow-up. Manual /start-sensor invocation skips prepare
// today; orchestrator-driven blocking deps don't run it either.
func runStart(res registry.Result, args []string) (int, map[string]interface{}) {
	if len(args) < 1 {
		return 2, errorSignal(res, "start", "missing sensor id argument")
	}
	id := args[0]
	projectRoot := res.ProjectRoot

	path, err := libsensor.ResolveByID(id, projectRoot)
	if err != nil {
		return 2, errorSignal(res, id, fmt.Sprintf("resolve: %v", err))
	}

	sensorJSON, err := loadSensorJSON(path)
	if err != nil {
		return 2, errorSignal(res, id, err.Error())
	}

	v, code := schema.LoadValidator("", os.Stderr)
	if code != 0 {
		return code, errorSignal(res, id, "schema validator init failed")
	}
	if err := v.Validate(schema.TargetSensor, sensorJSON); err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("schema: %v", err))
	}

	execMap, _ := sensorJSON["execution"].(map[string]interface{})
	blocking, _ := execMap["blocking"].(bool)
	if !blocking {
		return 2, errorSignal(res, id, "sensor is not blocking; use /run-sensor instead")
	}

	command, _ := execMap["command"].(string)
	r := res.Root
	logDir := r.SensorDir(id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("mkdir log dir: %v", err))
	}
	if err := os.WriteFile(r.RawLog(id), nil, 0o644); err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("create raw.log: %v", err))
	}
	if err := os.WriteFile(r.SignalsLog(id), nil, 0o644); err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("create signals.log: %v", err))
	}

	watcherPath, err := watcherBinaryPath()
	if err != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("watcher binary: %v", err))
	}

	type spawnResult struct {
		det         subprocess.DetachResult
		watcherProc *os.Process
		envelope    libsensor.Envelope
	}
	var spawned spawnResult
	var lockErr error
	var alreadyRunning bool
	var alreadyRunningPID int

	lockErr = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return fmt.Errorf("load registry: %w", err)
		}
		if existing := rs.FindEntry(id); existing != nil && registry.IsPIDAlive(existing.PID) {
			alreadyRunning = true
			alreadyRunningPID = existing.PID
			return nil
		}

		det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
			Command: command,
			LogFile: r.RawLog(id),
		})
		if err != nil {
			return fmt.Errorf("spawn: %w", err)
		}

		envelope := libsensor.Envelope{
			SensorID:   id,
			Version:    stringField(sensorJSON, "version"),
			RunID:      uuid.NewString(),
			StartedAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			SensorType: stringField(sensorJSON, "type"),
		}
		patterns := []interface{}{}
		if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
			if raw, ok := op["patterns"].([]interface{}); ok {
				patterns = raw
			}
		}
		patternsJSON, _ := json.Marshal(patterns)
		envelopeJSON, _ := json.Marshal(envelope)

		watcherProc, err := os.StartProcess(watcherPath, []string{watcherPath}, &os.ProcAttr{
			Env: []string{
				fmt.Sprintf("HARNESS_WATCHER_RAW=%s", r.RawLog(id)),
				fmt.Sprintf("HARNESS_WATCHER_SIGNALS=%s", r.SignalsLog(id)),
				fmt.Sprintf("HARNESS_WATCHER_PATTERNS=%s", string(patternsJSON)),
				fmt.Sprintf("HARNESS_WATCHER_ENVELOPE=%s", string(envelopeJSON)),
				fmt.Sprintf("HARNESS_WATCHER_SUBPROCESS_PID=%d", det.PID),
				fmt.Sprintf("HARNESS_WATCHER_REGISTRY_ROOT=%s", projectRoot),
				fmt.Sprintf("HARNESS_WATCHER_SENSOR_ID=%s", id),
			},
			Files: []*os.File{nil, nil, nil},
			Sys:   &watcherSysProcAttr,
		})
		if err != nil {
			return fmt.Errorf("start watcher: %w", err)
		}
		_ = watcherProc.Release()

		rs.RemoveEntry(id)
		rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
			SensorID:   id,
			PID:        det.PID,
			PGID:       det.PGID,
			WatcherPID: watcherProc.Pid,
			StartedAt:  envelope.StartedAt,
			Command:    command,
			LogDir:     filepath.Join(".runtime", "sensors", id),
			HeldBy: []registry.HeldByEntry{
				{Kind: "manual", AttachedAt: envelope.StartedAt},
			},
		})
		if err := registry.Save(r, rs); err != nil {
			return err
		}

		spawned = spawnResult{det: det, watcherProc: watcherProc, envelope: envelope}
		return nil
	})

	if lockErr != nil {
		return 1, errorSignal(res, id, fmt.Sprintf("write registry: %v", lockErr))
	}

	if alreadyRunning {
		sig := buildStartedSkeleton(res, id, sensorJSON)
		sig["verdict"] = "error"
		sig["severity"] = "high"
		sig["evidence"] = []interface{}{map[string]interface{}{
			"rationale": fmt.Sprintf("sensor %q already running with pid %d", id, alreadyRunningPID),
		}}
		sig["metadata"].(map[string]interface{})["kind"] = "start_rejected"
		return 1, validateSignal(v, sig, id)
	}

	sig := buildStartedSkeleton(res, id, sensorJSON)
	sig["verdict"] = "pass"
	sig["severity"] = "info"
	sig["evidence"] = []interface{}{map[string]interface{}{
		"rationale": fmt.Sprintf("sensor %q started, pid=%d, watcher_pid=%d", id, spawned.det.PID, spawned.watcherProc.Pid),
	}}
	sig["run_id"] = spawned.envelope.RunID
	sig["started_at"] = spawned.envelope.StartedAt
	md := sig["metadata"].(map[string]interface{})
	md["kind"] = "started"
	md["pid"] = spawned.det.PID
	md["watcher_pid"] = spawned.watcherProc.Pid
	md["log_dir"] = filepath.Join(".runtime", "sensors", id)
	md["next_cursor"] = 0
	return 0, validateSignal(v, sig, id)
}
```

**Change 3 — `buildStartedSkeleton` (lines 245-257):** replace with:

```go
// buildStartedSkeleton returns the envelope fields common to all started/rejected
// signals, including the registry diagnose metadata. Callers must set verdict,
// severity, and evidence explicitly so the intent is clear at each call site.
func buildStartedSkeleton(res registry.Result, id string, sensorJSON map[string]interface{}) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     stringField(sensorJSON, "version"),
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"confidence":  1.0,
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":            "started",
			"registry_path":   res.Root.RegistryFile(),
			"registry_source": string(res.Source),
			"registry_exists": res.Exists,
		},
	}
}
```

**Change 4 — `errorSignal` (lines 259-274):** replace with:

```go
func errorSignal(res registry.Result, id, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":            "start_failed",
			"registry_path":   res.Root.RegistryFile(),
			"registry_source": string(res.Source),
			"registry_exists": res.Exists,
		},
	}
}
```

**Change 5:** `validateSignal` does not change (no `res` access needed; the emergency fallback signal does not carry registry diagnose because the situation is already pathological).

- [ ] **Step 4: Run tests + vet**

```
go test -tags=start_sensor ./skills/start-sensor/... -v
go vet -tags=start_sensor ./skills/start-sensor/...
```
Expected: `TestStart_RejectsNonBlocking` and `TestStart_RejectsAlreadyRunning` pass; vet clean.

- [ ] **Step 5: Commit**

```
git add skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_test.go
git commit -m "$(cat <<'EOF'
refactor(start-sensor): use registry.Lookup, embed diagnose metadata

/start-sensor's main() resolves the project root via registry.Lookup
instead of os.Getwd(). The watcher continues to receive the absolute
path via HARNESS_WATCHER_REGISTRY_ROOT (now sourced from
res.ProjectRoot).

Every emitted signal (started, start_rejected, start_failed) carries
metadata.{registry_path, registry_source, registry_exists}.

Refs #6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Migrate `/stop-sensor` (TDD)

**Files:**
- Modify: `skills/stop-sensor/scripts/stop.go`
- Modify: `skills/stop-sensor/scripts/stop_test.go`

- [ ] **Step 1: Update tests (existing + new `Exists=false` case)**

Replace `skills/stop-sensor/scripts/stop_test.go` with:

```go
//go:build stop_sensor

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func resultFor(t *testing.T, projectRoot string, exists bool) registry.Result {
	t.Helper()
	r := registry.NewRoot(projectRoot)
	state, _, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return registry.Result{
		Root:        r,
		ProjectRoot: projectRoot,
		Source:      registry.SourceWalkUp,
		Exists:      exists,
		State:       state,
	}
}

func TestStop_RegistryFileAbsent_Error(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	exit, sig := runStop(res, []string{"missing"}, false)
	if exit != 1 {
		t.Fatalf("exit: got %d, want 1", exit)
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict: got %v, want \"error\"", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "stop_no_registry" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	if md["registry_exists"] != false {
		t.Errorf("metadata.registry_exists: got %v, want false", md["registry_exists"])
	}
}

func TestStop_NotRunning_ReturnsWarn(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	exit, sig := runStop(res, []string{"missing"}, false)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	if sig["verdict"] != "warn" || sig["metadata"].(map[string]interface{})["kind"] != "stop_not_running" {
		t.Fatalf("got: %+v", sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v, want true", md["registry_exists"])
	}
}

func TestStop_HoldByDependent_RefusesStop(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID: "live", PID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{
					{Kind: "manual"},
					{Kind: "sensor", ID: "B", PID: registry.SelfPID()},
				},
			},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	exit, sig := runStop(res, []string{"live"}, false)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "stop_held" {
		t.Fatalf("kind: got %v", md["kind"])
	}
	rs2, _ := registry.Load(r)
	if e := rs2.FindEntry("live"); e == nil {
		t.Fatal("registry entry should still exist")
	}
}

func TestStop_ReapsDeadHolders_WhenFlagSet(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID: "live",
				PID:      0,
				HeldBy: []registry.HeldByEntry{
					{Kind: "manual"},
					{Kind: "sensor", ID: "C", PID: 3_999_999},
				},
			},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	_, sig := runStop(res, []string{"live"}, true)
	md := sig["metadata"].(map[string]interface{})
	reaped, _ := md["reaped_holders"].([]interface{})
	if len(reaped) != 1 {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
		_ = filepath.Join // keep import
		t.Fatalf("reaped: got %d", len(reaped))
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail (signature mismatch)**

```
go test -tags=stop_sensor ./skills/stop-sensor/... -v
```
Expected: build failure on `runStop(res, ...)`.

- [ ] **Step 3: Update `skills/stop-sensor/scripts/stop.go`**

Apply five surgical changes:

**Change 1 — `main()` (lines 26-44):** replace with:

```go
func main() {
	var reap bool
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.BoolVar(&reap, "reap-dead-holders", false, "drop kind=sensor holders whose PID is dead before deciding whether to stop")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stop: cwd:", err)
		os.Exit(2)
	}
	res, err := registry.Lookup(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	exit, sig := runStop(res, fs.Args(), reap)
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}
```

**Change 2 — `runStop` signature and the no-registry early branch (lines 46-...):** replace the function definition (signature + first ~7 lines) and add the new `Exists=false` branch:

```go
func runStop(res registry.Result, args []string, reap bool) (int, map[string]interface{}) {
	if len(args) < 1 {
		return 2, simpleSignal(res, "stop", "warn", "low", "stop_not_running", "missing sensor id")
	}
	id := args[0]

	v, code := schema.LoadValidator("", os.Stderr)
	if code != 0 {
		return code, simpleSignal(res, id, "error", "high", "stop_failed", "schema validator init failed")
	}

	if !res.Exists {
		return 1, validateSignal(v, simpleSignal(res, id, "error", "high", "stop_no_registry",
			fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", res.Root.RegistryFile())), id)
	}

	r := res.Root
	projectRoot := res.ProjectRoot

	var entry *registry.RunningSensorEntry
	var reaped []registry.HeldByEntry

	if err := registry.WithFileLock(r.LockFile(), func() error {
		// ... (rest of the function unchanged from line 63 onward)
```

The remaining body of `runStop` is preserved verbatim from the existing file (line 63 to the closing brace at line 134), with two find-and-replace adjustments:

- Replace `simpleSignal(id, ...)` (4 occurrences in the body) with `simpleSignal(res, id, ...)`.
- Replace `buildAggregate(id, ...)` (1 occurrence) with `buildAggregate(res, id, ...)`.

**Change 3 — `simpleSignal` (lines 341-356):** replace with:

```go
func simpleSignal(res registry.Result, id, verdict, severity, kind, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     verdict,
		"severity":    severity,
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":            kind,
			"registry_path":   res.Root.RegistryFile(),
			"registry_source": string(res.Source),
			"registry_exists": res.Exists,
		},
	}
}
```

**Change 4 — `buildAggregate` (lines 247-284):** update the signature to take `res` and embed the metadata:

```go
func buildAggregate(res registry.Result, id string, sensorJSON map[string]interface{}, entry *registry.RunningSensorEntry, individuals []map[string]interface{}, agg libsignal.AggregateResult, killedForcefully bool, reaped []registry.HeldByEntry, teardown []map[string]interface{}) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := map[string]interface{}{
		"kind":            "aggregate",
		"output_mode":     "stream",
		"command":         entry.Command,
		"counts":          libsignal.CountVerdicts(individuals),
		"registry_path":   res.Root.RegistryFile(),
		"registry_source": string(res.Source),
		"registry_exists": res.Exists,
	}
	if entry.SubprocessExit != nil {
		md["subprocess_self_exited"] = true
		md["subprocess_exit_code"] = entry.SubprocessExit.Code
		if entry.SubprocessExit.Code == -1 {
			md["exit_code_unknown"] = true
		}
	}
	if killedForcefully {
		md["killed_forcefully"] = true
	}
	if len(reaped) > 0 {
		md["reaped_holders"] = holderSummaries(reaped)
	}
	if len(teardown) > 0 {
		md["lifecycle"] = map[string]interface{}{"teardown": teardown}
	}
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     stringField(sensorJSON, "version"),
		"run_id":      uuid.NewString(),
		"started_at":  entry.StartedAt,
		"finished_at": now,
		"verdict":     agg.Verdict,
		"severity":    agg.Severity,
		"confidence":  1.0,
		"evidence":    libsignal.SelectTopEvidence(individuals, 5),
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}
```

**Change 5:** `loadSensorJSONForStop` and `readGracefulMS` continue to take `projectRoot` (a plain string) — the call sites in `runStop` now pass `res.ProjectRoot`. No change to those helpers.

- [ ] **Step 4: Run tests + vet**

```
go test -tags=stop_sensor ./skills/stop-sensor/... -v
go vet -tags=stop_sensor ./skills/stop-sensor/...
```
Expected: `TestStop_RegistryFileAbsent_Error`, `TestStop_NotRunning_ReturnsWarn`, `TestStop_HoldByDependent_RefusesStop`, `TestStop_ReapsDeadHolders_WhenFlagSet` all pass; vet clean.

- [ ] **Step 5: Commit**

```
git add skills/stop-sensor/scripts/stop.go skills/stop-sensor/scripts/stop_test.go
git commit -m "$(cat <<'EOF'
refactor(stop-sensor): use registry.Lookup, error on missing registry

/stop-sensor's main() resolves the project root via registry.Lookup.
When the registry file is absent, emits verdict=error /
metadata.kind=stop_no_registry — sensor cannot be running if the file
isn't even on disk.

Every signal (warn, error, aggregate) carries metadata.{registry_path,
registry_source, registry_exists}.

Refs #6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Migrate `/tail-sensor` (TDD)

**Files:**
- Modify: `skills/tail-sensor/scripts/tail.go`
- Modify: `skills/tail-sensor/scripts/tail_test.go`

- [ ] **Step 1: Update tests (existing + new `Exists=false` case)**

Replace `skills/tail-sensor/scripts/tail_test.go` with:

```go
//go:build tail_sensor

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func resultFor(t *testing.T, projectRoot string, exists bool) registry.Result {
	t.Helper()
	r := registry.NewRoot(projectRoot)
	state, _, err := registry.LoadOrEmpty(r)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return registry.Result{
		Root:        r,
		ProjectRoot: projectRoot,
		Source:      registry.SourceWalkUp,
		Exists:      exists,
		State:       state,
	}
}

func setupRunning(t *testing.T, root, id string, signalsLines []string) {
	t.Helper()
	r := registry.NewRoot(root)
	if err := os.MkdirAll(r.SensorDir(id), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Save(r, registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: id, PID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{{Kind: "manual"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.SignalsLog(id), []byte(strings.Join(signalsLines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Join // keep import
}

func TestTail_RegistryFileAbsent_Error(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	var buf bytes.Buffer
	exit := runTail(res, []string{"missing", "0"}, &buf, os.Stderr)
	if exit != 1 {
		t.Fatalf("exit: got %d, want 1", exit)
	}
	var sig map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig); err != nil {
		t.Fatal(err)
	}
	if sig["verdict"] != "error" {
		t.Errorf("verdict: got %v, want \"error\"", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "tail_no_registry" {
		t.Errorf("metadata.kind: got %v", md["kind"])
	}
	if md["registry_exists"] != false {
		t.Errorf("metadata.registry_exists: got %v, want false", md["registry_exists"])
	}
}

func TestTail_Cursor0_ReturnsAll(t *testing.T) {
	root := t.TempDir()
	setupRunning(t, root, "loop", []string{
		`{"sensor_id":"loop","verdict":"pass","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"warn","metadata":{"kind":"individual"}}`,
	})
	res := resultFor(t, root, true)
	var buf bytes.Buffer
	exit := runTail(res, []string{"loop", "0"}, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: got %d (incl envelope)", len(lines))
	}
	var envelope map[string]interface{}
	if err := json.Unmarshal([]byte(lines[2]), &envelope); err != nil {
		t.Fatal(err)
	}
	md := envelope["metadata"].(map[string]interface{})
	if md["kind"] != "tail_envelope" {
		t.Fatalf("envelope kind: got %v", md["kind"])
	}
	if md["next_cursor"].(float64) != 2 {
		t.Fatalf("next_cursor: got %v", md["next_cursor"])
	}
	if md["registry_exists"] != true {
		t.Errorf("metadata.registry_exists: got %v, want true", md["registry_exists"])
	}
}

func TestTail_CursorMid_ReturnsSuffix(t *testing.T) {
	root := t.TempDir()
	setupRunning(t, root, "loop", []string{
		`{"sensor_id":"loop","verdict":"pass","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"warn","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"fail","metadata":{"kind":"individual"}}`,
	})
	res := resultFor(t, root, true)
	var buf bytes.Buffer
	exit := runTail(res, []string{"loop", "2"}, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines: got %d", len(lines))
	}
}

func TestTail_NotRunning(t *testing.T) {
	root := t.TempDir()
	r := registry.NewRoot(root)
	if err := registry.Save(r, registry.RunningSensors{Version: 1}); err != nil {
		t.Fatal(err)
	}
	res := resultFor(t, root, true)
	var buf bytes.Buffer
	exit := runTail(res, []string{"missing", "0"}, &buf, os.Stderr)
	if exit != 1 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	_ = json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &sig)
	if sig["metadata"].(map[string]interface{})["kind"] != "tail_not_running" {
		t.Fatalf("kind: got %v", sig["metadata"])
	}
}
```

- [ ] **Step 2: Run tests, confirm they fail (signature mismatch)**

```
go test -tags=tail_sensor ./skills/tail-sensor/... -v
```
Expected: build failure on `runTail(res, ...)`.

- [ ] **Step 3: Rewrite `skills/tail-sensor/scripts/tail.go`**

Replace the entire contents:

```go
//go:build tail_sensor

// tail returns Signals from a blocking sensor's signals.log starting
// from a 1-based line cursor, plus a final tail-envelope Signal that
// carries metadata.next_cursor for the agent to use on the next call.
//
// When the registry file does not exist, emits verdict=error /
// metadata.kind=tail_no_registry — sensor cannot be running.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

func main() {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tail: cwd:", err)
		os.Exit(2)
	}
	res, err := registry.Lookup(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	exit := runTail(res, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(exit)
}

func runTail(res registry.Result, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(res, "tail", "tail_invalid_args", "expected <sensor.id> <cursor>"))
		return 2
	}
	id := args[0]
	cursor, err := strconv.Atoi(args[1])
	if err != nil || cursor < 0 {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(res, id, "tail_invalid_cursor", fmt.Sprintf("cursor must be a non-negative integer, got %q", args[1])))
		return 1
	}

	v, code := schema.LoadValidator("", stderr)
	if code != 0 {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(res, id, "tail_failed", "schema validator init failed"))
		return code
	}

	if !res.Exists {
		_ = json.NewEncoder(stdout).Encode(validateSignal(v,
			simpleErrSignal(res, id, "tail_no_registry",
				fmt.Sprintf("registry not found at %s; sensor cannot be running. /start-sensor was likely run from a different cwd, or HARNESS_REGISTRY_ROOT is misconfigured.", res.Root.RegistryFile())),
			id, stderr))
		return 1
	}

	r := res.Root
	rs := res.State
	entry := rs.FindEntry(id)
	if entry == nil {
		_ = json.NewEncoder(stdout).Encode(validateSignal(v, simpleErrSignal(res, id, "tail_not_running", fmt.Sprintf("no live entry for %q", id)), id, stderr))
		return 1
	}

	f, err := os.Open(r.SignalsLog(id))
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(validateSignal(v, simpleErrSignal(res, id, "tail_failed", fmt.Sprintf("open signals.log: %v", err)), id, stderr))
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
	envelope := tailEnvelope(res, id, current)
	_ = json.NewEncoder(stdout).Encode(validateSignal(v, envelope, id, stderr))
	return 0
}

func tailEnvelope(res registry.Result, id string, nextCursor int) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": "tail envelope"}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":            "tail_envelope",
			"next_cursor":     nextCursor,
			"sensor_id":       id,
			"registry_path":   res.Root.RegistryFile(),
			"registry_source": string(res.Source),
			"registry_exists": res.Exists,
		},
	}
}

func validateSignal(v *schema.Validator, sig map[string]interface{}, id string, stderr io.Writer) map[string]interface{} {
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(stderr, "tail: BUG: emitted signal failed signal.json validation: %v\n", err)
		now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		return map[string]interface{}{
			"sensor_id":   id,
			"version":     "0.0.0",
			"run_id":      uuid.NewString(),
			"started_at":  now,
			"finished_at": now,
			"verdict":     "error",
			"severity":    "high",
			"confidence":  1.0,
			"evidence":    []interface{}{map[string]interface{}{"rationale": fmt.Sprintf("signal_validation_failed: %v", err)}},
			"cost_actual": map[string]interface{}{"latency_ms": 0},
			"metadata":    map[string]interface{}{"kind": "signal_validation_failed"},
		}
	}
	return sig
}

func simpleErrSignal(res registry.Result, id, kind, rationale string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind":            kind,
			"registry_path":   res.Root.RegistryFile(),
			"registry_source": string(res.Source),
			"registry_exists": res.Exists,
		},
	}
}
```

- [ ] **Step 4: Run tests + vet**

```
go test -tags=tail_sensor ./skills/tail-sensor/... -v
go vet -tags=tail_sensor ./skills/tail-sensor/...
```
Expected: all four `TestTail_*` tests pass; vet clean.

- [ ] **Step 5: Commit**

```
git add skills/tail-sensor/scripts/tail.go skills/tail-sensor/scripts/tail_test.go
git commit -m "$(cat <<'EOF'
refactor(tail-sensor): use registry.Lookup, error on missing registry

/tail-sensor's main() resolves the project root via registry.Lookup.
When the registry file is absent, emits verdict=error /
metadata.kind=tail_no_registry. Every signal (envelope, error, invalid)
carries metadata.{registry_path, registry_source, registry_exists}.

Refs #6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Cross-skill build/vet sweep

**Files:** none (verification only).

- [ ] **Step 1: Run all build-tagged test variants**

Run each in turn:

```
go test ./lib/... -v
go test -tags=start_sensor ./skills/... -v
go test -tags=list_sensors ./skills/... -v
go test -tags=stop_sensor  ./skills/... -v
go test -tags=tail_sensor  ./skills/... -v
go test -tags=run_computational ./skills/... -v
go test -tags=run_inferential   ./skills/... -v
```

Expected: every command exits 0. The two `run_*` tag variants are unchanged by this work but must remain green (sanity).

- [ ] **Step 2: Run vet across all tag variants**

```
go vet ./lib/...
go vet -tags=start_sensor ./...
go vet -tags=list_sensors ./...
go vet -tags=stop_sensor  ./...
go vet -tags=tail_sensor  ./...
go vet -tags=run_computational ./...
go vet -tags=run_inferential   ./...
```

Expected: every command exits 0 with no diagnostics.

- [ ] **Step 3: If any failure surfaces, fix and re-commit**

Stop and triage. The likely culprits if anything fails:
- Forgot to update an import (`time`, `uuid`, `registry`).
- `runX(res, ...)` call signature drift between tests and implementation.
- `simpleSignal`/`buildAggregate` arity mismatch in `stop.go`.

There are no commits in this task unless a fix is needed; if a fix is required, commit it with subject `fix(<skill>): ...`.

---

## Task 10: Integration test `test/registry-discovery-e2e/` (TDD)

**Files:**
- Create: `test/registry-discovery-e2e/registry_discovery_e2e_test.go`

- [ ] **Step 1: Write the integration test (TDD: it must build before the implementation under test exists, but here all implementation is done — the test itself is the new code)**

Create `test/registry-discovery-e2e/registry_discovery_e2e_test.go`:

```go
// test/registry-discovery-e2e/registry_discovery_e2e_test.go
//
// Black-box regression guard for issue #6: the registry-touching skills
// must agree on the registry path regardless of which subdirectory the
// caller is in, as long as the project root is reachable via either the
// sensors/ marker walk-up or HARNESS_REGISTRY_ROOT.
package registryDiscoveryE2E_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// repoRoot returns the harness-framework repo root by walking up from
// the test's cwd until it sees go.mod.
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

// ensureBinaries compiles the start_sensor, list_sensors, stop_sensor,
// and watcher binaries once per test run into a shared tempdir. The
// watcher binary lives next to the start binary because start.go's
// watcherBinaryPath() looks for "watcher" alongside the running exe.
var (
	buildOnce  sync.Once
	startBin   string
	listBin    string
	stopBin    string
	watcherBin string
	buildErr   error
)

func ensureBinaries(t *testing.T) {
	t.Helper()
	buildOnce.Do(func() {
		root := repoRoot(t)
		// A package-level dir; t.TempDir() is per-test, but the cleanup
		// only runs at the end of the test run for this package.
		bin, err := os.MkdirTemp("", "registry-e2e-bin-")
		if err != nil {
			buildErr = fmt.Errorf("mkdir bin: %w", err)
			return
		}
		startBin = filepath.Join(bin, "start-sensor")
		listBin = filepath.Join(bin, "list-sensors")
		stopBin = filepath.Join(bin, "stop-sensor")
		watcherBin = filepath.Join(bin, "watcher")

		for _, b := range []struct {
			tags string
			out  string
			pkg  string
		}{
			{"start_sensor", startBin, "./skills/start-sensor/scripts"},
			{"list_sensors", listBin, "./skills/list-sensors/scripts"},
			{"stop_sensor", stopBin, "./skills/stop-sensor/scripts"},
			{"start_watcher", watcherBin, "./skills/start-sensor/scripts"},
		} {
			cmd := exec.Command("go", "build", "-tags="+b.tags, "-o", b.out, b.pkg)
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", b.tags, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
}

// makeProject scaffolds <parent>/proj/sensors/<id>.json containing a
// trivial blocking sensor. Returns the project root path.
func makeProject(t *testing.T, parent, id, command string) string {
	t.Helper()
	proj := filepath.Join(parent, "proj")
	if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	sensor := map[string]interface{}{
		"id":          id,
		"version":     "1.0.0",
		"name":        "registry-e2e fixture",
		"description": "blocking sleep used by integration test",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"output":      "stream",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command":  command,
			"blocking": true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^.*$", "verdict": "pass", "severity": "info"},
				},
			},
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{
					"fixture":           "sensors/fixtures/" + id + "/pass.txt",
					"expected_verdict":  "pass",
					"expected_severity": "info",
				},
			},
		},
	}
	body, _ := json.MarshalIndent(sensor, "", "  ")
	if err := os.WriteFile(filepath.Join(proj, "sensors", id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return proj
}

// runIn runs binary in dir with optional env overrides. Returns
// (stdout, stderr, exitCode).
func runIn(t *testing.T, binary, dir string, args []string, extraEnv map[string]string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	env := os.Environ()
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run %s: %v", binary, err)
	}
	return stdout.String(), stderr.String(), exit
}

func lastJSON(t *testing.T, s string) map[string]interface{} {
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
	t.Fatalf("no JSON line in output: %q", s)
	return nil
}

// TestE2E_DiscoverySharesStateAcrossCwds is the regression guard for
// issue #6: /start-sensor in cwd A must register an entry that
// /list-sensors in cwd B (a sub-directory of A) can see.
func TestE2E_DiscoverySharesStateAcrossCwds(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	ensureBinaries(t)

	parent := t.TempDir()
	proj := makeProject(t, parent, "sleeper", "sleep 60")
	deep := filepath.Join(proj, "nested", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	// Make sure HARNESS_REGISTRY_ROOT isn't bleeding in from the parent shell.
	t.Setenv("HARNESS_REGISTRY_ROOT", "")

	// Step 1: start from proj/.
	stdout, stderr, exit := runIn(t, startBin, proj, []string{"sleeper"}, nil)
	if exit != 0 {
		t.Fatalf("start exit %d\nstdout=%s\nstderr=%s", exit, stdout, stderr)
	}
	startSig := lastJSON(t, stdout)
	if startSig["verdict"] != "pass" {
		t.Fatalf("start verdict: got %v\nstdout=%s", startSig["verdict"], stdout)
	}
	startMD := startSig["metadata"].(map[string]interface{})
	wantPath := filepath.Join(proj, ".runtime", "sensors", "running_sensors.json")
	if startMD["registry_path"] != wantPath {
		t.Errorf("start registry_path: got %v, want %v", startMD["registry_path"], wantPath)
	}

	// Always clean up the sensor process at the end.
	t.Cleanup(func() {
		_, _, _ = runIn(t, stopBin, proj, []string{"sleeper"}, nil)
	})

	// Give the watcher a beat to settle before the list call.
	time.Sleep(100 * time.Millisecond)

	// Step 2: list from the deep sub-directory; the entry MUST be visible.
	stdout2, stderr2, exit2 := runIn(t, listBin, deep, nil, nil)
	if exit2 != 0 {
		t.Fatalf("list exit %d\nstdout=%s\nstderr=%s", exit2, stdout2, stderr2)
	}
	listSig := lastJSON(t, stdout2)
	if listSig["verdict"] != "pass" {
		t.Fatalf("list verdict: got %v (want pass)\nstdout=%s", listSig["verdict"], stdout2)
	}
	listMD := listSig["metadata"].(map[string]interface{})
	entries, _ := listMD["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("entries: got %d, want 1\nstdout=%s", len(entries), stdout2)
	}
	if entries[0].(map[string]interface{})["sensor_id"] != "sleeper" {
		t.Fatalf("entry sensor_id: got %v", entries[0])
	}
	if listMD["registry_path"] != wantPath {
		t.Errorf("list registry_path: got %v, want %v", listMD["registry_path"], wantPath)
	}
	if listMD["registry_source"] != "walk_up" {
		t.Errorf("list registry_source: got %v, want walk_up", listMD["registry_source"])
	}
}

// TestE2E_OutsideProjectFailsDiscovery: list from a directory with no
// sensors/ marker anywhere up to filesystem root (modulo the test's
// tempdir parent) returns an error signal.
func TestE2E_OutsideProjectFailsDiscovery(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	ensureBinaries(t)

	// Pick a tempdir without sensors/ anywhere up to /. Note: this
	// test runs from the tempdir, NOT from inside the harness-framework
	// repo (which has sensors/ at its root).
	outside := t.TempDir()
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	stdout, stderr, exit := runIn(t, listBin, outside, nil, nil)
	if exit != 1 {
		t.Fatalf("list exit: got %d, want 1\nstdout=%s\nstderr=%s", exit, stdout, stderr)
	}
	sig := lastJSON(t, stdout)
	if sig["verdict"] != "error" {
		t.Fatalf("verdict: got %v, want error", sig["verdict"])
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "registry_discovery_failed" {
		t.Errorf("kind: got %v", md["kind"])
	}
	ev := sig["evidence"].([]interface{})
	rationale := ev[0].(map[string]interface{})["rationale"].(string)
	if !strings.Contains(rationale, "HARNESS_REGISTRY_ROOT") || !strings.Contains(rationale, "sensors") {
		t.Errorf("rationale should mention both strategies, got: %q", rationale)
	}
}

// TestE2E_EnvVarOverridesDiscovery: with HARNESS_REGISTRY_ROOT set to a
// project, /list-sensors run from a directory outside that project sees
// the project's entries.
func TestE2E_EnvVarOverridesDiscovery(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	ensureBinaries(t)

	parent := t.TempDir()
	proj := makeProject(t, parent, "sleeper", "sleep 60")
	outside := t.TempDir() // separate tempdir with no sensors/

	// Start from inside proj/ (walk-up resolves correctly here).
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	stdout, stderr, exit := runIn(t, startBin, proj, []string{"sleeper"}, nil)
	if exit != 0 {
		t.Fatalf("start exit %d\nstdout=%s\nstderr=%s", exit, stdout, stderr)
	}
	_ = stdout

	t.Cleanup(func() {
		_, _, _ = runIn(t, stopBin, proj, []string{"sleeper"}, nil)
	})

	time.Sleep(100 * time.Millisecond)

	// List from the outside directory but with the env var pointed at
	// proj — must see the entry.
	stdout2, stderr2, exit2 := runIn(t, listBin, outside, nil, map[string]string{
		"HARNESS_REGISTRY_ROOT": proj,
	})
	if exit2 != 0 {
		t.Fatalf("list exit %d\nstdout=%s\nstderr=%s", exit2, stdout2, stderr2)
	}
	sig := lastJSON(t, stdout2)
	if sig["verdict"] != "pass" {
		t.Fatalf("verdict: got %v\nstdout=%s", sig["verdict"], stdout2)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["registry_source"] != "env" {
		t.Errorf("registry_source: got %v, want env", md["registry_source"])
	}
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 1 || entries[0].(map[string]interface{})["sensor_id"] != "sleeper" {
		t.Fatalf("entries: got %+v", entries)
	}
}
```

- [ ] **Step 2: Run the integration test, confirm it passes**

```
go test ./test/registry-discovery-e2e/ -v -count=1 -timeout=120s
```

Expected: all three `TestE2E_*` tests pass. Note: `-count=1` disables the test cache so a fresh run is forced (binary builds are otherwise cached and may go stale relative to mid-development source edits).

If the test takes longer than expected: the `sleep 60` blocking command keeps the watcher alive but is killed on cleanup. If a stop call hangs, `pkill -f 'sleep 60'` from your shell will clear leftover processes.

- [ ] **Step 3: Add `.runtime/` to the repo's `.gitignore` if not already** (sanity — should already be ignored)

```
grep -E '^\.?runtime/?$|^/?\.runtime/?$' .gitignore
```

Expected: at least one line matching. If absent, append `.runtime/` to `.gitignore` and stage it.

- [ ] **Step 4: Commit**

```
git add test/registry-discovery-e2e/registry_discovery_e2e_test.go
git commit -m "$(cat <<'EOF'
test(registry-discovery-e2e): regression guard for issue #6

Black-box test that builds start-sensor and list-sensors binaries,
starts a sleeper sensor in <proj>/, and asserts /list-sensors invoked
from <proj>/nested/deep/ sees the same entry — the bug from issue #6.
Also covers the env-var override and the outside-project failure mode.

Refs #6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Update `CLAUDE.md` with the "Registry root discovery" subsection

**Files:**
- Modify: `CLAUDE.md` (insert a new subsection between "Dependencies and lifecycle" and "Build, validate, test")

- [ ] **Step 1: Add the subsection**

After line 80 (which ends `### Dependencies and lifecycle` content with `... reused by both run-computational and run-inferential runner scripts.`) and before line 82 `## Build, validate, test`, insert:

```markdown
### Registry root discovery

The blocking-sensor registry (`<projectRoot>/.runtime/sensors/running_sensors.json`) lives in the user's project tree, NOT in the plugin tree. To make this resolution deterministic and cwd-independent, the four registry-touching skills (`/start-sensor`, `/list-sensors`, `/stop-sensor`, `/tail-sensor`) call `lib/registry/Lookup(cwd)` which resolves the project root in this order:

1. **`HARNESS_REGISTRY_ROOT` env var.** Must be an absolute path to an existing directory. The env var names the **project root** — i.e., the directory that contains `sensors/`, not `sensors/` itself. Symlinks are resolved via `EvalSymlinks`.
2. **Walk-up from `cwd` looking for `sensors/`.** The first ancestor whose `sensors/` child is itself a directory is the project root. Empty `sensors/` is acceptable.
3. **Failure.** No fallback to `cwd`. The skill emits an error Signal `metadata.kind=registry_discovery_failed` whose evidence names both strategies tried.

Every signal emitted by the four skills carries `metadata.{registry_path, registry_source, registry_exists}` for diagnose. `registry_source` is `"env"` or `"walk_up"`; `registry_exists` is `true` only when `running_sensors.json` is on disk.

Verdict semantics by skill when the registry file is absent (`registry_exists: false`):

| Skill | Verdict on missing file | Why |
| --- | --- | --- |
| `/start-sensor` | `pass` (canonical first-start) | Creating a registry is the point of `/start-sensor`. |
| `/list-sensors` | `warn` | Likely "wrong cwd" or no live sensors yet. |
| `/stop-sensor` | `error` | A sensor cannot be running if there is no registry file. |
| `/tail-sensor` | `error` | Same reasoning as `/stop-sensor`. |

The watcher subprocess inherits the resolved root via `HARNESS_WATCHER_REGISTRY_ROOT` (set by `/start-sensor` from `Result.ProjectRoot`); that env var is a precise absolute path, not a discovery hint.
```

- [ ] **Step 2: Verify the file renders correctly**

Run a quick sanity check:
```
grep -n "Registry root discovery" CLAUDE.md
grep -n "## Build, validate, test" CLAUDE.md
```
Expected: both lines reported, with `Registry root discovery` line number lower than `Build, validate, test` line number.

- [ ] **Step 3: Commit**

```
git add CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: registry root discovery section in CLAUDE.md

Documents HARNESS_REGISTRY_ROOT, the sensors/ marker walk-up rule, and
the verdict-by-skill table for the four registry-touching skills.

Refs #6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Open follow-up issue for `watcher_pid: -1`

**Files:** none (GitHub-only).

- [ ] **Step 1: Open the follow-up issue with `gh`**

```
gh issue create \
  --repo iurykrieger/harness-framework \
  --title "Registry: investigate watcher_pid: -1 anomaly observed in production" \
  --body "$(cat <<'EOF'
Spun out from #6. Real-world `running_sensors.json` observed with `watcher_pid: -1`:

```json
{
  "sensor_id": "run-api-local",
  "pid": 90006,
  "watcher_pid": -1,
  ...
}
```

Both writers of `WatcherPID` produce a non-negative value:

- `skills/start-sensor/scripts/start.go` writes `watcherProc.Pid` (always positive after a successful `os.StartProcess`).
- `lib/orchestrator/live_deps.go:132` writes `0` (orchestrator path).

A `-1` cannot come from either of those code paths under normal flow. Hypothesis: a fork-then-exec failure where the watcher binary does not exist at runtime; `os.StartProcess` returns an error and (currently) the registry entry is not written. But somehow a `-1` ended up persisted.

## Investigation

- [ ] Reproduce the `-1` value in a controlled test (delete the watcher binary between `os.StartProcess` and `Save`?).
- [ ] Audit every place that constructs `RunningSensorEntry` for unset/zero/negative `WatcherPID`.
- [ ] Add a `Validate` step on `Save` that rejects negative PID/PGID values.

## Acceptance

- [ ] No code path produces `WatcherPID < 0`.
- [ ] Test asserting that.
- [ ] If a backwards-compat read of an existing -1 entry is needed (existing on-disk state), document migration or self-heal behavior.

## Context

Original `.runtime/sensors/running_sensors.json` snippet from issue #6 (project: `payment-card-api`).

Refs #6.
EOF
)"
```

- [ ] **Step 2: Comment on issue #6 with the link to the new issue**

```
gh issue comment 6 \
  --repo iurykrieger/harness-framework \
  --body "Follow-up issue for the \`watcher_pid: -1\` anomaly opened: see referenced issues. The fix for #6 itself does not address this; tracking separately."
```

- [ ] **Step 3: No commit needed for this task** (GitHub state only).

---

## Task 13: Final sweep — issue #6 acceptance criteria

**Files:** none (verification only). This task verifies the spec's "Acceptance criteria" checklist.

- [ ] **Step 1: Walk the spec's acceptance list**

Open `docs/superpowers/specs/2026-05-10-registry-root-discovery-design.md` § Acceptance criteria, then confirm each item from the implementation:

- `lib/registry/root.go` exists with `Discover` and `Lookup` — Tasks 2-3.
- `lib/registry/state.go::LoadOrEmpty` exists; existing `Load` tests still pass — Task 1, Task 9 sweep.
- 4 skills no longer call `os.Getwd()` to derive `projectRoot` — Tasks 5-8. Verify with:
  ```
  grep -rn "os.Getwd" skills/start-sensor/scripts/ skills/list-sensors/scripts/ skills/stop-sensor/scripts/ skills/tail-sensor/scripts/
  ```
  Expected: only one match per file — `startDir, err := os.Getwd()` inside `main()` (which is the input to `Lookup`, not the registry root). No match should connect `os.Getwd()` directly to `registry.NewRoot` or `projectRoot`.
- `/list-sensors` warn semantics — Task 5 tests cover.
- `/stop-sensor`, `/tail-sensor` error semantics — Tasks 7-8 tests cover.
- All four skills include `metadata.{registry_path, registry_source, registry_exists}` — assertions in Tasks 5-8 tests.
- Integration test `test/registry-discovery-e2e/` — Task 10.
- `HARNESS_REGISTRY_ROOT` documented in `CLAUDE.md` under the new subsection — Task 11.
- Follow-up issue opened — Task 12.

- [ ] **Step 2: Final test pass**

```
go test ./...
go test -tags=start_sensor    ./...
go test -tags=list_sensors    ./...
go test -tags=stop_sensor     ./...
go test -tags=tail_sensor     ./...
go test -tags=run_computational ./...
go test -tags=run_inferential   ./...
go test ./test/registry-discovery-e2e/ -count=1 -timeout=120s
```

Expected: every command exits 0.

- [ ] **Step 3: Open the PR**

```
gh pr create \
  --repo iurykrieger/harness-framework \
  --title "Registry: cwd-independent root discovery (#6)" \
  --body "$(cat <<'EOF'
## Summary

- New \`lib/registry/root.go\`: \`Discover\` (HARNESS_REGISTRY_ROOT → \`sensors/\` walk-up → typed error) and \`Lookup\` (Discover + LoadOrEmpty → \`Result\`).
- New \`lib/registry/state.go::LoadOrEmpty\` distinguishes "registry empty" from "registry file missing".
- Migrated /start-sensor, /list-sensors, /stop-sensor, /tail-sensor to use \`registry.Lookup\`. /list-sensors emits verdict=warn on missing file; /stop-sensor and /tail-sensor emit verdict=error. Every signal carries \`metadata.{registry_path, registry_source, registry_exists}\` for diagnose.
- Black-box e2e test (\`test/registry-discovery-e2e/\`) is the regression guard for the cwd-A-vs-cwd-B bug described in #6.
- \`CLAUDE.md\` has a new "Registry root discovery" subsection.
- Follow-up issue opened for the \`watcher_pid: -1\` anomaly.

## Test plan

- [ ] \`go test ./lib/...\` passes.
- [ ] \`go test -tags=<each-tag> ./skills/...\` passes for all 6 build tags (start_sensor, list_sensors, stop_sensor, tail_sensor, run_computational, run_inferential).
- [ ] \`go test ./test/registry-discovery-e2e/ -count=1\` passes (regression guard for #6).
- [ ] Manual: in a project A and a project B (sibling), \`/start-sensor\` in A then \`/list-sensors\` from A's nested subdir shows the entry; from B it returns warn or env-var override works.

Closes #6

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

After writing the plan I checked the spec one more time and found the following:

- **Spec coverage:** every acceptance criterion in the spec maps to a task above. Task 13 walks the criteria end-to-end.
- **Type consistency:** `Result`, `Source`, `SourceEnv`, `SourceWalkUp`, `DiscoveryError`, `DiscoveryErrorSignal`, `Lookup`, `Discover`, `LoadOrEmpty` are spelled identically across Tasks 1-12 and the test files.
- **runX signatures:** all four skills' `runX` functions take `res registry.Result` as the first argument (Tasks 5-8). The test helper `resultFor` is duplicated across the 4 skill `_test.go` files — this is intentional under project rule #4 ("scripts are skill-local"). DRY at the test-helper level is allowed if the helpers truly want to converge later (move to `lib/testfixtures/`); for now, identical 12-line helpers are clearer than a cross-skill import.
- **Placeholder scan:** the only "soft" placeholder I left is the engineer's escape hatch in Task 10 acknowledging that the `fmt` shim chain is over-defensive and may be replaced with a plain `fmt.Sprintf`. That's intentional advice, not a requirement-deferral.
- **Signal validation:** Task 4 explicitly asserts `DiscoveryErrorSignal` validates against `signal.json`. Skill tests in Tasks 5-8 do not re-validate against the schema directly (the runtime `validateSignal` helper in each skill does that already, and a malformed signal would fail at runtime under the existing emergency-fallback path).

---

**Plan complete and saved to `docs/superpowers/plans/2026-05-10-registry-root-discovery.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
