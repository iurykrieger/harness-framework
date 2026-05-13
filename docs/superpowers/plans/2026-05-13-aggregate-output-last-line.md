# Aggregate Output Last-Line Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore the public contract that the requested sensor's aggregate Signal is the LAST JSONL line on stdout of `/run-sensor`, even when blocking-dep teardown emits its own aggregate during detach (fixes issue #19).

**Architecture:** Introduce a non-emitting capture variant of `RunOneWithRoot` so the orchestrator can build the aggregate, run live-dep detach (which may emit teardown signals on stdout), then emit the captured aggregate last. The fix applies to both the computational path (`lib/orchestrator/run.go::runWithDepsImpl`) and the inferential path (`skills/run-sensor/scripts/run-inferential.go::run`). `defer detachAll()` is preserved as an idempotent safety net beside the explicit call.

**Tech Stack:** Go 1.25, standard library `testing`, table-driven tests where appropriate.

**Spec:** `docs/superpowers/specs/2026-05-13-aggregate-output-last-line-design.md`

**Branch:** Already on `fix/aggregate-output-last-line` (worktree at `.claude/worktrees/aggregate-output`).

---

## File Structure

**Modified (production):**
- `lib/orchestrator/lifecycle.go` — extract `emitAggregate bool` parameter; add `RunOneWithRootCapture` public function
- `lib/orchestrator/run.go` — rewrite `runWithDepsImpl` capture-then-detach-then-emit pattern
- `skills/run-sensor/scripts/run-inferential.go` — same pattern applied to the inferential runner
- `skills/run-sensor/SKILL.md` — update stream-ordering example block

**Modified (tests):**
- `lib/orchestrator/live_deps_test.go` — new tests `TestRunWithDepsImpl_blockingDep_aggregateLast` and `TestRunWithDepsImpl_blockingDep_cascade_aggregateLast` (external package `orchestrator_test`, where `writeBlockingDep` / `writeConsumer` helpers already live)
- `lib/orchestrator/lifecycle_test.go` — new `TestRunOneWithRootCapture_DoesNotEmitAggregate` focused unit test (create the file if it does not exist; otherwise append)
- `skills/run-sensor/scripts/run-inferential_test.go` — new test `TestRunInferential_BlockingDep_AggregateLast`

**Not touched:** `schemas/`, any signal-shape code. The fix is purely about ordering on stdout.

---

## Task 1: Internal refactor — thread `emitAggregate` through `RunOne` and `runOneWithPersistence`

This task is a pure structural refactor with no observable behavior change. It is the precondition for Task 2.

**Files:**
- Modify: `lib/orchestrator/lifecycle.go` (`RunOne` at lines 49-203, `RunOneWithRoot` at 217-225, `runOneWithPersistence` at 232 onward, `emitPreflightSignal` at 688-701)

- [ ] **Step 1.1: Read the current shape of the three emission points**

Read the three current `_ = json.NewEncoder(stdout).Encode(sig)` lines:
- `lifecycle.go:201` inside `RunOne` (after validation, before return).
- `lifecycle.go:465` inside `runOneWithPersistence` (after validation, before signals.log append).
- `lifecycle.go:699` inside `emitPreflightSignal`.

Each one is the single side-effect that this refactor needs to make optional. Everything else (validation, signals.log append, return values) is preserved.

- [ ] **Step 1.2: Rename `RunOne` and `runOneWithPersistence` bodies to private `*Impl` helpers that accept `emitAggregate bool`**

Change in `lib/orchestrator/lifecycle.go`:

Replace the existing `RunOne` declaration (current signature at line 49):

```go
func RunOne(ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator, stdout, stderr io.Writer) (map[string]interface{}, int) {
```

With a wrapper that delegates to a renamed body:

```go
func RunOne(ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator, stdout, stderr io.Writer) (map[string]interface{}, int) {
	return runOneImpl(ctx, s, projectRoot, schemasDir, v, stdout, stderr, true)
}

func runOneImpl(ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator, stdout, stderr io.Writer, emitAggregate bool) (map[string]interface{}, int) {
```

Inside `runOneImpl`, change two call sites to pass `emitAggregate` through:

- `return emitPreflightSignal(sig, v, stdout, stderr)` → `return emitPreflightSignal(sig, v, stdout, stderr, emitAggregate)`
- The final aggregate emit (current line 201):
  ```go
  _ = json.NewEncoder(stdout).Encode(sig)
  return sig, 0
  ```
  becomes:
  ```go
  if emitAggregate {
      _ = json.NewEncoder(stdout).Encode(sig)
  }
  return sig, 0
  ```

Apply the equivalent change to `runOneWithPersistence`. The `RunOneWithRoot` wrapper at line 217-225 stays — but update the fallback to thread `true`:

```go
func RunOneWithRoot(
	ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator,
	root *registry.Root, stdout, stderr io.Writer,
) (map[string]interface{}, int) {
	if root == nil {
		return runOneImpl(ctx, s, projectRoot, schemasDir, v, stdout, stderr, true)
	}
	return runOneWithPersistenceImpl(ctx, s, projectRoot, schemasDir, v, *root, stdout, stderr, true)
}
```

Rename `runOneWithPersistence` to `runOneWithPersistenceImpl` and add `emitAggregate bool` as the last parameter. Inside, change:
- `return emitPreflightSignal(sig, v, stdout, stderr)` → `return emitPreflightSignal(sig, v, stdout, stderr, emitAggregate)`
- Current line 465 `_ = json.NewEncoder(stdout).Encode(sig)` → guard with `if emitAggregate { ... }`

The signals.log append at lines 469-477 stays unguarded — that file is part of the run persistence, not stdout emission, and it should always happen when `runDir != ""`.

- [ ] **Step 1.3: Update `emitPreflightSignal` to honor `emitAggregate`**

Replace the function body at `lifecycle.go:692-700`:

```go
func emitPreflightSignal(sig map[string]interface{}, v *schema.Validator, stdout, stderr io.Writer, emitAggregate bool) (map[string]interface{}, int) {
	if v != nil {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return nil, 1
		}
	}
	if emitAggregate {
		_ = json.NewEncoder(stdout).Encode(sig)
	}
	return sig, 0
}
```

- [ ] **Step 1.4: Run existing tests — all must still pass**

Run:
```bash
go test ./lib/orchestrator/...
go test -tags=run_computational ./skills/run-sensor/...
go test -tags=run_inferential   ./skills/run-sensor/...
```

Expected: all pass. The refactor is a pure rename + threaded parameter; no test should break. If any test fails, the toggle was wired wrong — re-check Steps 1.2 and 1.3.

- [ ] **Step 1.5: Commit**

```bash
git add lib/orchestrator/lifecycle.go
git commit -m "$(cat <<'EOF'
refactor: thread emitAggregate through RunOne and runOneWithPersistence — #19

Precondition for the capture-then-detach-then-emit fix. No observable
behavior change: public RunOne and RunOneWithRoot delegate to *Impl
variants that take an emitAggregate bool, defaulting to true. Internal
helper emitPreflightSignal takes the same flag.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add public `RunOneWithRootCapture`

**Files:**
- Modify: `lib/orchestrator/lifecycle.go`
- Test: `lib/orchestrator/lifecycle_test.go` (create if it does not exist; otherwise append)

- [ ] **Step 2.1: Write the failing focused unit test**

Append to `lib/orchestrator/lifecycle_test.go` (create the file with the package declaration if missing):

```go
package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/schema/schematest"
)

// TestRunOneWithRootCapture_DoesNotEmitAggregate verifies the new
// capture variant returns the aggregate Signal without writing it to
// stdout. Individual Signals (none in single-mode) are unaffected.
func TestRunOneWithRootCapture_DoesNotEmitAggregate(t *testing.T) {
	proj := t.TempDir()
	sensorsDir := filepath.Join(proj, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sensorsDir, "echo.json"), []byte(`{
		"id": "echo", "version": "0.0.0", "kind": "observation",
		"type": "computational", "output": "single",
		"cost": {"compute": "low", "latency": {"timeout_ms": 5000}},
		"execution": {"command": "echo hi", "exit_code_map": [{"exit_code": 0, "verdict": "pass", "severity": "info"}]}
	}`), 0o644)

	schemasDir := schematest.RepoSchemasDir(t)
	v, code := schema.LoadValidator(schemasDir, os.Stderr)
	if code != 0 {
		t.Fatalf("validator init: code=%d", code)
	}
	rt := registry.NewRoot(proj)

	// Build orchestrator.Sensor directly from the file the test wrote.
	sensorJSON := mustLoadJSON(t, filepath.Join(sensorsDir, "echo.json"))
	s := orchestrator.Sensor{ID: "echo", JSON: sensorJSON}

	var stdout, stderr bytes.Buffer
	sig, exit := orchestrator.RunOneWithRootCapture(context.Background(), s, proj, schemasDir, v, &rt, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if sig == nil {
		t.Fatal("expected non-nil aggregate Signal")
	}
	if sig["verdict"] != "pass" {
		t.Errorf("verdict = %v, want pass", sig["verdict"])
	}

	// stdout MUST NOT contain the aggregate. Single-mode emits nothing
	// else, so stdout should be empty (or whitespace).
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("RunOneWithRootCapture emitted to stdout (must not):\n%s", stdout.String())
	}
}

func mustLoadJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
```

- [ ] **Step 2.2: Run test — expect compile failure (RunOneWithRootCapture undefined)**

Run:
```bash
go test ./lib/orchestrator/ -run TestRunOneWithRootCapture_DoesNotEmitAggregate
```

Expected: build error `undefined: orchestrator.RunOneWithRootCapture`.

- [ ] **Step 2.3: Add `RunOneWithRootCapture` to `lifecycle.go`**

Add a new exported function just below `RunOneWithRoot` (around line 226):

```go
// RunOneWithRootCapture is RunOneWithRoot with the final aggregate
// emission to stdout suppressed. The aggregate Signal is built,
// validated, and persisted (to signals.log when root != nil) exactly
// as in RunOneWithRoot, but it is NOT written to stdout — the caller
// is responsible for emitting it at the moment that satisfies its
// ordering constraints. Individual Signals during stream-mode command
// execution still flow to stdout in real time via the embedded
// StreamConfig.Stdout, unchanged.
//
// Intended for callers that orchestrate blocking-dep teardown after
// the command finishes and need the requested sensor's aggregate to
// remain the LAST JSONL line on stdout (see runWithDepsImpl).
func RunOneWithRootCapture(
	ctx context.Context, s Sensor, projectRoot, schemasDir string, v *schema.Validator,
	root *registry.Root, stdout, stderr io.Writer,
) (map[string]interface{}, int) {
	if root == nil {
		return runOneImpl(ctx, s, projectRoot, schemasDir, v, stdout, stderr, false)
	}
	return runOneWithPersistenceImpl(ctx, s, projectRoot, schemasDir, v, *root, stdout, stderr, false)
}
```

- [ ] **Step 2.4: Run the new test — expect pass**

Run:
```bash
go test ./lib/orchestrator/ -run TestRunOneWithRootCapture_DoesNotEmitAggregate -v
```

Expected: PASS. The stdout buffer should be empty.

- [ ] **Step 2.5: Run the full orchestrator suite — no regressions**

Run:
```bash
go test ./lib/orchestrator/...
```

Expected: all pass.

- [ ] **Step 2.6: Commit**

```bash
git add lib/orchestrator/lifecycle.go lib/orchestrator/lifecycle_test.go
git commit -m "$(cat <<'EOF'
feat: add RunOneWithRootCapture for deferred aggregate emission — #19

The new public function mirrors RunOneWithRoot but does not write the
aggregate Signal to stdout, returning it for the caller to emit at the
moment that preserves the "aggregate is LAST line" contract.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Fix `runWithDepsImpl` normal-target path

**Files:**
- Modify: `lib/orchestrator/run.go` (`runWithDepsImpl`, lines 50-126)
- Test: `lib/orchestrator/live_deps_test.go` (append, external package `orchestrator_test`)

- [ ] **Step 3.1: Write the failing test asserting "aggregate of requested sensor is the LAST JSONL line"**

Append to `lib/orchestrator/live_deps_test.go`:

```go
// TestRunWithDepsImpl_BlockingDep_AggregateLast verifies the documented
// contract that the requested sensor's aggregate Signal is the LAST
// JSONL line on stdout, even when a blocking dep's teardown emits its
// own aggregate during detach (issue #19).
func TestRunWithDepsImpl_BlockingDep_AggregateLast(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumer(t, root, "uses-tick")

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "uses-tick", root, schemasDir, &out, &errBuf)
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, errBuf.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 JSONL Signals, got %d:\n%s", len(lines), out.String())
	}

	var last map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last line: %v\nline=%q", err, lines[len(lines)-1])
	}
	if last["sensor_id"] != "uses-tick" {
		t.Errorf("last line sensor_id = %v, want uses-tick (entire stream:\n%s)", last["sensor_id"], out.String())
	}
	md, _ := last["metadata"].(map[string]interface{})
	if md == nil || md["kind"] != "aggregate" {
		t.Errorf("last line metadata.kind = %v, want aggregate", md)
	}
}
```

- [ ] **Step 3.2: Run the test — expect FAIL**

Run:
```bash
go test ./lib/orchestrator/ -run TestRunWithDepsImpl_BlockingDep_AggregateLast -v
```

Expected: FAIL. The current code emits the consumer's aggregate first, then the blocking dep's stop-aggregate (from `stopBlockingDep`) — `lines[len(lines)-1]` will be `sensor_id="blocking-tick"` instead of `"uses-tick"`.

If the test passes by accident, the fixture may be running too fast for the bug to surface. Verify by adding `t.Logf("stream:\n%s", out.String())` and observing the order.

- [ ] **Step 3.3: Rewrite `runWithDepsImpl` to use capture-then-detach-then-emit**

In `lib/orchestrator/run.go`, replace lines 105-126 (the current defer + dispatch + emit-cascade-or-target block) with:

```go
	// detachAll consumes the live-stack: iterate in reverse (LIFO)
	// calling DetachLiveDep, then clear the slice so the deferred
	// safety-net invocation below becomes a no-op. The deferred call
	// remains as a backstop for panic / mid-function early-return
	// paths where the explicit call did not happen.
	detachAll := func() {
		for i := len(pre.LiveStack) - 1; i >= 0; i-- {
			DetachLiveDep(pre.LiveStack[i], projectRoot, rootID, v, stdout, stderr)
		}
		pre.LiveStack = nil
	}
	defer detachAll()

	if pre.ExitCode != 0 {
		detachAll()
		return pre.ExitCode
	}
	if pre.CascadeSig != nil {
		if err := v.Validate(schema.TargetSignal, pre.CascadeSig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		detachAll()
		_ = json.NewEncoder(stdout).Encode(pre.CascadeSig)
		return 1
	}

	target := pre.Order[len(pre.Order)-1]
	sig, code := RunOneWithRootCapture(ctx, target, projectRoot, schemasDir, v, root, stdout, stderr)
	detachAll()
	if sig != nil {
		_ = json.NewEncoder(stdout).Encode(sig)
	}
	return code
}
```

Note the four changes:
1. The deferred closure is renamed `detachAll` so the explicit call can reuse it.
2. `pre.LiveStack = nil` at the end of the closure makes the deferred second call idempotent.
3. The `pre.ExitCode != 0` branch now calls `detachAll()` explicitly before returning (otherwise dep teardown still happens via defer, which is fine — the explicit call just makes ordering predictable).
4. The target call switches from `RunOneWithRoot` (which emits) to `RunOneWithRootCapture` (which returns the aggregate), then `detachAll()`, then explicit emit.

- [ ] **Step 3.4: Run the test from Step 3.1 — expect PASS**

Run:
```bash
go test ./lib/orchestrator/ -run TestRunWithDepsImpl_BlockingDep_AggregateLast -v
```

Expected: PASS. The last JSONL line on stdout is now the `uses-tick` aggregate.

- [ ] **Step 3.5: Run the full orchestrator suite + the computational runner suite**

Run:
```bash
go test ./lib/orchestrator/...
go test -tags=run_computational ./skills/run-sensor/...
```

Expected: all pass. In particular, `TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep` (live_deps_test.go:21) must still pass — its assertion is only "the blocking dep is gone from the registry afterwards", which is independent of stdout ordering.

- [ ] **Step 3.6: Commit**

```bash
git add lib/orchestrator/run.go lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
fix: aggregate of requested sensor is LAST line under blocking deps — #19

runWithDepsImpl now captures the target aggregate via the new
RunOneWithRootCapture, runs explicit detach of blocking deps (which
may emit their own stop-aggregate Signals during detach), then emits
the captured aggregate as the final stdout write. The defer remains
as an idempotent safety net.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Fix `runWithDepsImpl` cascade path with blocking deps

The cascade branch was already updated in Task 3 (it calls `detachAll()` before emitting the cascade Signal). This task adds a regression test that covers the cascade-with-blocking-deps-already-attached topology — the failure mode the spec describes in §"Cascade with blocking deps already attached".

**Files:**
- Test: `lib/orchestrator/live_deps_test.go` (append)

- [ ] **Step 4.1: Write a fixture helper for a non-blocking dep that fails**

Append to `lib/orchestrator/live_deps_test.go` (only if not already present from earlier tests — check first):

```go
// writeNonBlockingFailingDep writes a non-blocking setup sensor whose
// command returns exit 1, producing verdict=fail. Used to seed a
// cascade scenario.
func writeNonBlockingFailingDep(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Failing dep",
"description": "exits non-zero",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "fail", "expected_severity": "high"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "exit 1",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeConsumerWithTwoDeps writes a consumer that depends on both
// `firstDep` and `secondDep` (in that order). The first listed dep
// is the failing-non-blocking one (so FirstFailedDep selects it).
// The second listed dep is the blocking one (which RunDeps will
// still attempt to attach because deps are processed in declaration
// order, and a failed non-blocking sibling does not stop processing
// of other deps unless they declare the failed one as their own dep).
//
// NOTE: This fixture deliberately makes the blocking dep NOT depend
// on the failing one, so the blocking dep attaches successfully and
// is on the LiveStack when the cascade signal for the consumer is
// built. That is the exact topology described in the spec's
// "Cascade with blocking deps already attached" section.
func writeConsumerWithTwoDeps(t *testing.T, root, id, firstDep, secondDep string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Consumer with two deps",
"description": "for cascade-with-blocking-dep test",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"` + firstDep + `"},{"kind":"sensor","id":"` + secondDep + `"}],
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "echo never-runs",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 4.2: Add the cascade test**

Append:

```go
// TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast verifies that
// when a target cascades because one of its non-blocking deps failed,
// AND a sibling blocking dep was already attached, the cascade Signal
// for the requested sensor is the LAST JSONL line on stdout (the
// blocking dep's teardown signals appear before it).
func TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeNonBlockingFailingDep(t, root, "fails")
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumerWithTwoDeps(t, root, "cascaded", "fails", "blocking-tick")

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "cascaded", root, schemasDir, &out, &errBuf)
	if exit != 1 {
		t.Fatalf("exit=%d stderr=%s; want 1 (cascade)", exit, errBuf.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d:\n%s", len(lines), out.String())
	}

	var last map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last line: %v\nline=%q", err, lines[len(lines)-1])
	}
	if last["sensor_id"] != "cascaded" {
		t.Errorf("last sensor_id = %v, want cascaded (full stream:\n%s)", last["sensor_id"], out.String())
	}
	md, _ := last["metadata"].(map[string]interface{})
	if md == nil || md["kind"] != "cascade" {
		t.Errorf("last metadata.kind = %v, want cascade", md)
	}
}
```

- [ ] **Step 4.3: Run the test — expect PASS (Task 3's fix already covers this branch)**

Run:
```bash
go test ./lib/orchestrator/ -run TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast -v
```

Expected: PASS. The cascade branch in `runWithDepsImpl` was already changed to call `detachAll()` before emitting the cascade Signal in Task 3 Step 3.3.

If it FAILS, re-read Task 3 Step 3.3 — the cascade branch must look like:
```go
if pre.CascadeSig != nil {
    if err := v.Validate(...); err != nil { return 1 }
    detachAll()                        // ← must be before the Encode below
    _ = json.NewEncoder(stdout).Encode(pre.CascadeSig)
    return 1
}
```

- [ ] **Step 4.4: Run the full orchestrator suite**

Run:
```bash
go test ./lib/orchestrator/...
```

Expected: all pass.

- [ ] **Step 4.5: Commit**

```bash
git add lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
test: cascade with blocking deps already attached emits cascade last — #19

Adds regression coverage for the spec's "Cascade with blocking deps
already attached" topology: a consumer with two deps where the
non-blocking one fails and the blocking one is mid-flight. The
cascade Signal for the consumer must remain the LAST line of stdout.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Fix `run-inferential.go::run()` — same pattern

**Files:**
- Modify: `skills/run-sensor/scripts/run-inferential.go` (function `run`, lines ~140-325)
- Test: `skills/run-sensor/scripts/run-inferential_test.go` (append)

- [ ] **Step 5.1: Write the failing test for the inferential runner**

Append to `skills/run-sensor/scripts/run-inferential_test.go`:

```go
// TestRunInferential_BlockingDep_AggregateLast verifies the same
// last-line invariant for the inferential runner's ad-hoc deps loop
// (issue #19). When the requested inferential sensor depends on a
// blocking computational dep, the blocking dep is started, the
// inferential command runs, and the blocking dep is torn down. The
// requested sensor's aggregate must remain the LAST JSONL line.
func TestRunInferential_BlockingDep_AggregateLast(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	sensorsDir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Blocking dep — same shape as lib/orchestrator/live_deps_test.go's
	// writeBlockingDep, inlined here to avoid cross-package coupling.
	_ = os.WriteFile(filepath.Join(sensorsDir, "blocking-tick.json"), []byte(`{
"id": "blocking-tick", "version": "1.0.0",
"name": "Blocking tick", "description": "blocking tick",
"determinism": "high", "kind": "setup", "type": "computational",
"output": "stream", "regulation": "behaviour", "phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {"class":"cheap","compute":{"cpu":"low","memory_mb":32},"latency":{"p50_ms":10,"p95_ms":50}},
"execution": {
  "command": "while true; do echo TICK; sleep 0.1; done",
  "blocking": true, "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^TICK$","verdict":"pass","severity":"info"}]}
}
}`), 0o644)

	// Inferential consumer that depends on the blocking dep. Use the
	// same JSONL-emitting stub command pattern as TestRunInferential_Pass:
	// printf a single line whose first token matches a `^PASS` pattern,
	// then exit 0.
	id := writeInferentialSensor(t, root, "infr-with-blocking", `printf 'PASS judgment-1\n'`)
	// Adjust the sensor JSON to add the requires entry. writeInferentialSensor
	// doesn't take deps, so re-read, mutate, re-write.
	path := filepath.Join(sensorsDir, id+".json")
	b, _ := os.ReadFile(path)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	m["requires"] = []interface{}{
		map[string]interface{}{"kind": "sensor", "id": "blocking-tick"},
	}
	updated, _ := json.Marshal(m)
	_ = os.WriteFile(path, updated, 0o644)

	var out, errBuf bytes.Buffer
	if code := run([]string{"--schemas-dir", schemasDir, id}, root, &out, &errBuf); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errBuf.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d:\n%s", len(lines), out.String())
	}

	var last map[string]interface{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last line: %v\nline=%q", err, lines[len(lines)-1])
	}
	if last["sensor_id"] != id {
		t.Errorf("last sensor_id = %v, want %s (full stream:\n%s)", last["sensor_id"], id, out.String())
	}
	md, _ := last["metadata"].(map[string]interface{})
	if md == nil || md["kind"] != "aggregate" {
		t.Errorf("last metadata.kind = %v, want aggregate", md)
	}
}
```

- [ ] **Step 5.2: Run the test — expect FAIL**

Run:
```bash
go test -tags=run_inferential ./skills/run-sensor/scripts/ -run TestRunInferential_BlockingDep_AggregateLast -v
```

Expected: FAIL. The current code has the same defer-then-emit bug — the last line will be `sensor_id="blocking-tick"` (the dep's stop-aggregate from `stopBlockingDep` running inside the deferred `DetachLiveDep`).

- [ ] **Step 5.3: Rewrite `run` to use the capture-then-detach-then-emit pattern**

In `skills/run-sensor/scripts/run-inferential.go`, change the deps section starting at line 169:

Replace:

```go
	depSignals := map[string]map[string]interface{}{}
	var liveStack []orchestrator.LiveDep

	defer func() {
		for i := len(liveStack) - 1; i >= 0; i-- {
			orchestrator.DetachLiveDep(liveStack[i], projectRoot, rootID, v, stdout, stderr)
		}
	}()
```

With:

```go
	depSignals := map[string]map[string]interface{}{}
	var liveStack []orchestrator.LiveDep

	// detachAll is the consuming detach closure: iterate in reverse,
	// then clear the slice so the deferred safety-net call is a no-op
	// after an explicit invocation. The defer remains as a backstop
	// for panic / mid-function early-return paths.
	detachAll := func() {
		for i := len(liveStack) - 1; i >= 0; i-- {
			orchestrator.DetachLiveDep(liveStack[i], projectRoot, rootID, v, stdout, stderr)
		}
		liveStack = nil
	}
	defer detachAll()
```

Then locate each of the four exit points that currently call `_ = json.NewEncoder(stdout).Encode(...)` followed by `return` (or are followed by a `return` that ends the function while the deferred detach is still queued) and insert an explicit `detachAll()` immediately before the `Encode`. The four sites:

**Site A** — non-blocking-dep cascade inside the deps loop. Around line 198-207:

Before:
```go
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
```

After: this site does NOT exit the function — it `continue`s the loop. The deferred detach has not fired yet. The cascade Signal here is for a non-target dep (the loop is iterating `order[:len(order)-1]`), so this site is NOT the "last line" exit. Leave it unchanged.

**Site B** — requested-sensor cascade after the deps loop, around line 217-225:

Before:
```go
	if blocker := orchestrator.FirstFailedDep(requested, depSignals); blocker != nil {
		cascade := orchestrator.BuildCascadeSignal(requested, blocker)
		if err := v.Validate(schema.TargetSignal, cascade); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(cascade)
		return 1
	}
```

After:
```go
	if blocker := orchestrator.FirstFailedDep(requested, depSignals); blocker != nil {
		cascade := orchestrator.BuildCascadeSignal(requested, blocker)
		if err := v.Validate(schema.TargetSignal, cascade); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		detachAll()
		_ = json.NewEncoder(stdout).Encode(cascade)
		return 1
	}
```

**Site C** — gate failure for the requested sensor, around line 235-241:

Before:
```go
	if sig, failed := orchestrator.PreflightGate(requested, envelope, output); failed {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(sig)
		return 0
	}
```

After:
```go
	if sig, failed := orchestrator.PreflightGate(requested, envelope, output); failed {
		if err := v.Validate(schema.TargetSignal, sig); err != nil {
			schema.PrintValidationOrPlain(err, stderr)
			return 1
		}
		detachAll()
		_ = json.NewEncoder(stdout).Encode(sig)
		return 0
	}
```

**Site D** — the normal-target final emit, around line 321-324:

Before:
```go
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(sig)
	return 0
}
```

After:
```go
	if err := v.Validate(schema.TargetSignal, sig); err != nil {
		schema.PrintValidationOrPlain(err, stderr)
		return 1
	}
	detachAll()
	_ = json.NewEncoder(stdout).Encode(sig)
	return 0
}
```

Note that the validation-failure branches (the inner `return 1`s) do NOT call `detachAll()` explicitly — they emit nothing on stdout for the requested sensor, so the deferred safety-net detach fires after the return without breaking any contract.

- [ ] **Step 5.4: Run the test — expect PASS**

Run:
```bash
go test -tags=run_inferential ./skills/run-sensor/scripts/ -run TestRunInferential_BlockingDep_AggregateLast -v
```

Expected: PASS.

- [ ] **Step 5.5: Run the full inferential suite**

Run:
```bash
go test -tags=run_inferential ./skills/run-sensor/scripts/
```

Expected: all pass — including `TestRun_InferentialWithComputationalDep` (existing, line 287) which has a non-blocking dep and is unaffected by the change.

- [ ] **Step 5.6: Commit**

```bash
git add skills/run-sensor/scripts/run-inferential.go skills/run-sensor/scripts/run-inferential_test.go
git commit -m "$(cat <<'EOF'
fix: aggregate is LAST line in inferential runner with blocking deps — #19

The inferential runner's inline deps loop had the same defer-then-emit
bug as runWithDepsImpl. Switch to explicit detachAll() before every
final-line emit (normal target, requested-sensor cascade, gate fail).
The defer is preserved as an idempotent safety net.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Update `skills/run-sensor/SKILL.md` stream-ordering example

**Files:**
- Modify: `skills/run-sensor/SKILL.md`

- [ ] **Step 6.1: Read the current stream-ordering block**

Run:
```bash
grep -n "aggregate Signal of D" skills/run-sensor/SKILL.md
```

Locate the block that currently looks like:

```
{aggregate Signal of D1}
{aggregate Signal of D2}
{individual Signal 1 of X}    ← only when X has output: stream
{individual Signal 2 of X}
...
{aggregate Signal of X}        ← LAST line, contract preserved
```

- [ ] **Step 6.2: Replace the block with the new accurate ordering**

Use Edit to replace the block above with:

```
{aggregate Signal of D1}                      ← non-blocking dep aggregate
{dep_started Signal of D2}                    ← only when D2 has execution.blocking: true
{individual Signal 1 of X}                    ← only when X has output: stream
{individual Signal 2 of X}
...
{dep_detached or aggregate Signal of D2}      ← blocking dep teardown (LAST holder releases → stop-aggregate; otherwise dep_detached)
{aggregate Signal of X}                       ← LAST line, contract preserved
```

The "Callers using `tail -n 1 | jq` continue to see exactly the requested sensor's aggregate" sentence immediately after the block is preserved verbatim — the new diagram makes it clear *what else* can appear in the stream, but the invariant statement still holds.

- [ ] **Step 6.3: Validate the SKILL.md is still well-formed YAML+Markdown**

Run:
```bash
head -3 skills/run-sensor/SKILL.md
```

Expected: still starts with `---\nname: run-sensor\n` etc. (sanity-check that no frontmatter was disturbed.)

- [ ] **Step 6.4: Commit**

```bash
git add skills/run-sensor/SKILL.md
git commit -m "$(cat <<'EOF'
docs: clarify stream ordering with blocking deps in run-sensor SKILL — #19

The stream-ordering example now shows where dep_attached / dep_detached /
dep-stop-aggregate Signals appear relative to the requested sensor's
individuals and aggregate. The "aggregate is LAST line" invariant is
unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Sanity check — manual repro from the issue

This task is verification-only (no code), but it is the issue's acceptance criterion and the spec's DoD §8.

- [ ] **Step 7.1: Confirm the chain test path is covered by the unit tests**

The hand-reproduction in the issue used a three-sensor chain `dependencies-up-docker-compose` → `run-api-local` (blocking) → `health-check-ready`. The unit tests in Task 3 (`TestRunWithDepsImpl_BlockingDep_AggregateLast`) and Task 4 (`TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast`) cover the same topology and same invariant (last line = requested sensor) using a smaller two-sensor / three-sensor fixture (`blocking-tick`, `uses-tick`, optionally `fails`).

If you want belt-and-suspenders verification with the exact chain depth from the issue, write a one-off integration script (do NOT add to the repo). For the planned change, the unit tests are sufficient.

- [ ] **Step 7.2: Run all relevant test suites one last time**

Run:
```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential   ./skills/...
go vet -tags=run_computational  ./...
go vet -tags=run_inferential    ./...
```

Expected: all pass clean.

- [ ] **Step 7.3: Verify `git diff schemas/` is empty (DoD §7)**

Run:
```bash
git diff main -- schemas/
```

Expected: empty output. No schema changes were made.

- [ ] **Step 7.4: Push the branch and open a PR**

```bash
git push -u origin fix/aggregate-output-last-line
gh pr create --title "fix: aggregate of requested sensor is LAST line of stream — closes #19" --body "$(cat <<'EOF'
## Summary
- Adds `RunOneWithRootCapture` — variant of `RunOneWithRoot` that returns the aggregate Signal without writing it to stdout.
- Rewrites `runWithDepsImpl` and `run-inferential.go::run` to capture → explicit detach → emit, so blocking-dep teardown Signals appear before the requested sensor's aggregate instead of after.
- Restores the documented `tail -n 1 | jq` contract for `/run-sensor`.

## Test plan
- [ ] `go test ./lib/orchestrator/...` passes (existing + new `TestRunWithDepsImpl_BlockingDep_AggregateLast`, `TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast`, `TestRunOneWithRootCapture_DoesNotEmitAggregate`)
- [ ] `go test -tags=run_computational ./skills/...` passes
- [ ] `go test -tags=run_inferential ./skills/...` passes (existing + new `TestRunInferential_BlockingDep_AggregateLast`)
- [ ] `go vet -tags=run_computational ./...` clean
- [ ] `go vet -tags=run_inferential ./...` clean
- [ ] `git diff main -- schemas/` is empty

Closes #19.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Expected: PR URL is returned.

---

## Self-review notes (filled in during planning)

- **Spec coverage:** Every "What changes" item in the spec is covered:
  - §1 (RunOneWithRootCapture) → Tasks 1 & 2
  - §2 (runWithDepsImpl rewrite) → Task 3 (+ cascade branch validated in Task 4)
  - §3 (run-inferential.go rewrite) → Task 5
  - §4 (consuming detachAll) → Both Task 3 Step 3.3 and Task 5 Step 5.3 specify `liveStack = nil` / `pre.LiveStack = nil` at the end of the closure
  - §5 (SKILL.md update) → Task 6
  - §6 (new tests) → Tasks 2.1, 3.1, 4.2, 5.1
  - §7 (no schema changes) → verified in Task 7 Step 7.3

- **DoD coverage:** §1 (RunOneWithRootCapture signature) Task 2 Step 2.3; §2 (runWithDepsImpl capture path) Task 3; §3 (run-inferential.go pattern) Task 5; §4 (new tests exist and pass) Tasks 3-5; §5 (existing tests untouched in their assertions) Task 1 Step 1.4 + Task 3 Step 3.5; §6 (SKILL.md updated) Task 6; §7 (no schema diff) Task 7.3; §8 (manual repro) Task 7.1.

- **Placeholder scan:** No TBDs, no "implement later", no "similar to Task N" without code, no "add appropriate error handling". Every step that changes code shows the literal code.

- **Type consistency:** `RunOneWithRootCapture` signature is identical between Task 2 Step 2.3 (definition) and Task 3 Step 3.3 (call site). `detachAll` closure shape is identical between Task 3 and Task 5. Sensor JSON helpers (`writeBlockingDep`, `writeConsumer`, `writeNonBlockingFailingDep`, `writeConsumerWithTwoDeps`) all live in `lib/orchestrator/live_deps_test.go` (`package orchestrator_test`); test functions added there can call them directly.

- **Anti-scope adherence:** No schema changes, no refactor of non-blocking-dep emission, no changes to /start-sensor/-stop/-list/-tail, no CHANGELOG entry framed as breaking change.
