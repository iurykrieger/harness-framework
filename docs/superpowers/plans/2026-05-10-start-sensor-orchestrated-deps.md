# /start-sensor Orchestrated Dependencies Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `/start-sensor` to the existing dependency-resolution machinery so blocking sensors that depend on setup or other blocking sensors come up in the right order before the target spawns, including the target's `prepare[]`. Rename `metadata.kind` values across `/start-sensor`, `/stop-sensor`, and `/tail-sensor` to drop redundant command-name prefixes.

**Architecture:** A new `lib/orchestrator/preflight.go` exposes `RunDeps` (the dep iteration loop currently inline in `RunWithDeps`, factored out) and `RunPreparePhase` (Phase 1 of `RunOne`, exposed for `/start-sensor` to call without the rest of the lifecycle). `AttachLiveDep` gains an explicit `holderPID` parameter (no longer hardcodes `os.Getpid()`) and reaps dead same-id holders before adding a new one. A new `RebindDepHolderPID` does atomic pid swaps on existing holders, used by `/start-sensor` to swap the placeholder pid (start.go's pid) for the actual root subprocess pid post-spawn. `RunWithDeps` is refactored to delegate the dep loop to `RunDeps` (zero behavior change). `/start-sensor::runStart` is rewritten as a linear composition: resolve → schema-validate → reject if non-blocking → `RunDeps` (deps + cascade detection) → `RunPreparePhase` of root → flock + singleton + spawn detached + watcher + registry write → rebind dep holder pids → emit `started`. On any failure ≥ pre-flight, `detachAll(LiveStack)` walks reverse to undo the holders we added.

**Tech Stack:** Go 1.25, single module `github.com/iurykrieger/harness-framework`. Build tags `start_sensor`, `stop_sensor`, `tail_sensor` gate per-skill scripts. Tests use `testing` package, `t.TempDir()`, `t.Setenv()`. Library tests live alongside the code (`*_test.go`).

**Spec:** `docs/superpowers/specs/2026-05-10-start-sensor-orchestrated-deps-design.md`. Issue: [#7](https://github.com/iurykrieger/harness-framework/issues/7).

---

## Conventions used by every task

- Always run from the repo root (`cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework`). All `go` commands assume cwd = repo root.
- After every code change, run `go vet -tags=<relevant tags> ./...` to catch typos.
- Commit subject prefix conventions in this repo:
  - Library work: `feat(orchestrator): ...` or `refactor(orchestrator): ...`.
  - Skill work: `feat(start-sensor): ...` / `refactor(stop-sensor): ...` / `refactor(tail-sensor): ...`.
  - Tests-only: `test(<area>): ...`.
  - Docs: `docs: ...`.
- The repo uses `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` on commits made by Claude. Match that style.
- Run all `go test` commands with the build tags relevant to the package being tested:
  - `lib/orchestrator/...` → no tags needed.
  - `skills/start-sensor/...` → `-tags=start_sensor` (and `-tags=start_watcher` for the watcher binary).
  - `skills/stop-sensor/...` → `-tags=stop_sensor`.
  - `skills/tail-sensor/...` → `-tags=tail_sensor`.
- The orchestrator package uses two test packages: `package orchestrator` (white-box) and `package orchestrator_test` (black-box). Existing tests already follow this split — match it for new tests.

---

## File Structure

### New files

- `lib/orchestrator/preflight.go` — `RunDepsResult` struct; `RunDeps`; `RunPreparePhase`.
- `lib/orchestrator/preflight_test.go` — tests for `RunDeps` and `RunPreparePhase`. Black-box (`package orchestrator_test`).

### Modified files

- `lib/orchestrator/live_deps.go` — `AttachLiveDep` signature gains `holderPID int`; reap-on-attach added; `RebindDepHolderPID` added.
- `lib/orchestrator/live_deps_test.go` — extended with reap and rebind tests.
- `lib/orchestrator/run.go` — `RunWithDeps` refactored to delegate the dep loop to `RunDeps`. Existing call site of `AttachLiveDep` passes `os.Getpid()` explicitly.
- `lib/orchestrator/run_test.go` — no behavior change; existing tests must still pass.
- `skills/start-sensor/scripts/start.go` — `runStart` rewritten end-to-end. `errorSignal` removed; new `finalSignal` constructor; `detachAll` helper.
- `skills/start-sensor/scripts/start_test.go` — existing tests updated for renamed kinds; new tests added for orchestrated deps + rebind.
- `skills/start-sensor/SKILL.md` — Output contract section rewritten; lifecycle now includes pre-flight and prepare; "execution.prepare[] is not yet executed" disclaimer removed.
- `skills/stop-sensor/scripts/stop.go` — `metadata.kind` rename: `stop_not_running` → `not_running`, `stop_held`/`stop_held_with_dead_holders` → `held` (with `metadata.dead_holders=[...]`), `stop_failed` → `failed`. `metadata.kind=aggregate` for the success path stays.
- `skills/stop-sensor/scripts/stop_test.go` — assertion updates.
- `skills/stop-sensor/SKILL.md` — Output contract section updates.
- `skills/tail-sensor/scripts/tail.go` — `metadata.kind` rename: `tail_envelope` → `envelope`, `tail_not_running` → `not_running`.
- `skills/tail-sensor/scripts/tail_test.go` — assertion updates.
- `skills/tail-sensor/SKILL.md` — description and Output contract updates.
- `.claude-plugin/plugin.json` — version bump.

---

## Task 1: Add `RunPreparePhase` to `lib/orchestrator/preflight.go` (TDD)

`RunPreparePhase` is a thin wrapper around the existing private `runLifecyclePhase` that runs only the "prepare" phase fail-fast. Pure addition, no callers yet.

**Files:**
- Create: `lib/orchestrator/preflight.go`
- Create: `lib/orchestrator/preflight_test.go`

- [ ] **Step 1: Create `lib/orchestrator/preflight.go` with empty `RunPreparePhase` skeleton (so test imports compile)**

```go
package orchestrator

import (
	"context"
)

// RunPreparePhase runs sensor.execution.prepare[] fail-fast. Returns the
// per-step results (shaped for inclusion in metadata.lifecycle.prepare)
// and a bool indicating whether the phase failed (first non-pass step
// triggers fail-fast).
//
// Extracted from lifecycle.go::runLifecyclePhase("prepare", failFast=true)
// so callers that need only the prepare phase (notably /start-sensor
// before its detached spawn) can run it without paying for command +
// teardown.
func RunPreparePhase(ctx context.Context, target Sensor, defaultTimeoutMS int) (results []interface{}, failed bool) {
	return nil, false
}
```

- [ ] **Step 2: Write failing tests in `lib/orchestrator/preflight_test.go`**

```go
package orchestrator_test

import (
	"context"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
)

func TestRunPreparePhase_NoSteps(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "no-prep",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, 1000)
	if failed {
		t.Errorf("failed: got true, want false (no prepare steps)")
	}
	if len(results) != 0 {
		t.Errorf("results: got %d entries, want 0", len(results))
	}
}

func TestRunPreparePhase_AllPass(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "all-pass",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
				"prepare": []interface{}{
					map[string]interface{}{"command": "true"},
					map[string]interface{}{"command": "true"},
				},
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, 1000)
	if failed {
		t.Errorf("failed: got true, want false (all steps pass)")
	}
	if len(results) != 2 {
		t.Fatalf("results length: got %d, want 2", len(results))
	}
	for i, raw := range results {
		step, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("results[%d] not a map", i)
		}
		if step["verdict"] != "pass" {
			t.Errorf("results[%d].verdict = %v, want pass", i, step["verdict"])
		}
	}
}

func TestRunPreparePhase_FirstFails_FailFast(t *testing.T) {
	target := orchestrator.Sensor{
		ID: "first-fails",
		JSON: map[string]interface{}{
			"execution": map[string]interface{}{
				"command": "true",
				"prepare": []interface{}{
					map[string]interface{}{"command": "false"},
					map[string]interface{}{"command": "true"},
				},
			},
		},
	}
	results, failed := orchestrator.RunPreparePhase(context.Background(), target, 1000)
	if !failed {
		t.Errorf("failed: got false, want true (first step is `false`)")
	}
	if len(results) != 1 {
		t.Fatalf("results length: got %d, want 1 (fail-fast should abort)", len(results))
	}
	step := results[0].(map[string]interface{})
	if step["verdict"] != "fail" {
		t.Errorf("results[0].verdict = %v, want fail", step["verdict"])
	}
}
```

- [ ] **Step 3: Run tests and confirm they fail**

Run: `go test ./lib/orchestrator/ -run TestRunPreparePhase -v`

Expected: all three tests FAIL because `RunPreparePhase` returns nil/false unconditionally.

- [ ] **Step 4: Implement `RunPreparePhase` by delegating to `runLifecyclePhase`**

Replace the body in `lib/orchestrator/preflight.go`:

```go
package orchestrator

import (
	"context"
)

// RunPreparePhase runs sensor.execution.prepare[] fail-fast. Returns the
// per-step results (shaped for inclusion in metadata.lifecycle.prepare)
// and a bool indicating whether the phase failed (first non-pass step
// triggers fail-fast).
//
// Extracted from lifecycle.go::runLifecyclePhase("prepare", failFast=true)
// so callers that need only the prepare phase (notably /start-sensor
// before its detached spawn) can run it without paying for command +
// teardown.
func RunPreparePhase(ctx context.Context, target Sensor, defaultTimeoutMS int) (results []interface{}, failed bool) {
	execMap, _ := target.JSON["execution"].(map[string]interface{})
	if execMap == nil {
		return nil, false
	}
	return runLifecyclePhase(ctx, execMap, "prepare", defaultTimeoutMS, true)
}
```

- [ ] **Step 5: Run tests and confirm they pass**

Run: `go test ./lib/orchestrator/ -run TestRunPreparePhase -v`

Expected: all three tests PASS.

- [ ] **Step 6: Run vet to catch typos**

Run: `go vet ./lib/orchestrator/...`

Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add lib/orchestrator/preflight.go lib/orchestrator/preflight_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): add RunPreparePhase for prepare-only execution

Thin wrapper over runLifecyclePhase("prepare", failFast=true) so callers
that need only Phase 1 of the lifecycle can run it without paying for
command + teardown. /start-sensor will use this to honor the target's
prepare[] before spawning detached.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `holderPID` parameter to `AttachLiveDep` + reap dead same-id holders (TDD)

The current `AttachLiveDep` hardcodes `holderPID := os.Getpid()`. We change the signature so callers control the pid (necessary for `/start-sensor` to use the placeholder pid pattern). We also add reap-on-attach: when adding a new `(kind=sensor, id=H)` holder to an alive dep, drop any pre-existing `(kind=sensor, id=H, pid=DEAD)` entries first. This keeps held_by from accumulating dead duplicates of the same logical holder.

**Files:**
- Modify: `lib/orchestrator/live_deps.go` (function signature + reap logic)
- Modify: `lib/orchestrator/live_deps_test.go` (new tests + adapt sole existing usage if needed)
- Modify: `lib/orchestrator/run.go` (existing call site updated to pass `os.Getpid()`)

- [ ] **Step 1: Write failing tests in `lib/orchestrator/live_deps_test.go`**

Append these tests at the end of `lib/orchestrator/live_deps_test.go`:

```go
import (
	// add to existing imports:
	"bytes"

	"github.com/iurykrieger/harness-framework/lib/schema"
)
```

```go
// loadValidator returns a schema validator backed by the repo's schemas
// directory; failing to load aborts the test.
func loadValidator(t *testing.T) *schema.Validator {
	t.Helper()
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, code := schema.LoadValidator(schemasDir, io.Discard)
	if code != 0 {
		t.Fatalf("schema validator init failed (code=%d)", code)
	}
	return v
}

// loadDepSensor parses sensors/<id>.json from root into a Sensor struct.
func loadDepSensor(t *testing.T, root, id string) orchestrator.Sensor {
	t.Helper()
	path := filepath.Join(root, "sensors", id+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return orchestrator.Sensor{ID: id, Path: path, JSON: m}
}

func TestAttachLiveDep_PassesHolderPID(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	v := loadValidator(t)
	var stdout, stderr bytes.Buffer

	const customHolderPID = 99999
	depID, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", customHolderPID,
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	if depID != "blocking-tick" {
		t.Errorf("depID: got %q, want blocking-tick", depID)
	}

	r := registry.NewRoot(root)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	entry := rs.FindEntry("blocking-tick")
	if entry == nil {
		t.Fatal("expected entry in registry, got none")
	}
	if len(entry.HeldBy) != 1 {
		t.Fatalf("HeldBy length: got %d, want 1", len(entry.HeldBy))
	}
	h := entry.HeldBy[0]
	if h.Kind != "sensor" || h.ID != "holder-id" || h.PID != customHolderPID {
		t.Errorf("holder: got %+v, want kind=sensor id=holder-id pid=%d", h, customHolderPID)
	}

	// Tear down for cleanliness — kill the spawned subprocess.
	orchestrator.DetachLiveDep("blocking-tick", root, "holder-id", v, io.Discard, io.Discard)
}

func TestAttachLiveDep_ReapsStaleSameHolder(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")

	v := loadValidator(t)
	var stdout, stderr bytes.Buffer

	// First attach with a "real" pid (current process) so the dep is alive.
	livePID := os.Getpid()
	if _, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", livePID,
		v, &stdout, &stderr,
	); err != nil {
		t.Fatal(err)
	}

	// Manually inject a dead-pid holder of the same id, simulating a
	// previous run that crashed.
	r := registry.NewRoot(root)
	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, _ := registry.Load(r)
		entry := rs.FindEntry("blocking-tick")
		entry.HeldBy = append(entry.HeldBy, registry.HeldByEntry{
			Kind: "sensor", ID: "holder-id", PID: 999999, AttachedAt: "2026-05-10T00:00:00Z",
		})
		return registry.Save(r, rs)
	}); err != nil {
		t.Fatal(err)
	}

	// Confirm the dead holder is in held_by before re-attach.
	rs, _ := registry.Load(r)
	if got := len(rs.FindEntry("blocking-tick").HeldBy); got != 2 {
		t.Fatalf("pre-condition: HeldBy length = %d, want 2", got)
	}

	// Re-attach with the same holder id — the reap should remove the
	// dead holder before adding the new one. End state: 1 holder (live).
	if _, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", livePID,
		v, &stdout, &stderr,
	); err != nil {
		t.Fatal(err)
	}
	rs, _ = registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if len(entry.HeldBy) != 1 {
		t.Fatalf("post-attach HeldBy length: got %d, want 1 (reap should drop dead holder)", len(entry.HeldBy))
	}
	if entry.HeldBy[0].PID != livePID {
		t.Errorf("post-attach holder pid: got %d, want %d (live)", entry.HeldBy[0].PID, livePID)
	}

	// Cleanup.
	orchestrator.DetachLiveDep("blocking-tick", root, "holder-id", v, io.Discard, io.Discard)
}
```

- [ ] **Step 2: Run tests and confirm they fail**

Run: `go test ./lib/orchestrator/ -run TestAttachLiveDep_ -v`

Expected: tests FAIL because the call sites use 5 positional args; the new tests pass 6 (with `holderPID`). Compile error is the expected failure here.

- [ ] **Step 3: Update `AttachLiveDep` signature in `lib/orchestrator/live_deps.go`**

Change the function signature and replace the holderPID derivation. Replace lines 30-65 of `live_deps.go` (currently from `// AttachLiveDep starts ...` through the end of the function body):

```go
// AttachLiveDep starts (or attaches to) a blocking dep. Emits a
// `dep_attached` or `dep_started` Signal on stdout. Returns the dep id
// so the caller can stack it for detach.
//
// holderPID is recorded in held_by as the holder's pid. Callers that are
// the holder use os.Getpid(); callers that will hand the holder over to
// a different process (notably /start-sensor, which spawns a detached
// subprocess that becomes the holder) pass a placeholder pid and later
// rebind via RebindDepHolderPID.
//
// Reap-on-attach: when the dep is alive and we are adding a new
// (kind=sensor, id=holderID) holder, any pre-existing
// (kind=sensor, id=holderID, pid=DEAD) entries are dropped first. This
// prevents accumulation of dead holders across re-runs of the same
// holder identity (e.g., /start-sensor target re-runs after start.go
// crashes between AttachLiveDep and RebindDepHolderPID).
func AttachLiveDep(ctx context.Context, dep Sensor, projectRoot, holderID string, holderPID int, v *schema.Validator, stdout, stderr io.Writer) (string, error) {
	r := registry.NewRoot(projectRoot)
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	holder := registry.HeldByEntry{Kind: "sensor", ID: holderID, PID: holderPID, AttachedAt: now}

	startedFresh := false
	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		existing := rs.FindEntry(dep.ID)
		if existing != nil && registry.IsPIDAlive(existing.PID) {
			reapDeadSameIDHolders(existing, holderID)
			registry.AddHolder(existing, holder)
			return registry.Save(r, rs)
		}
		// Not live: start it.
		startedFresh = true
		return startBlockingDep(&rs, r, dep, holder)
	}); err != nil {
		return "", err
	}

	kind := "dep_attached"
	if startedFresh {
		kind = "dep_started"
	}
	sig := buildSimpleSignal(dep.ID, "pass", "info", kind, fmt.Sprintf("blocking dep %q held by %q", dep.ID, holderID))
	sig = validateOrFallback(v, sig, dep.ID, stderr)
	_ = json.NewEncoder(stdout).Encode(sig)
	return dep.ID, nil
}

// reapDeadSameIDHolders drops every (kind="sensor", id=holderID, pid=DEAD)
// entry from entry.HeldBy in place. Live holders, manual holders, and
// holders with different ids are preserved.
func reapDeadSameIDHolders(entry *registry.RunningSensorEntry, holderID string) {
	keep := entry.HeldBy[:0]
	for _, h := range entry.HeldBy {
		if h.Kind == "sensor" && h.ID == holderID && !registry.IsPIDAlive(h.PID) {
			continue
		}
		keep = append(keep, h)
	}
	entry.HeldBy = keep
}
```

- [ ] **Step 4: Update the existing call site in `lib/orchestrator/run.go` to pass `os.Getpid()`**

In `lib/orchestrator/run.go`, find the `AttachLiveDep` call (around line 72) and add the `holderPID` argument. Also add `os` to the imports if not already present.

Replace:

```go
		if blocking && s.ID != rootID {
			depID, err := AttachLiveDep(ctx, s, projectRoot, rootID, v, stdout, stderr)
```

with:

```go
		if blocking && s.ID != rootID {
			depID, err := AttachLiveDep(ctx, s, projectRoot, rootID, os.Getpid(), v, stdout, stderr)
```

Add `"os"` to the import block at the top of `run.go` if it's not already there.

- [ ] **Step 5: Run tests and confirm they pass**

Run: `go test ./lib/orchestrator/ -v`

Expected: all tests PASS, including the existing `TestRunWithDeps_*` and `TestRunOneWithLiveDeps_*` tests (regression).

- [ ] **Step 6: Run vet**

Run: `go vet ./lib/orchestrator/...`

Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go lib/orchestrator/run.go
git commit -m "$(cat <<'EOF'
refactor(orchestrator): explicit holderPID + reap-on-attach in AttachLiveDep

AttachLiveDep no longer hardcodes os.Getpid() for holder.PID. Callers
pass the pid explicitly: existing run.go call site passes os.Getpid()
(unchanged behavior); upcoming /start-sensor caller will pass a
placeholder pid and rebind post-spawn.

Reap-on-attach: when adding a new (kind=sensor, id=H) holder to an
alive dep, drop any pre-existing (kind=sensor, id=H, pid=DEAD) entries
first. Prevents dead-holder accumulation across crashes between
AttachLiveDep and any caller-side rebind.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Add `RebindDepHolderPID` to `lib/orchestrator/live_deps.go` (TDD)

Atomic pid swap on an existing holder. Used by `/start-sensor` to swap the placeholder pid (start.go's pid) for the actual root subprocess pid post-spawn.

**Files:**
- Modify: `lib/orchestrator/live_deps.go` (add new function)
- Modify: `lib/orchestrator/live_deps_test.go` (add tests)

- [ ] **Step 1: Write failing tests in `lib/orchestrator/live_deps_test.go`**

Append:

```go
func TestRebindDepHolderPID_Match(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")
	v := loadValidator(t)
	var out, errBuf bytes.Buffer

	const oldPID = 12345
	const newPID = 67890
	if _, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", oldPID,
		v, &out, &errBuf,
	); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.RebindDepHolderPID("blocking-tick", root, "holder-id", oldPID, newPID); err != nil {
		t.Fatal(err)
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if len(entry.HeldBy) != 1 {
		t.Fatalf("HeldBy length: got %d, want 1", len(entry.HeldBy))
	}
	if entry.HeldBy[0].PID != newPID {
		t.Errorf("rebound PID: got %d, want %d", entry.HeldBy[0].PID, newPID)
	}

	orchestrator.DetachLiveDep("blocking-tick", root, "holder-id", v, io.Discard, io.Discard)
}

func TestRebindDepHolderPID_NoMatch_IsNoop(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	dep := loadDepSensor(t, root, "blocking-tick")
	v := loadValidator(t)
	var out, errBuf bytes.Buffer

	const realPID = 12345
	if _, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", realPID,
		v, &out, &errBuf,
	); err != nil {
		t.Fatal(err)
	}

	// Rebind with non-matching oldPID — should silently succeed.
	if err := orchestrator.RebindDepHolderPID("blocking-tick", root, "holder-id", 99999, 777); err != nil {
		t.Errorf("RebindDepHolderPID: got error %v, want nil (idempotent no-op)", err)
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if entry.HeldBy[0].PID != realPID {
		t.Errorf("PID after no-op rebind: got %d, want %d (unchanged)", entry.HeldBy[0].PID, realPID)
	}

	orchestrator.DetachLiveDep("blocking-tick", root, "holder-id", v, io.Discard, io.Discard)
}

func TestRebindDepHolderPID_DepEntryMissing_IsNoop(t *testing.T) {
	root := t.TempDir()
	// No registry entry exists for this id.
	if err := orchestrator.RebindDepHolderPID("never-attached", root, "holder-id", 1, 2); err != nil {
		t.Errorf("RebindDepHolderPID for missing dep: got error %v, want nil (idempotent no-op)", err)
	}
}
```

- [ ] **Step 2: Run tests and confirm they fail**

Run: `go test ./lib/orchestrator/ -run TestRebindDepHolderPID -v`

Expected: compile error — `RebindDepHolderPID` is undefined.

- [ ] **Step 3: Implement `RebindDepHolderPID` in `lib/orchestrator/live_deps.go`**

Append at the end of `live_deps.go`:

```go
// RebindDepHolderPID atomically updates the pid of a holder in dep.HeldBy.
// Match by (kind="sensor", id=holderID, pid=oldPID); if found, swap to
// newPID. Idempotent: no matching holder (or no dep entry at all) →
// silent no-op (returns nil).
//
// Used by /start-sensor after spawning the root subprocess to swap the
// placeholder pid (os.Getpid() of start.go) for the actual root subproc
// pid, so /list-sensors and /stop-sensor see a holder pid that mirrors
// the root sensor's lifetime.
func RebindDepHolderPID(depID, projectRoot, holderID string, oldPID, newPID int) error {
	r := registry.NewRoot(projectRoot)
	return registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		entry := rs.FindEntry(depID)
		if entry == nil {
			return nil
		}
		matched := false
		for i := range entry.HeldBy {
			h := &entry.HeldBy[i]
			if h.Kind == "sensor" && h.ID == holderID && h.PID == oldPID {
				h.PID = newPID
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		return registry.Save(r, rs)
	})
}
```

- [ ] **Step 4: Run tests and confirm they pass**

Run: `go test ./lib/orchestrator/ -run TestRebindDepHolderPID -v`

Expected: all three tests PASS.

- [ ] **Step 5: Run all orchestrator tests for regression**

Run: `go test ./lib/orchestrator/ -v`

Expected: every test PASS.

- [ ] **Step 6: Run vet**

Run: `go vet ./lib/orchestrator/...`

Expected: no output.

- [ ] **Step 7: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): add RebindDepHolderPID

Atomic pid swap on an existing kind=sensor holder. Match by
(kind, id, oldPID); idempotent no-op when no match or no dep entry.
/start-sensor will use this to swap its placeholder holder pid for the
spawned root subprocess pid after the registry write succeeds.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Add `RunDeps` + `RunDepsResult` to `lib/orchestrator/preflight.go` (TDD)

`RunDeps` resolves the dep graph, validates every sensor, and iterates topologically — running non-blocking deps via `RunOne` and blocking deps via `AttachLiveDep`. The root is **skipped**; the caller handles it. Cascade signals for intermediate skipped deps are emitted to stdout; the cascade signal for the root (if any) is returned in `CascadeSig` for the caller to wrap.

**Files:**
- Modify: `lib/orchestrator/preflight.go` (add `RunDepsResult` and `RunDeps`)
- Modify: `lib/orchestrator/preflight_test.go` (add `RunDeps` tests)

- [ ] **Step 1: Add the type and a stub function to `lib/orchestrator/preflight.go` (so test imports compile)**

Update `lib/orchestrator/preflight.go` to also include:

```go
import (
	"context"
	"io"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// RunDepsResult carries the post-pre-flight state for the caller to
// decide the root's fate.
type RunDepsResult struct {
	// Order is the topo-sorted DAG (root last). Always populated when
	// ExitCode==0.
	Order []Sensor

	// Signals maps non-root sensor id → its emitted signal (RunOne
	// aggregate, AttachLiveDep ack, or BuildCascadeSignal for skipped deps).
	Signals map[string]map[string]interface{}

	// LiveStack is the ordered list of blocking dep ids that
	// AttachLiveDep succeeded on. Caller iterates in reverse for detach.
	LiveStack []string

	// CascadeSig is non-nil when a dep of the root produced fail/error
	// and the root would cascade. Caller emits and detaches LiveStack.
	CascadeSig map[string]interface{}

	// ExitCode: 0 ok, 1 DAG/schema failure, 2 io error.
	ExitCode int
}

// RunDeps resolves targetID's depends_on graph, validates every sensor
// against schemas/sensor.json, and iterates topologically — emitting
// per-dep aggregate (non-blocking via RunOne) or attach acks (blocking
// via AttachLiveDep). Cascade signals for intermediate deps are emitted
// on stdout during the loop. The root is NOT processed; caller handles
// it.
//
// Intermediate cascade: a non-blocking dep whose own dep failed gets a
// cascade signal emitted in stdout (metadata.kind=cascade), recorded in
// Signals, and processing continues. The cascade chain propagates: any
// dependent of the cascade-marked dep also cascades.
//
// Root cascade: when iteration finishes, if FirstFailedDep returns
// non-nil for the root sensor, BuildCascadeSignal is built but NOT
// emitted — returned in CascadeSig so the caller can wrap it (e.g.
// /start-sensor translates it to a `failed` signal with
// metadata.cause=dep_cascade).
func RunDeps(
	ctx context.Context,
	targetID, projectRoot, schemasDir, holderID string,
	holderPID int,
	v *schema.Validator,
	stdout, stderr io.Writer,
) *RunDepsResult {
	return &RunDepsResult{ExitCode: 1}
}
```

- [ ] **Step 2: Write failing tests in `lib/orchestrator/preflight_test.go`**

Append (the test helpers `writeSensorWithDeps`, `writeBlockingDep`, `loadDepSensor` are reusable from existing test files — duplicate them locally since `preflight_test.go` is `package orchestrator_test` while `run_test.go` is `package orchestrator`; trying to import across packages adds friction).

```go
import (
	// add to existing imports:
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)

func writeSensorJSON(t *testing.T, root, id string, body map[string]interface{}) {
	t.Helper()
	dir := filepath.Join(root, "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body["id"] = id
	data, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, id+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeNonBlockingDep(t *testing.T, root, id string, depsOn []string, command string) {
	t.Helper()
	s := testfixtures.ValidSensorComputational()
	if len(depsOn) > 0 {
		ds := []interface{}{}
		for _, d := range depsOn {
			ds = append(ds, d)
		}
		s["depends_on"] = ds
	}
	exec := s["execution"].(map[string]interface{})
	exec["command"] = command
	writeSensorJSON(t, root, id, s)
}

func writeBlockingDepFixture(t *testing.T, root, id string, depsOn []string) {
	t.Helper()
	body := map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Blocking " + id,
		"description": "blocking fixture for " + id,
		"determinism": "high",
		"kind":        "setup",
		"type":        "computational",
		"output":      "stream",
		"regulation":  "behaviour",
		"phase":       "continuous",
		"triggers":    []interface{}{map[string]interface{}{"on": "manual"}},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"execution": map[string]interface{}{
			"command":             "while true; do echo TICK; sleep 0.1; done",
			"blocking":            true,
			"graceful_timeout_ms": 200,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info"},
				},
			},
		},
	}
	if len(depsOn) > 0 {
		ds := []interface{}{}
		for _, d := range depsOn {
			ds = append(ds, d)
		}
		body["depends_on"] = ds
	}
	writeSensorJSON(t, root, id, body)
}

func runDepsForTest(t *testing.T, root, targetID, holderID string, holderPID int) (*orchestrator.RunDepsResult, string, string) {
	t.Helper()
	schemasDir := testfixtures.RepoSchemasDir(t)
	v, code := schema.LoadValidator(schemasDir, io.Discard)
	if code != 0 {
		t.Fatalf("schema validator init: code=%d", code)
	}
	var out, errBuf bytes.Buffer
	res := orchestrator.RunDeps(context.Background(), targetID, root, schemasDir, holderID, holderPID, v, &out, &errBuf)
	return res, out.String(), errBuf.String()
}

func TestRunDeps_NoDeps(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "lone", nil, "true")

	res, _, _ := runDepsForTest(t, root, "lone", "lone", os.Getpid())
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if len(res.Order) != 1 || res.Order[0].ID != "lone" {
		t.Errorf("Order: got %v, want [lone]", res.Order)
	}
	if len(res.LiveStack) != 0 {
		t.Errorf("LiveStack: got %v, want []", res.LiveStack)
	}
	if res.CascadeSig != nil {
		t.Errorf("CascadeSig: got non-nil, want nil")
	}
}

func TestRunDeps_SetupDepPASS(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "setup", nil, "true")
	writeNonBlockingDep(t, root, "target", []string{"setup"}, "true")

	res, stdout, _ := runDepsForTest(t, root, "target", "target", os.Getpid())
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if res.CascadeSig != nil {
		t.Fatalf("CascadeSig: got non-nil, want nil")
	}
	depSig, ok := res.Signals["setup"]
	if !ok {
		t.Fatal("Signals[setup] missing")
	}
	if depSig["verdict"] != "pass" {
		t.Errorf("setup verdict: got %v, want pass", depSig["verdict"])
	}
	if !strings.Contains(stdout, `"sensor_id":"setup"`) {
		t.Errorf("stdout should contain setup signal; got: %s", stdout)
	}
}

func TestRunDeps_SetupDepFAIL_RootCascadesViaCascadeSig(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "bad-setup", nil, "false")
	writeNonBlockingDep(t, root, "target", []string{"bad-setup"}, "true")

	res, stdout, _ := runDepsForTest(t, root, "target", "target", os.Getpid())
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if res.CascadeSig == nil {
		t.Fatal("CascadeSig: got nil, want non-nil (root would cascade)")
	}
	md := res.CascadeSig["metadata"].(map[string]interface{})
	if md["kind"] != "cascade" {
		t.Errorf("CascadeSig.metadata.kind: got %v, want cascade", md["kind"])
	}
	if md["failed_dep_id"] != "bad-setup" {
		t.Errorf("CascadeSig.metadata.failed_dep_id: got %v, want bad-setup", md["failed_dep_id"])
	}
	// CascadeSig should NOT have been emitted to stdout.
	if strings.Contains(stdout, `"kind":"cascade"`) {
		t.Errorf("root cascade should NOT be on stdout, but found: %s", stdout)
	}
}

func TestRunDeps_BlockingDepStartFresh(t *testing.T) {
	root := t.TempDir()
	writeBlockingDepFixture(t, root, "blocking-tick", nil)
	writeNonBlockingDep(t, root, "target", []string{"blocking-tick"}, "true")

	const customHolderPID = 12321
	res, _, _ := runDepsForTest(t, root, "target", "target", customHolderPID)
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	if got := res.LiveStack; len(got) != 1 || got[0] != "blocking-tick" {
		t.Errorf("LiveStack: got %v, want [blocking-tick]", got)
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry("blocking-tick")
	if entry == nil {
		t.Fatal("expected entry in registry, got nil")
	}
	if len(entry.HeldBy) != 1 || entry.HeldBy[0].PID != customHolderPID {
		t.Errorf("HeldBy: got %+v, want one (id=target, pid=%d)", entry.HeldBy, customHolderPID)
	}

	// Cleanup: detach all live deps in reverse.
	v, _ := schema.LoadValidator(testfixtures.RepoSchemasDir(t), io.Discard)
	for i := len(res.LiveStack) - 1; i >= 0; i-- {
		orchestrator.DetachLiveDep(res.LiveStack[i], root, "target", v, io.Discard, io.Discard)
	}
}

func TestRunDeps_DAGCycle(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "a", []string{"b"}, "true")
	writeNonBlockingDep(t, root, "b", []string{"a"}, "true")

	res, _, errBuf := runDepsForTest(t, root, "a", "a", os.Getpid())
	if res.ExitCode != 1 {
		t.Errorf("ExitCode: got %d, want 1", res.ExitCode)
	}
	if !strings.Contains(errBuf, "cycle") {
		t.Errorf("stderr should mention cycle; got: %s", errBuf)
	}
}

func TestRunDeps_DepFileMissing(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "target", []string{"ghost"}, "true")

	res, _, _ := runDepsForTest(t, root, "target", "target", os.Getpid())
	if res.ExitCode != 1 {
		t.Errorf("ExitCode: got %d, want 1", res.ExitCode)
	}
}

func TestRunDeps_TransitiveCascade(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingDep(t, root, "leaf-fail", nil, "false")
	writeNonBlockingDep(t, root, "middle", []string{"leaf-fail"}, "true")
	writeNonBlockingDep(t, root, "target", []string{"middle"}, "true")

	res, stdout, _ := runDepsForTest(t, root, "target", "target", os.Getpid())
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode: got %d, want 0", res.ExitCode)
	}
	// middle should have been cascade-skipped and emitted to stdout.
	if !strings.Contains(stdout, `"sensor_id":"middle"`) || !strings.Contains(stdout, `"kind":"cascade"`) {
		t.Errorf("expected middle's cascade signal on stdout; got: %s", stdout)
	}
	// target's cascade is in CascadeSig (not emitted).
	if res.CascadeSig == nil {
		t.Fatal("target should have CascadeSig (its dep middle cascaded)")
	}
}
```

- [ ] **Step 3: Run tests and confirm they fail**

Run: `go test ./lib/orchestrator/ -run TestRunDeps_ -v`

Expected: every test FAILS — `RunDeps` is the stub returning `ExitCode: 1`.

- [ ] **Step 4: Implement `RunDeps` in `lib/orchestrator/preflight.go`**

Replace the stub body of `RunDeps` with:

```go
func RunDeps(
	ctx context.Context,
	targetID, projectRoot, schemasDir, holderID string,
	holderPID int,
	v *schema.Validator,
	stdout, stderr io.Writer,
) *RunDepsResult {
	res := &RunDepsResult{
		Signals: map[string]map[string]interface{}{},
	}

	order, err := Resolve(targetID, projectRoot)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		res.ExitCode = 1
		return res
	}
	res.Order = order

	for _, s := range order {
		if err := v.Validate(schema.TargetSensor, s.JSON); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			res.ExitCode = 1
			return res
		}
	}

	for _, s := range order {
		if s.ID == targetID {
			continue
		}
		execMap, _ := s.JSON["execution"].(map[string]interface{})
		blocking, _ := execMap["blocking"].(bool)
		if blocking {
			depID, attachErr := AttachLiveDep(ctx, s, projectRoot, holderID, holderPID, v, stdout, stderr)
			if attachErr != nil {
				cascade := buildSimpleSignal(targetID, "error", "high", "dep_start_failed", attachErr.Error())
				_ = json.NewEncoder(stdout).Encode(cascade)
				res.ExitCode = 1
				return res
			}
			res.LiveStack = append(res.LiveStack, depID)
			res.Signals[s.ID] = map[string]interface{}{"verdict": "pass"}
			continue
		}
		if blocker := FirstFailedDep(s, res.Signals); blocker != nil {
			cascade := BuildCascadeSignal(s, blocker)
			if err := v.Validate(schema.TargetSignal, cascade); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				res.ExitCode = 1
				return res
			}
			_ = json.NewEncoder(stdout).Encode(cascade)
			res.Signals[s.ID] = cascade
			continue
		}
		sig, sigCode := RunOne(ctx, s, schemasDir, v, stdout, stderr)
		if sigCode != 0 {
			res.ExitCode = sigCode
			return res
		}
		res.Signals[s.ID] = sig
	}

	rootSensor := order[len(order)-1]
	if blocker := FirstFailedDep(rootSensor, res.Signals); blocker != nil {
		res.CascadeSig = BuildCascadeSignal(rootSensor, blocker)
	}
	return res
}
```

Update the import block in `preflight.go`:

```go
import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/iurykrieger/harness-framework/lib/schema"
)
```

- [ ] **Step 5: Run tests and confirm they pass**

Run: `go test ./lib/orchestrator/ -run TestRunDeps_ -v`

Expected: all `TestRunDeps_*` tests PASS.

- [ ] **Step 6: Run all orchestrator tests for regression**

Run: `go test ./lib/orchestrator/ -v`

Expected: every test PASS, including `TestRunWithDeps_*`, `TestRunOneWithLiveDeps_*`, `TestRunPreparePhase_*`, `TestAttachLiveDep_*`, `TestRebindDepHolderPID_*`.

- [ ] **Step 7: Run vet**

Run: `go vet ./lib/orchestrator/...`

Expected: no output.

- [ ] **Step 8: Commit**

```bash
git add lib/orchestrator/preflight.go lib/orchestrator/preflight_test.go
git commit -m "$(cat <<'EOF'
feat(orchestrator): add RunDeps for shared dep iteration

RunDeps resolves a sensor's depends_on graph, validates every sensor,
and iterates topologically — running non-blocking deps via RunOne and
blocking deps via AttachLiveDep. The root is intentionally skipped; the
caller handles it. Intermediate cascades emit to stdout; the root's
cascade (if any) returns in RunDepsResult.CascadeSig so the caller can
wrap it (e.g. /start-sensor translates to verdict=error with
metadata.cause=dep_cascade).

Pure addition; no callers yet. RunWithDeps refactor follows in the next
commit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Refactor `RunWithDeps` to delegate the dep loop to `RunDeps`

Zero behavior change for `/run-sensor`. The existing `TestRunWithDeps_*` tests are the regression suite.

**Files:**
- Modify: `lib/orchestrator/run.go` (rewrite `RunWithDeps` body)

- [ ] **Step 1: Replace `RunWithDeps` in `lib/orchestrator/run.go`**

Replace the entire body of `RunWithDeps` (currently lines 25-99) with:

```go
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

	rootID := StripJSONExt(filepath.Base(abs))
	holderPID := os.Getpid()
	pre := RunDeps(ctx, rootID, root, schemasDir, rootID, holderPID, v, stdout, stderr)

	projectRoot := filepath.Dir(filepath.Dir(abs))
	defer func() {
		for i := len(pre.LiveStack) - 1; i >= 0; i-- {
			DetachLiveDep(pre.LiveStack[i], projectRoot, rootID, v, stdout, stderr)
		}
	}()

	if pre.ExitCode != 0 {
		return pre.ExitCode
	}
	if pre.CascadeSig != nil {
		if err := v.Validate(schema.TargetSignal, pre.CascadeSig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(pre.CascadeSig)
		return 1
	}

	target := pre.Order[len(pre.Order)-1]
	_, code = RunOne(ctx, target, schemasDir, v, stdout, stderr)
	return code
}
```

The existing functions `FirstFailedDep`, `StripJSONExt`, `RunWithDepsRoot` remain unchanged. Imports may need pruning — confirm `encoding/json`, `path/filepath`, `os`, `fmt`, `io`, `context`, and the local `schema` package are imported and any unused ones removed.

- [ ] **Step 2: Run all orchestrator tests**

Run: `go test ./lib/orchestrator/ -v`

Expected: every test PASSES, including:
- `TestRunWithDeps_ChainPasses`
- `TestRunWithDeps_CascadesOnDepFail`
- `TestRunWithDeps_CycleAborts`
- `TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep`
- All `TestRunDeps_*`, `TestRunPreparePhase_*`, `TestAttachLiveDep_*`, `TestRebindDepHolderPID_*`.

- [ ] **Step 3: Run all `/run-sensor` runner tests for regression**

Run: `go test -tags=run_computational ./skills/run-sensor/...`
Run: `go test -tags=run_inferential ./skills/run-sensor/...`

Expected: all tests PASS.

- [ ] **Step 4: Run vet on the whole repo**

Run: `go vet ./lib/orchestrator/...`
Run: `go vet -tags=run_computational ./skills/run-sensor/...`
Run: `go vet -tags=run_inferential ./skills/run-sensor/...`

Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add lib/orchestrator/run.go
git commit -m "$(cat <<'EOF'
refactor(orchestrator): delegate RunWithDeps dep loop to RunDeps

Zero behavior change for /run-sensor. The dep iteration, cascade
emission, and reverse-detach defer are unchanged in semantics — they
now route through RunDeps for sharing with /start-sensor. Existing run
tests are the regression suite.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Rewrite `/start-sensor::runStart` for orchestrated deps + rename `metadata.kind` (TDD)

This is the issue's actual fix: `/start-sensor` now resolves `depends_on`, runs the target's `prepare[]` fail-fast, and rebinds dep holder pids post-spawn. Also applies the rename `metadata.kind` → `started`/`rejected`/`failed` with `metadata.cause` discriminator.

This task is large. Break into steps that each produce a green tree.

**Files:**
- Modify: `skills/start-sensor/scripts/start.go` (full rewrite of `runStart` + supporting functions)
- Modify: `skills/start-sensor/scripts/start_test.go` (existing test assertions + new tests)

- [ ] **Step 1: Update existing tests' assertions for the rename (still failing because runStart hasn't changed)**

In `skills/start-sensor/scripts/start_test.go`:

Replace:
```go
	exit, _ := runStart(root, []string{"not-blocking"})
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
```

with:
```go
	exit, sig := runStart(root, []string{"not-blocking"})
	if exit != 2 {
		t.Fatalf("expected exit 2, got %d", exit)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind: got %v, want failed", md["kind"])
	}
	if md["cause"] != "not_blocking" {
		t.Errorf("metadata.cause: got %v, want not_blocking", md["cause"])
	}
```

Replace:
```go
	if sig["metadata"].(map[string]interface{})["kind"] != "start_rejected" {
		t.Fatalf("metadata.kind: got %v", sig["metadata"])
	}
```

with:
```go
	if sig["metadata"].(map[string]interface{})["kind"] != "rejected" {
		t.Fatalf("metadata.kind: got %v", sig["metadata"])
	}
```

- [ ] **Step 2: Run tests and confirm they fail**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/ -v`

Expected: `TestStart_RejectsNonBlocking` and `TestStart_RejectsAlreadyRunning` FAIL because runStart still emits `start_failed`/`start_rejected`.

- [ ] **Step 3: Add new test fixture helpers + new tests for orchestrated deps**

Append to `skills/start-sensor/scripts/start_test.go`:

```go
import (
	// add to existing imports:
	"context"
	"io"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
)

// writeBlockingTarget writes a fixture sensor at <root>/sensors/<id>.json
// with execution.blocking=true and a stream output_parsing pattern.
// Adds depends_on if non-nil, prepare[] if non-nil.
func writeBlockingTarget(t *testing.T, root, id string, dependsOn []string, prepare []map[string]string) {
	t.Helper()
	body := blockingFixtureBody()
	if len(dependsOn) > 0 {
		ds := []interface{}{}
		for _, d := range dependsOn {
			ds = append(ds, d)
		}
		body["depends_on"] = ds
	}
	if len(prepare) > 0 {
		ps := []interface{}{}
		for _, p := range prepare {
			ps = append(ps, map[string]interface{}{"command": p["command"]})
		}
		body["execution"].(map[string]interface{})["prepare"] = ps
	}
	writeFixtureSensor(t, root, id, body)
}

// writeNonBlockingSetupDep writes a kind=setup non-blocking sensor that
// runs `command` (typically "true" or "false").
func writeNonBlockingSetupDep(t *testing.T, root, id, command string) {
	t.Helper()
	body := map[string]interface{}{
		"version":     "1.0.0",
		"name":        "Setup " + id,
		"description": "setup fixture for " + id,
		"determinism": "high",
		"kind":        "setup",
		"type":        "computational",
		"regulation":  "behaviour",
		"phase":       "on-demand",
		"output":      "single",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50, "timeout_ms": 1000},
		},
		"triggers": []interface{}{map[string]interface{}{"on": "manual"}},
		"execution": map[string]interface{}{
			"command": command,
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
				map[string]interface{}{"exit_code": "*", "verdict": "fail", "severity": "high"},
			},
		},
		"verification": map[string]interface{}{
			"golden_cases": []interface{}{
				map[string]interface{}{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"},
			},
		},
	}
	writeFixtureSensor(t, root, id, body)
}

func TestStart_WithSetupDepPASS(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingSetupDep(t, root, "setup-pass", "true")
	writeBlockingTarget(t, root, "target", []string{"setup-pass"}, nil)

	exit, sig := runStart(root, []string{"target"})
	defer cleanupStartedTarget(t, root, "target")

	if exit != 0 {
		t.Fatalf("exit: got %d, want 0; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "started" {
		t.Errorf("metadata.kind: got %v, want started", md["kind"])
	}
	depChain, _ := md["dep_chain"].([]interface{})
	if len(depChain) != 0 {
		// dep_chain only includes BLOCKING deps; setup is non-blocking.
		t.Errorf("dep_chain (blocking only): got %v, want []", depChain)
	}
	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("target") == nil {
		t.Errorf("target should be in registry after started")
	}
}

func TestStart_WithSetupDepFAIL(t *testing.T) {
	root := t.TempDir()
	writeNonBlockingSetupDep(t, root, "setup-fail", "false")
	writeBlockingTarget(t, root, "target", []string{"setup-fail"}, nil)

	exit, sig := runStart(root, []string{"target"})

	if exit != 1 {
		t.Fatalf("exit: got %d, want 1; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind: got %v, want failed", md["kind"])
	}
	if md["cause"] != "dep_cascade" {
		t.Errorf("metadata.cause: got %v, want dep_cascade", md["cause"])
	}
	if md["failed_dep_id"] != "setup-fail" {
		t.Errorf("metadata.failed_dep_id: got %v, want setup-fail", md["failed_dep_id"])
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("target") != nil {
		t.Errorf("target should NOT be in registry on dep cascade")
	}
}

func TestStart_WithBlockingDepStartFresh(t *testing.T) {
	root := t.TempDir()
	writeBlockingDepFixtureForStart(t, root, "blocking-tick")
	writeBlockingTarget(t, root, "target", []string{"blocking-tick"}, nil)

	exit, sig := runStart(root, []string{"target"})
	defer cleanupStartedTarget(t, root, "target")
	defer cleanupBlockingDep(t, root, "blocking-tick", "target")

	if exit != 0 {
		t.Fatalf("exit: got %d, want 0; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "started" {
		t.Fatalf("metadata.kind: got %v, want started", md["kind"])
	}
	depChain, _ := md["dep_chain"].([]interface{})
	if len(depChain) != 1 || depChain[0] != "blocking-tick" {
		t.Errorf("dep_chain: got %v, want [blocking-tick]", depChain)
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	depEntry := rs.FindEntry("blocking-tick")
	if depEntry == nil {
		t.Fatal("blocking-tick should be in registry")
	}
	if len(depEntry.HeldBy) != 1 {
		t.Fatalf("HeldBy length: got %d, want 1", len(depEntry.HeldBy))
	}
	targetEntry := rs.FindEntry("target")
	if targetEntry == nil {
		t.Fatal("target should be in registry")
	}
	if depEntry.HeldBy[0].PID != targetEntry.PID {
		t.Errorf("dep holder PID: got %d, want target subprocess PID %d (rebind failed)",
			depEntry.HeldBy[0].PID, targetEntry.PID)
	}
}

func TestStart_PrepareFAIL(t *testing.T) {
	root := t.TempDir()
	writeBlockingTarget(t, root, "target", nil, []map[string]string{
		{"command": "false"},
	})

	exit, sig := runStart(root, []string{"target"})

	if exit != 1 {
		t.Fatalf("exit: got %d, want 1; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "failed" {
		t.Errorf("metadata.kind: got %v, want failed", md["kind"])
	}
	if md["cause"] != "prepare_failed" {
		t.Errorf("metadata.cause: got %v, want prepare_failed", md["cause"])
	}
	lc, _ := md["lifecycle"].(map[string]interface{})
	if lc == nil {
		t.Fatal("metadata.lifecycle missing")
	}
	prep, _ := lc["prepare"].([]interface{})
	if len(prep) != 1 {
		t.Fatalf("lifecycle.prepare: got %d entries, want 1", len(prep))
	}
	step := prep[0].(map[string]interface{})
	if step["verdict"] != "fail" {
		t.Errorf("prepare[0].verdict: got %v, want fail", step["verdict"])
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("target") != nil {
		t.Error("target should NOT be in registry after prepare fail")
	}
}

func TestStart_PrepareFAIL_DetachesLiveStack(t *testing.T) {
	root := t.TempDir()
	writeBlockingDepFixtureForStart(t, root, "blocking-tick")
	writeBlockingTarget(t, root, "target", []string{"blocking-tick"}, []map[string]string{
		{"command": "false"},
	})

	exit, sig := runStart(root, []string{"target"})

	if exit != 1 {
		t.Fatalf("exit: got %d, want 1; sig=%+v", exit, sig)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["cause"] != "prepare_failed" {
		t.Fatalf("metadata.cause: got %v, want prepare_failed", md["cause"])
	}

	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	if rs.FindEntry("blocking-tick") != nil {
		t.Error("blocking-tick should be torn down after detachAll on prepare fail")
	}
	if rs.FindEntry("target") != nil {
		t.Error("target should NOT be in registry after prepare fail")
	}
}

// writeBlockingDepFixtureForStart is a local copy of the orchestrator
// test helper. Build-tagged tests live in distinct packages, so we
// duplicate the small helper rather than couple test packages.
func writeBlockingDepFixtureForStart(t *testing.T, root, id string) {
	t.Helper()
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Blocking tick",
"description": "blocking tick",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50}
},
"execution": {
  "command": "while true; do echo TICK; sleep 0.1; done",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^TICK$","verdict":"pass","severity":"info"}]}
}
}`)
	dir := filepath.Join(root, "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// cleanupStartedTarget brings down the spawned target so the temp dir
// can be safely cleaned up at end-of-test.
func cleanupStartedTarget(t *testing.T, root, id string) {
	t.Helper()
	r := registry.NewRoot(root)
	rs, _ := registry.Load(r)
	entry := rs.FindEntry(id)
	if entry == nil {
		return
	}
	// Best-effort SIGKILL the process group; ignore errors.
	if entry.PGID > 0 {
		_ = syscallKillGroup(entry.PGID)
	}
	if entry.WatcherPID > 0 {
		_ = syscallKillPID(entry.WatcherPID)
	}
}

// cleanupBlockingDep brings down a blocking dep that was attached during
// the test, by detaching the holder and (if last holder) stopping it.
func cleanupBlockingDep(t *testing.T, root, depID, holderID string) {
	t.Helper()
	v, code := schema.LoadValidator(testfixtures.RepoSchemasDir(t), io.Discard)
	if code != 0 {
		return
	}
	orchestrator.DetachLiveDep(depID, root, holderID, v, io.Discard, io.Discard)
}
```

Add a helper file `skills/start-sensor/scripts/start_unix_test_helpers.go` to provide cleanup primitives that need syscall:

```go
//go:build start_sensor

package main

import "syscall"

func syscallKillGroup(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

func syscallKillPID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
```

Update test-file imports as needed:

```go
import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/testfixtures"
)
```

(`context` is no longer needed if no test uses it directly; remove if unused.)

- [ ] **Step 4: Run tests and confirm they fail to compile or fail with current runStart**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/ -v`

Expected: tests FAIL because runStart hasn't been rewritten yet.

- [ ] **Step 5: Rewrite `runStart` and helpers in `skills/start-sensor/scripts/start.go`**

Replace the entire content of `start.go` with:

```go
//go:build start_sensor

// start spawns a blocking sensor's command in a detached session,
// records it in the registry, and emits a Signal verdict=pass,
// metadata.kind=started. Runs the full lifecycle: pre-flight (deps via
// orchestrator.RunDeps) → prepare[] of root → spawn detached + watcher
// + registry write → rebind dep holder pids → started.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	libsensor "github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start: cwd:", err)
		os.Exit(2)
	}
	exit, sig := runStart(root, os.Args[1:])
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}

// runStart performs the full /start-sensor lifecycle for sensor id given
// in args[0]. Returns (exitCode, finalSignal). The signal is encoded by
// the caller; tests inspect it directly.
func runStart(projectRoot string, args []string) (int, map[string]interface{}) {
	v, vCode := schema.LoadValidator("", os.Stderr)
	if vCode != 0 {
		return vCode, finalSignal("unknown", nil, "failed", "bootstrap_failed", nil, "schema validator init failed")
	}

	if len(args) < 1 {
		return 2, finalSignal("unknown", nil, "failed", "bootstrap_failed", nil, "missing sensor id argument")
	}
	id := args[0]

	path, err := libsensor.ResolveByID(id, projectRoot)
	if err != nil {
		return 2, finalSignal(id, nil, "failed", "resolve_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("resolve: %v", err))
	}

	sensorJSON, err := loadSensorJSON(path)
	if err != nil {
		return 2, finalSignal(id, nil, "failed", "resolve_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			err.Error())
	}

	if err := v.Validate(schema.TargetSensor, sensorJSON); err != nil {
		return 1, finalSignal(id, sensorJSON, "failed", "schema_invalid",
			map[string]interface{}{"error_excerpt": fmt.Sprintf("%v", err)},
			fmt.Sprintf("schema: %v", err))
	}

	execMap, _ := sensorJSON["execution"].(map[string]interface{})
	blocking, _ := execMap["blocking"].(bool)
	if !blocking {
		return 2, finalSignal(id, sensorJSON, "failed", "not_blocking", nil,
			"sensor is not blocking; use /run-sensor instead")
	}

	// Pre-flight: resolve DAG, run deps, detect cascade.
	placeholderPID := os.Getpid()
	pre := orchestrator.RunDeps(
		context.Background(), id, projectRoot, "" /*schemasDir*/, id /*holderID*/, placeholderPID,
		v, os.Stdout, os.Stderr,
	)
	detachAll := func() {
		for i := len(pre.LiveStack) - 1; i >= 0; i-- {
			orchestrator.DetachLiveDep(pre.LiveStack[i], projectRoot, id, v, os.Stdout, os.Stderr)
		}
	}

	if pre.ExitCode != 0 {
		detachAll()
		return pre.ExitCode, finalSignal(id, sensorJSON, "failed", "preflight_failed", nil,
			"pre-flight failed; see earlier signals or stderr")
	}
	if pre.CascadeSig != nil {
		md, _ := pre.CascadeSig["metadata"].(map[string]interface{})
		aux := map[string]interface{}{
			"failed_dep_id":       md["failed_dep_id"],
			"failed_dep_run_id":   md["failed_dep_run_id"],
			"failed_dep_verdict":  md["failed_dep_verdict"],
			"failed_dep_severity": md["failed_dep_severity"],
		}
		failedID, _ := md["failed_dep_id"].(string)
		failedVerdict, _ := md["failed_dep_verdict"].(string)
		detachAll()
		return 1, finalSignal(id, sensorJSON, "failed", "dep_cascade", aux,
			fmt.Sprintf("dependency %q produced verdict=%s; root not started", failedID, failedVerdict))
	}

	target := pre.Order[len(pre.Order)-1]

	// Run target's prepare[] fail-fast.
	prepResults, prepFailed := orchestrator.RunPreparePhase(context.Background(), target, readTimeoutMS(target.JSON))
	if prepFailed {
		detachAll()
		aux := map[string]interface{}{
			"lifecycle": map[string]interface{}{"prepare": prepResults},
		}
		return 1, finalSignal(id, sensorJSON, "failed", "prepare_failed", aux,
			"target prepare[] failed")
	}

	// Singleton + spawn detached + watcher + registry write.
	command, _ := execMap["command"].(string)
	r := registry.NewRoot(projectRoot)
	logDir := r.SensorDir(id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		detachAll()
		return 1, finalSignal(id, sensorJSON, "failed", "registry_write_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("mkdir log dir: %v", err))
	}
	if err := os.WriteFile(r.RawLog(id), nil, 0o644); err != nil {
		detachAll()
		return 1, finalSignal(id, sensorJSON, "failed", "registry_write_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("create raw.log: %v", err))
	}
	if err := os.WriteFile(r.SignalsLog(id), nil, 0o644); err != nil {
		detachAll()
		return 1, finalSignal(id, sensorJSON, "failed", "registry_write_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("create signals.log: %v", err))
	}

	watcherPath, err := watcherBinaryPath()
	if err != nil {
		detachAll()
		return 1, finalSignal(id, sensorJSON, "failed", "watcher_spawn_failed",
			map[string]interface{}{"error_excerpt": err.Error()},
			fmt.Sprintf("watcher binary: %v", err))
	}

	type spawnResult struct {
		det         subprocess.DetachResult
		watcherProc *os.Process
		envelope    libsensor.Envelope
	}
	var spawned spawnResult
	var alreadyRunning bool
	var alreadyRunningPID int

	lockErr := registry.WithFileLock(r.LockFile(), func() error {
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
			// Kill the just-spawned root subprocess so we don't orphan it.
			if det.PGID > 0 {
				_ = syscallKillGroupForBackout(det.PGID)
			}
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
		cause := "registry_write_failed"
		if isWatcherSpawnError(lockErr) {
			cause = "watcher_spawn_failed"
		}
		detachAll()
		return 1, finalSignal(id, sensorJSON, "failed", cause,
			map[string]interface{}{"error_excerpt": lockErr.Error()},
			fmt.Sprintf("write registry: %v", lockErr))
	}

	if alreadyRunning {
		detachAll()
		return 1, finalSignal(id, sensorJSON, "rejected", "",
			map[string]interface{}{"existing_pid": alreadyRunningPID},
			fmt.Sprintf("sensor %q already running with pid %d", id, alreadyRunningPID))
	}

	// Rebind: dep holders go from placeholderPID to spawned.det.PID.
	var rebindWarnings []interface{}
	for _, depID := range pre.LiveStack {
		if err := orchestrator.RebindDepHolderPID(depID, projectRoot, id, placeholderPID, spawned.det.PID); err != nil {
			rebindWarnings = append(rebindWarnings, map[string]interface{}{
				"dep_id": depID,
				"error":  err.Error(),
			})
		}
	}

	aux := map[string]interface{}{
		"pid":         spawned.det.PID,
		"watcher_pid": spawned.watcherProc.Pid,
		"log_dir":     filepath.Join(".runtime", "sensors", id),
		"next_cursor": 0,
	}
	if len(prepResults) > 0 {
		aux["lifecycle"] = map[string]interface{}{"prepare": prepResults}
	}
	if len(pre.LiveStack) > 0 {
		ds := []interface{}{}
		for _, d := range pre.LiveStack {
			ds = append(ds, d)
		}
		aux["dep_chain"] = ds
	}
	if len(rebindWarnings) > 0 {
		aux["rebind_warnings"] = rebindWarnings
	}

	sig := finalSignal(id, sensorJSON, "started", "", aux,
		fmt.Sprintf("sensor %q started, pid=%d, watcher_pid=%d", id, spawned.det.PID, spawned.watcherProc.Pid))
	sig["run_id"] = spawned.envelope.RunID
	sig["started_at"] = spawned.envelope.StartedAt
	return 0, validateSignal(v, sig, id)
}

func loadSensorJSON(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read sensor: %w", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse sensor: %w", err)
	}
	return m, nil
}

func stringField(m map[string]interface{}, k string) string {
	v, _ := m[k].(string)
	return v
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
	if v, ok := lat["timeout_ms"].(float64); ok {
		return int(v)
	}
	return 0
}

// finalSignal builds the terminal signal of /start-sensor. cause is
// required for kind="failed" and ignored for "started"/"rejected".
// aux is merged into metadata, carrying kind-specific fields per the
// design spec's table.
func finalSignal(
	id string,
	sensorJSON map[string]interface{},
	kind string,
	cause string,
	aux map[string]interface{},
	rationale string,
) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	verdict := "error"
	severity := "high"
	if kind == "started" {
		verdict = "pass"
		severity = "info"
	}
	version := "0.0.0"
	if sensorJSON != nil {
		version = stringField(sensorJSON, "version")
		if version == "" {
			version = "0.0.0"
		}
	}
	md := map[string]interface{}{"kind": kind}
	if kind == "failed" && cause != "" {
		md["cause"] = cause
	}
	for k, v := range aux {
		md[k] = v
	}
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     version,
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     verdict,
		"severity":    severity,
		"confidence":  1.0,
		"evidence": []interface{}{
			map[string]interface{}{"rationale": rationale},
		},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}

// validateSignal checks sig against signal.json. If validation fails it
// logs the error to stderr and returns a minimal emergency signal so
// the bug surfaces without recursion. On success it returns sig
// unchanged.
func validateSignal(v *schema.Validator, sig map[string]interface{}, id string) map[string]interface{} {
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		fmt.Fprintf(os.Stderr, "start: BUG: emitted signal failed signal.json validation: %v\n", err)
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

// isWatcherSpawnError detects whether a flock-callback error originated
// from the watcher spawn step. The callback wraps it as "start watcher: ...".
func isWatcherSpawnError(err error) bool {
	return err != nil && len(err.Error()) >= 14 && err.Error()[:14] == "start watcher:"
}

// syscallKillGroupForBackout sends SIGKILL to a process group. Used to
// undo a just-spawned root subprocess when the watcher spawn fails
// inside the flock callback.
func syscallKillGroupForBackout(pgid int) error {
	return killGroup(pgid)
}

// io is unused after the rewrite; suppress the import linter if needed
// by keeping a typed alias usage. The `io.Discard` reference in tests
// keeps imports of the package happy.
var _ = io.Discard
```

Add `skills/start-sensor/scripts/start_unix.go` updates if needed (the existing file holds `watcherSysProcAttr`). Confirm this file already exists:

```bash
cat skills/start-sensor/scripts/start_unix.go
```

It should define `watcherSysProcAttr` and possibly `watcherBinaryPath`. Add a `killGroup` helper to it if absent:

```go
//go:build start_sensor

package main

import "syscall"

// killGroup sends SIGKILL to the entire process group identified by pgid.
func killGroup(pgid int) error {
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
```

(Only add if `killGroup` is not already defined elsewhere.)

- [ ] **Step 6: Run tests and confirm they pass**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/ -v`

Expected: every test PASSES — `TestStart_RejectsNonBlocking`, `TestStart_RejectsAlreadyRunning`, `TestStart_WithSetupDepPASS`, `TestStart_WithSetupDepFAIL`, `TestStart_WithBlockingDepStartFresh`, `TestStart_PrepareFAIL`, `TestStart_PrepareFAIL_DetachesLiveStack`.

If any test fails, debug:
- `TestStart_WithBlockingDepStartFresh` — likely a rebind issue. Verify `RebindDepHolderPID` is called with the right (oldPID=placeholder, newPID=spawned.det.PID).
- `TestStart_WithSetupDepFAIL` — likely cascade signal field mapping. Verify `aux["failed_dep_id"]` is set.
- `TestStart_PrepareFAIL_DetachesLiveStack` — `detachAll` ordering. Verify it runs before the `return`.

- [ ] **Step 7: Run vet**

Run: `go vet -tags=start_sensor ./skills/start-sensor/...`

Expected: no output.

- [ ] **Step 8: Run all repo tests for regression**

Run: `go test ./lib/...`
Run: `go test -tags=start_sensor ./skills/start-sensor/...`
Run: `go test -tags=start_watcher ./skills/start-sensor/...`
Run: `go test -tags=run_computational ./skills/run-sensor/...`
Run: `go test -tags=run_inferential ./skills/run-sensor/...`
Run: `go test -tags=stop_sensor ./skills/stop-sensor/...`
Run: `go test -tags=tail_sensor ./skills/tail-sensor/...`
Run: `go test -tags=list_sensors ./skills/list-sensors/...`

Expected: every package passes.

- [ ] **Step 9: Commit**

```bash
git add skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_test.go skills/start-sensor/scripts/start_unix.go skills/start-sensor/scripts/start_unix_test_helpers.go
git commit -m "$(cat <<'EOF'
feat(start-sensor): orchestrate depends_on + run target prepare (#7)

/start-sensor now resolves the target's depends_on graph via
orchestrator.RunDeps before spawning the target subprocess: setup and
non-blocking deps run via RunOne; blocking deps come up via
AttachLiveDep with a placeholder pid (start.go's pid). After the target
subprocess is spawned, dep holder pids are rebound to the new
subprocess pid via RebindDepHolderPID so /list-sensors and /stop-sensor
see a holder that mirrors the target's lifetime.

The target's execution.prepare[] now runs fail-fast before spawn (was a
known gap, removed disclaimer). Cascade from a failed dep emits a
final signal with metadata.kind=failed, cause=dep_cascade, populated
failed_dep_* fields, BEFORE any subprocess is spawned.

Renames metadata.kind values for /start-sensor terminal signals:
- start_rejected → rejected
- start_failed   → failed (with metadata.cause discriminator)
The "started" kind keeps its name. metadata.cause discriminates failure
modes: dep_cascade, prepare_failed, spawn_failed, watcher_spawn_failed,
registry_write_failed, schema_invalid, resolve_failed, preflight_failed,
not_blocking, bootstrap_failed.

Closes #7.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Rename `metadata.kind` in `/stop-sensor`

Drops `stop_` prefix; folds `stop_held_with_dead_holders` into `held` with `metadata.dead_holders=[...]`.

**Files:**
- Modify: `skills/stop-sensor/scripts/stop.go`
- Modify: `skills/stop-sensor/scripts/stop_test.go`

- [ ] **Step 1: Update assertions in `stop_test.go`**

Replace:
```go
	if sig["verdict"] != "warn" || sig["metadata"].(map[string]interface{})["kind"] != "stop_not_running" {
```
with:
```go
	if sig["verdict"] != "warn" || sig["metadata"].(map[string]interface{})["kind"] != "not_running" {
```

Replace:
```go
	if md["kind"] != "stop_held" {
```
with:
```go
	if md["kind"] != "held" {
```

- [ ] **Step 2: Run tests and confirm they fail**

Run: `go test -tags=stop_sensor ./skills/stop-sensor/scripts/ -v`

Expected: tests FAIL because stop.go still emits `stop_*` kinds.

- [ ] **Step 3: Update kind literals in `stop.go`**

In `skills/stop-sensor/scripts/stop.go`, apply these replacements:

| Old | New |
|---|---|
| `"stop_not_running"` (line 53) | `"not_running"` |
| `"stop_failed"` (line 59) | `"failed"` |
| `"stop_failed"` (line 90) | `"failed"` |
| `"stop_not_running"` (line 94) | `"not_running"` |
| `kind := "stop_held"` (line 98) | `kind := "held"` |
| `kind = "stop_held_with_dead_holders"` (line 100) | (remove this branch entirely) |
| `"stop_failed"` (line 142) | `"failed"` |

Also fold the `stop_held_with_dead_holders` logic into `held` by including a `dead_holders` array in metadata. Update lines 97-108 (the `if registry.IsHeld(entry)` block) to:

```go
	if registry.IsHeld(entry) {
		sig := simpleSignal(res, id, "warn", "low", "held", fmt.Sprintf("sensor %q still held by %d holders", id, len(entry.HeldBy)))
		md := sig["metadata"].(map[string]interface{})
		md["holders"] = holderSummaries(entry.HeldBy)
		md["dead_holders"] = deadHolderSummaries(entry.HeldBy)
		if len(reaped) > 0 {
			md["reaped_holders"] = holderSummaries(reaped)
		}
		return 0, validateSignal(v, sig, id)
	}
```

Add a helper at the bottom of `stop.go` (or wherever `holderSummaries` lives — find it via `grep -n "func holderSummaries" skills/stop-sensor/scripts/stop.go`):

```go
// deadHolderSummaries returns the subset of holders with kind=sensor and
// pid no longer alive. Empty slice (not nil) when none.
func deadHolderSummaries(holders []registry.HeldByEntry) []map[string]interface{} {
	out := []map[string]interface{}{}
	for _, h := range holders {
		if h.Kind != "sensor" {
			continue
		}
		if registry.IsPIDAlive(h.PID) {
			continue
		}
		out = append(out, map[string]interface{}{
			"kind":        h.Kind,
			"id":          h.ID,
			"pid":         h.PID,
			"attached_at": h.AttachedAt,
		})
	}
	return out
}
```

You will also no longer need `hasDeadHolder` — remove the function definition (`grep -n "func hasDeadHolder" skills/stop-sensor/scripts/stop.go`) since the kind branch is gone.

- [ ] **Step 4: Run tests and confirm they pass**

Run: `go test -tags=stop_sensor ./skills/stop-sensor/scripts/ -v`

Expected: every test PASSES.

- [ ] **Step 5: Run vet**

Run: `go vet -tags=stop_sensor ./skills/stop-sensor/...`

Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add skills/stop-sensor/scripts/stop.go skills/stop-sensor/scripts/stop_test.go
git commit -m "$(cat <<'EOF'
refactor(stop-sensor): drop stop_ prefix from metadata.kind values

Renames terminal-signal metadata.kind values:
- stop_not_running → not_running
- stop_held       → held (folded with stop_held_with_dead_holders)
- stop_failed     → failed
The success-path "aggregate" kind is preserved (it describes the signal
shape, not the action).

stop_held_with_dead_holders is removed in favor of "held" with a new
metadata.dead_holders array listing the subset of remaining holders
whose pid is no longer alive (empty when none).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Rename `metadata.kind` in `/tail-sensor`

**Files:**
- Modify: `skills/tail-sensor/scripts/tail.go`
- Modify: `skills/tail-sensor/scripts/tail_test.go`

- [ ] **Step 1: Update assertions in `tail_test.go`**

Replace:
```go
	if md["kind"] != "tail_envelope" {
```
with:
```go
	if md["kind"] != "envelope" {
```

Replace:
```go
	if sig["metadata"].(map[string]interface{})["kind"] != "tail_not_running" {
```
with:
```go
	if sig["metadata"].(map[string]interface{})["kind"] != "not_running" {
```

- [ ] **Step 2: Run tests and confirm they fail**

Run: `go test -tags=tail_sensor ./skills/tail-sensor/scripts/ -v`

Expected: tests FAIL.

- [ ] **Step 3: Update kind literals in `tail.go`**

In `skills/tail-sensor/scripts/tail.go`:

Replace `"tail_not_running"` (line 71) with `"not_running"`.
Replace `md["kind"] = "tail_envelope"` (line 100) with `md["kind"] = "envelope"`.

- [ ] **Step 4: Run tests and confirm they pass**

Run: `go test -tags=tail_sensor ./skills/tail-sensor/scripts/ -v`

Expected: every test PASSES.

- [ ] **Step 5: Run vet**

Run: `go vet -tags=tail_sensor ./skills/tail-sensor/...`

Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add skills/tail-sensor/scripts/tail.go skills/tail-sensor/scripts/tail_test.go
git commit -m "$(cat <<'EOF'
refactor(tail-sensor): drop tail_ prefix from metadata.kind values

Renames:
- tail_envelope     → envelope
- tail_not_running  → not_running

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Update SKILL.md files + bump plugin version

Documentation pass for the renamed kinds and the new `/start-sensor` orchestrated lifecycle. Plugin version bump for the `metadata.kind` contract change.

**Files:**
- Modify: `skills/start-sensor/SKILL.md`
- Modify: `skills/stop-sensor/SKILL.md`
- Modify: `skills/tail-sensor/SKILL.md`
- Modify: `.claude-plugin/plugin.json`

- [ ] **Step 1: Update `skills/start-sensor/SKILL.md` Output contract section**

Replace the existing "Output contract" section with:

```markdown
## Output contract

Stdout is JSONL. Multiple Signals can be emitted in order:

1. Aggregates of `kind=setup` or non-blocking deps that ran via `RunOne` (`metadata.kind=aggregate`).
2. Acks of blocking deps that the orchestrator brought up (`metadata.kind` ∈ {`dep_attached`, `dep_started`}).
3. Cascade signals for intermediate deps that were skipped because their own dep failed (`metadata.kind=cascade`).
4. **Exactly one** terminal Signal whose `metadata.kind` is one of:
   - `started` — subprocess and watcher are up; the sensor is now alive in the registry. `verdict=pass`. `metadata.next_cursor=0`. Carries `metadata.lifecycle.prepare`, `metadata.dep_chain`, `metadata.rebind_warnings` (omitted when empty).
   - `rejected` — already running (singleton check failed). `verdict=error`. `metadata.existing_pid` carries the live entry's pid.
   - `failed` — anything else preventing a `started` signal. `verdict=error`. `metadata.cause` discriminates: `dep_cascade`, `prepare_failed`, `spawn_failed`, `watcher_spawn_failed`, `registry_write_failed`, `schema_invalid`, `resolve_failed`, `preflight_failed`, `not_blocking`, `bootstrap_failed`. The `dep_cascade` cause carries `failed_dep_id`/`failed_dep_run_id`/`failed_dep_verdict`/`failed_dep_severity` from the failed dep's signal.
```

Replace the existing "Lifecycle integration" section with:

```markdown
## Lifecycle integration

When the target sensor declares `depends_on`, `/start-sensor` resolves the dep graph and brings deps up before spawning the target:

- Setup or non-blocking deps run via `RunOne` (their command terminates; the result PASS or FAIL).
- Blocking deps come up via `AttachLiveDep` — if the dep is already alive in the registry, `/start-sensor` adds a holder; otherwise the dep is started fresh.
- The target's `execution.prepare[]` runs fail-fast after deps are up but before the target subprocess spawns.
- After the target subprocess is spawned, dep holder pids are rebound from `/start-sensor`'s pid to the target subprocess pid, so `/list-sensors` and `/stop-sensor` see a holder that mirrors the target's lifetime.
- On any failure (cascade, prepare fail, spawn fail, watcher fail, registry write fail), every blocking dep we attached this run is detached in reverse order. If the detach drops a dep's last holder, the dep is stopped (SIGTERM/SIGKILL). State is left as before `/start-sensor` ran.

Use `/start-sensor` directly when:

- The blocking sensor is the observation target itself (e.g., the agent wants to watch logs while doing other work in parallel).
- The agent needs to interact with the live process (curl, edit, observe) without an immediately-dependent sensor driving the workflow.
```

Remove any sentence stating "execution.prepare[] is not yet executed" (left over from the previous limitation).

- [ ] **Step 2: Update `skills/stop-sensor/SKILL.md`**

In the description (frontmatter) and body, replace `stop_held` with `held`. Replace the "Output contract" section with:

```markdown
## Output contract

A single aggregate Signal on stdout. `metadata.kind` is one of:

- `aggregate` — the subprocess and watcher were brought down cleanly. `verdict` is the worst-of-stream and exit-side per `signal.Aggregate`.
- `not_running` — no live entry; `verdict=warn`.
- `held` — other holders remain. `verdict=warn`. `metadata.holders` lists remaining holders; `metadata.dead_holders` is the subset whose pid is no longer alive (empty when none). Process not stopped.
- `failed` — registry I/O failed. `verdict=error`.
```

In the "When to use --reap-dead-holders" section, replace:
```markdown
If `/list-sensors` shows the sensor with `held_by` entries whose `pid_alive=false`, the holder process (typically a crashed orchestrator running a dependent sensor) leaked the hold. Pass `--reap-dead-holders` to drop those entries before evaluating whether the sensor is still held. The aggregate Signal carries `metadata.reaped_holders` listing what was removed.
```
with:
```markdown
If `/list-sensors` shows the sensor with `held_by` entries whose `pid_alive=false` — or the `held` signal carries a non-empty `metadata.dead_holders` — the holder process (typically a crashed orchestrator running a dependent sensor, or a `/start-sensor` that was SIGKILL'd between attach and rebind) leaked the hold. Pass `--reap-dead-holders` to drop those entries before evaluating whether the sensor is still held. The aggregate Signal carries `metadata.reaped_holders` listing what was removed.
```

- [ ] **Step 3: Update `skills/tail-sensor/SKILL.md`**

In the description (frontmatter), replace `tail_envelope` with `envelope`. Replace the "Output contract" section with:

```markdown
## Output contract

JSONL on stdout: zero or more individual Signals, then exactly one envelope Signal whose `metadata.kind=envelope` and `metadata.next_cursor=<line count after this read>`. The agent should parse the LAST line, extract `next_cursor`, and pass it as `<cursor>` on the next call. Cursor=0 is also useful for troubleshooting — it dumps the entire signals.log, so you can re-read history.

If no live entry exists for the given `<sensor.id>`, a single Signal with `metadata.kind=not_running` and `verdict=error` is emitted instead.
```

- [ ] **Step 4: Bump plugin version**

Find the current version:

```bash
grep -E '"version"' .claude-plugin/plugin.json
```

Increment the minor digit (e.g., `0.5.0` → `0.6.0`). Edit `.claude-plugin/plugin.json`:

Replace the `"version"` line with the bumped version. Example: if the current is `"version": "0.5.0"`, change to `"version": "0.6.0"`.

- [ ] **Step 5: Verify everything still compiles and passes**

Run: `go test ./lib/...`
Run: `go test -tags=start_sensor ./skills/start-sensor/...`
Run: `go test -tags=start_watcher ./skills/start-sensor/...`
Run: `go test -tags=run_computational ./skills/run-sensor/...`
Run: `go test -tags=run_inferential ./skills/run-sensor/...`
Run: `go test -tags=stop_sensor ./skills/stop-sensor/...`
Run: `go test -tags=tail_sensor ./skills/tail-sensor/...`
Run: `go test -tags=list_sensors ./skills/list-sensors/...`

Expected: every package passes.

- [ ] **Step 6: Commit**

```bash
git add skills/start-sensor/SKILL.md skills/stop-sensor/SKILL.md skills/tail-sensor/SKILL.md .claude-plugin/plugin.json
git commit -m "$(cat <<'EOF'
docs: SKILL.md updates for orchestrated /start-sensor + kind rename

Updates SKILL.md for /start-sensor (new pre-flight + prepare semantics;
new metadata.kind table started/rejected/failed with cause), /stop-sensor
(renamed kinds, dead_holders array), and /tail-sensor (envelope, not_running).
Bumps plugin version for the metadata.kind contract change.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist

After all tasks complete, run a final pass:

- [ ] **Spec coverage:**
  - "Lifecycle of /start-sensor" → Task 6.
  - `RunDeps` + `RunDepsResult` → Task 4.
  - `RunPreparePhase` → Task 1.
  - `AttachLiveDep` signature change + reap-on-attach → Task 2.
  - `RebindDepHolderPID` → Task 3.
  - `RunWithDeps` refactor → Task 5.
  - `metadata.kind` rename across start/stop/tail → Tasks 6, 7, 8.
  - SKILL.md updates + plugin version bump → Task 9.
  - All 5 acceptance criteria from the issue have at least one `TestStart_*` test → Task 6.

- [ ] **Final integration test (manual smoke if possible):**

```bash
# Build all the binaries to confirm no link errors:
go build -tags=start_sensor   -o /tmp/start ./skills/start-sensor/scripts
go build -tags=start_watcher  -o /tmp/watch ./skills/start-sensor/scripts
go build -tags=stop_sensor    -o /tmp/stop  ./skills/stop-sensor/scripts
go build -tags=tail_sensor    -o /tmp/tail  ./skills/tail-sensor/scripts
go build -tags=list_sensors   -o /tmp/list  ./skills/list-sensors/scripts
go build -tags=run_computational -o /tmp/runc ./skills/run-sensor/scripts
go build -tags=run_inferential   -o /tmp/runi ./skills/run-sensor/scripts
```

Each command should succeed silently.

- [ ] **Final regression sweep:**

```bash
go test ./lib/... \
  && go test -tags=start_sensor   ./skills/start-sensor/... \
  && go test -tags=start_watcher  ./skills/start-sensor/... \
  && go test -tags=stop_sensor    ./skills/stop-sensor/... \
  && go test -tags=tail_sensor    ./skills/tail-sensor/... \
  && go test -tags=list_sensors   ./skills/list-sensors/... \
  && go test -tags=run_computational ./skills/run-sensor/... \
  && go test -tags=run_inferential   ./skills/run-sensor/...
```

Expected: every command exits 0.
