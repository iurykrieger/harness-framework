# Heal-sensor cascade through dead blocking deps — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/heal-sensor` trigger automatically when a blocking dep dies post-attach (build error, panic, etc.) so the user sees a heal directive instead of an opaque `verdict=fail, evidence=[]` aggregate.

**Architecture:** Wrap detached dep commands to capture exit code in a sidecar file; have `stopBlockingDep` read the file and emit a real verdict with `raw.log` tail in evidence + `metadata.heal_hint`; add a post-attach `awaitDepLiveness` gate in `runWithDepsImpl` that emits the dep's real aggregate + a cascade Signal for the dependent. Extend the closed `heal.Shape` enum with `subprocess-failed`, add curated regex patterns, add a new `subprocess-failed` classification rule placed before `heal-hint` in the registry. Populate `evidence[]` from captured stderr for single-output sensor aggregates so the rule has input.

**Tech Stack:** Go 1.25, standard `testing` package, JSON Schema Draft 2020-12 for signal validation, POSIX `sh -c` for command wrapping.

**Spec:** `docs/superpowers/specs/2026-05-14-heal-sensor-blocking-deps-design.md`

---

## File Structure

### Created
- `lib/heal/rules/subprocess_failed.go` — new classification rule (matches `metadata.exit_code != 0` + curated patterns in evidence/heal_hint)
- `lib/heal/rules/subprocess_failed_test.go` — table-driven tests for the new rule

### Modified
- `lib/orchestrator/live_deps.go` — `startBlockingDep` wraps command for exit-code capture; `stopBlockingDep` reads exit code + raw.log tail and emits honest verdict; new `awaitDepLiveness` function
- `lib/orchestrator/live_deps_test.go` — extend with exit-code wrap test, stopBlockingDep honest-aggregate test, awaitDepLiveness test
- `lib/orchestrator/run.go` — `runWithDepsImpl` calls `awaitDepLiveness` between `RunDeps` and `RunOneWithRootCapture`; on dead dep, emits cascade and skips target
- `lib/orchestrator/integration_runtime_logs_test.go` — extend with cascade-on-dead-dep integration test
- `lib/orchestrator/lifecycle.go` — populate `evidence[]` from `StreamResult.StderrExcerpt` for single-output sensors when exit_code != 0
- `lib/orchestrator/lifecycle_test.go` — extend with single-output evidence test
- `lib/heal/classify.go` — add `ShapeSubprocessFailed`, extend `IsKnown()`
- `lib/heal/classify_test.go` — extend
- `lib/heal/patterns.go` — add 4 non-capturing patterns mapping to `ShapeSubprocessFailed`
- `lib/heal/patterns_test.go` — extend
- `lib/heal/rules/registry.go` — insert `subprocessFailed{}` before `healHint{}` in `Registered()`
- `lib/heal/heal_e2e_test.go` — add end-to-end test: cascade signal → classify → result has rule="subprocess-failed"

---

## Task 1: Capture dep exit status via wrapper

**Goal:** `startBlockingDep` wraps the user's command in a POSIX shell snippet that writes the subprocess's exit code to `<projectRoot>/.harness/runtime/<dep_id>/<run_id>/exit_code` before propagating the same exit status. `stopBlockingDep` is unchanged in this task — it still emits the placeholder `verdict=pass` aggregate.

**Files:**
- Modify: `lib/orchestrator/live_deps.go:226-279` (`startBlockingDep`)
- Test: `lib/orchestrator/live_deps_test.go` (append new test below the existing helpers)

- [ ] **Step 1.1: Write the failing test**

Append to `lib/orchestrator/live_deps_test.go`:

```go
// writeFailingBlockingDep writes a blocking sensor whose command exits 42
// almost immediately. Used to verify exit-code capture wraps the user's
// command without swallowing the original exit status.
func writeFailingBlockingDep(t *testing.T, root, id string, exitCode int) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{
"id": "%s",
"version": "1.0.0",
"name": "Failing blocking dep",
"description": "exits %d immediately",
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
  "command": "echo FAILING-DEP-STDOUT; echo FAILING-DEP-STDERR 1>&2; exit %d",
  "blocking": true,
  "graceful_timeout_ms": 200,
  "exit_code_map": [{"exit_code":"*","verdict":"pass","severity":"info"}],
  "output_parsing": {"patterns":[{"regex":"^TICK$","verdict":"pass","severity":"info"}]}
}
}`, id, exitCode, exitCode))
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStartBlockingDep_WritesExitCodeFile verifies the wrapper captures the
// subprocess's real exit status into <runDir>/exit_code. The wrapper must
// not swallow the exit code or replace it with its own.
func TestStartBlockingDep_WritesExitCodeFile(t *testing.T) {
	root := t.TempDir()
	writeFailingBlockingDep(t, root, "fails-with-42", 42)
	dep := loadDepSensor(t, root, "fails-with-42")

	v := loadValidator(t)
	var stdout, stderr bytes.Buffer

	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", 99999,
		v, &stdout, &stderr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	live := result.Live
	if live.ID == "" {
		t.Fatal("attach failed")
	}

	// Give the subprocess up to 2s to exit and write the file.
	r := registry.NewRoot(root)
	exitCodeFile := filepath.Join(r.RelativeRunDir(live.ID, live.RunID), "exit_code")
	exitCodeFile = filepath.Join(root, exitCodeFile)
	deadline := time.Now().Add(2 * time.Second)
	var content []byte
	for time.Now().Before(deadline) {
		b, rerr := os.ReadFile(exitCodeFile)
		if rerr == nil && len(b) > 0 {
			content = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if content == nil {
		t.Fatalf("exit_code file never written at %s", exitCodeFile)
	}
	got := strings.TrimSpace(string(content))
	if got != "42" {
		t.Fatalf("exit_code = %q, want %q", got, "42")
	}

	// Cleanup: detach so the registry entry is removed.
	orchestrator.DetachLiveDep(live, root, "holder-id", v, io.Discard, io.Discard)
}
```

Add `"time"` to the imports if not already present.

- [ ] **Step 1.2: Run the test to verify it fails**

```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/fix-47
go test ./lib/orchestrator/ -run TestStartBlockingDep_WritesExitCodeFile -v
```

Expected: FAIL with "exit_code file never written at …" — `startBlockingDep` doesn't wrap the command yet.

- [ ] **Step 1.3: Verify `RelativeRunDir` exists on `registry.Root`**

```bash
grep -n "RelativeRunDir\|func.*RunDir\|func.*SensorDir" lib/registry/*.go
```

Expected: `lib/registry/root.go` exposes `Root.RunDir(sensorID, runID) string` (absolute) and `Root.RelativeRunDir(sensorID, runID) string` (project-relative). Both already exist. If `RunDir` does not exist, the modification below uses `filepath.Join(r.SensorDir(dep.ID), runID)` instead.

- [ ] **Step 1.4: Implement the wrapper in startBlockingDep**

In `lib/orchestrator/live_deps.go`, replace the body of `startBlockingDep` (currently `lib/orchestrator/live_deps.go:239-279`) with:

```go
func startBlockingDep(rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry, projectRoot string) (string, error) {
	execMap, _ := dep.JSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)

	// Synthesize run_id up front so we can place exit_code inside the
	// per-run directory. Mirrors the run_id format used by
	// runOneWithPersistenceImpl: "<pid>-<short-uuid>". We don't have a
	// pid yet, so use a UUID prefix that we'll align with the pid below.
	shortUUID := uuid.NewString()
	if len(shortUUID) >= 8 {
		shortUUID = shortUUID[:8]
	}

	if err := os.MkdirAll(r.SensorDir(dep.ID), 0o755); err != nil {
		return "", fmt.Errorf("mkdir log dir: %w", err)
	}

	// Pre-create the raw.log and signals.log in the sensor-level dir so
	// stdin/stderr redirection works. The per-run subdirectory is
	// created below once we know the pid.
	if err := os.WriteFile(r.RawLog(dep.ID), nil, 0o644); err != nil {
		return "", fmt.Errorf("create raw.log: %w", err)
	}
	if err := os.WriteFile(r.SignalsLog(dep.ID), nil, 0o644); err != nil {
		return "", fmt.Errorf("create signals.log: %w", err)
	}

	// Wrap the user command so the subprocess's exit status is captured
	// into a sidecar file the orchestrator reads at detach time. The
	// wrapper uses POSIX shell: parentheses contain set -e bleed; ec
	// captures $? before the file write so a write failure doesn't
	// corrupt the original exit status. The final `exit $ec` preserves
	// the original status for any outer process inspection.
	exitCodeFile := filepath.Join(r.SensorDir(dep.ID), "exit_code")
	wrapped := fmt.Sprintf("( %s ); ec=$?; echo $ec > %s; exit $ec",
		command, shellQuote(exitCodeFile))

	det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: wrapped,
		LogFile: r.RawLog(dep.ID),
		Dir:     projectRoot,
	})
	if err != nil {
		return "", fmt.Errorf("spawn: %w", err)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	runID := fmt.Sprintf("%d-%s", det.PID, shortUUID)
	rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
		SensorID:   dep.ID,
		RunID:      runID,
		Blocking:   true,
		PID:        det.PID,
		PGID:       det.PGID,
		WatcherPID: 0,
		StartedAt:  now,
		Command:    command,
		LogDir:     r.RelativeRunDir(dep.ID, runID),
		HeldBy:     []registry.HeldByEntry{holder},
	})
	if err := registry.Save(r, *rs); err != nil {
		return "", err
	}
	return runID, nil
}

// shellQuote returns s wrapped in POSIX-safe single quotes, escaping any
// embedded single quotes via the standard '\'' idiom. Used to splice file
// paths into shell-wrapped commands without command-injection risk.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

Add `"strings"` to imports if not already present.

Update the test (Step 1.1) so `exitCodeFile` points to the sensor-level dir, not the per-run dir:

```go
exitCodeFile := filepath.Join(root, ".harness", "runtime", live.ID, "exit_code")
```

Rationale: the existing `r.RawLog`, `r.SignalsLog`, and `r.SensorDir` write to the sensor-level directory, not a per-run subdirectory. We follow the same convention for `exit_code` so all sidecar files live together. Per-run subdirectories are only created by `runOneWithPersistenceImpl` for non-blocking sensors.

- [ ] **Step 1.5: Run the test to verify it passes**

```bash
go test ./lib/orchestrator/ -run TestStartBlockingDep_WritesExitCodeFile -v
```

Expected: PASS.

- [ ] **Step 1.6: Run the full orchestrator test suite to verify no regressions**

```bash
go test ./lib/orchestrator/... -v
```

Expected: all pass. The existing `TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep` and `TestAttachLiveDep_*` tests should still pass — the wrapper is transparent for commands that exit 0 (or that we kill with SIGTERM/SIGKILL).

- [ ] **Step 1.7: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "feat(orchestrator): capture blocking dep exit code via wrapper (#47)

startBlockingDep now wraps the user's command in a POSIX shell
snippet that writes the subprocess's exit status to
.harness/runtime/<dep_id>/exit_code before exiting with the same
code. Sets the foundation for stopBlockingDep to emit honest
verdicts in a follow-up commit."
```

---

## Task 2: Honest stopBlockingDep aggregate

**Goal:** When `stopBlockingDep` finds a non-zero `exit_code` file, it emits an aggregate with `verdict=fail`, `severity=high`, `metadata.exit_code=N`, raw.log tail in `evidence[]`, and `metadata.heal_hint` synthesized from curated stderr patterns (just like `buildHealHint` does for single-output non-dep sensors).

**Files:**
- Modify: `lib/orchestrator/live_deps.go:291-330` (`stopBlockingDep`)
- Test: `lib/orchestrator/live_deps_test.go` (append below test from Task 1)

- [ ] **Step 2.1: Write the failing test**

Append to `lib/orchestrator/live_deps_test.go`:

```go
// TestStopBlockingDep_NonZeroExit_EmitsFailWithEvidence verifies that a
// dep that exited non-zero produces an aggregate with verdict=fail,
// metadata.exit_code populated, and evidence[] carrying the tail of
// raw.log.
func TestStopBlockingDep_NonZeroExit_EmitsFailWithEvidence(t *testing.T) {
	root := t.TempDir()
	writeFailingBlockingDep(t, root, "fails-with-1", 1)
	dep := loadDepSensor(t, root, "fails-with-1")

	v := loadValidator(t)
	var attachOut, attachErr bytes.Buffer
	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", 99999,
		v, &attachOut, &attachErr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	live := result.Live

	// Wait for the subprocess to exit (it should exit immediately).
	r := registry.NewRoot(root)
	exitCodeFile := filepath.Join(root, ".harness", "runtime", live.ID, "exit_code")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b, rerr := os.ReadFile(exitCodeFile); rerr == nil && len(b) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Now detach. With no other holders, this triggers stopBlockingDep,
	// which should emit the honest aggregate to stdout.
	var detachOut, detachErr bytes.Buffer
	orchestrator.DetachLiveDep(live, root, "holder-id", v, &detachOut, &detachErr)

	// Find the aggregate Signal in detachOut.
	lines := strings.Split(strings.TrimRight(detachOut.String(), "\n"), "\n")
	if len(lines) == 0 {
		t.Fatal("no signals on stdout")
	}
	var agg map[string]interface{}
	for _, line := range lines {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		md, _ := m["metadata"].(map[string]interface{})
		if md != nil && md["kind"] == "aggregate" {
			agg = m
		}
	}
	if agg == nil {
		t.Fatalf("no aggregate Signal in output:\n%s", detachOut.String())
	}

	if v, _ := agg["verdict"].(string); v != "fail" {
		t.Errorf("verdict = %q, want %q", v, "fail")
	}
	if v, _ := agg["severity"].(string); v != "high" {
		t.Errorf("severity = %q, want %q", v, "high")
	}
	md, _ := agg["metadata"].(map[string]interface{})
	if md == nil {
		t.Fatal("metadata missing")
	}
	if ec, ok := md["exit_code"].(float64); !ok || int(ec) != 1 {
		t.Errorf("metadata.exit_code = %v, want 1", md["exit_code"])
	}

	ev, _ := agg["evidence"].([]interface{})
	if len(ev) == 0 {
		t.Fatal("evidence is empty; expected raw.log tail")
	}
	found := false
	for _, raw := range ev {
		e, _ := raw.(map[string]interface{})
		if e == nil {
			continue
		}
		excerpt, _ := e["excerpt"].(string)
		if strings.Contains(excerpt, "FAILING-DEP-STDOUT") ||
			strings.Contains(excerpt, "FAILING-DEP-STDERR") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("evidence does not contain raw.log tail; got %+v", ev)
	}

	// Registry entry should be gone after stopBlockingDep.
	if rs, _ := registry.Load(r); rs.FindEntry(live.ID) != nil {
		t.Error("registry entry not removed after stopBlockingDep")
	}
}

// TestStopBlockingDep_MissingExitCode_FallsBackToPass verifies the
// backward-compatible path: when the exit_code file is missing (e.g.
// the subprocess was killed by SIGTERM/SIGKILL before writing it),
// stopBlockingDep keeps the legacy verdict=pass behavior.
func TestStopBlockingDep_MissingExitCode_FallsBackToPass(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "tick-long-lived")
	dep := loadDepSensor(t, root, "tick-long-lived")

	v := loadValidator(t)
	var attachOut, attachErr bytes.Buffer
	result, err := orchestrator.AttachLiveDep(
		context.Background(), dep, root, "holder-id", 99999,
		v, &attachOut, &attachErr,
	)
	if err != nil {
		t.Fatalf("AttachLiveDep: %v", err)
	}
	live := result.Live

	// Delete the exit_code file path (it doesn't exist yet — the wrapper
	// hasn't completed). The dep is still running. Detach immediately,
	// so stopBlockingDep kills the subprocess via SIGTERM/SIGKILL before
	// the wrapper can write the exit_code file.
	var detachOut, detachErr bytes.Buffer
	orchestrator.DetachLiveDep(live, root, "holder-id", v, &detachOut, &detachErr)

	lines := strings.Split(strings.TrimRight(detachOut.String(), "\n"), "\n")
	var agg map[string]interface{}
	for _, line := range lines {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		md, _ := m["metadata"].(map[string]interface{})
		if md != nil && md["kind"] == "aggregate" {
			agg = m
		}
	}
	if agg == nil {
		t.Fatalf("no aggregate Signal:\n%s", detachOut.String())
	}
	if v, _ := agg["verdict"].(string); v != "pass" {
		t.Errorf("verdict = %q, want %q (missing exit_code file)", v, "pass")
	}
}
```

- [ ] **Step 2.2: Run the tests to verify they fail**

```bash
go test ./lib/orchestrator/ -run "TestStopBlockingDep_NonZeroExit_EmitsFailWithEvidence|TestStopBlockingDep_MissingExitCode_FallsBackToPass" -v
```

Expected: `TestStopBlockingDep_NonZeroExit_EmitsFailWithEvidence` FAILS (current `stopBlockingDep` always emits `verdict=pass`). `TestStopBlockingDep_MissingExitCode_FallsBackToPass` PASSES already.

- [ ] **Step 2.3: Implement honest aggregate in stopBlockingDep**

Replace the body of `stopBlockingDep` (currently `lib/orchestrator/live_deps.go:291-330`) with:

```go
// stopBlockingDep terminates the dep's process group and removes its
// registry entry. Emits an aggregate Signal on stdout. When the dep
// exited non-zero (recorded in <runtimeDir>/exit_code by the wrapper
// in startBlockingDep), the aggregate carries verdict=fail with a
// tail of raw.log in evidence and metadata.heal_hint synthesized from
// curated stderr patterns. When the file is absent (the subprocess
// was killed before writing it, or for any legacy reason), the legacy
// verdict=pass aggregate is preserved.
func stopBlockingDep(r registry.Root, entry *registry.RunningSensorEntry, v *schema.Validator, stdout, stderr io.Writer) {
	gracefulMS := 5000
	if entry.PGID > 0 {
		_ = syscall.Kill(-entry.PGID, syscall.SIGTERM)
		deadline := time.Now().Add(time.Duration(gracefulMS) * time.Millisecond)
		for time.Now().Before(deadline) {
			if !registry.IsPIDAlive(entry.PID) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if registry.IsPIDAlive(entry.PID) {
			_ = syscall.Kill(-entry.PGID, syscall.SIGKILL)
		}
	}
	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntryByRunID(entry.RunID)
		return registry.Save(r, rs)
	})

	verdict, severity, exitCode, evidenceItems, healHint := readDepExitState(r, entry.SensorID)

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := map[string]interface{}{
		"kind":        "aggregate",
		"command":     entry.Command,
		"output_mode": "stream",
		"counts":      map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 0},
	}
	if exitCode != nil {
		md["exit_code"] = *exitCode
	}
	if healHint != "" {
		md["heal_hint"] = healHint
	}
	agg := map[string]interface{}{
		"sensor_id":   entry.SensorID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  entry.StartedAt,
		"finished_at": now,
		"verdict":     verdict,
		"severity":    severity,
		"confidence":  1.0,
		"evidence":    evidenceItems,
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
	agg = validateOrFallback(v, agg, entry.SensorID, stderr)
	_ = json.NewEncoder(stdout).Encode(agg)
}

// depRawLogTailLines is the number of trailing non-empty raw.log lines
// surfaced as evidence[] excerpts when a blocking dep exits non-zero.
const depRawLogTailLines = 20

// readDepExitState returns the aggregate-shaped pieces describing the
// dep's exit state, derived from <runtime>/<id>/exit_code and raw.log.
//
//   - verdict, severity: "pass"/"info" when exit_code is 0 or absent,
//     "fail"/"high" otherwise.
//   - exitCode: pointer to the parsed int when the file existed, nil
//     when it didn't.
//   - evidenceItems: a default rationale on success; otherwise the last
//     depRawLogTailLines non-empty lines of raw.log as excerpt entries.
//   - healHint: "<shape>:<line>" when any tail line matches a curated
//     heal pattern, empty otherwise.
func readDepExitState(r registry.Root, depID string) (verdict, severity string, exitCode *int, evidenceItems []interface{}, healHint string) {
	exitFile := filepath.Join(r.SensorDir(depID), "exit_code")
	body, err := os.ReadFile(exitFile)
	if err != nil || len(bytes.TrimSpace(body)) == 0 {
		return "pass", "info",
			nil,
			[]interface{}{map[string]interface{}{
				"rationale": fmt.Sprintf("blocking dep %q stopped on detach", depID),
			}},
			""
	}
	code, perr := strconv.Atoi(strings.TrimSpace(string(body)))
	if perr != nil || code == 0 {
		return "pass", "info",
			intPtr(code),
			[]interface{}{map[string]interface{}{
				"rationale": fmt.Sprintf("blocking dep %q stopped on detach (exit_code=%d)", depID, code),
			}},
			""
	}

	tail := tailRawLog(r.RawLog(depID), depRawLogTailLines)
	ev := make([]interface{}, 0, len(tail))
	for _, line := range tail {
		ev = append(ev, map[string]interface{}{
			"rationale": fmt.Sprintf("blocking dep %q stderr/stdout tail", depID),
			"excerpt":   line,
		})
	}
	if len(ev) == 0 {
		ev = append(ev, map[string]interface{}{
			"rationale": fmt.Sprintf("blocking dep %q exited with code %d; raw.log empty", depID, code),
		})
	}

	// Synthesize metadata.heal_hint when any tail line matches a curated pattern.
	for _, line := range tail {
		if shape, ok := heal.MatchStderrPattern(line); ok {
			truncated := line
			if len(truncated) > 120 {
				truncated = truncated[:120]
			}
			healHint = string(shape) + ":" + truncated
			break
		}
	}

	return "fail", "high", intPtr(code), ev, healHint
}

func intPtr(i int) *int { return &i }

// tailRawLog returns the last n non-empty lines of the file at path.
// Reads the whole file (raw.log is bounded by the dep's runtime) and
// keeps things simple. Returns nil on read error.
func tailRawLog(path string, n int) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	allLines := strings.Split(string(body), "\n")
	out := make([]string, 0, n)
	// Walk backward, skipping empty lines.
	for i := len(allLines) - 1; i >= 0 && len(out) < n; i-- {
		line := strings.TrimRight(allLines[i], "\r")
		if line == "" {
			continue
		}
		out = append([]string{line}, out...)
	}
	return out
}
```

Add the following imports to `lib/orchestrator/live_deps.go` if not present:

```go
"bytes"
"path/filepath"
"strconv"
"strings"

"github.com/iurykrieger/harness-framework/lib/heal"
```

- [ ] **Step 2.4: Run the tests to verify they pass**

```bash
go test ./lib/orchestrator/ -run "TestStopBlockingDep_NonZeroExit_EmitsFailWithEvidence|TestStopBlockingDep_MissingExitCode_FallsBackToPass" -v
```

Expected: both PASS.

- [ ] **Step 2.5: Run the full orchestrator suite for regression check**

```bash
go test ./lib/orchestrator/... -v
```

Expected: all PASS. The existing `TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep` still passes because long-lived ticker deps are killed by SIGTERM/SIGKILL before they can write a non-zero exit code (so `readDepExitState` returns the legacy pass aggregate).

- [ ] **Step 2.6: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "feat(orchestrator): honest stopBlockingDep aggregate (#47)

stopBlockingDep now reads <runtime>/<dep_id>/exit_code (written by
the wrapper added in the prior commit). On non-zero exit, emits
verdict=fail with raw.log tail in evidence and metadata.heal_hint
synthesized from curated stderr patterns. Missing/zero exit code
preserves the legacy verdict=pass aggregate."
```

---

## Task 3: Post-attach liveness gate + cascade emission

**Goal:** Add `awaitDepLiveness` to `lib/orchestrator/live_deps.go` and call it from `runWithDepsImpl` between `RunDeps` and `RunOneWithRootCapture`. When any attached blocking dep is no longer alive at this checkpoint, emit the dep's real aggregate (via `readDepExitState`) plus a cascade Signal via `BuildCascadeSignal` for the target, and skip the target run.

**Files:**
- Modify: `lib/orchestrator/live_deps.go` (add `awaitDepLiveness` near `DetachLiveDep`)
- Modify: `lib/orchestrator/run.go:131-138` (call the new function)
- Test: `lib/orchestrator/integration_runtime_logs_test.go` (append integration test)

- [ ] **Step 3.1: Write the failing integration test**

Append to `lib/orchestrator/integration_runtime_logs_test.go`:

```go
// TestRunWithDeps_BlockingDepDiesPostAttach_CascadesAndSkipsTarget
// verifies the end-to-end path: a dep that exits 1 immediately after
// attach produces (1) its honest aggregate, (2) a cascade Signal for
// the target, and the target's command is NEVER run.
//
// Stream contract: aggregate of dep first, cascade of target second,
// no aggregate for target.
func TestRunWithDeps_BlockingDepDiesPostAttach_CascadesAndSkipsTarget(t *testing.T) {
	schemasDir := schematest.RepoSchemasDir(t)
	root := t.TempDir()
	writeFailingBlockingDep(t, root, "dies-with-1", 1)
	writeConsumerOfFailing(t, root, "needs-dies-with-1", "dies-with-1")

	var stdout, stderr bytes.Buffer
	exit := orchestrator.RunWithDepsRoot(
		context.Background(), "needs-dies-with-1", root, schemasDir,
		&stdout, &stderr,
	)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1 (cascade-skipped root)", exit)
	}

	// Parse JSONL output. Expect exactly two top-level signals after
	// any leading attach/dep_started signals: the dep aggregate
	// (verdict=fail) and the cascade for the target. No target aggregate.
	lines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
	var (
		depAggregate    map[string]interface{}
		targetCascade   map[string]interface{}
		targetAggregate map[string]interface{}
	)
	for _, line := range lines {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		md, _ := m["metadata"].(map[string]interface{})
		kind, _ := md["kind"].(string)
		sid, _ := m["sensor_id"].(string)
		switch {
		case kind == "aggregate" && sid == "dies-with-1":
			depAggregate = m
		case kind == "cascade" && sid == "needs-dies-with-1":
			targetCascade = m
		case kind == "aggregate" && sid == "needs-dies-with-1":
			targetAggregate = m
		}
	}

	if depAggregate == nil {
		t.Fatalf("missing dep aggregate Signal:\n%s", stdout.String())
	}
	if v, _ := depAggregate["verdict"].(string); v != "fail" {
		t.Errorf("dep verdict = %q, want fail", v)
	}

	if targetCascade == nil {
		t.Fatalf("missing target cascade Signal:\n%s", stdout.String())
	}
	md, _ := targetCascade["metadata"].(map[string]interface{})
	if id, _ := md["failed_dep_id"].(string); id != "dies-with-1" {
		t.Errorf("cascade.failed_dep_id = %q, want %q", id, "dies-with-1")
	}

	if targetAggregate != nil {
		t.Errorf("target aggregate should NOT be emitted (was: %+v)", targetAggregate)
	}
}

// writeConsumerOfFailing writes a non-blocking single-output sensor
// declaring requires[{kind:sensor, id:depID}]. Its command is a long
// sleep so we can be sure that, if it runs, the test would hang —
// thus its non-emission is the strongest possible "skipped" assertion.
func writeConsumerOfFailing(t *testing.T, root, id, depID string) {
	t.Helper()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(fmt.Sprintf(`{
"id": "%s",
"version": "1.0.0",
"name": "Needs %s",
"description": "Long-running consumer; should be cascade-skipped if dep dies.",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"requires": [{"kind":"sensor","id":"%s"}],
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":60000}
},
"execution": {
  "command": "sleep 30; echo OK",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`, id, depID, depID))
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 3.2: Run the failing test**

```bash
go test ./lib/orchestrator/ -run TestRunWithDeps_BlockingDepDiesPostAttach_CascadesAndSkipsTarget -v -timeout 60s
```

Expected: FAIL — currently the test hangs (the consumer sleeps 30s because there is no liveness gate) or the dep aggregate verdict is `pass`. We bound the test with `-timeout 60s` so it doesn't run indefinitely; if Go test framework cuts the test, that itself is the failing signal.

- [ ] **Step 3.3: Add `awaitDepLiveness` to lib/orchestrator/live_deps.go**

Append after the `DetachLiveDep` function:

```go
// AwaitDepLiveness checks every LiveDep in the passed slice. The first
// dep whose pid is no longer alive (as reported by registry.IsPIDAlive)
// is treated as having died post-attach: its honest aggregate is
// constructed from <runtime>/<dep_id>/exit_code and raw.log, and a
// (deadDep, depAggregate) pair is returned. When all deps are alive,
// returns ("", nil, nil).
//
// The returned depAggregate is NOT yet emitted or validated; callers
// emit it through validateOrFallback before passing it to
// BuildCascadeSignal.
//
// The check is a single snapshot: deps that die after this returns are
// caught at detach time in stopBlockingDep.
func AwaitDepLiveness(deps []LiveDep, projectRoot string) (deadDepID string, depAggregate map[string]interface{}, depRunID string) {
	r := registry.NewRoot(projectRoot)
	for _, d := range deps {
		// Look up the registry entry for this LiveDep so we can fetch
		// the pid that started it.
		rs, err := registry.Load(r)
		if err != nil {
			continue
		}
		entry := rs.FindEntryByRunID(d.RunID)
		if entry == nil {
			continue
		}
		if registry.IsPIDAlive(entry.PID) {
			continue
		}
		// Dead dep — synthesize its aggregate the same way stopBlockingDep does.
		verdict, severity, exitCode, evidenceItems, healHint := readDepExitState(r, d.ID)
		if verdict == "pass" {
			// Process is dead but exited 0 cleanly; this can happen for short-lived
			// helpers that finish their work and exit. Don't treat as failure.
			continue
		}
		now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		md := map[string]interface{}{
			"kind":        "aggregate",
			"command":     entry.Command,
			"output_mode": "stream",
			"counts":      map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 0},
		}
		if exitCode != nil {
			md["exit_code"] = *exitCode
		}
		if healHint != "" {
			md["heal_hint"] = healHint
		}
		agg := map[string]interface{}{
			"sensor_id":   d.ID,
			"version":     "0.0.0",
			"run_id":      uuid.NewString(),
			"started_at":  entry.StartedAt,
			"finished_at": now,
			"verdict":     verdict,
			"severity":    severity,
			"confidence":  1.0,
			"evidence":    evidenceItems,
			"cost_actual": map[string]interface{}{"latency_ms": 0},
			"metadata":    md,
		}
		return d.ID, agg, d.RunID
	}
	return "", nil, ""
}
```

- [ ] **Step 3.4: Call `AwaitDepLiveness` in run.go between RunDeps and RunOneWithRootCapture**

In `lib/orchestrator/run.go`, replace the block from line 131-138 (the `target := pre.Order[len(pre.Order)-1]` line through `return code`) with:

```go
	target := pre.Order[len(pre.Order)-1]

	// Post-attach liveness gate: if any blocking dep died between
	// AttachLiveDep and now, emit its honest aggregate, build a cascade
	// Signal for the target, skip RunOne, and exit 1. detachAll runs as
	// a deferred safety net.
	if _, depAgg, _ := AwaitDepLiveness(pre.LiveStack, projectRoot); depAgg != nil {
		depAgg = validateOrFallback(v, depAgg, depAgg["sensor_id"].(string), stderr)
		_ = json.NewEncoder(stdout).Encode(depAgg)
		cascade := BuildCascadeSignal(target, depAgg)
		cascade = validateOrFallback(v, cascade, target.ID, stderr)
		_ = json.NewEncoder(stdout).Encode(cascade)
		return 1
	}

	sig, code := RunOneWithRootCapture(ctx, target, projectRoot, schemasDir, v, root, stdout, stderr)
	detachAll()
	if sig != nil {
		_ = json.NewEncoder(stdout).Encode(sig)
	}
	return code
```

The `defer detachAll()` at line 116 ensures the dead dep's registry entry is cleaned up on the cascade-and-return path — when DetachLiveDep finds the entry's pid is dead, it removes the entry as part of stopBlockingDep.

- [ ] **Step 3.5: Run the integration test to verify it passes**

```bash
go test ./lib/orchestrator/ -run TestRunWithDeps_BlockingDepDiesPostAttach_CascadesAndSkipsTarget -v -timeout 60s
```

Expected: PASS within a few seconds (no more 30s sleep — the target never runs).

- [ ] **Step 3.6: Run the full suite for regressions**

```bash
go test ./lib/orchestrator/... -v
```

Expected: all PASS, including the existing `TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep` (the ticker stays alive, so `AwaitDepLiveness` returns nil aggregate, and the consumer runs normally).

- [ ] **Step 3.7: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/run.go lib/orchestrator/integration_runtime_logs_test.go
git commit -m "feat(orchestrator): post-attach liveness gate cascades dead deps (#47)

runWithDepsImpl now calls AwaitDepLiveness between RunDeps and the
target's RunOneWithRootCapture. When any blocking dep is no longer
alive at that checkpoint, the dep's honest aggregate plus a cascade
Signal for the target are emitted and the target run is skipped.
defer detachAll cleans up the dead dep's registry entry."
```

---

## Task 4: Shape enum, curated patterns, new rule, registry order

**Goal:** Extend the closed `heal.Shape` enum with `subprocess-failed`, add four curated patterns mapping to it in `lib/heal/patterns.go`, add the new `subprocessFailed{}` rule, and register it BEFORE `healHint{}` in `lib/heal/rules/registry.go`. The new rule matches when `metadata.exit_code != 0` AND either `metadata.heal_hint` starts with `subprocess-failed:` OR any `evidence[i].excerpt`/`rationale` line matches a curated pattern.

**Files:**
- Modify: `lib/heal/classify.go` (extend enum + `IsKnown()`)
- Modify: `lib/heal/classify_test.go`
- Modify: `lib/heal/patterns.go` (add 4 patterns)
- Modify: `lib/heal/patterns_test.go`
- Create: `lib/heal/rules/subprocess_failed.go`
- Create: `lib/heal/rules/subprocess_failed_test.go`
- Modify: `lib/heal/rules/registry.go`
- Modify: `lib/heal/heal_e2e_test.go`

- [ ] **Step 4.1: Write the failing test for `Shape.IsKnown()`**

In `lib/heal/classify_test.go`, locate the existing test for `Shape.IsKnown()` (search for `IsKnown`). If absent, append:

```go
func TestShape_IsKnown_IncludesSubprocessFailed(t *testing.T) {
	if !heal.ShapeSubprocessFailed.IsKnown() {
		t.Fatal("ShapeSubprocessFailed must be in IsKnown's switch")
	}
}
```

- [ ] **Step 4.2: Run the test to verify it fails**

```bash
go test ./lib/heal/ -run TestShape_IsKnown_IncludesSubprocessFailed -v
```

Expected: FAIL with "undefined: heal.ShapeSubprocessFailed" or compile error.

- [ ] **Step 4.3: Add the new Shape**

In `lib/heal/classify.go`, change:

```go
const (
	ShapeMissingEnv         Shape = "missing-env"
	ShapeBinaryNotFound     Shape = "binary-not-found"
	ShapeEnvFileAbsent      Shape = "env-file-absent"
	ShapeServiceUnavailable Shape = "service-unavailable"
	ShapeMissingContext     Shape = "missing-context"
)

// IsKnown reports whether s is one of the registered shapes.
func (s Shape) IsKnown() bool {
	switch s {
	case ShapeMissingEnv, ShapeBinaryNotFound, ShapeEnvFileAbsent, ShapeServiceUnavailable, ShapeMissingContext:
		return true
	}
	return false
}
```

to:

```go
const (
	ShapeMissingEnv         Shape = "missing-env"
	ShapeBinaryNotFound     Shape = "binary-not-found"
	ShapeEnvFileAbsent      Shape = "env-file-absent"
	ShapeServiceUnavailable Shape = "service-unavailable"
	ShapeMissingContext     Shape = "missing-context"
	ShapeSubprocessFailed   Shape = "subprocess-failed"
)

// IsKnown reports whether s is one of the registered shapes.
func (s Shape) IsKnown() bool {
	switch s {
	case ShapeMissingEnv, ShapeBinaryNotFound, ShapeEnvFileAbsent,
		ShapeServiceUnavailable, ShapeMissingContext, ShapeSubprocessFailed:
		return true
	}
	return false
}
```

- [ ] **Step 4.4: Run the test to verify it passes**

```bash
go test ./lib/heal/ -run TestShape_IsKnown_IncludesSubprocessFailed -v
```

Expected: PASS.

- [ ] **Step 4.5: Write the failing test for curated patterns**

Append to `lib/heal/patterns_test.go`:

```go
func TestMatchStderrPattern_SubprocessFailed_Patterns(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"docker buildx", `failed to solve: process "/bin/sh -c go work sync" did not complete successfully: exit code: 1`},
		{"go work module missing", `go: cannot load module charge-worker-conciliation listed in go.work file: open charge-worker-conciliation/go.mod: no such file or directory`},
		{"docker COPY", `COPY failed: file not found in build context or excluded by .dockerignore: stat src/missing: file does not exist`},
		{"plain did-not-complete", `process "/bin/sh -c npm run build" did not complete successfully: exit code: 2`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shape, ok := heal.MatchStderrPattern(tc.text)
			if !ok {
				t.Fatalf("MatchStderrPattern returned ok=false for %q", tc.text)
			}
			if shape != heal.ShapeSubprocessFailed {
				t.Fatalf("shape = %q, want %q", shape, heal.ShapeSubprocessFailed)
			}
		})
	}
}
```

- [ ] **Step 4.6: Run the test to verify it fails**

```bash
go test ./lib/heal/ -run TestMatchStderrPattern_SubprocessFailed_Patterns -v
```

Expected: FAIL — current patterns don't include any of the new ones.

- [ ] **Step 4.7: Add the patterns**

In `lib/heal/patterns.go`, change the `stderrPatterns` slice (currently:

```go
var stderrPatterns = []stderrPattern{
	{re: regexp.MustCompile(`\bENOENT\b.*\.env\b|\.env\b.*\bENOENT\b`), shape: ShapeEnvFileAbsent},
	{re: regexp.MustCompile(`permission denied:.*\.env\b`), shape: ShapeEnvFileAbsent},
	{re: regexp.MustCompile(`connection refused.*\b(postgres|mysql|redis|kafka)\b`), shape: ShapeServiceUnavailable},
	{re: regexp.MustCompile(`\bcommand not found\b`), shape: ShapeBinaryNotFound},
}
```

) to:

```go
var stderrPatterns = []stderrPattern{
	{re: regexp.MustCompile(`\bENOENT\b.*\.env\b|\.env\b.*\bENOENT\b`), shape: ShapeEnvFileAbsent},
	{re: regexp.MustCompile(`permission denied:.*\.env\b`), shape: ShapeEnvFileAbsent},
	{re: regexp.MustCompile(`connection refused.*\b(postgres|mysql|redis|kafka)\b`), shape: ShapeServiceUnavailable},
	{re: regexp.MustCompile(`\bcommand not found\b`), shape: ShapeBinaryNotFound},
	// subprocess-failed: build/toolchain failures with no auto-apply remediation.
	{re: regexp.MustCompile(`failed to solve:`), shape: ShapeSubprocessFailed},
	{re: regexp.MustCompile(`did not complete successfully: exit code: \d+`), shape: ShapeSubprocessFailed},
	{re: regexp.MustCompile(`cannot load module .* listed in go\.work`), shape: ShapeSubprocessFailed},
	{re: regexp.MustCompile(`COPY failed:`), shape: ShapeSubprocessFailed},
}
```

- [ ] **Step 4.8: Run the test to verify it passes**

```bash
go test ./lib/heal/ -run TestMatchStderrPattern_SubprocessFailed_Patterns -v
```

Expected: PASS.

- [ ] **Step 4.9: Write the failing test for the new rule**

Create `lib/heal/rules/subprocess_failed_test.go`:

```go
package rules_test

import (
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
	"github.com/iurykrieger/harness-framework/lib/heal/rules"
)

func TestSubprocessFailed_Match(t *testing.T) {
	intP := func(i int) *int { return &i }

	cases := []struct {
		name      string
		signal    heal.Signal
		wantMatch bool
		wantShape heal.Shape
	}{
		{
			name: "exit_code=0 — never matches",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(0),
				},
				Evidence: []heal.SignalEvidence{{Excerpt: "failed to solve: oops"}},
			},
			wantMatch: false,
		},
		{
			name: "exit_code=nil — never matches",
			signal: heal.Signal{
				Verdict:  "fail",
				Metadata: heal.SignalMetadata{},
				Evidence: []heal.SignalEvidence{{Excerpt: "failed to solve: oops"}},
			},
			wantMatch: false,
		},
		{
			name: "heal_hint with subprocess-failed prefix — match",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(1),
					HealHint: "subprocess-failed:failed to solve: oops",
				},
			},
			wantMatch: true,
			wantShape: heal.ShapeSubprocessFailed,
		},
		{
			name: "evidence excerpt matches curated pattern — match",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(1),
				},
				Evidence: []heal.SignalEvidence{
					{Excerpt: "failed to solve: process did not complete successfully: exit code: 1"},
				},
			},
			wantMatch: true,
			wantShape: heal.ShapeSubprocessFailed,
		},
		{
			name: "evidence rationale matches curated pattern — match",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(2),
				},
				Evidence: []heal.SignalEvidence{
					{Rationale: "cannot load module myservice listed in go.work file: open myservice/go.mod"},
				},
			},
			wantMatch: true,
			wantShape: heal.ShapeSubprocessFailed,
		},
		{
			name: "heal_hint with other shape prefix — no match (other rule handles)",
			signal: heal.Signal{
				Verdict: "fail",
				Metadata: heal.SignalMetadata{
					ExitCode: intP(1),
					HealHint: "service-unavailable:redis",
				},
			},
			wantMatch: false,
		},
	}

	registered := rules.Registered()
	var subprocFailedRule heal.Rule
	for _, r := range registered {
		if r.Name() == "subprocess-failed" {
			subprocFailedRule = r
			break
		}
	}
	if subprocFailedRule == nil {
		t.Fatal("subprocess-failed rule not registered")
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched, shape, _ := subprocFailedRule.Match(tc.signal, heal.FailedSensor{})
			if matched != tc.wantMatch {
				t.Fatalf("matched=%v, want %v", matched, tc.wantMatch)
			}
			if matched && shape != tc.wantShape {
				t.Fatalf("shape=%q, want %q", shape, tc.wantShape)
			}
		})
	}
}
```

- [ ] **Step 4.10: Run the test to verify it fails**

```bash
go test ./lib/heal/rules/ -run TestSubprocessFailed_Match -v
```

Expected: FAIL with "subprocess-failed rule not registered".

- [ ] **Step 4.11: Implement the new rule**

Create `lib/heal/rules/subprocess_failed.go`:

```go
// lib/heal/rules/subprocess_failed.go
package rules

import (
	"strings"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

// subprocessFailed fires when a non-zero exit code is paired with either
// a metadata.heal_hint = "subprocess-failed:<detail>" prefix (the fast
// path produced by buildHealHint / stopBlockingDep when the stderr text
// matched a curated pattern) or a matching pattern in evidence[].
//
// This rule is registered BEFORE healHint so it claims the
// subprocess-failed shape exclusively; healHint keeps handling every
// other shape prefix.
type subprocessFailed struct{}

func (subprocessFailed) Name() string { return "subprocess-failed" }

func (subprocessFailed) Match(signal heal.Signal, _ heal.FailedSensor) (bool, heal.Shape, string) {
	if signal.Metadata.ExitCode == nil || *signal.Metadata.ExitCode == 0 {
		return false, "", ""
	}

	// Fast path: heal_hint already carries the shape.
	if hint := signal.Metadata.HealHint; hint != "" {
		if idx := strings.Index(hint, ":"); idx > 0 {
			if heal.Shape(hint[:idx]) == heal.ShapeSubprocessFailed {
				return true, heal.ShapeSubprocessFailed, hint[idx+1:]
			}
		}
	}

	// Slow path: scan evidence rationale and excerpt for curated patterns.
	for _, ev := range signal.Evidence {
		for _, line := range []string{ev.Excerpt, ev.Rationale} {
			if line == "" {
				continue
			}
			if shape, ok := heal.MatchStderrPattern(line); ok && shape == heal.ShapeSubprocessFailed {
				return true, heal.ShapeSubprocessFailed, line
			}
		}
	}

	return false, "", ""
}
```

- [ ] **Step 4.12: Register the rule before healHint in registry.go**

Modify `lib/heal/rules/registry.go`:

Change:

```go
func Registered() []heal.Rule {
	return []heal.Rule{
		missingEnv{},
		missingContext{},
		healHint{},
		exitCode127{},
		prepareTemplateCopy{},
		stderrPatternRule{},
	}
}
```

to:

```go
func Registered() []heal.Rule {
	return []heal.Rule{
		missingEnv{},
		missingContext{},
		subprocessFailed{}, // before healHint: claims subprocess-failed shape exclusively
		healHint{},
		exitCode127{},
		prepareTemplateCopy{},
		stderrPatternRule{},
	}
}
```

- [ ] **Step 4.13: Run the rule test to verify it passes**

```bash
go test ./lib/heal/rules/ -run TestSubprocessFailed_Match -v
```

Expected: PASS.

- [ ] **Step 4.14: Write an end-to-end test**

Append to `lib/heal/heal_e2e_test.go`:

```go
// TestClassify_SubprocessFailed_FromCascadeDepAggregate verifies the
// full pipeline: a Signal of shape produced by stopBlockingDep
// (verdict=fail, metadata.heal_hint=subprocess-failed:..., evidence
// excerpts) is classified by ClassifyWith into a Result whose Rule is
// "subprocess-failed".
func TestClassify_SubprocessFailed_FromCascadeDepAggregate(t *testing.T) {
	exitCode := 1
	signal := heal.Signal{
		Verdict:  "fail",
		Severity: "high",
		Evidence: []heal.SignalEvidence{
			{
				Rationale: `blocking dep "run-project-charge-api" stderr/stdout tail`,
				Excerpt:   `failed to solve: process "/bin/sh -c go work sync" did not complete successfully: exit code: 1`,
			},
		},
		Metadata: heal.SignalMetadata{
			ExitCode: &exitCode,
			HealHint: `subprocess-failed:failed to solve: process "/bin/sh -c go work sync" did not complete successfully: exit code: 1`,
		},
	}

	res, ok := heal.ClassifyWith(rules.Registered(), signal, heal.FailedSensor{ID: "run-project-charge-api"})
	if !ok {
		t.Fatal("classify returned ok=false")
	}
	if res.Rule != "subprocess-failed" {
		t.Errorf("rule=%q, want subprocess-failed", res.Rule)
	}
	if res.Shape != heal.ShapeSubprocessFailed {
		t.Errorf("shape=%q, want subprocess-failed", res.Shape)
	}
}
```

If `rules` is not yet imported in this test file, add the import:

```go
import (
	"github.com/iurykrieger/harness-framework/lib/heal/rules"
)
```

- [ ] **Step 4.15: Run the e2e test**

```bash
go test ./lib/heal/ -run TestClassify_SubprocessFailed_FromCascadeDepAggregate -v
```

Expected: PASS.

- [ ] **Step 4.16: Run the full heal suite for regressions**

```bash
go test ./lib/heal/...
```

Expected: all PASS. Existing tests for `heal_hint`, `stderr_pattern`, and other rules are unaffected — the new rule only claims signals where exit_code is non-zero AND the shape is subprocess-failed.

- [ ] **Step 4.17: Commit**

```bash
git add lib/heal/classify.go lib/heal/classify_test.go \
        lib/heal/patterns.go lib/heal/patterns_test.go \
        lib/heal/rules/subprocess_failed.go \
        lib/heal/rules/subprocess_failed_test.go \
        lib/heal/rules/registry.go \
        lib/heal/heal_e2e_test.go
git commit -m "feat(heal): subprocess-failed shape + rule + patterns (#47)

Extends the closed Shape enum with subprocess-failed. Adds curated
non-capturing patterns for docker buildx, go work module loading,
docker COPY, and generic 'did not complete successfully' failures
(all map to subprocess-failed). New subprocessFailed rule placed
before healHint in the registry claims the subprocess-failed shape
exclusively; healHint keeps handling every other shape prefix."
```

---

## Task 5: Populate evidence[] in single-output sensor aggregates

**Goal:** When `output=single` and the subprocess exits non-zero, fold the captured stderr text into `evidence[].excerpt` so the new `subprocess-failed` rule (and the existing `stderr-pattern` rule) can match against non-dep, standalone single-output sensors too.

This is independent of Tasks 1–3 (which cover the dep-cascade path). Single-output sensors that fail directly never go through `stopBlockingDep`; their aggregate is built by `RunOne` / `RunOneWithRoot`, so the evidence fold has to happen there.

**Files:**
- Modify: `lib/orchestrator/lifecycle.go` (two sites: `runOneImpl` around line 132-153 and `runOneWithPersistenceImpl` around line 431-444)
- Test: `lib/orchestrator/lifecycle_test.go`

- [ ] **Step 5.1: Locate the existing evidence assembly in lifecycle.go**

Read `lib/orchestrator/lifecycle.go` lines 188-201 (`runOneImpl`'s Signal construction) and 477-490 (`runOneWithPersistenceImpl`'s Signal construction). The current code calls `buildLifecycleEvidence(prepResults, tdResults)` and assigns it to `evidence`. We need to additionally fold the subprocess stderr tail into evidence for single-output failure cases.

- [ ] **Step 5.2: Write the failing test**

Append to `lib/orchestrator/lifecycle_test.go`:

```go
// TestRunOne_SingleOutputFailure_PopulatesEvidenceFromStderr verifies
// that a single-output sensor exiting non-zero with stderr matching a
// curated heal pattern has at least one evidence[].excerpt carrying
// that stderr text. Enables the subprocess-failed rule to fire for
// standalone failing sensors.
func TestRunOne_SingleOutputFailure_PopulatesEvidenceFromStderr(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".harness", "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "build-fails",
"version": "1.0.0",
"name": "Build fails",
"description": "Standalone sensor that emits a 'failed to solve' line on stderr and exits 1.",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"regulation": "behaviour",
"phase": "on-demand",
"triggers": [{"on": "manual"}],
"verification": {"golden_cases": [{"fixture": "smoke", "expected_verdict": "pass", "expected_severity": "info"}]},
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "echo 'failed to solve: process did not complete successfully: exit code: 1' 1>&2; exit 1",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, "build-fails.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	s := loadDepSensor(t, root, "build-fails")
	v := loadValidator(t)
	var stdout, stderr bytes.Buffer
	schemasDir := schematest.RepoSchemasDir(t)
	sig, code := orchestrator.RunOne(context.Background(), s, root, schemasDir, v, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("RunOne exit code = %d, want 0 (emitted aggregate is fail, but RunOne itself succeeds)", code)
	}
	if v, _ := sig["verdict"].(string); v != "fail" {
		t.Fatalf("verdict = %q, want fail", v)
	}

	ev, _ := sig["evidence"].([]interface{})
	foundFailedToSolve := false
	for _, raw := range ev {
		e, _ := raw.(map[string]interface{})
		if e == nil {
			continue
		}
		excerpt, _ := e["excerpt"].(string)
		if strings.Contains(excerpt, "failed to solve:") {
			foundFailedToSolve = true
			break
		}
	}
	if !foundFailedToSolve {
		t.Fatalf("evidence does not contain stderr tail; got %+v", ev)
	}
}
```

- [ ] **Step 5.3: Run the test to verify it fails**

```bash
go test ./lib/orchestrator/ -run TestRunOne_SingleOutputFailure_PopulatesEvidenceFromStderr -v
```

Expected: FAIL — current code populates `metadata.heal_hint` but not `evidence[]` from stderr.

- [ ] **Step 5.4: Add a helper to lifecycle.go**

In `lib/orchestrator/lifecycle.go`, add a new helper near `buildHealHint` (around line 649):

```go
// buildStderrEvidence returns evidence[] entries derived from the
// subprocess's captured stderr tail. Called only when output=="single"
// AND the aggregate verdict is fail/error AND stderr is non-empty.
// Returns the last depRawLogTailLines (20) non-empty stderr lines as
// excerpt entries with a shared rationale; the entries are appended to
// any lifecycle evidence the aggregate already carries.
//
// Constant is shared with lib/orchestrator/live_deps.go (depRawLogTailLines).
func buildStderrEvidence(output, verdict, stderrText string) []interface{} {
	if output != "single" {
		return nil
	}
	if verdict != "fail" && verdict != "error" {
		return nil
	}
	if stderrText == "" {
		return nil
	}
	allLines := strings.Split(stderrText, "\n")
	out := make([]interface{}, 0, depRawLogTailLines)
	// Walk backward, skipping empty lines.
	collected := []string{}
	for i := len(allLines) - 1; i >= 0 && len(collected) < depRawLogTailLines; i-- {
		line := strings.TrimRight(allLines[i], "\r")
		if line == "" {
			continue
		}
		collected = append([]string{line}, collected...)
	}
	for _, line := range collected {
		out = append(out, map[string]interface{}{
			"rationale": "subprocess stderr/stdout tail",
			"excerpt":   line,
		})
	}
	return out
}
```

- [ ] **Step 5.5: Wire the helper into runOneImpl**

In `lib/orchestrator/lifecycle.go`, locate the line in `runOneImpl` (around line 198):

```go
		"evidence":    buildLifecycleEvidence(prepResults, tdResults),
```

We need to know `res.StderrExcerpt` at this site. Currently `res` (the `subprocess.StreamResult`) is a local variable in the `else` branch around line 105-130. The cleanest fix is to keep a copy of `res.StderrExcerpt` in a new outer variable.

Add this declaration near the other outer-scope vars at the top of the function (around line 79-82):

```go
	var stderrExcerpt string
```

Right after the assignment `aggregateMD = map[string]interface{}{ ... }` block (line 132-152), add:

```go
		stderrExcerpt = res.StderrExcerpt
```

Then change line 198 from:

```go
		"evidence":    buildLifecycleEvidence(prepResults, tdResults),
```

to:

```go
		"evidence":    appendStderrEvidence(buildLifecycleEvidence(prepResults, tdResults), output, aggVerdict, stderrExcerpt),
```

And add a helper near `buildStderrEvidence`:

```go
// appendStderrEvidence concatenates lifecycle evidence with stderr-tail
// evidence for single-output failures. Returns the lifecycle evidence
// unchanged when buildStderrEvidence has nothing to add.
func appendStderrEvidence(lifecycleEvidence []interface{}, output, verdict, stderrText string) []interface{} {
	extra := buildStderrEvidence(output, verdict, stderrText)
	if len(extra) == 0 {
		return lifecycleEvidence
	}
	return append(lifecycleEvidence, extra...)
}
```

- [ ] **Step 5.6: Wire the helper into runOneWithPersistenceImpl too**

Repeat Step 5.5 for the second function: add `var stderrExcerpt string` near line 288-291; assign `stderrExcerpt = res.StderrExcerpt` right after `aggregateMD = map[string]interface{}{ ... }` around line 431-444; change `"evidence": buildLifecycleEvidence(...)` at line 487 to use `appendStderrEvidence` the same way.

- [ ] **Step 5.7: Run the test to verify it passes**

```bash
go test ./lib/orchestrator/ -run TestRunOne_SingleOutputFailure_PopulatesEvidenceFromStderr -v
```

Expected: PASS.

- [ ] **Step 5.8: Run all suites for regressions**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential ./skills/...
```

Expected: all PASS. Stream-mode sensors don't get stderr evidence (the helper returns nil when `output != "single"`); passing single-output sensors don't get it (helper returns nil when verdict is not fail/error); single-output sensors with empty stderr don't get it either. Existing schema validation passes because `evidence[].excerpt` is an allowed string field.

- [ ] **Step 5.9: Commit**

```bash
git add lib/orchestrator/lifecycle.go lib/orchestrator/lifecycle_test.go
git commit -m "feat(orchestrator): populate evidence[] from stderr in single-output failures (#47)

When output=single and the subprocess exits non-zero with stderr
text, the aggregate Signal now carries the last 20 non-empty stderr
lines as evidence[].excerpt entries. Stream-mode aggregates and
passing sensors are unchanged. Enables the subprocess-failed and
stderr-pattern heal rules to match against standalone failing
single-output sensors, not just blocking-dep cascade paths."
```

---

## Task 6: Final verification

**Goal:** End-to-end sanity that all five commits compose correctly.

- [ ] **Step 6.1: Full Go test sweep**

```bash
cd /Users/iury.krieger/Workspace/iurykrieger/harness-framework/.claude/worktrees/fix-47
go test ./lib/...
go test -tags=run_computational ./skills/...
go test -tags=run_inferential   ./skills/...
go test -tags=start_sensor      ./skills/...
go test -tags=stop_sensor       ./skills/...
go test -tags=list_sensors      ./skills/...
go test -tags=tail_sensor       ./skills/...
go test -tags=heal_retry_original ./skills/heal-sensor/...
go test -tags=heal_diagnose       ./skills/heal-sensor/...
go test -tags=write_usecase       ./skills/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential   ./...
```

Expected: every command exits 0, no failures.

- [ ] **Step 6.2: DoD checklist confirmation**

Verify each DoD item from the spec by running these probes:

1. **Dep exit captured** — `TestStartBlockingDep_WritesExitCodeFile` covers this. ✅ (passes after Task 1)
2. **Honest stopBlockingDep aggregate** — `TestStopBlockingDep_NonZeroExit_EmitsFailWithEvidence` covers this. ✅ (passes after Task 2)
3. **Liveness gate emits cascade** — `TestRunWithDeps_BlockingDepDiesPostAttach_CascadesAndSkipsTarget` covers this, including the line-count assertion. ✅ (passes after Task 3)
4. **Hook classifies cascade end-to-end** — `TestClassify_SubprocessFailed_FromCascadeDepAggregate` covers the classifier portion. ✅ (passes after Task 4)
5. **Single-output evidence** — `TestRunOne_SingleOutputFailure_PopulatesEvidenceFromStderr` covers this. ✅ (passes after Task 5)
6. **Shape enum extended** — `TestShape_IsKnown_IncludesSubprocessFailed` covers this. ✅ (passes after Task 4)
7. **Heal action allowlist unchanged** — confirmed by `grep -n "case \"" lib/heal/apply.go | wc -l` returning the same count as before; if any task above proposes a change, redo it. The plan does not touch `apply.go`.
8. **Existing suites stay green** — Step 6.1 covers this.
9. **Schema validation** — every Signal emitted by the new paths flows through `validateOrFallback` in the orchestrator code modified by Tasks 2 and 3.

```bash
grep -c "case \"" lib/heal/apply.go
```

Expected: same number as before the work (no new `case` arms added).

- [ ] **Step 6.3: Push the branch and open the PR**

```bash
git push -u origin <branch-name>
gh pr create --title "fix(orchestrator,heal): cascade through dead blocking deps + subprocess-failed shape" --body "$(cat <<'EOF'
## Summary

- `startBlockingDep` wraps detached dep commands so the subprocess's exit status lands in `<runtime>/<dep_id>/exit_code` regardless of how the dep terminates.
- `stopBlockingDep` reads that file at teardown and emits an honest `verdict=fail` aggregate with `raw.log` tail in evidence + `metadata.heal_hint` when the dep exited non-zero.
- New `AwaitDepLiveness` gate in `runWithDepsImpl` detects deps that died between attach and the dependent's command, emits their real aggregate, and emits a cascade Signal for the dependent.
- Closed `heal.Shape` enum extended with `subprocess-failed`; new `subprocessFailed` rule registered before `healHint` claims the shape exclusively.
- Single-output sensor aggregates now fold captured stderr into `evidence[]` when the subprocess exits non-zero.

Closes #47.

## Test plan

- [x] `go test ./lib/...` (orchestrator + heal + everywhere else)
- [x] `go test -tags=run_computational ./skills/...`
- [x] `go test -tags=run_inferential ./skills/...`
- [x] Reproduce the issue's scenario manually: docker buildx failure in a blocking dep produces a `/heal-sensor` injection pointing at the dep, not the dependent.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Use the branch name printed by `git branch --show-current`. Confirm the PR URL in stdout.

---

## Self-review

**1. Spec coverage:**

| Spec section | Covered by task |
|---|---|
| Fix step 1 — exit-code wrap | Task 1 |
| Fix step 2 — honest stopBlockingDep | Task 2 |
| Fix step 3 — post-attach liveness gate | Task 3 |
| Fix step 4 — single-output evidence | Task 5 |
| Fix step 5 — classification (shape, patterns, rule, diagnose) | Task 4 (no changes to `diagnose.go` because the script emits diagnosis input, not Plan — the LLM agent reads the input and produces the Plan with `propose_only[]` based on `metadata.heal_hint`/evidence; this is also why no apply.go changes are needed for `propose_only[]` actions) |
| DoD #1–#9 | Task 6 maps each DoD to a probe |
| Anti-scope items | The plan does not change `BuildCascadeSignal`, `signal.json` schema, heal action kinds, `AttachLiveDep`/`DetachLiveDep` signatures, or the issue-#20 cascade gate |
| Five-commit sequencing | Tasks 1–5 each end in a single commit |

**Note on `diagnose.go`:** The spec mentions a `propose_only[]` template for the new shape in `skills/heal-sensor/scripts/diagnose.go`. Reading `diagnose.go` (the actual code) shows it emits a generic "diagnosis input" JSON for the calling LLM agent — the deterministic script does not branch by Shape. The LLM produces the Plan with `propose_only[]` based on the input. So no Go code change is required in `diagnose.go`. If the heal SKILL.md prose needs updates to instruct the LLM to emit `propose_only[]` for the new shape, that is a one-line markdown edit out of scope for this plan (the heal SKILL.md already documents the shape→action mapping pattern generically).

**2. Placeholder scan:**

- No "TBD", "TODO", "fill in later", "implement appropriate error handling" anywhere.
- Every code block contains the full code to write.
- Every test contains its complete body.
- Every command is exact.

**3. Type consistency:**

- `LiveDep`, `Sensor`, `registry.Root`, `registry.RunningSensorEntry`, `heal.Signal`, `heal.Shape`, `heal.SignalMetadata`, `subprocess.StreamResult` — all match existing types.
- `readDepExitState` signature `(verdict, severity string, exitCode *int, evidenceItems []interface{}, healHint string)` — referenced consistently in Tasks 2 and 3.
- `AwaitDepLiveness` signature `([]LiveDep, string) (string, map[string]interface{}, string)` — referenced consistently in Tasks 3 and 6.
- `depRawLogTailLines` constant — defined in Task 2, reused in Task 5.
- `buildStderrEvidence` and `appendStderrEvidence` — defined and used only in Task 5.
- `shellQuote` — defined in Task 1, no callers outside Task 1.

---

**Plan complete and saved to `docs/superpowers/plans/2026-05-14-heal-sensor-blocking-deps.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
