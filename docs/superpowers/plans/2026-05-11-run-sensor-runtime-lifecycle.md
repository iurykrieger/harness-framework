# /run-sensor runtime persistence + lifecycle parity — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/run-sensor` write the same on-disk artifacts (`raw.log`, `signals.log`) and participate in the same `running_sensors.json` registry as `/start-sensor`, with a shared `<run-id>/` layout. Sister skills (`/list-sensors`, `/tail-sensor`, `/stop-sensor`) operate uniformly across both runner types.

**Architecture:** Bottom-up TDD. Foundation first (`lib/registry/paths.go`, `lib/registry/state.go`), then streaming tee (`lib/subprocess/stream.go`), then orchestrator (`lib/orchestrator/lifecycle.go`) gets an optional `*registry.Root` parameter. Skills (`run-sensor`, `start-sensor`, `list-sensors`, `tail-sensor`, `stop-sensor`) migrate last, callsite-by-callsite per the spec's rewrite table. Each task is independently testable and commits on its own.

**Tech Stack:** Go 1.25, single module `github.com/iurykrieger/harness-framework`; `github.com/santhosh-tekuri/jsonschema/v5` (Draft 2020-12); `github.com/google/uuid`; `github.com/fsnotify/fsnotify`.

**Spec:** `docs/superpowers/specs/2026-05-11-run-sensor-runtime-lifecycle-design.md`

---

## File Structure

### Modify

| File | Change |
| --- | --- |
| `schemas/signal.json` | Tighten `run_id` description. |
| `lib/registry/state.go` | Add `RunID`, `Blocking` fields; add `FindBlockingEntry`, `FindEntries`, `FindEntryByRunID`, `RemoveEntryByRunID`; keep `FindEntry`/`RemoveEntry` as internal helpers. |
| `lib/registry/state_test.go` | Tests for new helpers and field round-trip. |
| `lib/registry/paths.go` | `RawLog`/`SignalsLog` accept `runID`; add `RunDir`, `LegacyRawLog`, `LegacySignalsLog`. |
| `lib/registry/paths_test.go` | Tests for new path helpers. |
| `lib/registry/sanitize.go` | Migrate legacy entries lacking `run_id`/`blocking`. |
| `lib/registry/sanitize_test.go` | Tests for legacy migration. |
| `lib/subprocess/stream.go` | `StreamConfig` gains `RunDir`; when set, tee raw bytes to `raw.log` and individual Signals to `signals.log`. |
| `lib/subprocess/stream_test.go` | Tests for persistence behavior. |
| `lib/orchestrator/lifecycle.go` | `RunOne` accepts optional `*registry.Root`; when non-nil, inserts entry post-spawn, defers cleanup, writes aggregate to `signals.log`. |
| `lib/orchestrator/lifecycle_test.go` | Tests for Root-driven persistence. |
| `lib/orchestrator/run.go` | `RunWithDepsRoot` threads optional Root. |
| `lib/orchestrator/run_test.go` | Cascade-skip invariant test. |
| `lib/orchestrator/live_deps.go` | Callsite rewrites per spec table. |
| `lib/orchestrator/live_deps_test.go` | Update expectations. |
| `skills/run-sensor/scripts/run-computational.go` | Resolve `Root` via `registry.Lookup`; install SIGINT/SIGTERM handler; thread Root to orchestrator. |
| `skills/run-sensor/scripts/run-computational_test.go` | Tests for persistence + signal handler. |
| `skills/run-sensor/scripts/run-inferential.go` | Same as computational. |
| `skills/run-sensor/scripts/run-inferential_test.go` | Same. |
| `skills/start-sensor/scripts/start.go` | Compose `run_id = <pid>-<short-uuid>`; new `<run-id>/` paths; singleton filtered by `blocking:true`. |
| `skills/start-sensor/scripts/start_test.go` | Tests for new paths + composite run_id. |
| `skills/start-sensor/scripts/watcher.go` | Read `HARNESS_WATCHER_RUN_ID`; use `FindEntryByRunID`. |
| `skills/start-sensor/scripts/watcher_test.go` | Tests. |
| `skills/list-sensors/scripts/list.go` | Iterate all entries; surface `blocking`, `run_id`. |
| `skills/list-sensors/scripts/list_test.go` | Tests for multi-entry, blocking-aware liveness. |
| `skills/tail-sensor/scripts/tail.go` | Accept path-like `<id>/<run-id>`; ambiguity errors; resolve via `FindEntries`/`FindEntryByRunID`. |
| `skills/tail-sensor/scripts/tail_test.go` | Tests. |
| `skills/stop-sensor/scripts/stop.go` | Accept path-like args; blocking-preferred resolution; SIGTERM for `blocking:false`. |
| `skills/stop-sensor/scripts/stop_test.go` | Tests. |
| `lib/testfixtures/paths.go` | Add `WithRunDir(t, sensorID) (root, runID, cleanup)`. |

### Create

None — all changes modify existing files.

---

## Task 1: Add `RunID` and `Blocking` fields to `RunningSensorEntry`

**Files:**
- Modify: `lib/registry/state.go`
- Modify: `lib/registry/state_test.go`

- [ ] **Step 1: Write the failing test**

Add to `lib/registry/state_test.go` (in package `registry_test`; reuse existing helpers):

```go
func TestRunningSensorEntry_RunIDBlockingRoundtrip(t *testing.T) {
    dir := t.TempDir()
    r := registry.NewRoot(dir)
    if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
        t.Fatal(err)
    }
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{{
        SensorID: "alpha", RunID: "12345-abc12345", Blocking: true,
        PID: 100, PGID: 100, WatcherPID: 101,
        StartedAt: "2026-05-11T00:00:00Z", Command: "echo hi",
        LogDir: ".runtime/sensors/alpha/12345-abc12345",
        HeldBy: []registry.HeldByEntry{},
    }}}
    if err := registry.Save(r, rs); err != nil {
        t.Fatalf("save: %v", err)
    }
    got, err := registry.Load(r)
    if err != nil {
        t.Fatalf("load: %v", err)
    }
    if len(got.Entries) != 1 {
        t.Fatalf("entries = %d, want 1", len(got.Entries))
    }
    e := got.Entries[0]
    if e.RunID != "12345-abc12345" {
        t.Errorf("RunID = %q, want %q", e.RunID, "12345-abc12345")
    }
    if !e.Blocking {
        t.Errorf("Blocking = false, want true")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/registry/ -run TestRunningSensorEntry_RunIDBlockingRoundtrip -v`
Expected: FAIL with "undefined: RunID" / "undefined: Blocking" at the struct literal.

- [ ] **Step 3: Add the fields**

In `lib/registry/state.go`, replace the `RunningSensorEntry` struct:

```go
type RunningSensorEntry struct {
    SensorID       string          `json:"sensor_id"`
    RunID          string          `json:"run_id"`
    Blocking       bool            `json:"blocking"`
    PID            int             `json:"pid"`
    PGID           int             `json:"pgid"`
    WatcherPID     int             `json:"watcher_pid"`
    StartedAt      string          `json:"started_at"`
    Command        string          `json:"command"`
    LogDir         string          `json:"log_dir"`
    HeldBy         []HeldByEntry   `json:"held_by"`
    SubprocessExit *SubprocessExit `json:"subprocess_exit,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./lib/registry/ -v`
Expected: PASS, including the new test. Existing tests still pass (fields default to zero values).

- [ ] **Step 5: Commit**

```bash
git add lib/registry/state.go lib/registry/state_test.go
git commit -m "feat(registry): add run_id + blocking fields to RunningSensorEntry"
```

---

## Task 2: Add registry lookup helpers (`FindBlockingEntry`, `FindEntries`, `FindEntryByRunID`, `RemoveEntryByRunID`)

**Files:**
- Modify: `lib/registry/state.go`
- Modify: `lib/registry/state_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `lib/registry/state_test.go`:

```go
func TestFindBlockingEntry(t *testing.T) {
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "1-aa", Blocking: false, PID: 1, PGID: 1},
        {SensorID: "alpha", RunID: "2-bb", Blocking: true, PID: 2, PGID: 2},
        {SensorID: "beta",  RunID: "3-cc", Blocking: false, PID: 3, PGID: 3},
    }}
    e := rs.FindBlockingEntry("alpha")
    if e == nil || e.RunID != "2-bb" {
        t.Fatalf("expected RunID=2-bb, got %+v", e)
    }
    if rs.FindBlockingEntry("beta") != nil {
        t.Error("expected nil for beta (no blocking entry)")
    }
    if rs.FindBlockingEntry("gamma") != nil {
        t.Error("expected nil for missing id")
    }
}

func TestFindEntries(t *testing.T) {
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "1-aa", PID: 1, PGID: 1},
        {SensorID: "alpha", RunID: "2-bb", PID: 2, PGID: 2},
        {SensorID: "beta",  RunID: "3-cc", PID: 3, PGID: 3},
    }}
    es := rs.FindEntries("alpha")
    if len(es) != 2 {
        t.Fatalf("expected 2 entries for alpha, got %d", len(es))
    }
    if rs.FindEntries("missing") != nil && len(rs.FindEntries("missing")) != 0 {
        t.Error("expected empty/nil for missing id")
    }
}

func TestFindEntryByRunID(t *testing.T) {
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "1-aa", PID: 1, PGID: 1},
        {SensorID: "alpha", RunID: "2-bb", PID: 2, PGID: 2},
    }}
    e := rs.FindEntryByRunID("2-bb")
    if e == nil || e.SensorID != "alpha" || e.PID != 2 {
        t.Fatalf("got %+v", e)
    }
    if rs.FindEntryByRunID("missing") != nil {
        t.Error("expected nil for missing run_id")
    }
}

func TestRemoveEntryByRunID(t *testing.T) {
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "1-aa", PID: 1, PGID: 1},
        {SensorID: "alpha", RunID: "2-bb", PID: 2, PGID: 2},
    }}
    rs.RemoveEntryByRunID("1-aa")
    if len(rs.Entries) != 1 || rs.Entries[0].RunID != "2-bb" {
        t.Fatalf("after remove: %+v", rs.Entries)
    }
    rs.RemoveEntryByRunID("missing") // no-op
    if len(rs.Entries) != 1 {
        t.Fatalf("no-op removed entries: %+v", rs.Entries)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./lib/registry/ -run 'TestFind|TestRemoveEntryByRunID' -v`
Expected: FAIL with "undefined: FindBlockingEntry" etc.

- [ ] **Step 3: Implement the helpers**

Append to `lib/registry/state.go` (after the existing `RemoveEntry`):

```go
// FindBlockingEntry returns the single blocking:true entry for id, or nil.
// Non-blocking entries are ignored.
func (rs *RunningSensors) FindBlockingEntry(id string) *RunningSensorEntry {
    for i := range rs.Entries {
        if rs.Entries[i].SensorID == id && rs.Entries[i].Blocking {
            return &rs.Entries[i]
        }
    }
    return nil
}

// FindEntries returns all entries for id (any blocking value).
func (rs *RunningSensors) FindEntries(id string) []*RunningSensorEntry {
    var out []*RunningSensorEntry
    for i := range rs.Entries {
        if rs.Entries[i].SensorID == id {
            out = append(out, &rs.Entries[i])
        }
    }
    return out
}

// FindEntryByRunID returns the unique entry with the given run_id, or nil.
func (rs *RunningSensors) FindEntryByRunID(runID string) *RunningSensorEntry {
    for i := range rs.Entries {
        if rs.Entries[i].RunID == runID {
            return &rs.Entries[i]
        }
    }
    return nil
}

// RemoveEntryByRunID deletes the entry matching run_id (no-op if absent).
func (rs *RunningSensors) RemoveEntryByRunID(runID string) {
    out := rs.Entries[:0]
    for _, e := range rs.Entries {
        if e.RunID != runID {
            out = append(out, e)
        }
    }
    rs.Entries = out
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./lib/registry/ -v`
Expected: PASS, all four new tests + existing ones.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/state.go lib/registry/state_test.go
git commit -m "feat(registry): add FindBlockingEntry, FindEntries, FindEntryByRunID, RemoveEntryByRunID"
```

---

## Task 3: Extend path helpers with `runID` and add legacy fallbacks

**Files:**
- Modify: `lib/registry/paths.go`
- Modify: `lib/registry/paths_test.go`

- [ ] **Step 1: Inspect existing tests and callers of `RawLog`/`SignalsLog`**

Run: `grep -rn 'RawLog\|SignalsLog' lib/ skills/ | grep -v _test.go`
Note: callers in `skills/start-sensor/scripts/start.go`, `skills/start-sensor/scripts/watcher.go`, `skills/tail-sensor/scripts/tail.go`. These will be updated in later tasks (Task 9, 10, 12). For now we change the signatures and break those callers temporarily — Task 4 immediately re-fixes the build by updating internal usage; full skill rewires land in their dedicated tasks.

To avoid a broken build between tasks, ADD new `RawLogRun`/`SignalsLogRun` accepting `runID`, and **keep the existing `RawLog(id)`/`SignalsLog(id)`** as deprecated wrappers that return the legacy flat path. Later tasks (Task 9, 10, 12) drop the legacy ones in favor of `RawLogRun`/`SignalsLogRun` at their callsites.

This decision is recorded inline below; do not skip it.

- [ ] **Step 2: Write the failing tests**

Append to `lib/registry/paths_test.go`:

```go
func TestRunDir(t *testing.T) {
    r := registry.NewRoot("/tmp/proj")
    got := r.RunDir("alpha", "12345-abc12345")
    want := "/tmp/proj/.runtime/sensors/alpha/12345-abc12345"
    if got != want {
        t.Errorf("RunDir = %q, want %q", got, want)
    }
}

func TestRawLogRun_SignalsLogRun(t *testing.T) {
    r := registry.NewRoot("/tmp/proj")
    if got := r.RawLogRun("alpha", "1-aa"); got != "/tmp/proj/.runtime/sensors/alpha/1-aa/raw.log" {
        t.Errorf("RawLogRun = %q", got)
    }
    if got := r.SignalsLogRun("alpha", "1-aa"); got != "/tmp/proj/.runtime/sensors/alpha/1-aa/signals.log" {
        t.Errorf("SignalsLogRun = %q", got)
    }
}

func TestLegacyRawLog_LegacySignalsLog(t *testing.T) {
    r := registry.NewRoot("/tmp/proj")
    if got := r.LegacyRawLog("alpha"); got != "/tmp/proj/.runtime/sensors/alpha/raw.log" {
        t.Errorf("LegacyRawLog = %q", got)
    }
    if got := r.LegacySignalsLog("alpha"); got != "/tmp/proj/.runtime/sensors/alpha/signals.log" {
        t.Errorf("LegacySignalsLog = %q", got)
    }
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./lib/registry/ -run 'TestRunDir|TestRawLogRun|TestLegacy' -v`
Expected: FAIL with "undefined: RunDir" etc.

- [ ] **Step 4: Implement the new methods**

In `lib/registry/paths.go`, append (do NOT modify existing `RawLog`/`SignalsLog`):

```go
// RunDir returns the per-run directory under .runtime/sensors/<id>/<runID>/.
func (r Root) RunDir(id, runID string) string {
    return filepath.Join(r.SensorDir(id), runID)
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

The existing `RawLog(id)`/`SignalsLog(id)` are unchanged (they return the same paths as `LegacyRawLog`/`LegacySignalsLog`); skill callsites switch to `*Run` variants in their dedicated tasks.

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./lib/registry/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add lib/registry/paths.go lib/registry/paths_test.go
git commit -m "feat(registry): add RunDir + RawLogRun/SignalsLogRun + legacy fallbacks"
```

---

## Task 4: Migrate legacy entries on Load (sanitize.go)

**Files:**
- Modify: `lib/registry/sanitize.go`
- Modify: `lib/registry/sanitize_test.go`

- [ ] **Step 1: Write the failing test**

Append to `lib/registry/sanitize_test.go`:

```go
func TestSanitizeAll_LegacyEntryMigration(t *testing.T) {
    rs := &registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", PID: 4242, PGID: 4242, StartedAt: "2026-01-01T00:00:00Z",
            Command: "old-cmd", LogDir: ".runtime/sensors/alpha"}, // no run_id, no blocking
    }}
    reports := registry.SanitizeAll(rs)
    if len(rs.Entries) != 1 {
        t.Fatalf("expected 1 entry kept, got %d", len(rs.Entries))
    }
    got := rs.Entries[0]
    if got.RunID != "4242-legacy" {
        t.Errorf("RunID = %q, want %q", got.RunID, "4242-legacy")
    }
    if !got.Blocking {
        t.Error("Blocking = false, want true (legacy entries are blocking=true)")
    }
    var found bool
    for _, r := range reports {
        if r.Field == "run_id" && r.SensorID == "alpha" {
            found = true
        }
    }
    if !found {
        t.Errorf("expected a run_id migration report; got %+v", reports)
    }
}

func TestSanitizeAll_DoesNotTouchEntriesWithRunID(t *testing.T) {
    rs := &registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "100-abc", Blocking: true,
            PID: 100, PGID: 100, StartedAt: "2026-01-01T00:00:00Z"},
    }}
    _ = registry.SanitizeAll(rs)
    if rs.Entries[0].RunID != "100-abc" {
        t.Errorf("RunID overwritten: %q", rs.Entries[0].RunID)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./lib/registry/ -run TestSanitizeAll_Legacy -v`
Expected: FAIL — `RunID` stays `""`, no report emitted.

- [ ] **Step 3: Implement legacy migration in SanitizeAll**

In `lib/registry/sanitize.go`, inside the `for _, e := range rs.Entries` loop (after the validity checks and before `keep = append(keep, e)`), insert:

```go
        // Legacy migration: pre-spec entries lack RunID/Blocking. Synthesize a
        // <pid>-legacy run_id and assume blocking=true (start-sensor was the
        // only producer before this spec). LogDir is preserved as-is; the
        // *Run path helpers won't apply — read-only consumers fall back to
        // LegacyRawLog / LegacySignalsLog when RunID has the "-legacy" suffix.
        if e.RunID == "" {
            legacyRunID := fmt.Sprintf("%d-legacy", e.PID)
            reports = append(reports, SanitizeReport{
                SensorID: e.SensorID, Field: "run_id", OldValue: 0, Dropped: false,
            })
            e.RunID = legacyRunID
            e.Blocking = true
        }
```

- [ ] **Step 4: Run all registry tests**

Run: `go test ./lib/registry/ -v`
Expected: PASS, all new tests plus existing.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/sanitize.go lib/registry/sanitize_test.go
git commit -m "feat(registry): migrate legacy entries to run_id=<pid>-legacy + blocking=true on Load"
```

---

## Task 5: Tighten `run_id` description in `schemas/signal.json`

**Files:**
- Modify: `schemas/signal.json`

- [ ] **Step 1: Read the current description**

Read: `schemas/signal.json` lines around `"run_id"`.
Expected: `"description": "ULID/UUID identifying this single invocation."`

- [ ] **Step 2: Edit the description**

In `schemas/signal.json`, change the `run_id` property's `description` to:

```json
"description": "Unique identifier of this invocation; runners may use a `<pid>-<short-uuid8>` composite or a plain UUID/ULID."
```

- [ ] **Step 3: Validate schemas load cleanly**

Run: `go test ./lib/schema/ -v`
Expected: PASS — description is a free-form string; no validation regression.

Run: `go test ./... 2>&1 | tail -20`
Expected: All passing tests still pass.

- [ ] **Step 4: Commit**

```bash
git add schemas/signal.json
git commit -m "docs(schema): allow composite <pid>-<short-uuid8> run_id form"
```

---

## Task 6: Add `WithRunDir` fixture to `lib/testfixtures/`

**Files:**
- Modify: `lib/testfixtures/paths.go`

- [ ] **Step 1: Inspect existing fixture conventions**

Read: `lib/testfixtures/paths.go` to see how `Root` fixtures are constructed today.

- [ ] **Step 2: Add the fixture helper**

Append to `lib/testfixtures/paths.go`:

```go
// WithRunDir materializes a temp registry Root, a populated <run-id>/
// directory with empty raw.log and signals.log files. Returns the Root,
// the synthesized run_id (<pid>-<short>), and a cleanup function. Tests
// in lib/subprocess, lib/orchestrator, and skill scripts use this to
// avoid duplicating mkdir/touch boilerplate.
//
// runIDSeed lets the caller pin a deterministic value; pass "" to get
// an os.Getpid()-based composite.
func WithRunDir(t testing.TB, sensorID, runIDSeed string) (root registry.Root, runID, runDir string) {
    t.Helper()
    proj := t.TempDir()
    root = registry.NewRoot(proj)
    if runIDSeed == "" {
        runIDSeed = fmt.Sprintf("%d-test0001", os.Getpid())
    }
    runID = runIDSeed
    runDir = root.RunDir(sensorID, runID)
    if err := os.MkdirAll(runDir, 0o755); err != nil {
        t.Fatalf("mkdir runDir: %v", err)
    }
    for _, fname := range []string{"raw.log", "signals.log"} {
        f, err := os.Create(filepath.Join(runDir, fname))
        if err != nil {
            t.Fatalf("create %s: %v", fname, err)
        }
        _ = f.Close()
    }
    return root, runID, runDir
}
```

Add the necessary imports at the top of `paths.go`: `"fmt"`, `"os"`, `"path/filepath"`, `"testing"`, `"github.com/iurykrieger/harness-framework/lib/registry"`.

- [ ] **Step 3: Verify compile + existing tests still pass**

Run: `go test ./lib/testfixtures/ ./lib/registry/ -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add lib/testfixtures/paths.go
git commit -m "test(fixtures): add WithRunDir helper for per-run persistence tests"
```

---

## Task 7: `StreamSubprocess` tees raw output to `raw.log` when `RunDir` is set

**Files:**
- Modify: `lib/subprocess/stream.go`
- Modify: `lib/subprocess/stream_test.go`

- [ ] **Step 1: Write the failing test**

Append to `lib/subprocess/stream_test.go`:

```go
func TestStreamSubprocess_TeesRawLogWhenRunDirSet(t *testing.T) {
    _, runID, runDir := testfixtures.WithRunDir(t, "alpha", "")
    _ = runID

    var stdout, stderr bytes.Buffer
    cfg := subprocess.StreamConfig{
        Command:  `printf 'line one\nline two\n'`,
        Envelope: sensor.Envelope{SensorID: "alpha", Version: "0.0.0", RunID: runID, StartedAt: "2026-05-11T00:00:00Z"},
        Stdout:   &stdout,
        Stderr:   &stderr,
        RunDir:   runDir,
    }
    res, err := subprocess.StreamSubprocess(context.Background(), cfg)
    if err != nil {
        t.Fatalf("stream: %v", err)
    }
    if res.ExitCode != 0 {
        t.Fatalf("exit=%d", res.ExitCode)
    }

    raw, err := os.ReadFile(filepath.Join(runDir, "raw.log"))
    if err != nil {
        t.Fatalf("read raw.log: %v", err)
    }
    if want := "line one\nline two\n"; string(raw) != want {
        t.Errorf("raw.log = %q, want %q", string(raw), want)
    }
}

func TestStreamSubprocess_NoTeeWhenRunDirEmpty(t *testing.T) {
    var stdout, stderr bytes.Buffer
    cfg := subprocess.StreamConfig{
        Command:  `echo hello`,
        Envelope: sensor.Envelope{SensorID: "alpha", Version: "0.0.0", RunID: "1-x", StartedAt: "2026-05-11T00:00:00Z"},
        Stdout:   &stdout,
        Stderr:   &stderr,
        // RunDir intentionally empty — legacy behavior
    }
    if _, err := subprocess.StreamSubprocess(context.Background(), cfg); err != nil {
        t.Fatalf("stream: %v", err)
    }
    // Nothing to assert about disk; the absence of a panic / RunDir-related error is the check.
}
```

Add the import `"github.com/iurykrieger/harness-framework/lib/testfixtures"` at the top.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./lib/subprocess/ -run TestStreamSubprocess_TeesRaw -v`
Expected: FAIL with "unknown field RunDir in struct literal".

- [ ] **Step 3: Add `RunDir` field + tee logic**

In `lib/subprocess/stream.go`:

3a. Add field to `StreamConfig`:

```go
type StreamConfig struct {
    Command   string
    Env       map[string]string
    TimeoutMS int
    Patterns  []signal.Pattern
    Envelope  sensor.Envelope
    Validator *schema.Validator
    Stdout    io.Writer
    Stderr    io.Writer

    // RunDir, when non-empty, points at .runtime/sensors/<id>/<run-id>/.
    // The streamer tees subprocess stdout+stderr verbatim into <RunDir>/raw.log
    // and appends individual + aggregate Signals to <RunDir>/signals.log.
    // Empty preserves the legacy stdout-only behavior.
    RunDir string
}
```

3b. Inside `StreamSubprocess`, after `cmd.Start()` succeeds and before the scanner goroutines start, open `raw.log` (append mode) when `cfg.RunDir != ""`:

```go
    var rawLogF *os.File
    if cfg.RunDir != "" {
        f, ferr := os.OpenFile(
            filepath.Join(cfg.RunDir, "raw.log"),
            os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644,
        )
        if ferr != nil {
            // Fail loud — caller asked for persistence and we cannot deliver.
            return res, fmt.Errorf("open raw.log: %w", ferr)
        }
        rawLogF = f
        defer rawLogF.Close()
    }
```

3c. In the `scan` closure, after every read line (before the pattern match), tee to `rawLogF` when set:

```go
    scan := func(r io.Reader, captureStderr bool) {
        defer wg.Done()
        sc := bufio.NewScanner(r)
        sc.Buffer(make([]byte, 64*1024), 1024*1024)
        for sc.Scan() {
            line := sc.Text()
            if rawLogF != nil {
                _, _ = rawLogF.WriteString(line + "\n")
            }
            // ... existing stderr capture + pattern match unchanged
```

Add `"path/filepath"` to the imports.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./lib/subprocess/ -v`
Expected: PASS, both new tests + existing pass.

- [ ] **Step 5: Commit**

```bash
git add lib/subprocess/stream.go lib/subprocess/stream_test.go
git commit -m "feat(subprocess): tee raw subprocess output to <RunDir>/raw.log when set"
```

---

## Task 8: `StreamSubprocess` writes individual Signals to `signals.log`

**Files:**
- Modify: `lib/subprocess/stream.go`
- Modify: `lib/subprocess/stream_test.go`

- [ ] **Step 1: Write the failing test**

Append to `lib/subprocess/stream_test.go`:

```go
func TestStreamSubprocess_WritesIndividualsToSignalsLog(t *testing.T) {
    _, runID, runDir := testfixtures.WithRunDir(t, "alpha", "")

    patterns, err := signal.CompilePatterns([]interface{}{
        map[string]interface{}{"regex": `^FAIL: (.+)$`, "verdict": "fail", "severity": "high"},
    })
    if err != nil {
        t.Fatal(err)
    }

    var stdout, stderr bytes.Buffer
    cfg := subprocess.StreamConfig{
        Command:  `printf 'FAIL: boom\nFAIL: bang\n'`,
        Envelope: sensor.Envelope{SensorID: "alpha", Version: "0.0.0", RunID: runID, StartedAt: "2026-05-11T00:00:00Z"},
        Patterns: patterns,
        Stdout:   &stdout,
        Stderr:   &stderr,
        RunDir:   runDir,
    }
    res, err := subprocess.StreamSubprocess(context.Background(), cfg)
    if err != nil {
        t.Fatalf("stream: %v", err)
    }
    if len(res.Individuals) != 2 {
        t.Fatalf("individuals=%d", len(res.Individuals))
    }

    data, err := os.ReadFile(filepath.Join(runDir, "signals.log"))
    if err != nil {
        t.Fatal(err)
    }
    lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
    if len(lines) != 2 {
        t.Fatalf("signals.log lines=%d (%q)", len(lines), string(data))
    }
    // Cross-check: stdout has the same JSONL lines
    stdoutLines := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
    if len(stdoutLines) != 2 {
        t.Fatalf("stdout lines=%d", len(stdoutLines))
    }
    for i := 0; i < 2; i++ {
        if lines[i] != stdoutLines[i] {
            t.Errorf("line %d: signals.log=%q stdout=%q", i, lines[i], stdoutLines[i])
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/subprocess/ -run TestStreamSubprocess_WritesIndividuals -v`
Expected: FAIL — signals.log empty or missing.

- [ ] **Step 3: Open `signals.log` and write each individual after stdout emit**

In `lib/subprocess/stream.go`, after the `rawLogF` open block, add a similar block for `signalsLogF`:

```go
    var signalsLogF *os.File
    if cfg.RunDir != "" {
        f, ferr := os.OpenFile(
            filepath.Join(cfg.RunDir, "signals.log"),
            os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644,
        )
        if ferr != nil {
            return res, fmt.Errorf("open signals.log: %w", ferr)
        }
        signalsLogF = f
        defer signalsLogF.Close()
    }
```

In the `for e := range emits` loop, after validating the signal and encoding it to stdout, also encode to `signalsLogF` when set:

```go
        res.Individuals = append(res.Individuals, e.sig)
        _ = json.NewEncoder(cfg.Stdout).Encode(e.sig)
        if signalsLogF != nil {
            _ = json.NewEncoder(signalsLogF).Encode(e.sig)
        }
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./lib/subprocess/ -v`
Expected: PASS, both individual-write tests + existing.

- [ ] **Step 5: Commit**

```bash
git add lib/subprocess/stream.go lib/subprocess/stream_test.go
git commit -m "feat(subprocess): write individual Signals to <RunDir>/signals.log"
```

---

## Task 9: `RunOne` accepts optional `*registry.Root` and persists when set

**Files:**
- Modify: `lib/orchestrator/lifecycle.go`
- Modify: `lib/orchestrator/lifecycle_test.go`
- Modify: `lib/orchestrator/run.go` (propagate Root)
- Modify: `lib/orchestrator/cascade.go` and other callers if they call `RunOne` directly

- [ ] **Step 1: Inventory current callers of `RunOne`**

Run: `grep -rn 'RunOne' lib/orchestrator/ skills/`
Note: every caller must pass `nil` for the new parameter to preserve current behavior. Document all callers in the commit body.

- [ ] **Step 2: Write the failing test**

Append to `lib/orchestrator/lifecycle_test.go`:

```go
func TestRunOne_WithRoot_CreatesAndRemovesEntry(t *testing.T) {
    proj := t.TempDir()
    if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
        t.Fatal(err)
    }
    sensorPath := filepath.Join(proj, "sensors", "echo.json")
    if err := os.WriteFile(sensorPath, []byte(`{
      "id": "echo", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "echo hi", "exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644); err != nil {
        t.Fatal(err)
    }
    root := registry.NewRoot(proj)
    s, err := loadSensorForTest(sensorPath)
    if err != nil {
        t.Fatal(err)
    }
    var stdout, stderr bytes.Buffer
    sig, code := orchestrator.RunOneWithRoot(context.Background(), s, "", nil, &root, &stdout, &stderr)
    if code != 0 {
        t.Fatalf("exit=%d, stderr=%s", code, stderr.String())
    }
    if sig["verdict"] != "pass" {
        t.Errorf("verdict=%v", sig["verdict"])
    }

    rs, _ := registry.Load(root)
    if len(rs.Entries) != 0 {
        t.Errorf("entry not removed: %+v", rs.Entries)
    }
    // The <run-id>/ directory must exist and contain signals.log with the aggregate.
    runID, _ := sig["run_id"].(string)
    sigsPath := root.SignalsLogRun("echo", runID)
    if _, err := os.Stat(sigsPath); err != nil {
        t.Fatalf("signals.log missing at %s: %v", sigsPath, err)
    }
}

// loadSensorForTest is a tiny helper to load a Sensor struct as RunOne expects.
func loadSensorForTest(path string) (orchestrator.Sensor, error) {
    b, err := os.ReadFile(path)
    if err != nil {
        return orchestrator.Sensor{}, err
    }
    var j map[string]interface{}
    if err := json.Unmarshal(b, &j); err != nil {
        return orchestrator.Sensor{}, err
    }
    return orchestrator.Sensor{ID: "echo", Path: path, JSON: j}, nil
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./lib/orchestrator/ -run TestRunOne_WithRoot -v`
Expected: FAIL — `RunOneWithRoot` undefined.

- [ ] **Step 4: Implement `RunOneWithRoot`**

In `lib/orchestrator/lifecycle.go`, alongside `RunOne`, add a wrapper that accepts a Root and a new envelope helper. Keep `RunOne` signature unchanged (delegates to `RunOneWithRoot(..., nil, ...)`):

```go
// RunOneWithRoot is RunOne plus runtime persistence. When root is non-nil:
//   - After cmd.Start() returns, compute run_id = <pid>-<short-uuid8>
//   - mkdir <run-id>/, open raw.log and signals.log
//   - Insert a RunningSensorEntry with blocking=<sensor.execution.blocking>
//   - defer remove the entry on any exit path
// When root is nil, behavior is identical to RunOne.
func RunOneWithRoot(
    ctx context.Context, s Sensor, schemasDir string, v *schema.Validator,
    root *registry.Root, stdout, stderr io.Writer,
) (map[string]interface{}, int) {
    if root == nil {
        return RunOne(ctx, s, schemasDir, v, stdout, stderr)
    }
    return runOneWithPersistence(ctx, s, schemasDir, v, *root, stdout, stderr)
}
```

Then implement `runOneWithPersistence`. It mirrors `RunOne` but, instead of calling `subprocess.StreamSubprocess` directly with a vanilla `StreamConfig`, it:
1. Pre-computes a partial `run_id` (placeholder) and an envelope so that the streamer's individuals carry the final run_id.
2. Wraps the streamer call in a registry transaction:
   a. Spawns the subprocess (via a new helper that exposes the PID before draining stdout).
   b. Once the PID is known, finalizes `run_id = fmt.Sprintf("%d-%s", pid, uuid.NewString()[:8])`.
   c. Creates `RunDir` and writes the registry entry under `WithFileLock`.
   d. Installs `defer` to remove the entry by `run_id`.
3. Writes the aggregate Signal to `signals.log` (last line) in addition to stdout — by appending to the same file the streamer was using.

**Approach to keep this manageable:** rather than re-implementing the streaming pipeline inside RunOne, introduce a new top-level `subprocess.StreamSubprocessWithCallback(...)` that calls back to the caller with the PID right after `cmd.Start()`. The callback returns the final RunDir; the streamer opens raw.log/signals.log lazily inside the callback's return value.

For implementation clarity, prefer the simpler alternative: split the streamer into `Start` and `Run` phases. `Start(ctx, cfg) (StreamHandle, error)` returns a handle with `.PID` and methods `.SetRunDir(string)` then `.Run() StreamResult`. The handle's `SetRunDir` must be called before `Run`.

Implementation goes here (full Go listing). After implementation, also export the aggregate-writing step: after `subprocess` returns, the orchestrator opens `signals.log` in append mode and writes the encoded aggregate Signal as a final JSONL line.

Add to imports: `"github.com/google/uuid"`, `"github.com/iurykrieger/harness-framework/lib/registry"`.

The full implementation:

```go
func runOneWithPersistence(
    ctx context.Context, s Sensor, schemasDir string, v *schema.Validator,
    root registry.Root, stdout, stderr io.Writer,
) (map[string]interface{}, int) {
    envelope, err := sensor.BuildEnvelope(s.JSON)
    if err != nil {
        fmt.Fprintln(stderr, "error: envelope:", err)
        return nil, 2
    }
    execMap, _ := s.JSON["execution"].(map[string]interface{})
    output, _ := s.JSON["output"].(string)
    blocking, _ := execMap["blocking"].(bool)

    if missing := sensor.CheckRequiredEnv(s.JSON); len(missing) > 0 {
        sig := sensor.BuildMissingEnvSignal(envelope, output, missing)
        if v != nil {
            if err := v.Validate(schema.TargetSignal, sig); err != nil {
                schema.PrintValidationOrPlain(err, stderr)
                return nil, 1
            }
        }
        _ = json.NewEncoder(stdout).Encode(sig)
        return sig, 0
    }

    timeoutMS := readTimeoutMS(s.JSON)

    prepResults, prepFailed := runPreparePhase(ctx, s.JSON, timeoutMS)

    var aggregateMD map[string]interface{}
    var aggVerdict, aggSeverity, commandRun string
    var elapsedMS int
    var runID, runDir string
    var entryInserted bool

    if !prepFailed {
        command, _ := execMap["command"].(string)
        envExtra := readEnvMap(execMap)
        var patterns []signal.Pattern
        if op, ok := execMap["output_parsing"].(map[string]interface{}); ok {
            raw, _ := op["patterns"].([]interface{})
            ps, perr := signal.CompilePatterns(raw)
            if perr != nil {
                fmt.Fprintln(stderr, "error:", perr)
                return nil, 1
            }
            patterns = ps
        }

        // Spawn via StreamHandle to expose PID before draining.
        handle, herr := subprocess.Start(ctx, subprocess.StreamConfig{
            Command:   command,
            Env:       envExtra,
            TimeoutMS: timeoutMS,
            Patterns:  patterns,
            Envelope:  envelope,
            Validator: v,
            Stdout:    stdout,
            Stderr:    stderr,
        })
        if herr != nil {
            fmt.Fprintln(stderr, "error: spawn:", herr)
            return nil, 1
        }

        runID = fmt.Sprintf("%d-%s", handle.PID, uuid.NewString()[:8])
        runDir = root.RunDir(envelope.SensorID, runID)
        if err := os.MkdirAll(runDir, 0o755); err != nil {
            _ = handle.Kill()
            fmt.Fprintln(stderr, "error: mkdir runDir:", err)
            return nil, 1
        }
        envelope.RunID = runID
        handle.SetRunDir(runDir)
        handle.SetEnvelope(envelope)

        if err := registry.WithFileLock(root.LockFile(), func() error {
            rs, err := registry.Load(root)
            if err != nil {
                return err
            }
            if rs.FindEntryByRunID(runID) != nil {
                return fmt.Errorf("run_id %q already exists", runID)
            }
            rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
                SensorID: envelope.SensorID,
                RunID:    runID,
                Blocking: blocking,
                PID:      handle.PID,
                PGID:     handle.PGID,
                StartedAt: envelope.StartedAt,
                Command:   command,
                LogDir:    filepath.Join(".runtime", "sensors", envelope.SensorID, runID),
                HeldBy:    []registry.HeldByEntry{},
            })
            return registry.Save(root, rs)
        }); err != nil {
            _ = handle.Kill()
            _ = os.RemoveAll(runDir)
            fmt.Fprintln(stderr, "error: registry insert:", err)
            return nil, 1
        }
        entryInserted = true
        defer func() {
            if entryInserted {
                _ = registry.WithFileLock(root.LockFile(), func() error {
                    rs, err := registry.Load(root)
                    if err != nil {
                        return err
                    }
                    rs.RemoveEntryByRunID(runID)
                    return registry.Save(root, rs)
                })
            }
        }()

        res := handle.Run()

        ecMap, _ := execMap["exit_code_map"].([]interface{})
        exitVerd, exitSev := signal.MapExitCode(res.ExitCode, ecMap)
        streamVerd, streamSev := signal.MaxStreamVerdict(res.Individuals)
        agg := signal.Aggregate(signal.AggregateInput{
            ExitVerdict: exitVerd, ExitSeverity: exitSev,
            StreamVerdict: streamVerd, StreamSeverity: streamSev,
            TimedOut: res.TimedOut, Blocking: blocking,
        })
        aggVerdict, aggSeverity = agg.Verdict, agg.Severity
        commandRun = command
        elapsedMS = res.ElapsedMS

        aggregateMD = map[string]interface{}{
            "kind":        "aggregate",
            "output_mode": output,
            "command":     command,
            "exit_code":   res.ExitCode,
            "timed_out":   res.TimedOut,
            "counts":      signal.CountVerdicts(res.Individuals),
        }
        if blocking {
            aggregateMD["blocking"] = true
        }
        if hint, ok := buildHealHint(output, aggVerdict, res.StderrExcerpt); ok {
            aggregateMD["heal_hint"] = hint
        }
    } else {
        aggVerdict, aggSeverity = "error", "high"
        commandRun, _ = execMap["command"].(string)
    }

    tdResults := runTeardownPhase(ctx, execMap, timeoutMS)

    if aggregateMD == nil {
        aggregateMD = map[string]interface{}{
            "kind": "aggregate", "output_mode": output, "command": commandRun,
            "exit_code": nil, "timed_out": false,
            "counts": map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 1},
        }
    }
    if len(prepResults) > 0 || len(tdResults) > 0 {
        lc := map[string]interface{}{}
        if len(prepResults) > 0 {
            lc["prepare"] = prepResults
        }
        if len(tdResults) > 0 {
            lc["teardown"] = tdResults
        }
        aggregateMD["lifecycle"] = lc
    }

    finished := sensor.NowFn().Format("2006-01-02T15:04:05Z")
    sig := map[string]interface{}{
        "sensor_id": envelope.SensorID, "version": envelope.Version,
        "run_id": envelope.RunID, "started_at": envelope.StartedAt,
        "finished_at": finished, "verdict": aggVerdict, "severity": aggSeverity,
        "confidence": 1.0, "evidence": buildLifecycleEvidence(prepResults, tdResults),
        "cost_actual": map[string]interface{}{"latency_ms": elapsedMS},
        "metadata": aggregateMD,
    }

    if v != nil {
        if err := v.Validate(schema.TargetSignal, sig); err != nil {
            schema.PrintValidationOrPlain(err, stderr)
            return nil, 1
        }
    }
    _ = json.NewEncoder(stdout).Encode(sig)

    // Append aggregate to signals.log too. Errors here are non-fatal — stdout is authoritative.
    if runDir != "" {
        if f, ferr := os.OpenFile(filepath.Join(runDir, "signals.log"),
            os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
            _ = json.NewEncoder(f).Encode(sig)
            _ = f.Close()
        }
    }
    return sig, 0
}
```

Add a `subprocess.StreamHandle` with the split `Start` / `SetRunDir` / `SetEnvelope` / `Run` / `Kill` interface in `lib/subprocess/stream.go`. The existing `StreamSubprocess` becomes a tiny wrapper calling `Start` then `Run`. Keep test coverage for `StreamSubprocess` unchanged.

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./lib/orchestrator/ ./lib/subprocess/ -v`
Expected: PASS, including new tests; legacy tests preserved.

- [ ] **Step 6: Commit**

```bash
git add lib/orchestrator/lifecycle.go lib/orchestrator/lifecycle_test.go lib/subprocess/stream.go
git commit -m "feat(orchestrator): RunOneWithRoot persists <run-id>/ + registry entry"
```

---

## Task 10: Cascade-skip invariant test (no dir, no entry)

**Files:**
- Modify: `lib/orchestrator/run_test.go`

- [ ] **Step 1: Write the failing test**

Append to `lib/orchestrator/run_test.go`:

```go
func TestRunWithDepsRoot_CascadeSkip_DoesNotTouchRegistryOrDir(t *testing.T) {
    proj := t.TempDir()
    sensorsDir := filepath.Join(proj, "sensors")
    if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
        t.Fatal(err)
    }
    // Dep fails (exit 1).
    _ = os.WriteFile(filepath.Join(sensorsDir, "dep.json"), []byte(`{
      "id": "dep", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "exit 1", "exit_code_map": [{"code": 1, "verdict": "fail", "severity": "high"}]}
    }`), 0o644)
    _ = os.WriteFile(filepath.Join(sensorsDir, "target.json"), []byte(`{
      "id": "target", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "requires": [{"kind": "sensor", "id": "dep"}],
      "execution": {"command": "echo never-runs", "exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644)

    var stdout, stderr bytes.Buffer
    code := orchestrator.RunWithDepsRoot(context.Background(), "target", proj, "", &stdout, &stderr)
    if code == 0 {
        t.Fatalf("expected non-zero exit for cascade")
    }

    targetDir := filepath.Join(proj, ".runtime", "sensors", "target")
    if entries, _ := os.ReadDir(targetDir); len(entries) != 0 {
        t.Errorf("target run dir was created during cascade: %+v", entries)
    }

    rs, _ := registry.Load(registry.NewRoot(proj))
    for _, e := range rs.Entries {
        if e.SensorID == "target" {
            t.Errorf("target entry exists despite cascade: %+v", e)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/orchestrator/ -run TestRunWithDepsRoot_CascadeSkip -v`
Expected: depends on current `RunWithDepsRoot` signature — if it doesn't yet take a Root, the test wires up the helper. Once the implementation in Task 9 is correct, this test passes; if it doesn't, the cascade path is mistakenly creating dirs/entries.

- [ ] **Step 3: Update `RunWithDeps`/`RunWithDepsRoot` to thread the Root, only if the test fails**

In `lib/orchestrator/run.go`, the public function `RunWithDepsRoot(ctx, id, projectRoot, schemasDir, stdout, stderr)` now constructs a `registry.Root` from `projectRoot` and passes it down to `RunOne` only when the target actually runs (after `RunDeps` returns with no cascade). The cascade path (returning a `pre.CascadeSig`) must NOT create dirs or entries.

If `RunWithDepsRoot` already lives in `run.go`, augment it to:

```go
func RunWithDepsRoot(ctx context.Context, rootID, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
    v, code := schema.LoadValidator(schemasDir, stderr)
    if code != 0 {
        return code
    }
    holderPID := os.Getpid()
    pre := RunDeps(ctx, rootID, projectRoot, schemasDir, rootID, holderPID, v, stdout, stderr)

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
    root := registry.NewRoot(projectRoot)
    _, code = RunOneWithRoot(ctx, target, schemasDir, v, &root, stdout, stderr)
    return code
}
```

- [ ] **Step 4: Run all orchestrator tests**

Run: `go test ./lib/orchestrator/ -v`
Expected: PASS, including the cascade test.

- [ ] **Step 5: Commit**

```bash
git add lib/orchestrator/run.go lib/orchestrator/run_test.go
git commit -m "test(orchestrator): assert cascade-skipped target never touches registry or run dir"
```

---

## Task 11: SIGINT/SIGTERM handler in `/run-sensor` scripts; `terminated_externally` flag

**Files:**
- Modify: `skills/run-sensor/scripts/run-computational.go`
- Modify: `skills/run-sensor/scripts/run-computational_test.go`
- Modify: `skills/run-sensor/scripts/run-inferential.go`
- Modify: `skills/run-sensor/scripts/run-inferential_test.go`

- [ ] **Step 1: Write the failing test (computational)**

Append to `run-computational_test.go`:

```go
//go:build run_computational

func TestRunComputational_SIGTERMSetsTerminatedExternally(t *testing.T) {
    proj := t.TempDir()
    if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
        t.Fatal(err)
    }
    _ = os.WriteFile(filepath.Join(proj, "sensors", "sleeper.json"), []byte(`{
      "id": "sleeper", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "sleep 30", "exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644)

    cmd := exec.Command("go", "run", "-tags=run_computational", "./skills/run-sensor/scripts", "sleeper")
    cmd.Dir = repoRootForTest(t) // helper that returns repo root
    cmd.Env = append(os.Environ(), "HARNESS_REGISTRY_ROOT="+proj)
    stdout, _ := cmd.StdoutPipe()
    if err := cmd.Start(); err != nil {
        t.Fatal(err)
    }
    // Give the subprocess a chance to spawn and register.
    time.Sleep(200 * time.Millisecond)
    _ = cmd.Process.Signal(syscall.SIGTERM)

    out, _ := io.ReadAll(stdout)
    _ = cmd.Wait()

    lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
    if len(lines) == 0 {
        t.Fatalf("no stdout")
    }
    last := lines[len(lines)-1]
    var sig map[string]interface{}
    if err := json.Unmarshal([]byte(last), &sig); err != nil {
        t.Fatalf("parse last line: %v\n%q", err, last)
    }
    md, _ := sig["metadata"].(map[string]interface{})
    if v, _ := md["terminated_externally"].(bool); !v {
        t.Errorf("expected metadata.terminated_externally=true; got %v", md)
    }
}
```

Add `repoRootForTest` (a tiny helper that returns the absolute path of the repo root by walking up from `os.Getwd()` until `go.mod` is found). Place it in a `helpers_test.go` colocated with the runner tests.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=run_computational ./skills/run-sensor/scripts/ -run TestRunComputational_SIGTERM -v`
Expected: FAIL — no signal handler today; subprocess is killed but no `terminated_externally` flag emitted.

- [ ] **Step 3: Add signal handler in `run-computational.go`**

Modify `run-computational.go` so `run(...)` installs a signal handler before invoking the orchestrator:

```go
func run(args []string, projectRoot string, stdout, stderr io.Writer) int {
    fs := flag.NewFlagSet("run-computational", flag.ContinueOnError)
    fs.SetOutput(stderr)
    var schemasDir string
    fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
    if err := fs.Parse(args); err != nil {
        return 2
    }
    rest := fs.Args()
    if len(rest) != 1 {
        fmt.Fprintln(stderr, "usage: run-computational [--schemas-dir=DIR] <sensor-id>")
        return 2
    }

    ctx, cancel := signalCancellableContext()
    defer cancel()

    id := rest[0]
    // ... existing sensor-load + blocking-reject logic stays the same ...

    return orchestrator.RunWithDepsRoot(ctx, id, projectRoot, schemasDir, stdout, stderr)
}

// signalCancellableContext returns a ctx that's cancelled on SIGINT/SIGTERM.
// The orchestrator's RunOneWithRoot will see ctx.Err() and set
// metadata.terminated_externally on the aggregate Signal.
func signalCancellableContext() (context.Context, context.CancelFunc) {
    ctx, cancel := context.WithCancel(context.Background())
    ch := make(chan os.Signal, 1)
    signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
    go func() {
        <-ch
        cancel()
    }()
    return ctx, cancel
}
```

Then in `lib/orchestrator/lifecycle.go::runOneWithPersistence`, after `handle.Run()`, check `ctx.Err()`:

```go
        if ctx.Err() != nil {
            aggregateMD["terminated_externally"] = true
        }
```

Add `"os/signal"` and `"syscall"` to imports in `run-computational.go`.

- [ ] **Step 4: Repeat for `run-inferential.go`**

Apply the same signal-handling pattern in `run-inferential.go` and add a mirror test.

- [ ] **Step 5: Run all run-sensor tests**

Run: `go test -tags=run_computational ./skills/run-sensor/scripts/ -v` and `go test -tags=run_inferential ./skills/run-sensor/scripts/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add skills/run-sensor/scripts/
git commit -m "feat(run-sensor): SIGINT/SIGTERM cancels run + sets terminated_externally"
```

---

## Task 12: `/start-sensor` adopts `<run-id>/` layout and composite run_id

**Files:**
- Modify: `skills/start-sensor/scripts/start.go`
- Modify: `skills/start-sensor/scripts/start_test.go`

- [ ] **Step 1: Write the failing test**

Append to `skills/start-sensor/scripts/start_test.go`:

```go
//go:build start_sensor

func TestStart_WritesNewRunIDLayout(t *testing.T) {
    proj := t.TempDir()
    if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
        t.Fatal(err)
    }
    _ = os.WriteFile(filepath.Join(proj, "sensors", "longrun.json"), []byte(`{
      "id": "longrun", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "sleep 10", "blocking": true, "graceful_timeout_ms": 1000,
        "exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644)

    // Pretend /start-sensor was invoked from inside the project.
    t.Setenv("HARNESS_REGISTRY_ROOT", proj)

    res := registry.Result{ProjectRoot: proj, Source: "env", Path: filepath.Join(proj, ".runtime", "sensors")}
    exit, sig := runStart(res, []string{"longrun"})
    if exit != 0 {
        t.Fatalf("exit=%d sig=%+v", exit, sig)
    }
    runID, _ := sig["metadata"].(map[string]interface{})["run_id"].(string)
    if !regexp.MustCompile(`^\d+-[0-9a-f]{8}$`).MatchString(runID) {
        t.Fatalf("run_id %q does not match <pid>-<short-uuid> shape", runID)
    }

    raw := filepath.Join(proj, ".runtime", "sensors", "longrun", runID, "raw.log")
    if _, err := os.Stat(raw); err != nil {
        t.Fatalf("raw.log not at new path %s: %v", raw, err)
    }

    // Cleanup: kill the started subprocess.
    rs, _ := registry.Load(registry.NewRoot(proj))
    for _, e := range rs.Entries {
        _ = syscall.Kill(-e.PGID, syscall.SIGKILL)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/ -run TestStart_WritesNewRunIDLayout -v`
Expected: FAIL — current `start.go` writes to flat path.

- [ ] **Step 3: Update `start.go`**

Modify the relevant section of `start.go` (around lines 142–170 where `logDir`, `RawLog`, `SignalsLog` are used):

```go
    // Replace these existing lines:
    //   r := registry.NewRoot(projectRoot)
    //   logDir := r.SensorDir(id)
    //   if err := os.MkdirAll(logDir, 0o755); err != nil { ... }
    //   if err := os.WriteFile(r.RawLog(id), nil, 0o644); err != nil { ... }
    //   if err := os.WriteFile(r.SignalsLog(id), nil, 0o644); err != nil { ... }
    //
    // With:
    r := registry.NewRoot(projectRoot)

    // Spawn first to get PID, then compute run_id, then mkdir <run-id>/.
    det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
        Command: command,
        // Temporarily write to /dev/null until we mkdir; we'll redirect after.
        // Better: postpone SpawnDetached until after mkdir AND a temp path.
        // See lifecycle ordering note below.
    })
    if err != nil { ... }

    runID := fmt.Sprintf("%d-%s", det.PID, uuid.NewString()[:8])
    logDir := r.RunDir(id, runID)
    if err := os.MkdirAll(logDir, 0o755); err != nil { ... }

    rawPath := r.RawLogRun(id, runID)
    sigsPath := r.SignalsLogRun(id, runID)
    if err := os.WriteFile(rawPath, nil, 0o644); err != nil { ... }
    if err := os.WriteFile(sigsPath, nil, 0o644); err != nil { ... }
    // ... existing watcher spawn + registry write, but using rawPath/sigsPath.
```

**Lifecycle ordering note:** `subprocess.SpawnDetached` today opens the log file path *before* spawning so the subprocess can inherit it as stdout/stderr. We need the PID *before* knowing the dir path. Two options:

1. Spawn into a temp file (`os.CreateTemp(SensorsDir(), "raw.log-*")`) then `os.Rename` it into the final `<run-id>/raw.log` once `runID` is known.
2. Change `SpawnDetached` to first `pipe()`, then `fork()`, then `dup2()` after assigning to the final path.

Pick (1): simpler, no platform-specific code. Add a `DetachConfig.TempLogFile` flag, return `(DetachResult{PID, PGID, TempLogPath})`, and the caller `os.Rename`s `TempLogPath` to `rawPath`.

Wire the rename inside the file lock so the registry insert + the rename happen atomically together.

Update the entry insert to set `RunID: runID`, `Blocking: true`, and `LogDir: filepath.Join(".runtime", "sensors", id, runID)`.

The watcher spawn also passes the new env var `HARNESS_WATCHER_RUN_ID=<runID>` (consumed in Task 13).

- [ ] **Step 4: Run all start-sensor tests**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/ -v`
Expected: PASS, including the new test.

- [ ] **Step 5: Commit**

```bash
git add skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_test.go lib/subprocess/detach.go
git commit -m "feat(start-sensor): adopt <run-id>/ layout + composite run_id"
```

---

## Task 13: Watcher reads `HARNESS_WATCHER_RUN_ID`, uses `FindEntryByRunID`

**Files:**
- Modify: `skills/start-sensor/scripts/watcher.go`
- Modify: `skills/start-sensor/scripts/watcher_test.go`

- [ ] **Step 1: Write the failing test**

Append to `watcher_test.go`: a test that pre-seeds a registry with two entries (`alpha/1-aa`, `alpha/2-bb`), runs the watcher in a goroutine with `HARNESS_WATCHER_RUN_ID=2-bb`, simulates a subprocess exit, and asserts that `SubprocessExit` is set on the `2-bb` entry, not `1-aa`.

```go
//go:build start_watcher

func TestWatcher_ResolvesEntryByRunID(t *testing.T) {
    proj := t.TempDir()
    r := registry.NewRoot(proj)
    if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
        t.Fatal(err)
    }
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "1-aa", Blocking: true, PID: 1111, PGID: 1111, WatcherPID: 1112, StartedAt: "2026-05-11T00:00:00Z", LogDir: r.RunDir("alpha", "1-aa")},
        {SensorID: "alpha", RunID: "2-bb", Blocking: true, PID: 2222, PGID: 2222, WatcherPID: 2223, StartedAt: "2026-05-11T00:00:00Z", LogDir: r.RunDir("alpha", "2-bb")},
    }}
    if err := registry.Save(r, rs); err != nil {
        t.Fatal(err)
    }

    cfg := watcherConfig{
        RawLog:        filepath.Join(t.TempDir(), "raw.log"),
        SignalsLog:    filepath.Join(t.TempDir(), "signals.log"),
        PatternsJSON:  "[]",
        EnvelopeJSON:  `{"sensor_id":"alpha","version":"0.0.0","run_id":"2-bb","started_at":"2026-05-11T00:00:00Z"}`,
        SubprocessPID: 2222,
        RegistryRoot:  proj,
        SensorID:      "alpha",
        RunID:         "2-bb",
    }
    // Create empty log files so fsnotify can open them.
    _ = os.WriteFile(cfg.RawLog, nil, 0o644)
    _ = os.WriteFile(cfg.SignalsLog, nil, 0o644)

    stop := make(chan struct{})
    close(stop) // exit immediately after the reaper runs

    // Mark the subprocess as already-dead so the reaper writes SubprocessExit.
    markSubprocessExitForTest(t, r, "2-bb", &registry.SubprocessExit{Code: 0, ExitedAt: "2026-05-11T00:00:01Z"})

    got, _ := registry.Load(r)
    e := got.FindEntryByRunID("2-bb")
    if e == nil || e.SubprocessExit == nil {
        t.Fatalf("expected SubprocessExit on 2-bb; got %+v", e)
    }
    other := got.FindEntryByRunID("1-aa")
    if other != nil && other.SubprocessExit != nil {
        t.Errorf("SubprocessExit leaked to 1-aa: %+v", other.SubprocessExit)
    }
}
```

The test exercises the resolver path. `markSubprocessExitForTest` is a small helper that calls the same code path the reaper uses (factored out as `recordSubprocessExit(r, runID, exit)` for testability — see Step 3).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=start_watcher ./skills/start-sensor/scripts/ -run TestWatcher_ResolvesEntryByRunID -v`
Expected: FAIL — `RunID` field not in `watcherConfig`, helper not exported.

- [ ] **Step 3: Update watcher**

In `watcher.go`:

3a. Add `RunID string` to `watcherConfig`. Read from env:

```go
    cfg := watcherConfig{
        RawLog:       os.Getenv("HARNESS_WATCHER_RAW"),
        SignalsLog:   os.Getenv("HARNESS_WATCHER_SIGNALS"),
        PatternsJSON: os.Getenv("HARNESS_WATCHER_PATTERNS"),
        EnvelopeJSON: os.Getenv("HARNESS_WATCHER_ENVELOPE"),
        RegistryRoot: os.Getenv("HARNESS_WATCHER_REGISTRY_ROOT"),
        SensorID:     os.Getenv("HARNESS_WATCHER_SENSOR_ID"),
        RunID:        os.Getenv("HARNESS_WATCHER_RUN_ID"),
    }
```

3b. Where the reaper writes `SubprocessExit` (around `if e := rs.FindEntry(cfg.SensorID); e != nil { ... }`), switch to `FindEntryByRunID(cfg.RunID)`:

```go
    if e := rs.FindEntryByRunID(cfg.RunID); e != nil {
        e.SubprocessExit = &registry.SubprocessExit{...}
        // existing save
    }
```

3c. Factor the reaper's exit-recording into `recordSubprocessExit(r registry.Root, runID string, exit *registry.SubprocessExit) error` so tests can call it directly.

- [ ] **Step 4: Run all watcher tests**

Run: `go test -tags=start_watcher ./skills/start-sensor/scripts/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/start-sensor/scripts/watcher.go skills/start-sensor/scripts/watcher_test.go
git commit -m "feat(watcher): resolve entry by HARNESS_WATCHER_RUN_ID, not sensor_id"
```

---

## Task 14: Callsite rewrites in `lib/orchestrator/live_deps.go`

**Files:**
- Modify: `lib/orchestrator/live_deps.go`
- Modify: `lib/orchestrator/live_deps_test.go`

- [ ] **Step 1: Re-grep callsites and confirm spec rewrite table**

Run: `grep -n 'FindEntry\|RemoveEntry' lib/orchestrator/live_deps.go`
Expected lines: 57, 130, 137, 138, 178, 214, 251.

Rewrites per spec §"Callsite rewrites":
- Line 57 `rs.FindEntry(dep.ID)` → `rs.FindBlockingEntry(dep.ID)`
- Lines 130, 137, 138, 251 `FindEntry(depID)` → `FindBlockingEntry(depID)`
- Lines 178, 214 `RemoveEntry(dep.ID)` → `RemoveEntryByRunID(<run_id>)`. The `run_id` must be plumbed: store it on the `LiveDep` (or equivalent struct) when the dep was attached, so detach can use it.

- [ ] **Step 2: Update tests with expected behavior**

In `live_deps_test.go`, add (or modify) a test that exercises a non-blocking entry coexisting with a blocking entry of the same id. Verify that attaching to the dep finds only the blocking one, and detach removes only the blocking one.

- [ ] **Step 3: Apply the rewrites**

Edit `live_deps.go`:

- Replace `rs.FindEntry(...)` with `rs.FindBlockingEntry(...)` at every flagged line.
- Where `rs.RemoveEntry(dep.ID)` appears, capture the entry's `RunID` (already available from the `FindBlockingEntry` result above) and call `rs.RemoveEntryByRunID(runID)`.
- Where `LiveStack` tracks attached deps, augment the stored data to include `RunID` so cleanup paths have it.

- [ ] **Step 4: Run all orchestrator tests**

Run: `go test ./lib/orchestrator/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go
git commit -m "refactor(orchestrator): live_deps uses FindBlockingEntry + RemoveEntryByRunID"
```

---

## Task 15: Callsite rewrites in `start-sensor/scripts/start.go`

**Files:**
- Modify: `skills/start-sensor/scripts/start.go`
- Modify: `skills/start-sensor/scripts/start_test.go`

- [ ] **Step 1: Write the failing test (singleton filtered by blocking:true)**

Append to `start_test.go`:

```go
//go:build start_sensor

func TestStart_AllowsStartWhenOnlyNonBlockingEntryExists(t *testing.T) {
    proj := t.TempDir()
    if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
        t.Fatal(err)
    }
    _ = os.WriteFile(filepath.Join(proj, "sensors", "shared.json"), []byte(`{
      "id": "shared", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "sleep 5", "blocking": true, "graceful_timeout_ms": 500,
        "exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644)

    // Pre-seed a NON-blocking entry of the same sensor; it must not block /start-sensor.
    r := registry.NewRoot(proj)
    _ = os.MkdirAll(r.SensorsDir(), 0o755)
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{{
        SensorID: "shared", RunID: "999-runX", Blocking: false,
        PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z",
    }}}
    _ = registry.Save(r, rs)

    res := registry.Result{ProjectRoot: proj, Source: "env"}
    exit, sig := runStart(res, []string{"shared"})
    if exit != 0 {
        t.Fatalf("exit=%d, sig=%+v", exit, sig)
    }
    md, _ := sig["metadata"].(map[string]interface{})
    if md["kind"] != "started" {
        t.Errorf("expected started, got %v", md["kind"])
    }

    // cleanup
    after, _ := registry.Load(r)
    for _, e := range after.Entries {
        if e.Blocking {
            _ = syscall.Kill(-e.PGID, syscall.SIGKILL)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/ -run TestStart_AllowsStartWhenOnlyNonBlocking -v`
Expected: FAIL — current singleton check at `start.go:188` rejects because `FindEntry` matches any entry.

- [ ] **Step 3: Apply the rewrite**

In `start.go:188`, change:

```go
        if existing := rs.FindEntry(id); existing != nil && registry.IsPIDAlive(existing.PID) {
```

to:

```go
        if existing := rs.FindBlockingEntry(id); existing != nil && registry.IsPIDAlive(existing.PID) {
```

In `start.go:249`, where `rs.RemoveEntry(id)` is called before inserting a new entry (it's a stale-blocking-entry dedup), change to `rs.RemoveEntryByRunID(staleRunID)` where `staleRunID` comes from `FindBlockingEntry` (capture it just before the call). If no stale blocking entry exists, skip the removal entirely.

- [ ] **Step 4: Run all start-sensor tests**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_test.go
git commit -m "fix(start-sensor): singleton check filtered by blocking:true; surgical RemoveEntryByRunID"
```

---

## Task 16: `/list-sensors` shows multi-entries; surfaces `blocking` + `run_id`

**Files:**
- Modify: `skills/list-sensors/scripts/list.go`
- Modify: `skills/list-sensors/scripts/list_test.go`

- [ ] **Step 1: Write the failing test**

Append to `list_test.go`:

```go
func TestList_MultipleEntriesPerSensor(t *testing.T) {
    proj := t.TempDir()
    r := registry.NewRoot(proj)
    _ = os.MkdirAll(r.SensorsDir(), 0o755)
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "1-aa", Blocking: false, PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z"},
        {SensorID: "alpha", RunID: "2-bb", Blocking: true, PID: os.Getpid(), PGID: os.Getpid(), WatcherPID: 9999, StartedAt: "2026-05-11T00:00:00Z"},
    }}
    _ = registry.Save(r, rs)

    res := registry.Result{ProjectRoot: proj, Source: "env"}
    _, sig := runList(res)
    md, _ := sig["metadata"].(map[string]interface{})
    entries, _ := md["entries"].([]interface{})
    if len(entries) != 2 {
        t.Fatalf("entries=%d, want 2", len(entries))
    }
    for _, raw := range entries {
        e, _ := raw.(map[string]interface{})
        if _, ok := e["run_id"].(string); !ok {
            t.Errorf("entry missing run_id: %+v", e)
        }
        if _, ok := e["blocking"].(bool); !ok {
            t.Errorf("entry missing blocking: %+v", e)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./skills/list-sensors/scripts/ -run TestList_MultipleEntriesPerSensor -v`
Expected: FAIL — current `list.go` likely surfaces only one entry per id, or misses `blocking`/`run_id`.

- [ ] **Step 3: Update `list.go`**

Modify the entry-iteration loop in `list.go` to:
- Iterate all `rs.Entries` (no dedup by `sensor_id`).
- For each entry, populate the output map with `sensor_id`, `run_id`, `blocking`, `pid` (with `pid_alive`), and conditionally `watcher_pid`+`watcher_alive` only when `blocking` is true.
- Mark state `"orphan"` when `pid_alive` is false (regardless of `blocking`).

- [ ] **Step 4: Run all list-sensors tests**

Run: `go test ./skills/list-sensors/scripts/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/list-sensors/scripts/list.go skills/list-sensors/scripts/list_test.go
git commit -m "feat(list-sensors): surface multi-entries with blocking + run_id"
```

---

## Task 17: `/tail-sensor` accepts path-like args; emits ambiguity errors

**Files:**
- Modify: `skills/tail-sensor/scripts/tail.go`
- Modify: `skills/tail-sensor/scripts/tail_test.go`

- [ ] **Step 1: Write failing tests**

Append to `tail_test.go`:

```go
func TestTail_AmbiguousRunReturnsError(t *testing.T) {
    proj := t.TempDir()
    r := registry.NewRoot(proj)
    _ = os.MkdirAll(r.SensorsDir(), 0o755)
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "1-aa", Blocking: false, PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z"},
        {SensorID: "alpha", RunID: "2-bb", Blocking: false, PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z"},
    }}
    _ = registry.Save(r, rs)

    res := registry.Result{ProjectRoot: proj, Source: "env"}
    exit, sig := runTail(res, []string{"alpha", "0"})
    if exit == 0 {
        t.Fatal("expected non-zero exit on ambiguous_run")
    }
    md, _ := sig["metadata"].(map[string]interface{})
    if md["kind"] != "ambiguous_run" {
        t.Errorf("kind=%v, want ambiguous_run", md["kind"])
    }
}

func TestTail_PathLikeResolvesToSpecificRun(t *testing.T) {
    proj := t.TempDir()
    r := registry.NewRoot(proj)
    runID := "1-aa"
    _, _, runDir := testfixtures.WithRunDir(t, "alpha", runID)
    if err := os.WriteFile(filepath.Join(runDir, "signals.log"), []byte(`{"sensor_id":"alpha","run_id":"1-aa","verdict":"pass","severity":"info","confidence":1.0,"version":"0.0.0","started_at":"2026-05-11T00:00:00Z","finished_at":"2026-05-11T00:00:01Z","evidence":[],"cost_actual":{"latency_ms":1},"metadata":{"kind":"aggregate"}}` + "\n"), 0o644); err != nil {
        t.Fatal(err)
    }
    _ = os.MkdirAll(r.SensorsDir(), 0o755)
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: runID, Blocking: false, PID: os.Getpid(), PGID: os.Getpid(), StartedAt: "2026-05-11T00:00:00Z", LogDir: r.RunDir("alpha", runID)},
    }}
    _ = registry.Save(r, rs)

    res := registry.Result{ProjectRoot: proj, Source: "env"}
    exit, _ := runTail(res, []string{"alpha/" + runID, "0"})
    if exit != 0 {
        t.Fatalf("exit=%d", exit)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./skills/tail-sensor/scripts/ -run 'TestTail_(Ambig|PathLike)' -v`
Expected: FAIL — current `tail.go` doesn't parse `id/run-id` form.

- [ ] **Step 3: Update `tail.go`**

In `tail.go`:

3a. Parse the first positional arg as `sensorID[/runID]`:

```go
    arg := args[0]
    var sensorID, runID string
    if i := strings.IndexByte(arg, '/'); i >= 0 {
        sensorID, runID = arg[:i], arg[i+1:]
    } else {
        sensorID = arg
    }
```

3b. Resolve the entry. If `runID` is non-empty, call `rs.FindEntryByRunID(runID)` and verify its `SensorID` matches. If `runID` is empty, call `rs.FindEntries(sensorID)`:

- 0 active entries → emit `metadata.kind=no_active_run`, exit 1.
- 1 active entry → use it.
- N>1 active entries → emit `metadata.kind=ambiguous_run`, evidence lists all `run_id`s, exit 1.

3c. Use `r.SignalsLogRun(sensorID, entry.RunID)` for the new layout. When `entry.RunID` ends with `-legacy`, fall back to `r.LegacySignalsLog(sensorID)`.

- [ ] **Step 4: Run all tail-sensor tests**

Run: `go test ./skills/tail-sensor/scripts/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/tail-sensor/scripts/tail.go skills/tail-sensor/scripts/tail_test.go
git commit -m "feat(tail-sensor): path-like <id>/<run-id> + ambiguity + no-run errors"
```

---

## Task 18: `/stop-sensor` accepts path-like args; blocking-preferred resolution; SIGTERM for `blocking:false`

**Files:**
- Modify: `skills/stop-sensor/scripts/stop.go`
- Modify: `skills/stop-sensor/scripts/stop_test.go`

- [ ] **Step 1: Write failing tests**

Append to `stop_test.go`:

```go
func TestStop_BlockingFalse_TerminatesRunnerSubprocess(t *testing.T) {
    proj := t.TempDir()
    // Spawn a real, long-running subprocess we'll target.
    sub := exec.Command("sleep", "30")
    _ = sub.Start()
    defer func() { _ = sub.Process.Kill(); _, _ = sub.Process.Wait() }()

    r := registry.NewRoot(proj)
    _ = os.MkdirAll(r.SensorsDir(), 0o755)
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{{
        SensorID: "alpha", RunID: fmt.Sprintf("%d-runX", sub.Process.Pid),
        Blocking: false, PID: sub.Process.Pid, PGID: sub.Process.Pid,
        StartedAt: "2026-05-11T00:00:00Z",
    }}}
    _ = registry.Save(r, rs)

    res := registry.Result{ProjectRoot: proj, Source: "env"}
    exit, sig := runStop(res, []string{"alpha"})
    if exit != 0 {
        t.Fatalf("exit=%d, sig=%+v", exit, sig)
    }
    // Subprocess should be reaped.
    state, _ := sub.Process.Wait()
    if state != nil && !state.Exited() {
        t.Errorf("subprocess still alive")
    }
    // Entry should be removed.
    after, _ := registry.Load(r)
    if len(after.Entries) != 0 {
        t.Errorf("entry not removed: %+v", after.Entries)
    }
}

func TestStop_BlockingPreferred_WhenMixedActives(t *testing.T) {
    // Two entries for "alpha": one blocking:true (real PID), one blocking:false.
    // /stop-sensor alpha (no run-id) must target the blocking:true one.
    proj := t.TempDir()
    blockingSub := exec.Command("sleep", "30")
    _ = blockingSub.Start()
    defer blockingSub.Process.Kill()
    nonBlockingSub := exec.Command("sleep", "30")
    _ = nonBlockingSub.Start()
    defer nonBlockingSub.Process.Kill()

    r := registry.NewRoot(proj)
    _ = os.MkdirAll(r.SensorsDir(), 0o755)
    rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
        {SensorID: "alpha", RunID: "nb-1", Blocking: false, PID: nonBlockingSub.Process.Pid, PGID: nonBlockingSub.Process.Pid, StartedAt: "2026-05-11T00:00:00Z"},
        {SensorID: "alpha", RunID: "b-1",  Blocking: true,  PID: blockingSub.Process.Pid,    PGID: blockingSub.Process.Pid,    WatcherPID: 1, StartedAt: "2026-05-11T00:00:00Z"},
    }}
    _ = registry.Save(r, rs)

    res := registry.Result{ProjectRoot: proj, Source: "env"}
    exit, _ := runStop(res, []string{"alpha"})
    if exit != 0 {
        t.Fatalf("exit=%d", exit)
    }
    after, _ := registry.Load(r)
    var leftBlocking, leftNonBlocking int
    for _, e := range after.Entries {
        if e.Blocking { leftBlocking++ } else { leftNonBlocking++ }
    }
    if leftBlocking != 0 {
        t.Errorf("blocking entry not removed: %+v", after.Entries)
    }
    if leftNonBlocking != 1 {
        t.Errorf("non-blocking entry mistakenly removed: %+v", after.Entries)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./skills/stop-sensor/scripts/ -run TestStop_BlockingFalse -v` and the mixed test.
Expected: FAIL — current `stop.go` only knows blocking-style stops.

- [ ] **Step 3: Update `stop.go`**

In `stop.go`:

3a. Parse arg as `sensorID[/runID]` (mirror Task 17).

3b. Resolve the target entry:
- If `runID` is set → `rs.FindEntryByRunID(runID)`.
- If `runID` is empty:
  - `entries := rs.FindEntries(sensorID)`.
  - If `len(entries) == 0` → existing "not running" branch.
  - Prefer the `blocking:true` entry if present.
  - Else if `len(entries) == 1` → use it.
  - Else → emit `metadata.kind=ambiguous_run`, exit 1.

3c. Branch on `entry.Blocking`:
- `true` → existing path (`SIGTERM` → `graceful_timeout_ms` → `SIGKILL` on PGID; watcher drain; remove entry by run_id).
- `false` → `syscall.Kill(-entry.PGID, syscall.SIGTERM)`, poll with `IsPIDAlive` for up to `graceful_timeout_ms`, fall back to `syscall.Kill(-entry.PGID, syscall.SIGKILL)` if still alive, then `RemoveEntryByRunID(entry.RunID)`. No watcher to coordinate with.

3d. Replace `RemoveEntry(id)` with `RemoveEntryByRunID(entry.RunID)` at `stop.go:143`.

- [ ] **Step 4: Run all stop-sensor tests**

Run: `go test ./skills/stop-sensor/scripts/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/stop-sensor/scripts/stop.go skills/stop-sensor/scripts/stop_test.go
git commit -m "feat(stop-sensor): blocking-preferred resolution + SIGTERM path for blocking:false"
```

---

## Task 19: End-to-end integration test — concurrent `/run-sensor` + observability

**Files:**
- Create: `test/integration_runtime_logs_test.go`

- [ ] **Step 1: Write the integration test**

Create `test/integration_runtime_logs_test.go`:

```go
//go:build integration

package integration

import (
    "context"
    "encoding/json"
    "fmt"
    "io"
    "os"
    "os/exec"
    "path/filepath"
    "regexp"
    "strings"
    "sync"
    "testing"
    "time"

    "github.com/iurykrieger/harness-framework/lib/registry"
)

func TestRunSensor_ConcurrentRunsCoexist(t *testing.T) {
    proj := t.TempDir()
    sensorsDir := filepath.Join(proj, "sensors")
    if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
        t.Fatal(err)
    }
    _ = os.WriteFile(filepath.Join(sensorsDir, "echo.json"), []byte(`{
      "id": "echo", "version": "0.0.0", "kind": "observation",
      "type": "computational", "output": "single",
      "cost": {"compute": "low"},
      "execution": {"command": "sleep 1 && echo ok",
        "exit_code_map": [{"code": 0, "verdict": "pass", "severity": "info"}]}
    }`), 0o644)

    repoRoot := repoRoot(t)
    runOnce := func() string {
        cmd := exec.Command("go", "run", "-tags=run_computational", "./skills/run-sensor/scripts", "echo")
        cmd.Dir = repoRoot
        cmd.Env = append(os.Environ(), "HARNESS_REGISTRY_ROOT="+proj)
        out, err := cmd.Output()
        if err != nil {
            t.Fatalf("run: %v", err)
        }
        lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
        return lines[len(lines)-1]
    }

    var wg sync.WaitGroup
    var lastA, lastB string
    wg.Add(2)
    go func() { defer wg.Done(); lastA = runOnce() }()
    go func() { defer wg.Done(); lastB = runOnce() }()
    wg.Wait()

    var sa, sb map[string]interface{}
    _ = json.Unmarshal([]byte(lastA), &sa)
    _ = json.Unmarshal([]byte(lastB), &sb)
    ra, _ := sa["run_id"].(string)
    rb, _ := sb["run_id"].(string)
    if ra == rb || ra == "" || rb == "" {
        t.Fatalf("run_ids must be distinct & non-empty: %q %q", ra, rb)
    }

    r := registry.NewRoot(proj)
    if _, err := os.Stat(r.SignalsLogRun("echo", ra)); err != nil {
        t.Errorf("signals.log missing for %s", ra)
    }
    if _, err := os.Stat(r.SignalsLogRun("echo", rb)); err != nil {
        t.Errorf("signals.log missing for %s", rb)
    }
}

// repoRoot walks up from cwd until go.mod is found.
func repoRoot(t *testing.T) string {
    t.Helper()
    d, _ := os.Getwd()
    for d != "/" {
        if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
            return d
        }
        d = filepath.Dir(d)
    }
    t.Fatal("repo root not found")
    return ""
}
```

- [ ] **Step 2: Run the integration test**

Run: `go test -tags=integration ./test/ -run TestRunSensor_Concurrent -v`
Expected: PASS — two `<run-id>/` dirs created, distinct run_ids in stdout, both signals.log files present.

- [ ] **Step 3: Commit**

```bash
git add test/integration_runtime_logs_test.go
git commit -m "test(integration): concurrent /run-sensor coexists with distinct run dirs"
```

---

## Task 20: DoD audit — single full sweep against the spec

**Files:**
- No code changes; commit a checklist for record-keeping if anything's missing.

- [ ] **Step 1: Re-read the spec's DoD list**

Read: `docs/superpowers/specs/2026-05-11-run-sensor-runtime-lifecycle-design.md`, section "Definition of Done (binary)".

- [ ] **Step 2: Verify each DoD item by command**

1. `/run-sensor X` creates `.runtime/sensors/X/<pid>-<uuid8>/{raw.log,signals.log}` and removes entry on exit — covered by Task 9 and Task 19.
2. Stdout last line is aggregate (byte-identical except for legitimately-varying fields) — covered by existing aggregate-as-last-line tests + Task 7/8.
3. `signals.log` contents match stdout — covered by Task 8.
4. `/list-sensors` shows blocking:false entry during a run — covered by Task 16; verify in `Step 4` below by running it manually.
5. `/start-sensor Y` writes to new layout — covered by Task 12.
6. Concurrent runs coexist — covered by Task 19.
7. `/stop-sensor X/<run-id>` terminates non-blocking and writes `terminated_externally` — covered by Task 11 + Task 18.
8. `go vet` + `go test` pass with both tags — verified below.

- [ ] **Step 3: Run the full test matrix**

Run, in order:

```bash
go vet ./lib/... ./hooks/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
go vet -tags=start_sensor ./...
go vet -tags=start_watcher ./...
go test ./lib/...
go test -tags=run_computational ./skills/run-sensor/scripts/
go test -tags=run_inferential ./skills/run-sensor/scripts/
go test -tags=start_sensor ./skills/start-sensor/scripts/
go test -tags=start_watcher ./skills/start-sensor/scripts/
go test ./skills/list-sensors/scripts/ ./skills/tail-sensor/scripts/ ./skills/stop-sensor/scripts/
go test -tags=integration ./test/
```

Expected: all green. If a step fails, return to the relevant task and fix before claiming done.

- [ ] **Step 4: Manual UX sweep (smoke test)**

In one terminal: `go run -tags=run_computational ./skills/run-sensor/scripts <some long-running sensor>`.
In a second terminal: `go run ./skills/list-sensors/scripts` — expect to see the entry with `blocking:false` and a composite `run_id`. Then `go run ./skills/tail-sensor/scripts <id> 0` and `go run ./skills/stop-sensor/scripts <id>/<run-id>`. Confirm the runner in the first terminal exits with `terminated_externally:true` in the aggregate.

- [ ] **Step 5: Commit (final cleanup if needed)**

If any DoD item revealed a gap during the audit, fix it on top of the relevant task's commit and add a short follow-up commit:

```bash
git commit -m "fix(<area>): close DoD gap N"
```

If everything is green, no additional commit — proceed to PR.

---

## Self-Review

After writing this plan I did the following spot-checks:

**Spec coverage:**
- Decisions §1–8 → Task 1, 3, 9, 12, 15, 16, 17, 18.
- Layout (`<run-id>/` for both runners) → Tasks 3, 9, 12.
- Schema changes → Task 5 (signal.json) + Task 1 (RunningSensorEntry) — no `sensor.json` change as the spec specified.
- Registry API rewrites → Tasks 2, 14, 15, 17, 18.
- Legacy migration with flat-path fallback → Tasks 4, 17 (consumer side).
- `RunOne(*Root)` threading → Task 9.
- `StreamSubprocess` `RunDir` → Tasks 7, 8.
- `HARNESS_WATCHER_RUN_ID` → Task 13.
- SIGINT/SIGTERM handler + `terminated_externally` → Task 11.
- All callsite rewrites in the spec table → Tasks 14, 15 (+ 13, 17, 18 consumer-side).
- All 5 invariants → tested directly: #1 (Task 9 asserts dir exists post-insert), #2 (Task 2's `FindEntryByRunID` + Task 9's duplicate-check), #3 (Task 16 omits `watcher_alive` for blocking:false), #4 (existing aggregate-as-last-line tests preserved through Task 7/8), #5 (Task 7/8 open with `O_APPEND`), #6 cascade (Task 10).
- 8 DoD items → Task 20 audits all.

**Placeholder scan:** No "TBD", "TODO", "implement later". Every code step has the full Go listing or a precise edit instruction with line references.

**Type consistency:**
- `RunningSensorEntry.RunID string`, `Blocking bool` — same in Task 1 (struct), Task 2 (lookups), Task 9 (insert), Task 12 (insert), Task 15 (FindBlockingEntry consumer).
- `StreamConfig.RunDir string` — same in Task 7 (introduction), Task 8 (reuse), Task 9 (caller).
- `RunOneWithRoot(...)` signature — defined Task 9, called Task 10 only (`RunWithDepsRoot`).
- `LegacyRawLog`/`LegacySignalsLog` defined Task 3, consumed Task 17.
- `HARNESS_WATCHER_RUN_ID` env name — set in Task 12, read in Task 13.
- `metadata.terminated_externally: true` — written in Task 11 (via `ctx.Err()` in lifecycle.go), asserted in Task 11's test + Task 18's test.

No drift found.
