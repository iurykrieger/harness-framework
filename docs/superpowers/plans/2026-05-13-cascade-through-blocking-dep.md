# Cascade through blocking dep Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/run-sensor` honour its documented cascade contract when the failing dep sits upstream of a blocking dep (issue #20).

**Architecture:** Move the existing `FirstFailedDep` cascade gate one block earlier in `RunDeps` so it runs **before** the `if blocking` branch. Both blocking and non-blocking deps then share a single cascade decision point: any dep whose own (transitive) dep failed emits a cascade Signal, records it in `Signals[s.ID]`, and is skipped — no `AttachLiveDep`, no `RunOne`, no subprocess.

**Tech Stack:** Go 1.25, standard library `testing` (table-driven where natural), `encoding/json` for JSONL stream assertions, schema validator from `lib/schema`.

---

## File Structure

- **Modify** `lib/orchestrator/preflight.go` — hoist the cascade gate above the `if blocking` branch in `RunDeps`. Net: ~10 lines moved, no new helpers.
- **Modify** `lib/orchestrator/live_deps_test.go` — add one new helper (`writeBlockingDepWithDep`) and one new test (`TestRunWithDepsImpl_CascadeThroughBlockingDep`). Co-locating the test with `TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast` keeps all cascade-with-blocking coverage in one file.

No other files change. Schemas, runners, `BuildCascadeSignal`, and the detach machinery stay as-is.

---

### Task 1: Add the failing test (chain topology, before the fix)

**Files:**
- Modify: `lib/orchestrator/live_deps_test.go` (append helper + test at end of file)

This task is pure TDD: write the test that captures the bug, run it, watch it fail with the current production code. It both documents the contract and acts as the regression guard once Task 2 lands.

- [ ] **Step 1: Append the new helper to `live_deps_test.go`**

The existing `writeBlockingDep` produces a blocking sensor with no `requires[]`. We need a variant that declares a dependency on another sensor so we can wire the chain `failing-setup → blocking-intermediate → consumer`.

Append at the end of `lib/orchestrator/live_deps_test.go`:

```go
// writeBlockingDepWithDep writes a blocking sensor (output=stream, blocking=true)
// that declares requires[{kind:"sensor", id:depID}]. Used to express chain
// topologies like failing-setup → blocking-intermediate → consumer, where the
// blocking intermediate has its own failing dep upstream.
func writeBlockingDepWithDep(t *testing.T, root, id, depID string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Blocking tick with dep",
"description": "blocking tick depending on ` + depID + `",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
"regulation": "behaviour",
"phase": "continuous",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"` + depID + `"}],
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
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Append the new test to `live_deps_test.go`**

The test mirrors `TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast` but with the **chain** topology (issue #20): the failing setup is the blocking intermediate's own dep, not a sibling of the root.

Existing fixtures we reuse:
- `writeNonBlockingFailingDep(t, root, "fails")` — non-blocking setup, command `exit 1`, verdict=fail. Lives in `live_deps_test.go`.
- `writeConsumer(t, root, "consumer")` — assertion that `requires` exactly one dep. We need a `writeConsumerWithDep` variant **only if** `writeConsumer` cannot point at our blocking intermediate. Re-reading the file: `writeConsumer` hard-codes `requires: [{"kind":"sensor","id":"blocking-tick"}]`. We need it to depend on `blocking-intermediate` instead.

The simplest path is to extend the existing test to use a parametrised helper. But the spec mandates **co-located, focused additions** — no refactor of existing helpers. So we declare a dedicated consumer inline inside the test, mirroring how `writeConsumerWithTwoDeps` already exists for the sibling topology.

Append at the end of `lib/orchestrator/live_deps_test.go`:

```go
// writeConsumerWithDep writes an assertion sensor that declares exactly one
// requires[kind=sensor] entry pointing at depID. Differs from writeConsumer
// (which hard-codes "blocking-tick" as the dep) by parameterising the dep id.
func writeConsumerWithDep(t *testing.T, root, id, depID string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"name": "Consumer with one dep",
"description": "for cascade-through-blocking-dep test",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"` + depID + `"}],
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

// TestRunWithDepsImpl_CascadeThroughBlockingDep verifies that when a failing
// non-blocking dep sits UPSTREAM of a blocking dep in the requires chain
// (failing-setup → blocking-intermediate → consumer), the cascade Signal
// propagates through the blocking dep. The blocking intermediate must NOT
// be attached as a subprocess; the consumer must NOT run its command.
//
// This is the regression test for issue #20.
func TestRunWithDepsImpl_CascadeThroughBlockingDep(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeNonBlockingFailingDep(t, root, "fails")
	writeBlockingDepWithDep(t, root, "blocking-intermediate", "fails")
	writeConsumerWithDep(t, root, "consumer", "blocking-intermediate")

	var out, errBuf bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(context.Background(), "consumer", root, schemasDir, &out, &errBuf)
	if exit != 1 {
		t.Fatalf("exit=%d stderr=%s; want 1 (cascade)", exit, errBuf.String())
	}

	// The JSONL stream must contain exactly three Signals, in this order:
	//   1. failing-setup aggregate (verdict=fail)
	//   2. blocking-intermediate cascade (kind=cascade, failed_dep_id=fails)
	//   3. consumer cascade           (kind=cascade, failed_dep_id=blocking-intermediate)
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), out.String())
	}

	type want struct {
		sensorID    string
		verdict     string
		kind        string
		failedDepID string // empty when not applicable
	}
	wants := []want{
		{sensorID: "fails", verdict: "fail", kind: "aggregate"},
		{sensorID: "blocking-intermediate", verdict: "error", kind: "cascade", failedDepID: "fails"},
		{sensorID: "consumer", verdict: "error", kind: "cascade", failedDepID: "blocking-intermediate"},
	}

	for i, w := range wants {
		var sig map[string]interface{}
		if err := json.Unmarshal([]byte(lines[i]), &sig); err != nil {
			t.Fatalf("line %d unmarshal: %v\nline=%q", i, err, lines[i])
		}
		if got := sig["sensor_id"]; got != w.sensorID {
			t.Errorf("line %d sensor_id = %v, want %s", i, got, w.sensorID)
		}
		if got := sig["verdict"]; got != w.verdict {
			t.Errorf("line %d verdict = %v, want %s", i, got, w.verdict)
		}
		md, _ := sig["metadata"].(map[string]interface{})
		if got := md["kind"]; got != w.kind {
			t.Errorf("line %d metadata.kind = %v, want %s", i, got, w.kind)
		}
		if w.failedDepID != "" {
			if got := md["failed_dep_id"]; got != w.failedDepID {
				t.Errorf("line %d metadata.failed_dep_id = %v, want %s", i, got, w.failedDepID)
			}
			// Cascade Signals must have zero cost — the dep never ran.
			cost, _ := sig["cost_actual"].(map[string]interface{})
			if got := cost["latency_ms"]; got != float64(0) {
				t.Errorf("line %d cost_actual.latency_ms = %v, want 0", i, got)
			}
		}
	}

	// The blocking intermediate must NOT have spawned a subprocess: the
	// registry must contain no entry for it.
	r := registry.NewRoot(root)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.FindEntry("blocking-intermediate") != nil {
		t.Error("blocking-intermediate must not have been attached; found a registry entry")
	}
}
```

- [ ] **Step 3: Run the test, confirm it fails for the documented reason**

Run:

```bash
go test ./lib/orchestrator/ -run TestRunWithDepsImpl_CascadeThroughBlockingDep -v
```

Expected: **FAIL**. Concretely, you should see one of two failure shapes (depending on whether the blocking subprocess starts up fast enough to write a registry entry before the test reads it):

- `expected 3 lines, got N` (because the blocking-intermediate emits `dep_started` instead of cascade, and possibly `dep_detached`/aggregate on detach), and/or
- `blocking-intermediate must not have been attached; found a registry entry` (because `AttachLiveDep` wrote one).

Either failure confirms the bug.

- [ ] **Step 4: Commit the failing test**

```bash
git add lib/orchestrator/live_deps_test.go
git commit -m "$(cat <<'EOF'
test: failing test for cascade through blocking dep (#20)

Captures the regression: in the chain
  failing-setup (non-blocking, fail)
    → blocking-intermediate (blocking)
      → consumer (root)

the current RunDeps attaches blocking-intermediate as a subprocess
instead of cascading, then runs the consumer command. The test asserts
the documented contract: three cascade-style Signals, no subprocess for
the blocking intermediate.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Hoist the cascade gate in `RunDeps`

**Files:**
- Modify: `lib/orchestrator/preflight.go:82-106`

- [ ] **Step 1: Apply the production change**

Open `lib/orchestrator/preflight.go`. The current loop body (lines 82–124 inside `RunDeps`) looks like this:

```go
	for _, s := range order {
		if s.ID == targetID {
			continue
		}
		execMap, _ := s.JSON["execution"].(map[string]interface{})
		blocking, _ := execMap["blocking"].(bool)
		if blocking {
			result, attachErr := AttachLiveDep(ctx, s, projectRoot, holderID, holderPID, v, stdout, stderr)
			if attachErr != nil {
				cascade := buildSimpleSignal(targetID, "error", "high", "dep_start_failed", attachErr.Error())
				_ = json.NewEncoder(stdout).Encode(cascade)
				res.ExitCode = 1
				return res
			}
			if result.GateSignal != nil {
				// AttachLiveDep already emitted on stdout and validated.
				// Record so FirstFailedDep / BuildCascadeSignal propagate to
				// dependents (including the root) on later iterations.
				res.Signals[s.ID] = result.GateSignal
				continue
			}
			res.LiveStack = append(res.LiveStack, result.Live)
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
		sig, sigCode := RunOne(ctx, s, projectRoot, schemasDir, v, stdout, stderr)
		if sigCode != 0 {
			res.ExitCode = sigCode
			return res
		}
		res.Signals[s.ID] = sig
	}
```

Replace it with:

```go
	for _, s := range order {
		if s.ID == targetID {
			continue
		}
		// Cascade gate applies to both blocking and non-blocking deps. A
		// dep whose own (transitive) dep failed must not run its command
		// nor be attached as a blocking subprocess: emit a cascade Signal
		// and record it so downstream FirstFailedDep calls propagate.
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
		execMap, _ := s.JSON["execution"].(map[string]interface{})
		blocking, _ := execMap["blocking"].(bool)
		if blocking {
			result, attachErr := AttachLiveDep(ctx, s, projectRoot, holderID, holderPID, v, stdout, stderr)
			if attachErr != nil {
				cascade := buildSimpleSignal(targetID, "error", "high", "dep_start_failed", attachErr.Error())
				_ = json.NewEncoder(stdout).Encode(cascade)
				res.ExitCode = 1
				return res
			}
			if result.GateSignal != nil {
				// AttachLiveDep already emitted on stdout and validated.
				// Record so FirstFailedDep / BuildCascadeSignal propagate to
				// dependents (including the root) on later iterations.
				res.Signals[s.ID] = result.GateSignal
				continue
			}
			res.LiveStack = append(res.LiveStack, result.Live)
			res.Signals[s.ID] = map[string]interface{}{"verdict": "pass"}
			continue
		}
		sig, sigCode := RunOne(ctx, s, projectRoot, schemasDir, v, stdout, stderr)
		if sigCode != 0 {
			res.ExitCode = sigCode
			return res
		}
		res.Signals[s.ID] = sig
	}
```

The diff is mechanical: the `if blocker := FirstFailedDep(...)` block (10 lines) moves from its post-blocking position to immediately after the `if s.ID == targetID { continue }` guard, with a clarifying comment. The `execMap`/`blocking` extraction moves down with the blocking branch since the cascade gate doesn't need them.

Do not change any other code in this file.

- [ ] **Step 2: Run the regression test, confirm it passes**

```bash
go test ./lib/orchestrator/ -run TestRunWithDepsImpl_CascadeThroughBlockingDep -v
```

Expected: **PASS**.

- [ ] **Step 3: Run the full orchestrator suite, confirm no regression**

```bash
go test ./lib/orchestrator/
```

Expected: **PASS**, all tests. Pay particular attention to:
- `TestRunWithDepsImpl_BlockingDep_CascadeAggregateLast` (sibling topology) — must stay green; the blocking sibling does not depend on the failing dep, so its `FirstFailedDep` returns nil and it still attaches.
- `TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep` — happy path, no failing deps.
- Any cascade tests in `cascade_test.go` and `run_test.go`.

If any test fails, the fix went wrong — re-read Task 2 Step 1 carefully. Do NOT add compensating code; the change is mechanical and any failure points at a real semantic bug introduced.

- [ ] **Step 4: Run the broader library suite + runner integration**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
```

Expected: **PASS** across the board. These confirm that no caller of `RunDeps` (notably the run-sensor scripts and `/start-sensor`) regressed.

- [ ] **Step 5: Commit the fix**

```bash
git add lib/orchestrator/preflight.go
git commit -m "$(cat <<'EOF'
fix: cascade through blocking deps in RunDeps — closes #20

Move the FirstFailedDep cascade gate above the if blocking branch in
RunDeps so both blocking and non-blocking deps share a single cascade
decision point. Previously, a failing non-blocking setup sitting
upstream of a blocking dep did not propagate: the blocking dep was
attached as a live subprocess, its placeholder verdict=pass was stored
in Signals, and the root then saw no failed direct dep and ran its
command.

Cascade Signals already carry verdict=error, so downstream FirstFailedDep
calls pick them up naturally and the chain propagates without any other
changes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Final verification

**Files:** none (verification only).

- [ ] **Step 1: Run `go vet` with both runner tags**

```bash
go vet ./...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
```

Expected: clean exit on all three.

- [ ] **Step 2: Verify the full DoD against the spec**

Open `docs/superpowers/specs/2026-05-13-cascade-through-blocking-dep-design.md` and tick through items 1–6 in the "Definition of Done" section against actual test output:

1. Three-signal stream in chain topology — covered by `TestRunWithDepsImpl_CascadeThroughBlockingDep` Step 2 assertions.
2. No `running_sensors.json` entry for the cascaded blocking dep — covered by the same test's `rs.FindEntry("blocking-intermediate") != nil` check.
3. `cost_actual.latency_ms = 0` on the cascade Signal — covered by the cost assertion in the same test.
4. Exit code 1 — covered by `if exit != 1` in the same test.
5. All existing tests still pass — confirmed by Task 2 Step 3 and Step 4.
6. Cascade Signal validates against `schemas/signal.json` — the validation runs inline inside `RunDeps` (lines 109–112 of the post-fix file), so any schema mismatch would abort the test with exit code 1 plus a validation error on stderr.

If every item is observably satisfied, the work is done.

- [ ] **Step 3: Open the PR**

The branch already lives in the `cascade-error` worktree on the `worktree-cascade-error` branch. Push and open:

```bash
git push -u origin HEAD
gh pr create --title "fix: cascade through blocking deps in RunDeps — closes #20" --body "$(cat <<'EOF'
## Summary
- Hoist the `FirstFailedDep` cascade gate above the `if blocking` branch in `lib/orchestrator/preflight.go::RunDeps` so blocking and non-blocking deps share a single cascade decision point.
- Add `TestRunWithDepsImpl_CascadeThroughBlockingDep` covering the chain topology from issue #20: `failing-setup (non-blocking, fail) → blocking-intermediate (blocking) → consumer (root)`.

Closes #20.

## Test plan
- [ ] `go test ./lib/orchestrator/` — full package, all green.
- [ ] `go test ./lib/...` — full library suite, all green.
- [ ] `go test -tags=run_computational ./skills/...` — runner integration green.
- [ ] `go test -tags=run_inferential ./skills/...` — runner integration green.
- [ ] `go vet ./...` and the two runner-tag variants — clean.
- [ ] `TestRunWithDepsImpl_CascadeThroughBlockingDep` asserts the three-signal stream, the missing registry entry, the zero latency, and the exit-1 behaviour.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review (run before handing off)

**Spec coverage:** every DoD item maps to a concrete assertion in Task 1 Step 2 (test) or Task 3 Step 2 (verification checklist). Spec items "in scope" map to Task 2 (production fix) and Task 1 (test). Spec items "out of scope" are not touched.

**Placeholder scan:** no TODOs, no "TBD", no "implement later", no "similar to Task N". Every code block is concrete and self-contained.

**Type consistency:** the test uses `float64(0)` for the cost-zero assertion because `encoding/json` deserialises all JSON numbers into `float64` by default — this matches `BuildCascadeSignal`'s `"cost_actual": map[string]interface{}{"latency_ms": 0}` after round-trip through `json.Unmarshal`. Helper names (`writeBlockingDepWithDep`, `writeConsumerWithDep`) are referenced consistently. Test name matches the spec's `TestRunWithDepsImpl_CascadeThroughBlockingDep` recommendation and the existing `live_deps_test.go` naming convention.
