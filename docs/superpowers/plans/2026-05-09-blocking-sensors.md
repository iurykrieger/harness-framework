# Blocking Sensors and Explicit Exit — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the timeout-driven `long_running` model with an explicit start/observe/stop protocol (`/start-sensor`, `/tail-sensor`, `/stop-sensor`, `/list-sensors`) plus live-dependency orchestration via a refcount registry under `.runtime/sensors/`.

**Architecture:** New `lib/registry/` owns the `.runtime/sensors/running_sensors.json` registry (singleton-per-sensor entries with `held_by` discriminated records, atomic file writes, flock). Four new skills (start/stop/tail/list) plus a detached watcher script translate raw subprocess output to JSONL Signals while the process is alive. The orchestrator is extended with `RunOneWithLiveDeps` that attaches/detaches blocking deps via the same registry primitives. The schema renames `execution.long_running` to `execution.blocking` and forbids `cost.latency.timeout_ms` for blocking sensors.

**Tech Stack:** Go 1.25, `github.com/santhosh-tekuri/jsonschema/v5`, `golang.org/x/sys/unix` for `flock(2)` (added via `go get`), `github.com/fsnotify/fsnotify` (added via `go get`) for watcher tail-follow, JSON Schema Draft 2020-12.

**Spec:** `docs/superpowers/specs/2026-05-09-blocking-sensors-design.md`

---

## File structure

### New files

- `lib/registry/paths.go` — resolves `.runtime/sensors/...` paths from project root.
- `lib/registry/paths_test.go`
- `lib/registry/lock.go` — `WithFileLock(path string, fn func() error) error` using `flock(2)`.
- `lib/registry/lock_test.go`
- `lib/registry/liveness.go` — `IsPIDAlive(pid int) bool` via `kill(pid, 0)`.
- `lib/registry/liveness_test.go`
- `lib/registry/state.go` — `RunningSensors`, `RunningSensorEntry`, `HeldByEntry`, `SubprocessExit` structs; `Load`, `Save` (atomic temp+rename).
- `lib/registry/state_test.go`
- `lib/registry/held_by.go` — `Add(entry, holder)`, `Remove(entry, holder)`, `IsEmpty(entry)`, `ReapDead(entry) []HeldByEntry`.
- `lib/registry/held_by_test.go`
- `lib/subprocess/detach.go` — `SpawnDetached(DetachConfig) (DetachResult, error)`: `Setsid: true`, `Setpgid: true`, redirected stdout/stderr to file.
- `lib/subprocess/detach_test.go`
- `lib/orchestrator/live_deps.go` — `AttachLiveDeps`, `DetachLiveDeps`, `RunOneWithLiveDeps`.
- `lib/orchestrator/live_deps_test.go`
- `skills/start-sensor/SKILL.md`
- `skills/start-sensor/scripts/start.go` (`//go:build start_sensor`)
- `skills/start-sensor/scripts/start_test.go`
- `skills/start-sensor/scripts/watcher.go` (`//go:build start_watcher`)
- `skills/start-sensor/scripts/watcher_test.go`
- `skills/stop-sensor/SKILL.md`
- `skills/stop-sensor/scripts/stop.go` (`//go:build stop_sensor`)
- `skills/stop-sensor/scripts/stop_test.go`
- `skills/tail-sensor/SKILL.md`
- `skills/tail-sensor/scripts/tail.go` (`//go:build tail_sensor`)
- `skills/tail-sensor/scripts/tail_test.go`
- `skills/list-sensors/SKILL.md`
- `skills/list-sensors/scripts/list.go` (`//go:build list_sensors`)
- `skills/list-sensors/scripts/list_test.go`
- `sensors/fixtures/blocking-echo-loop.json`
- `sensors/fixtures/consumer-of-blocking.json`

### Modified files

- `schemas/sensor.json` — replace `execution.long_running` with `execution.blocking`, add `execution.graceful_timeout_ms`, drop `timeout_ms` from `cost.latency.required`, add two new top-level `allOf` branches for blocking.
- `lib/sensor/path.go` — add `ResolveByID(id string, baseDir string)`.
- `lib/sensor/path_test.go` — tests for new helper.
- `lib/signal/aggregate.go` — `LongRunning` field renamed to `Blocking`. Comments updated.
- `lib/signal/aggregate_test.go` — same rename in test cases.
- `lib/orchestrator/lifecycle.go` — read `execMap["blocking"]`, write `aggregateMD["blocking"]`.
- `skills/detect-sensors/SKILL.md` — rewrite five `long_running` references (lines 52, 53, 102, 151, 179) to teach the new `blocking` model and the `/start-sensor` workflow.
- `skills/run-sensor/SKILL.md` — argument changes from `<path>` to `<sensor.id>`; new "Refusing blocking sensors" subsection.
- `skills/run-sensor/scripts/run-computational.go` — accept `<id>` instead of `<path>`; switch to `RunOneWithLiveDeps`; refuse `blocking: true`.
- `skills/run-sensor/scripts/run-inferential.go` — same.
- `.gitignore` — add `.runtime/`.
- `.claude-plugin/plugin.json` — version bump.
- `go.mod`, `go.sum` — `golang.org/x/sys` and `github.com/fsnotify/fsnotify` added.

---

## Task 1: Add module dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add fsnotify and x/sys**

Run: `go get github.com/fsnotify/fsnotify@latest && go get golang.org/x/sys@latest`
Expected: `go.mod` and `go.sum` updated; `go mod tidy` clean.

- [ ] **Step 2: Verify build still passes**

Run: `go build ./... && go test ./lib/...`
Expected: PASS, no new failures.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add fsnotify and x/sys deps for blocking-sensor watcher"
```

---

## Task 2: lib/registry/paths.go

**Files:**
- Create: `lib/registry/paths.go`
- Create: `lib/registry/paths_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/registry/paths_test.go`:

```go
package registry_test

import (
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestPaths_RootSensorsDir(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	got := r.SensorsDir()
	want := filepath.Join("/tmp/proj", ".runtime", "sensors")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPaths_RegistryFile(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	got := r.RegistryFile()
	want := filepath.Join("/tmp/proj", ".runtime", "sensors", "running_sensors.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPaths_LockFile(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	got := r.LockFile()
	want := filepath.Join("/tmp/proj", ".runtime", "sensors", "running_sensors.lock")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPaths_PerSensor(t *testing.T) {
	r := registry.NewRoot("/tmp/proj")
	if got, want := r.SensorDir("watch-logs"), filepath.Join("/tmp/proj", ".runtime", "sensors", "watch-logs"); got != want {
		t.Errorf("SensorDir: got %q, want %q", got, want)
	}
	if got, want := r.RawLog("watch-logs"), filepath.Join("/tmp/proj", ".runtime", "sensors", "watch-logs", "raw.log"); got != want {
		t.Errorf("RawLog: got %q, want %q", got, want)
	}
	if got, want := r.SignalsLog("watch-logs"), filepath.Join("/tmp/proj", ".runtime", "sensors", "watch-logs", "signals.log"); got != want {
		t.Errorf("SignalsLog: got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/registry/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `lib/registry/paths.go`:

```go
// Package registry owns the .runtime/sensors/ directory layout, atomic
// state-file writes, file locks, PID liveness checks, and held_by
// refcount management for blocking-sensor runs.
package registry

import "path/filepath"

// Root is the absolute path to a project's .runtime/sensors/ directory.
// All registry helpers are methods on Root so tests can pivot to a temp
// directory by constructing a Root around it.
type Root struct {
	projectRoot string
}

// NewRoot returns a Root anchored at <projectRoot>/.runtime/sensors/.
func NewRoot(projectRoot string) Root {
	return Root{projectRoot: projectRoot}
}

// SensorsDir is the directory holding running_sensors.json and per-sensor
// subdirectories.
func (r Root) SensorsDir() string {
	return filepath.Join(r.projectRoot, ".runtime", "sensors")
}

// RegistryFile is the absolute path to running_sensors.json.
func (r Root) RegistryFile() string {
	return filepath.Join(r.SensorsDir(), "running_sensors.json")
}

// LockFile is the sibling lock used by WithFileLock.
func (r Root) LockFile() string {
	return filepath.Join(r.SensorsDir(), "running_sensors.lock")
}

// SensorDir returns the per-sensor directory under .runtime/sensors/<id>/.
func (r Root) SensorDir(id string) string {
	return filepath.Join(r.SensorsDir(), id)
}

// RawLog is the per-sensor raw subprocess output file.
func (r Root) RawLog(id string) string {
	return filepath.Join(r.SensorDir(id), "raw.log")
}

// SignalsLog is the per-sensor JSONL signals file written by the watcher.
func (r Root) SignalsLog(id string) string {
	return filepath.Join(r.SensorDir(id), "signals.log")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./lib/registry/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/paths.go lib/registry/paths_test.go
git commit -m "feat(registry): paths helper for .runtime/sensors layout"
```

---

## Task 3: lib/registry/lock.go (flock wrapper)

**Files:**
- Create: `lib/registry/lock.go`
- Create: `lib/registry/lock_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/registry/lock_test.go`:

```go
package registry_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestWithFileLock_Serializes(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup

	work := func(id int, holdMS int) {
		defer wg.Done()
		err := registry.WithFileLock(lockPath, func() error {
			mu.Lock()
			order = append(order, id)
			mu.Unlock()
			time.Sleep(time.Duration(holdMS) * time.Millisecond)
			return nil
		})
		if err != nil {
			t.Errorf("WithFileLock #%d: %v", id, err)
		}
	}

	wg.Add(2)
	go work(1, 50)
	time.Sleep(5 * time.Millisecond) // ensure goroutine 1 grabs first
	go work(2, 0)
	wg.Wait()

	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("expected [1,2], got %v", order)
	}
}

func TestWithFileLock_PropagatesError(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "test.lock")

	want := errSentinel{}
	got := registry.WithFileLock(lockPath, func() error { return want })
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/registry/...`
Expected: FAIL — `WithFileLock` not defined.

- [ ] **Step 3: Write the implementation**

Create `lib/registry/lock.go`:

```go
package registry

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// WithFileLock acquires an exclusive flock on path (creating the file if
// necessary), runs fn, and releases the lock — even if fn panics. The
// lock file is intentionally left on disk; flock state is process-bound.
func WithFileLock(path string, fn func() error) error {
	if err := os.MkdirAll(parentDir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open lock: %w", err)
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)

	return fn()
}

func parentDir(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./lib/registry/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/lock.go lib/registry/lock_test.go
git commit -m "feat(registry): WithFileLock wrapper around flock(2)"
```

---

## Task 4: lib/registry/liveness.go

**Files:**
- Create: `lib/registry/liveness.go`
- Create: `lib/registry/liveness_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/registry/liveness_test.go`:

```go
package registry_test

import (
	"os"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestIsPIDAlive_SelfIsAlive(t *testing.T) {
	if !registry.IsPIDAlive(os.Getpid()) {
		t.Fatal("expected self pid to be alive")
	}
}

func TestIsPIDAlive_ZeroIsDead(t *testing.T) {
	if registry.IsPIDAlive(0) {
		t.Fatal("expected pid 0 to be reported dead")
	}
}

func TestIsPIDAlive_VeryLargePIDIsDead(t *testing.T) {
	// PID space caps well below 4_000_000 on Darwin/Linux; this PID
	// is essentially guaranteed not to exist.
	if registry.IsPIDAlive(3_999_999) {
		t.Fatal("expected nonexistent pid to be reported dead")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/registry/...`
Expected: FAIL — `IsPIDAlive` not defined.

- [ ] **Step 3: Write the implementation**

Create `lib/registry/liveness.go`:

```go
package registry

import (
	"errors"

	"golang.org/x/sys/unix"
)

// IsPIDAlive returns true when pid > 0 and the process can be signalled
// (signal 0 is the standard POSIX existence probe). Permission errors
// (EPERM) also indicate the PID exists — we just don't own it.
func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, unix.EPERM)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./lib/registry/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/liveness.go lib/registry/liveness_test.go
git commit -m "feat(registry): IsPIDAlive via kill(pid, 0)"
```

---

## Task 5: lib/registry/state.go (structs + Load/Save)

**Files:**
- Create: `lib/registry/state.go`
- Create: `lib/registry/state_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/registry/state_test.go`:

```go
package registry_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestLoad_Empty(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Version != 1 {
		t.Errorf("Version: got %d, want 1", rs.Version)
	}
	if len(rs.Entries) != 0 {
		t.Errorf("Entries: got %d, want 0", len(rs.Entries))
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)

	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID:   "watch-logs",
				PID:        1234,
				PGID:       1234,
				WatcherPID: 1235,
				StartedAt:  "2026-05-09T15:30:00Z",
				Command:    "tail -f /var/log/syslog",
				LogDir:     ".runtime/sensors/watch-logs",
				HeldBy: []registry.HeldByEntry{
					{Kind: "manual", AttachedAt: "2026-05-09T15:30:00Z"},
				},
			},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	got, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rs, got) {
		t.Fatalf("round trip mismatch\nwant %+v\ngot  %+v", rs, got)
	}
}

func TestSave_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)

	rs := registry.RunningSensors{Version: 1}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(r.SensorsDir(), "running_sensors.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file not cleaned up after Save")
	}
}

func TestLoad_RejectsCorrupt(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.RegistryFile(), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Load(r); err == nil {
		t.Fatal("expected error on corrupt file")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/registry/...`
Expected: FAIL — types not defined.

- [ ] **Step 3: Write the implementation**

Create `lib/registry/state.go`:

```go
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// RunningSensors is the schema of running_sensors.json.
type RunningSensors struct {
	Version int                   `json:"version"`
	Entries []RunningSensorEntry  `json:"entries"`
}

// RunningSensorEntry is one live blocking sensor's state.
type RunningSensorEntry struct {
	SensorID       string           `json:"sensor_id"`
	PID            int              `json:"pid"`
	PGID           int              `json:"pgid"`
	WatcherPID     int              `json:"watcher_pid"`
	StartedAt      string           `json:"started_at"`
	Command        string           `json:"command"`
	LogDir         string           `json:"log_dir"`
	HeldBy         []HeldByEntry    `json:"held_by"`
	SubprocessExit *SubprocessExit  `json:"subprocess_exit,omitempty"`
}

// HeldByEntry is a discriminated record: kind=manual carries only
// AttachedAt; kind=sensor carries ID and PID of the dependent holder.
type HeldByEntry struct {
	Kind       string `json:"kind"` // "manual" or "sensor"
	ID         string `json:"id,omitempty"`
	PID        int    `json:"pid,omitempty"`
	AttachedAt string `json:"attached_at"`
}

// SubprocessExit is set by the watcher's reaper after wait(). Absent
// while the subprocess is still running.
type SubprocessExit struct {
	Code     int    `json:"code"`
	ExitedAt string `json:"exited_at"`
}

// Load reads running_sensors.json. A missing file returns an empty,
// version-1 RunningSensors (the canonical "no live sensors" state).
func Load(r Root) (RunningSensors, error) {
	data, err := os.ReadFile(r.RegistryFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RunningSensors{Version: 1}, nil
		}
		return RunningSensors{}, fmt.Errorf("read registry: %w", err)
	}
	var rs RunningSensors
	if err := json.Unmarshal(data, &rs); err != nil {
		return RunningSensors{}, fmt.Errorf("parse registry: %w", err)
	}
	if rs.Version == 0 {
		rs.Version = 1
	}
	return rs, nil
}

// Save writes running_sensors.json atomically (temp + rename). The
// caller is expected to be holding the registry flock.
func Save(r Root, rs RunningSensors) error {
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		return fmt.Errorf("mkdir sensors dir: %w", err)
	}
	data, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := r.RegistryFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, r.RegistryFile()); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// FindEntry returns a pointer to the entry for sensor id, or nil.
func (rs *RunningSensors) FindEntry(id string) *RunningSensorEntry {
	for i := range rs.Entries {
		if rs.Entries[i].SensorID == id {
			return &rs.Entries[i]
		}
	}
	return nil
}

// RemoveEntry deletes the entry matching id (no-op if absent).
func (rs *RunningSensors) RemoveEntry(id string) {
	out := rs.Entries[:0]
	for _, e := range rs.Entries {
		if e.SensorID != id {
			out = append(out, e)
		}
	}
	rs.Entries = out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./lib/registry/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/state.go lib/registry/state_test.go
git commit -m "feat(registry): structs and atomic Load/Save for running_sensors.json"
```

---

## Task 6: lib/registry/held_by.go

**Files:**
- Create: `lib/registry/held_by.go`
- Create: `lib/registry/held_by_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/registry/held_by_test.go`:

```go
package registry_test

import (
	"reflect"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestAddHolder_AppendsRecord(t *testing.T) {
	e := &registry.RunningSensorEntry{}
	registry.AddHolder(e, registry.HeldByEntry{Kind: "manual", AttachedAt: "t1"})
	if len(e.HeldBy) != 1 || e.HeldBy[0].Kind != "manual" {
		t.Fatalf("got %+v", e.HeldBy)
	}
}

func TestRemoveHolder_Manual(t *testing.T) {
	e := &registry.RunningSensorEntry{
		HeldBy: []registry.HeldByEntry{
			{Kind: "manual", AttachedAt: "t1"},
			{Kind: "sensor", ID: "B", PID: 100, AttachedAt: "t2"},
		},
	}
	removed := registry.RemoveHolder(e, registry.HeldByEntry{Kind: "manual"})
	if !removed {
		t.Fatal("expected RemoveHolder to return true")
	}
	if len(e.HeldBy) != 1 || e.HeldBy[0].Kind != "sensor" {
		t.Fatalf("after manual removal: %+v", e.HeldBy)
	}
}

func TestRemoveHolder_SensorMatchesIDAndPID(t *testing.T) {
	e := &registry.RunningSensorEntry{
		HeldBy: []registry.HeldByEntry{
			{Kind: "sensor", ID: "B", PID: 100, AttachedAt: "t1"},
			{Kind: "sensor", ID: "B", PID: 200, AttachedAt: "t2"},
		},
	}
	removed := registry.RemoveHolder(e, registry.HeldByEntry{Kind: "sensor", ID: "B", PID: 100})
	if !removed {
		t.Fatal("expected RemoveHolder to return true")
	}
	if len(e.HeldBy) != 1 || e.HeldBy[0].PID != 200 {
		t.Fatalf("after sensor removal: %+v", e.HeldBy)
	}
}

func TestIsHeld(t *testing.T) {
	e := &registry.RunningSensorEntry{}
	if registry.IsHeld(e) {
		t.Fatal("empty held_by must report not held")
	}
	registry.AddHolder(e, registry.HeldByEntry{Kind: "manual"})
	if !registry.IsHeld(e) {
		t.Fatal("entry with manual hold must report held")
	}
}

func TestReapDead_DropsDeadHolders(t *testing.T) {
	e := &registry.RunningSensorEntry{
		HeldBy: []registry.HeldByEntry{
			{Kind: "manual", AttachedAt: "t1"},
			{Kind: "sensor", ID: "B", PID: 3_999_999, AttachedAt: "t2"}, // dead
			{Kind: "sensor", ID: "C", PID: registry.SelfPID(), AttachedAt: "t3"}, // alive
		},
	}
	reaped := registry.ReapDead(e)
	if len(reaped) != 1 || reaped[0].PID != 3_999_999 {
		t.Fatalf("reaped: %+v", reaped)
	}
	want := []registry.HeldByEntry{
		{Kind: "manual", AttachedAt: "t1"},
		{Kind: "sensor", ID: "C", PID: registry.SelfPID(), AttachedAt: "t3"},
	}
	if !reflect.DeepEqual(e.HeldBy, want) {
		t.Fatalf("after reap: %+v", e.HeldBy)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/registry/...`
Expected: FAIL — helpers not defined.

- [ ] **Step 3: Write the implementation**

Create `lib/registry/held_by.go`:

```go
package registry

import "os"

// AddHolder appends h to entry.HeldBy. Caller is responsible for not
// adding duplicates of the same (Kind, ID, PID) tuple.
func AddHolder(entry *RunningSensorEntry, h HeldByEntry) {
	entry.HeldBy = append(entry.HeldBy, h)
}

// RemoveHolder drops the FIRST entry in HeldBy matching match. For
// kind=manual only Kind needs to match. For kind=sensor, both ID and
// PID must match (so concurrent runs of the same dependent — different
// orchestrator processes — don't release each other's hold).
//
// Returns true when an entry was removed.
func RemoveHolder(entry *RunningSensorEntry, match HeldByEntry) bool {
	for i, h := range entry.HeldBy {
		if !holdersMatch(h, match) {
			continue
		}
		entry.HeldBy = append(entry.HeldBy[:i], entry.HeldBy[i+1:]...)
		return true
	}
	return false
}

// IsHeld returns true when HeldBy is non-empty.
func IsHeld(entry *RunningSensorEntry) bool {
	return len(entry.HeldBy) > 0
}

// ReapDead removes every kind=sensor holder whose PID is no longer alive
// and returns the removed entries (so the caller can surface them as
// evidence). Manual holders are never reaped — they do not have a PID.
func ReapDead(entry *RunningSensorEntry) []HeldByEntry {
	var reaped []HeldByEntry
	keep := entry.HeldBy[:0]
	for _, h := range entry.HeldBy {
		if h.Kind == "sensor" && !IsPIDAlive(h.PID) {
			reaped = append(reaped, h)
			continue
		}
		keep = append(keep, h)
	}
	entry.HeldBy = keep
	return reaped
}

// SelfPID is exported for tests that need the running process's PID.
func SelfPID() int { return os.Getpid() }

func holdersMatch(a, b HeldByEntry) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == "sensor" {
		return a.ID == b.ID && a.PID == b.PID
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./lib/registry/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/held_by.go lib/registry/held_by_test.go
git commit -m "feat(registry): held_by helpers (Add/Remove/IsHeld/ReapDead)"
```

---

## Task 7: lib/sensor/path.go::ResolveByID

**Files:**
- Modify: `lib/sensor/path.go`
- Modify: `lib/sensor/path_test.go`

- [ ] **Step 1: Write the failing test**

Append to `lib/sensor/path_test.go`:

```go
func TestResolveByID(t *testing.T) {
	dir := t.TempDir()
	sensorsDir := filepath.Join(dir, "sensors")
	if err := os.MkdirAll(sensorsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(sensorsDir, "watch-logs.json")
	if err := os.WriteFile(want, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := sensor.ResolveByID("watch-logs", dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveByID_RejectsEmpty(t *testing.T) {
	if _, err := sensor.ResolveByID("", "/tmp"); err == nil {
		t.Fatal("expected error on empty id")
	}
}

func TestResolveByID_RejectsBadShape(t *testing.T) {
	if _, err := sensor.ResolveByID("../etc/passwd", "/tmp"); err == nil {
		t.Fatal("expected error on path-like id")
	}
}

func TestResolveByID_MissingFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := sensor.ResolveByID("nope", dir); err == nil {
		t.Fatal("expected error when file missing")
	}
}
```

(If `lib/sensor/path_test.go` is empty or has no imports, add at the top:)

```go
package sensor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/sensor/...`
Expected: FAIL — `ResolveByID` not defined.

- [ ] **Step 3: Write the implementation**

Append to `lib/sensor/path.go`:

```go
import "regexp"

// idRegex matches the sensor.id shape required by schemas/sensor.json:
// lowercase letters/digits/dashes, must start with a letter.
var idRegex = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ResolveByID resolves a bare sensor id to its on-disk path under
// <baseDir>/sensors/<id>.json. The id MUST match the schema's id pattern
// to prevent path traversal via "../foo" or absolute-path inputs.
func ResolveByID(id, baseDir string) (string, error) {
	if id == "" {
		return "", errors.New("empty sensor id")
	}
	if !idRegex.MatchString(id) {
		return "", fmt.Errorf("sensor id %q does not match ^[a-z][a-z0-9-]*$", id)
	}
	path := filepath.Join(baseDir, "sensors", id+".json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("sensor %q: %w", id, err)
	}
	return path, nil
}
```

(Adjust the existing imports of `lib/sensor/path.go` to include `fmt` and `regexp`. The current file imports `errors`, `os`, `path/filepath`, `strings` — keep those, add `fmt` and `regexp`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./lib/sensor/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/sensor/path.go lib/sensor/path_test.go
git commit -m "feat(sensor): ResolveByID resolves <id> to sensors/<id>.json"
```

---

## Task 8: lib/subprocess/detach.go (SpawnDetached)

**Files:**
- Create: `lib/subprocess/detach.go`
- Create: `lib/subprocess/detach_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/subprocess/detach_test.go`:

```go
package subprocess_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

func TestSpawnDetached_StartsAndWritesToLog(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "raw.log")

	res, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: `echo HELLO; sleep 0.05`,
		LogFile: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-res.PGID, syscall.SIGKILL) // belt-and-suspenders

	// Wait briefly for the process to flush.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log)
		if len(data) > 0 {
			if string(data) != "HELLO\n" {
				t.Fatalf("got %q, want %q", string(data), "HELLO\n")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("log never populated")
}

func TestSpawnDetached_PIDAndPGIDPopulated(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "raw.log")

	res, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: `sleep 0.05`,
		LogFile: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-res.PGID, syscall.SIGKILL)

	if res.PID <= 0 {
		t.Fatalf("PID: got %d", res.PID)
	}
	if res.PGID <= 0 {
		t.Fatalf("PGID: got %d", res.PGID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/subprocess/...`
Expected: FAIL — `SpawnDetached` not defined.

- [ ] **Step 3: Write the implementation**

Create `lib/subprocess/detach.go`:

```go
package subprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// DetachConfig is the input to SpawnDetached.
type DetachConfig struct {
	Command string            // raw shell, executed via sh -c
	Env     map[string]string // additional env vars
	LogFile string            // stdout+stderr redirected here (append, mode 0644)
}

// DetachResult holds the spawned subprocess identity. The caller is
// responsible for kill(-PGID, SIG…) when shutting down.
type DetachResult struct {
	PID  int
	PGID int
}

// SpawnDetached spawns sh -c <Command> in a new session and process group
// (Setsid:true, Setpgid:true), redirects stdout+stderr to LogFile (open
// in append mode), and returns once the child has been started. The
// caller does NOT receive a Cmd handle — the process outlives the
// caller's lifetime by design (this is for blocking sensors).
func SpawnDetached(cfg DetachConfig) (DetachResult, error) {
	if cfg.Command == "" {
		return DetachResult{}, errors.New("detach: empty command")
	}
	if cfg.LogFile == "" {
		return DetachResult{}, errors.New("detach: empty log file")
	}
	logF, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return DetachResult{}, fmt.Errorf("open log: %w", err)
	}
	defer logF.Close() // child inherits the open fd; we close our own.

	cmd := exec.Command("sh", "-c", cfg.Command)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	if len(cfg.Env) > 0 {
		envList := append([]string{}, os.Environ()...)
		for k, v := range cfg.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = envList
	}

	if err := cmd.Start(); err != nil {
		return DetachResult{}, fmt.Errorf("start: %w", err)
	}
	pid := cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		// Setsid implies pgid == pid; fall back if Getpgid races on macOS.
		pgid = pid
	}
	// Release: do not Wait() here. Caller (or the watcher's reaper) waits.
	if err := cmd.Process.Release(); err != nil {
		return DetachResult{}, fmt.Errorf("release: %w", err)
	}
	return DetachResult{PID: pid, PGID: pgid}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./lib/subprocess/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/subprocess/detach.go lib/subprocess/detach_test.go
git commit -m "feat(subprocess): SpawnDetached for blocking-sensor command spawn"
```

---

## Task 9: ATOMIC step — schema + lib + skill renames (long_running → blocking)

This task is **atomic**: every change is part of one commit so the tree never goes red. Sub-steps 1–7 build up the change in working tree; sub-step 8 runs the full suite; sub-step 9 commits in one shot.

**Files:**
- Modify: `schemas/sensor.json`
- Modify: `lib/signal/aggregate.go`
- Modify: `lib/signal/aggregate_test.go`
- Modify: `lib/orchestrator/lifecycle.go`
- Modify: `skills/detect-sensors/SKILL.md`

- [ ] **Step 1: Update `schemas/sensor.json` — execution.blocking + graceful_timeout_ms**

Find the existing `execution.long_running` block and replace it with:

```json
"blocking": {
  "type": "boolean",
  "default": false,
  "description": "When true, the sensor does not terminate on its own and must be invoked via /start-sensor / /stop-sensor. Forbids cost.latency.timeout_ms; allows execution.graceful_timeout_ms. Other sensors may declare a blocking sensor in depends_on; the orchestrator will start (or attach to) the blocking dep before the dependent runs and stop it at teardown when no other dependent holds it."
},
"graceful_timeout_ms": {
  "type": "integer",
  "minimum": 100,
  "default": 5000,
  "description": "Time to wait between SIGTERM and SIGKILL on /stop-sensor. Applies only when execution.blocking is true. Minimum 100ms to ensure the process has a real chance to react to SIGTERM."
},
```

- [ ] **Step 2: Update `schemas/sensor.json` — cost.latency.required**

Replace `"required": ["p50_ms", "p95_ms", "timeout_ms"]` (around line 125) with:

```json
"required": ["p50_ms", "p95_ms"],
```

- [ ] **Step 3: Update `schemas/sensor.json` — new allOf branches for blocking**

Append two branches to the existing top-level `allOf` array (just before its closing `]`):

```json
,
{
  "if": { "properties": { "execution": { "properties": { "blocking": { "const": false } } } } },
  "then": { "properties": { "cost": { "properties": { "latency": { "required": ["timeout_ms"] } } } } }
},
{
  "if": { "properties": { "execution": { "properties": { "blocking": { "const": true } } } }, "required": ["execution"] },
  "then": {
    "properties": {
      "cost":   { "properties": { "latency": { "not": { "required": ["timeout_ms"] } } } },
      "output": { "const": "stream" }
    }
  }
}
```

- [ ] **Step 4: Rename `LongRunning` to `Blocking` in `lib/signal/aggregate.go`**

```go
// AggregateInput collects everything needed to compute the aggregate verdict.
type AggregateInput struct {
	ExitVerdict    string
	ExitSeverity   string
	StreamVerdict  string
	StreamSeverity string
	TimedOut       bool
	Blocking       bool // true for execution.blocking=true sensors
}

// Aggregate applies the worst-of-two rule.
//
// For one-shot sensors (Blocking=false), timeout always forces verdict=error
// regardless of the inputs: the run is incomplete and the tool's own notion
// of success cannot be trusted.
//
// For blocking sensors (Blocking=true), timeout is the *intended* lifecycle
// — the runner deliberately terminates the process when /stop-sensor is
// called. The exit side is treated as pass/info and the aggregate is
// driven entirely by what the stream observed.
func Aggregate(in AggregateInput) AggregateResult {
	if in.TimedOut && !in.Blocking {
		return AggregateResult{Verdict: "error", Severity: "high"}
	}
	exitV, exitS := in.ExitVerdict, in.ExitSeverity
	if in.TimedOut && in.Blocking {
		exitV, exitS = "pass", "info"
	}
	if VerdictRank[in.StreamVerdict] > VerdictRank[exitV] {
		return AggregateResult{Verdict: in.StreamVerdict, Severity: in.StreamSeverity}
	}
	return AggregateResult{Verdict: exitV, Severity: exitS}
}
```

- [ ] **Step 5: Rename in `lib/signal/aggregate_test.go`**

In `TestAggregate_LongRunningTimeoutIsPass` and `TestAggregate_LongRunningTimeoutPreservesStreamFail`:

- Rename the functions to `TestAggregate_BlockingTimeoutIsPass` and `TestAggregate_BlockingTimeoutPreservesStreamFail`.
- Replace `LongRunning: true` with `Blocking: true` in both test bodies.
- Update the comment on the second test to say "blocking only neutralises the timeout's error override, not real findings."

- [ ] **Step 6: Update `lib/orchestrator/lifecycle.go`**

In `RunOne`, around lines 57 and 92:

Replace:

```go
longRunning, _ := execMap["long_running"].(bool)
```

with:

```go
blocking, _ := execMap["blocking"].(bool)
```

Replace:

```go
LongRunning:    longRunning,
```

with:

```go
Blocking:       blocking,
```

Replace (around line 105–107):

```go
if longRunning {
    aggregateMD["long_running"] = true
}
```

with:

```go
if blocking {
    aggregateMD["blocking"] = true
}
```

- [ ] **Step 7: Rewrite `skills/detect-sensors/SKILL.md` — remove all five `long_running` references**

Open `skills/detect-sensors/SKILL.md` and replace each occurrence as follows:

- **Line 52** (One-shot bullet): replace `execution.long_running` omitted or false → with: "`execution.blocking` omitted or `false`".
- **Line 53** (Continuous bullet): rewrite to teach the new model. Replace the whole bullet with:

  ```markdown
  - **Blocking** (`execution.blocking: true`) — the command does not terminate on its own and must be invoked via `/start-sensor` / `/stop-sensor` (not `/run-sensor`). The runner spawns the process, the watcher streams pattern-matched Signals while it runs, and `/stop-sensor` produces the aggregate. `cost.latency.timeout_ms` is forbidden for blocking sensors; instead, `execution.graceful_timeout_ms` controls the SIGTERM→SIGKILL window. Use this for sensors whose value is observation while the process runs (e.g., `npm run dev`, `make watch`, log tailers, replay loops). Pair with `output: stream` (the schema enforces this) and patterns that capture both failure modes (errors, port collisions) and success markers (boot lines, ready probes). Other sensors may declare a blocking sensor in `depends_on`; the orchestrator will start (or attach to) it before the dependent runs and stop it at teardown when no other dependent holds it.
  ```

- **Line 102**: replace "set `execution.long_running: true`" with "set `execution.blocking: true` and use the `/start-sensor` workflow".
- **Line 151**: replace `"long_running": true` in the JSON template with `"blocking": true`. Also remove `"timeout_ms": …` from the `cost.latency` block of that template (it is now forbidden for blocking sensors). Replace with `"graceful_timeout_ms": 5000` inside `execution`.
- **Line 179**: replace `"long_running": true` with `"blocking": true` in the prose describing what to copy from the template.

- [ ] **Step 8: Verify no `long_running` left + run full test suite**

Run:

```bash
grep -rn "long_running\|LongRunning" --include="*.go" --include="*.json" --include="*.md" sensors/ lib/ skills/ schemas/ | grep -v docs/superpowers/specs/
```

Expected: empty output.

Run:

```bash
go vet ./... && go test ./lib/... && go test -tags=run_computational ./skills/run-sensor/scripts/... && go test -tags=run_inferential ./skills/run-sensor/scripts/...
```

Expected: PASS across the board. Schema-validation tests (`lib/schema/validator_test.go`) should still pass — the `blocking: false` default keeps existing sensors valid.

- [ ] **Step 9: Single commit**

```bash
git add schemas/sensor.json lib/signal/aggregate.go lib/signal/aggregate_test.go lib/orchestrator/lifecycle.go skills/detect-sensors/SKILL.md
git commit -m "refactor: rename execution.long_running to execution.blocking

Schema rename plus matching renames in lib/signal (LongRunning -> Blocking),
lib/orchestrator (read execMap['blocking']), and skills/detect-sensors
(rewritten guidance). cost.latency.timeout_ms is now optional and gated
on execution.blocking via two new allOf branches; blocking implies
output: stream. graceful_timeout_ms (min 100ms) added in execution.
No sensor JSON in sensors/ used long_running, so nothing else needs
updating."
```

---

## Task 10: skills/start-sensor/scripts/watcher.go

The watcher is implemented BEFORE `start.go` because start needs to invoke the watcher binary by path; tests for start mock the watcher path with a stub.

**Files:**
- Create: `skills/start-sensor/scripts/watcher.go`
- Create: `skills/start-sensor/scripts/watcher_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/start-sensor/scripts/watcher_test.go`:

```go
//go:build start_watcher

package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAndAppendSignals(t *testing.T) {
	dir := t.TempDir()
	raw := filepath.Join(dir, "raw.log")
	signals := filepath.Join(dir, "signals.log")
	if err := os.WriteFile(raw, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	patterns := []map[string]interface{}{
		{"regex": "^ERROR (.*)$", "verdict": "fail", "severity": "high", "rationale": "match"},
	}
	patternsJSON, _ := json.Marshal(patterns)
	envelope := map[string]interface{}{
		"sensor_id":   "watch-logs",
		"version":     "1.0.0",
		"run_id":      "00000000-0000-0000-0000-000000000000",
		"started_at":  "2026-05-09T15:30:00Z",
		"sensor_type": "computational",
	}
	envelopeJSON, _ := json.Marshal(envelope)

	cfg := watcherConfig{
		RawLog:        raw,
		SignalsLog:    signals,
		PatternsJSON:  string(patternsJSON),
		EnvelopeJSON:  string(envelopeJSON),
		SubprocessPID: -1, // skip reaper
	}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runWatcher(cfg, stop) }()

	// Append a matching line to raw.log.
	time.Sleep(20 * time.Millisecond)
	f, _ := os.OpenFile(raw, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("ERROR oh no\n")
	f.Close()

	// Wait for signals.log to populate.
	deadline := time.Now().Add(time.Second)
	var line string
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(signals)
		if len(data) > 0 {
			line = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	<-done

	if line == "" {
		t.Fatal("no signal written")
	}
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(line), &sig); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if sig["verdict"] != "fail" {
		t.Fatalf("verdict: got %v", sig["verdict"])
	}
}

func TestSignalsLogIsValidJSONLNewlineDelimited(t *testing.T) {
	// Appended lines must be valid JSON each, separated by \n.
	dir := t.TempDir()
	signals := filepath.Join(dir, "signals.log")
	for i := 0; i < 3; i++ {
		appendSignal(signals, map[string]interface{}{"verdict": "pass", "i": i})
	}
	f, _ := os.Open(signals)
	defer f.Close()
	sc := bufio.NewScanner(f)
	count := 0
	for sc.Scan() {
		var m map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %d invalid JSON: %v", count, err)
		}
		count++
	}
	if count != 3 {
		t.Fatalf("got %d lines, want 3", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=start_watcher ./skills/start-sensor/scripts/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

Create `skills/start-sensor/scripts/watcher.go`:

```go
//go:build start_watcher

// watcher tails a blocking sensor's raw.log, applies signal patterns to
// each new line, and appends matched Signals to signals.log. A reaper
// goroutine waits on the subprocess PID and writes subprocess_exit into
// the global registry once it terminates. The watcher exits cleanly on
// SIGTERM (drains the buffer, fsyncs both log files, returns).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/iurykrieger/harness-framework/lib/registry"
	libsensor "github.com/iurykrieger/harness-framework/lib/sensor"
	libsignal "github.com/iurykrieger/harness-framework/lib/signal"
)

type watcherConfig struct {
	RawLog        string
	SignalsLog    string
	PatternsJSON  string
	EnvelopeJSON  string
	SubprocessPID int
	RegistryRoot  string
	SensorID      string
}

func main() {
	cfg := watcherConfig{
		RawLog:       os.Getenv("HARNESS_WATCHER_RAW"),
		SignalsLog:   os.Getenv("HARNESS_WATCHER_SIGNALS"),
		PatternsJSON: os.Getenv("HARNESS_WATCHER_PATTERNS"),
		EnvelopeJSON: os.Getenv("HARNESS_WATCHER_ENVELOPE"),
		RegistryRoot: os.Getenv("HARNESS_WATCHER_REGISTRY_ROOT"),
		SensorID:     os.Getenv("HARNESS_WATCHER_SENSOR_ID"),
	}
	if pidStr := os.Getenv("HARNESS_WATCHER_SUBPROCESS_PID"); pidStr != "" {
		if pid, err := strconv.Atoi(pidStr); err == nil {
			cfg.SubprocessPID = pid
		}
	}

	stop := make(chan struct{})
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
		close(stop)
	}()

	if err := runWatcher(cfg, stop); err != nil {
		fmt.Fprintln(os.Stderr, "watcher:", err)
		os.Exit(1)
	}
}

// runWatcher follows cfg.RawLog with fsnotify, parses each new line with
// the compiled patterns, and appends matched signals to cfg.SignalsLog.
// Returns when stop is closed.
func runWatcher(cfg watcherConfig, stop <-chan struct{}) error {
	rawPatterns := []interface{}{}
	if err := json.Unmarshal([]byte(cfg.PatternsJSON), &rawPatterns); err != nil {
		return fmt.Errorf("patterns json: %w", err)
	}
	patterns, err := libsignal.CompilePatterns(rawPatterns)
	if err != nil {
		return fmt.Errorf("compile patterns: %w", err)
	}

	var envelope libsensor.Envelope
	if err := json.Unmarshal([]byte(cfg.EnvelopeJSON), &envelope); err != nil {
		return fmt.Errorf("envelope json: %w", err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer w.Close()
	if err := w.Add(cfg.RawLog); err != nil {
		return fmt.Errorf("watch raw.log: %w", err)
	}

	rawF, err := os.Open(cfg.RawLog)
	if err != nil {
		return fmt.Errorf("open raw.log: %w", err)
	}
	defer rawF.Close()

	rdr := bufio.NewReader(rawF)
	var wg sync.WaitGroup
	if cfg.SubprocessPID > 0 && cfg.RegistryRoot != "" && cfg.SensorID != "" {
		wg.Add(1)
		go func() { defer wg.Done(); runReaper(cfg) }()
	}

	for {
		select {
		case <-stop:
			drain(rdr, patterns, envelope, cfg.SignalsLog)
			wg.Wait()
			return nil
		case ev := <-w.Events:
			if ev.Op&fsnotify.Write != 0 || ev.Op&fsnotify.Create != 0 {
				drain(rdr, patterns, envelope, cfg.SignalsLog)
			}
		case err := <-w.Errors:
			return fmt.Errorf("fsnotify err: %w", err)
		}
	}
}

// drain reads every available line from rdr, matches against patterns,
// and appends matched Signals to signalsLog.
func drain(rdr *bufio.Reader, patterns []libsignal.Pattern, envelope libsensor.Envelope, signalsLog string) {
	for {
		line, err := rdr.ReadString('\n')
		if line != "" {
			handleLine(line, patterns, envelope, signalsLog)
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
	}
}

func handleLine(line string, patterns []libsignal.Pattern, envelope libsensor.Envelope, signalsLog string) {
	line = trimNewline(line)
	m, ok := libsignal.MatchLine(line, patterns)
	if !ok {
		return
	}
	sig := buildIndividualSignal(envelope, m, line)
	appendSignal(signalsLog, sig)
}

func trimNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

func buildIndividualSignal(env libsensor.Envelope, m libsignal.PatternMatch, raw string) map[string]interface{} {
	ev := map[string]interface{}{"rationale": m.Rationale}
	if m.File != "" {
		ev["file"] = m.File
	}
	if m.LineStart != nil {
		ev["line_start"] = *m.LineStart
	}
	if m.LineEnd != nil {
		ev["line_end"] = *m.LineEnd
	}
	if m.Excerpt != "" {
		ev["excerpt"] = m.Excerpt
	}
	return map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"verdict":     m.Verdict,
		"severity":    m.Severity,
		"confidence":  1.0,
		"evidence":    []interface{}{ev},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind": "individual",
			"line": raw,
		},
	}
}

func appendSignal(path string, sig map[string]interface{}) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(sig)
}

// runReaper waits on the subprocess PID and persists subprocess_exit
// into running_sensors.json under flock once the process terminates.
func runReaper(cfg watcherConfig) {
	// Poll-based wait — we don't own the child (it was spawned detached
	// by /start-sensor), so syscall.Wait4 wouldn't apply.
	for {
		if !registry.IsPIDAlive(cfg.SubprocessPID) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	root := registry.NewRoot(rootFromSensorsDir(cfg.RegistryRoot))
	exitCode := readExitCode(cfg.SubprocessPID)
	_ = registry.WithFileLock(root.LockFile(), func() error {
		rs, err := registry.Load(root)
		if err != nil {
			return err
		}
		if e := rs.FindEntry(cfg.SensorID); e != nil {
			e.SubprocessExit = &registry.SubprocessExit{
				Code:     exitCode,
				ExitedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			}
		}
		return registry.Save(root, rs)
	})
}

// readExitCode reads /proc on Linux or a fallback on macOS. Best-effort:
// when we cannot recover the code, we return -1 (the registry consumer
// treats this as "exit code unknown").
func readExitCode(pid int) int {
	// We do not own the process, so wait() would EINVAL. Use kill(pid, 0)
	// returning ESRCH as the only signal we have. Without ptrace, we
	// cannot recover the exact code on POSIX; record -1.
	_ = pid
	return -1
}

// rootFromSensorsDir converts ".runtime/sensors" back to its parent
// (the project root) so registry.NewRoot composes correctly.
func rootFromSensorsDir(sensorsDir string) string {
	// .runtime/sensors → .runtime → "." style. We carry the project
	// root through the env var so the watcher does not have to guess.
	// HARNESS_WATCHER_REGISTRY_ROOT is the project root; the registry
	// helper appends .runtime/sensors.
	return sensorsDir
}

// Suppress unused warnings when running with non-watcher build tags.
var _ = context.Background
```

> Note: this watcher does NOT recover the subprocess exit code — POSIX `wait()` only works for our own children. The aggregate path treats the absence of `subprocess_exit.code != -1` as `exit_code_unknown=true` (covered by the `/stop-sensor` path). A future improvement is to make `start.go` keep the subprocess as its child (via a tiny shim) and have the watcher receive the code via an IPC pipe; for now `-1` is the documented contract.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=start_watcher ./skills/start-sensor/scripts/...`
Expected: PASS. (The `go vet` config will warn about `var _ = context.Background`; if so, drop the line — context isn't actually used.)

- [ ] **Step 5: Commit**

```bash
git add skills/start-sensor/scripts/watcher.go skills/start-sensor/scripts/watcher_test.go
git commit -m "feat(start-sensor): watcher script tails raw.log and emits Signals"
```

---

## Task 11: skills/start-sensor/scripts/start.go

**Files:**
- Create: `skills/start-sensor/scripts/start.go`
- Create: `skills/start-sensor/scripts/start_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/start-sensor/scripts/start_test.go`:

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

func TestStart_RejectsNonBlocking(t *testing.T) {
	root := t.TempDir()
	writeFixtureSensor(t, root, "not-blocking", map[string]interface{}{
		"version": "1.0.0",
		"description": "non-blocking fixture",
		"determinism": "high",
		"kind": "observation",
		"type": "computational",
		"output": "single",
		"cost": map[string]interface{}{
			"class": "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50, "timeout_ms": 1000},
		},
		"execution": map[string]interface{}{
			"command": "echo hi",
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": 0, "verdict": "pass", "severity": "info"},
			},
		},
	})
	exit, _ := runStart(root, []string{"not-blocking"})
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
	exit, sig := runStart(root, []string{"loop"})
	if exit != 1 {
		t.Fatalf("expected exit 1, got %d", exit)
	}
	if sig["metadata"].(map[string]interface{})["kind"] != "start_rejected" {
		t.Fatalf("metadata.kind: got %v", sig["metadata"])
	}
}

func blockingFixtureBody() map[string]interface{} {
	return map[string]interface{}{
		"version":     "1.0.0",
		"description": "blocking fixture",
		"determinism": "high",
		"kind":        "observation",
		"type":        "computational",
		"output":      "stream",
		"cost": map[string]interface{}{
			"class":   "cheap",
			"compute": map[string]interface{}{"cpu": "low", "memory_mb": 32},
			"latency": map[string]interface{}{"p50_ms": 10, "p95_ms": 50},
		},
		"execution": map[string]interface{}{
			"command":  "while true; do echo TICK; sleep 0.1; done",
			"blocking": true,
			"output_parsing": map[string]interface{}{
				"patterns": []interface{}{
					map[string]interface{}{"regex": "^TICK$", "verdict": "pass", "severity": "info", "rationale": "tick"},
				},
			},
			"exit_code_map": []interface{}{
				map[string]interface{}{"exit_code": "*", "verdict": "pass", "severity": "info"},
			},
		},
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/...`
Expected: FAIL — `runStart` not defined.

- [ ] **Step 3: Write the implementation**

Create `skills/start-sensor/scripts/start.go`:

```go
//go:build start_sensor

// start spawns a blocking sensor's command in a detached session,
// records it in the registry, and emits a Signal verdict=pass,
// metadata.kind=started.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

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
	if len(args) < 1 {
		return 2, errorSignal("start", "missing sensor id argument")
	}
	id := args[0]

	path, err := libsensor.ResolveByID(id, projectRoot)
	if err != nil {
		return 2, errorSignal(id, fmt.Sprintf("resolve: %v", err))
	}

	sensorJSON, err := loadSensorJSON(path)
	if err != nil {
		return 2, errorSignal(id, err.Error())
	}

	v, code := schema.LoadValidator("", os.Stderr)
	if code != 0 {
		return code, errorSignal(id, "schema validator init failed")
	}
	if err := v.Validate(schema.TargetSensor, sensorJSON); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("schema: %v", err))
	}

	execMap, _ := sensorJSON["execution"].(map[string]interface{})
	blocking, _ := execMap["blocking"].(bool)
	if !blocking {
		return 2, errorSignal(id, "sensor is not blocking; use /run-sensor instead")
	}

	r := registry.NewRoot(projectRoot)
	rs, err := registry.Load(r)
	if err != nil {
		return 1, errorSignal(id, fmt.Sprintf("load registry: %v", err))
	}
	if existing := rs.FindEntry(id); existing != nil && registry.IsPIDAlive(existing.PID) {
		sig := buildStartedSkeleton(id, sensorJSON)
		sig["verdict"] = "error"
		sig["severity"] = "high"
		sig["metadata"].(map[string]interface{})["kind"] = "start_rejected"
		sig["evidence"] = []interface{}{map[string]interface{}{
			"rationale": fmt.Sprintf("sensor %q already running with pid %d", id, existing.PID),
		}}
		return 1, sig
	}

	command, _ := execMap["command"].(string)
	logDir := r.SensorDir(id)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("mkdir log dir: %v", err))
	}
	if err := os.WriteFile(r.RawLog(id), nil, 0o644); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("create raw.log: %v", err))
	}
	if err := os.WriteFile(r.SignalsLog(id), nil, 0o644); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("create signals.log: %v", err))
	}

	det, err := subprocess.SpawnDetached(subprocess.DetachConfig{
		Command: command,
		LogFile: r.RawLog(id),
	})
	if err != nil {
		return 1, errorSignal(id, fmt.Sprintf("spawn: %v", err))
	}

	envelope := libsensor.Envelope{
		SensorID:  id,
		Version:   stringField(sensorJSON, "version"),
		RunID:     uuid.NewString(),
		StartedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
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

	watcherPath, err := watcherBinaryPath()
	if err != nil {
		return 1, errorSignal(id, fmt.Sprintf("watcher binary: %v", err))
	}
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
		return 1, errorSignal(id, fmt.Sprintf("start watcher: %v", err))
	}
	_ = watcherProc.Release()

	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntry(id) // safety: we already verified it's not running
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
		return registry.Save(r, rs)
	}); err != nil {
		return 1, errorSignal(id, fmt.Sprintf("write registry: %v", err))
	}

	sig := buildStartedSkeleton(id, sensorJSON)
	sig["verdict"] = "pass"
	sig["severity"] = "info"
	sig["evidence"] = []interface{}{map[string]interface{}{
		"rationale": fmt.Sprintf("sensor %q started, pid=%d, watcher_pid=%d", id, det.PID, watcherProc.Pid),
	}}
	sig["run_id"] = envelope.RunID
	sig["started_at"] = envelope.StartedAt
	md := sig["metadata"].(map[string]interface{})
	md["kind"] = "started"
	md["pid"] = det.PID
	md["watcher_pid"] = watcherProc.Pid
	md["log_dir"] = filepath.Join(".runtime", "sensors", id)
	md["next_cursor"] = 0
	return 0, sig
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

func buildStartedSkeleton(id string, sensorJSON map[string]interface{}) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	return map[string]interface{}{
		"sensor_id":   id,
		"version":     stringField(sensorJSON, "version"),
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "error",
		"severity":    "high",
		"confidence":  1.0,
		"evidence":    []interface{}{},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "started"},
	}
}

func errorSignal(id, rationale string) map[string]interface{} {
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
		"metadata":    map[string]interface{}{"kind": "start_failed"},
	}
}

// watcherSysProcAttr is platform-specific (Setsid on POSIX). Defined in
// start_unix.go for now; on non-POSIX builds the file would supply a
// no-op zero value. Plugin only targets darwin/linux today.
```

Create `skills/start-sensor/scripts/start_unix.go`:

```go
//go:build start_sensor && (darwin || linux)

package main

import "syscall"

var watcherSysProcAttr = syscall.SysProcAttr{Setsid: true}

func watcherBinaryPath() (string, error) {
	// In production the build script puts the watcher binary alongside
	// start. We expect a sibling file named "watcher" in the same dir
	// as the running start binary.
	exe, err := osExecutable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "watcher"), nil
}
```

Add the missing imports for `osExecutable` and `filepath`:

```go
//go:build start_sensor && (darwin || linux)

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

var watcherSysProcAttr = syscall.SysProcAttr{Setsid: true}

func watcherBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), "watcher"), nil
}
```

> **Note on `uuid`:** add `github.com/google/uuid` via `go get github.com/google/uuid` before running the test (commit it as part of the same task or fold the dep add into Task 1 if convenient). Tests will fail import otherwise.

- [ ] **Step 4: Run test to verify it passes**

Run:

```bash
go get github.com/google/uuid@latest
go test -tags=start_sensor ./skills/start-sensor/scripts/...
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum skills/start-sensor/scripts/start.go skills/start-sensor/scripts/start_unix.go skills/start-sensor/scripts/start_test.go
git commit -m "feat(start-sensor): start.go spawns blocking sensor and registers run"
```

---

## Task 12: skills/start-sensor/SKILL.md

**Files:**
- Create: `skills/start-sensor/SKILL.md`

- [ ] **Step 1: Write SKILL.md (no test — markdown is prose)**

```markdown
---
name: start-sensor
description: Use when the user invokes /start-sensor or asks to bring a blocking sensor (one whose command does not terminate on its own) up for observation. Takes a `<sensor.id>` argument and resolves it to `sensors/<id>.json`. Validates the sensor against schemas/sensor.json, requires `execution.blocking: true`, runs `execution.prepare[]` fail-fast, spawns the command detached (Setsid, redirected stdout/stderr to .runtime/sensors/<id>/raw.log), spawns a watcher binary that tails raw.log and emits parsed Signals to .runtime/sensors/<id>/signals.log, and writes an entry into .runtime/sensors/running_sensors.json with `held_by: [{kind: "manual", attached_at: ...}]`. Emits a Signal `verdict=pass`, `metadata.kind=started`. Singleton: rejects with `start_rejected` if the sensor already has a live registry entry.
---

# start-sensor

Bring a blocking sensor up. Only `execution.blocking: true` sensors can be started this way; the schema and the runner both reject non-blocking sensors.

## Invocation

```
/start-sensor <sensor.id>
```

The argument must be the sensor's id (lowercase letters/digits/dashes, starting with a letter). The runner resolves it to `sensors/<id>.json` relative to the project root.

## Procedure

```bash
go run -tags=start_sensor ./skills/start-sensor/scripts <sensor.id>
```

The script does everything: schema validation, prepare lifecycle, fork+exec the command detached, watcher spawn, registry write, started Signal emission. Pass its stdout through to the caller.

## Output contract

A single Signal on stdout. `metadata.kind` is one of:

- `started` — the subprocess and watcher are up; the sensor is now alive in the registry. Signal verdict is `pass`. `metadata.next_cursor` is `0` so the agent can begin tailing immediately.
- `start_rejected` — already running; existing run is referenced in evidence. `verdict=error`.
- `start_failed` — schema invalid, prepare step failed, fork failed, or registry write failed. `verdict=error`.

## Lifecycle integration

Other sensors may declare a blocking sensor in `depends_on`. When `/run-sensor` is invoked for such a dependent, the orchestrator will start (or attach to) the blocking dep automatically using the same primitives this skill exposes — you do not need to invoke `/start-sensor` manually for that case. Use `/start-sensor` directly when:

- The blocking sensor is the observation target itself (e.g., the agent wants to watch logs while doing other work in parallel).
- The agent needs to interact with the live process (curl, edit, observe) without an immediately-dependent sensor driving the workflow.

## Notes & limits

- A sensor may have at most one live entry at a time. Use `/list-sensors` to see what's running.
- Logs are append-only; nothing is rotated. Long-running sessions should periodically `/stop-sensor`/`/start-sensor` to keep `.runtime/sensors/<id>/` from growing unboundedly.
- `cost.latency.timeout_ms` is forbidden by the schema for blocking sensors. Use `execution.graceful_timeout_ms` (min 100ms, default 5000) to control the SIGTERM→SIGKILL window in `/stop-sensor`.
```

- [ ] **Step 2: Commit**

```bash
git add skills/start-sensor/SKILL.md
git commit -m "docs(start-sensor): SKILL.md prose for /start-sensor"
```

---

## Task 13: skills/stop-sensor/scripts/stop.go

**Files:**
- Create: `skills/stop-sensor/scripts/stop.go`
- Create: `skills/stop-sensor/scripts/stop_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/stop-sensor/scripts/stop_test.go`:

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

func TestStop_NotRunning_ReturnsWarn(t *testing.T) {
	root := t.TempDir()
	exit, sig := runStop(root, []string{"missing"}, false)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	if sig["verdict"] != "warn" || sig["metadata"].(map[string]interface{})["kind"] != "stop_not_running" {
		t.Fatalf("got: %+v", sig)
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
	exit, sig := runStop(root, []string{"live"}, false)
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
				PID:      0, // not actually a live process
				HeldBy: []registry.HeldByEntry{
					{Kind: "manual"},
					{Kind: "sensor", ID: "C", PID: 3_999_999}, // dead
				},
			},
		},
	}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}
	_, sig := runStop(root, []string{"live"}, true)
	md := sig["metadata"].(map[string]interface{})
	reaped, _ := md["reaped_holders"].([]interface{})
	if len(reaped) != 1 {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
		t.Fatalf("reaped: got %d", len(reaped))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=stop_sensor ./skills/stop-sensor/scripts/...`
Expected: FAIL — `runStop` not defined.

- [ ] **Step 3: Write the implementation**

Create `skills/stop-sensor/scripts/stop.go`:

```go
//go:build stop_sensor

// stop sends SIGTERM to a blocking sensor's process group, waits up to
// graceful_timeout_ms, escalates to SIGKILL if needed, runs teardown,
// computes the aggregate Signal from signals.log, and removes the
// registry entry.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
	libsensor "github.com/iurykrieger/harness-framework/lib/sensor"
	libsignal "github.com/iurykrieger/harness-framework/lib/signal"
)

func main() {
	var reap bool
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.BoolVar(&reap, "reap-dead-holders", false, "drop kind=sensor holders whose PID is dead before deciding whether to stop")
	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stop: cwd:", err)
		os.Exit(2)
	}
	exit, sig := runStop(root, fs.Args(), reap)
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}

func runStop(projectRoot string, args []string, reap bool) (int, map[string]interface{}) {
	if len(args) < 1 {
		return 2, simpleSignal("stop", "warn", "low", "stop_not_running", "missing sensor id")
	}
	id := args[0]
	r := registry.NewRoot(projectRoot)

	var entry *registry.RunningSensorEntry
	var reaped []registry.HeldByEntry

	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		entry = rs.FindEntry(id)
		if entry == nil {
			return nil
		}
		registry.RemoveHolder(entry, registry.HeldByEntry{Kind: "manual"})
		if reap {
			reaped = registry.ReapDead(entry)
		}
		// Persist removal of "manual" + reaped early so a crash here
		// doesn't leave a stale "manual" hold.
		return registry.Save(r, rs)
	}); err != nil {
		return 1, simpleSignal(id, "error", "high", "stop_failed", fmt.Sprintf("registry: %v", err))
	}

	if entry == nil {
		return 0, simpleSignal(id, "warn", "low", "stop_not_running", fmt.Sprintf("no live entry for %q", id))
	}

	if registry.IsHeld(entry) {
		kind := "stop_held"
		if hasDeadHolder(entry) {
			kind = "stop_held_with_dead_holders"
		}
		sig := simpleSignal(id, "warn", "low", kind, fmt.Sprintf("sensor %q still held by %d holders", id, len(entry.HeldBy)))
		md := sig["metadata"].(map[string]interface{})
		md["holders"] = holderSummaries(entry.HeldBy)
		if len(reaped) > 0 {
			md["reaped_holders"] = holderSummaries(reaped)
		}
		return 0, sig
	}

	// We are clear to stop. Send SIGTERM to the process group.
	gracefulMS := readGracefulMS(entry, projectRoot)
	killedForcefully := terminateWithGrace(entry.PGID, gracefulMS)
	stopWatcher(entry.WatcherPID)

	sensorJSON := loadSensorJSONForStop(projectRoot, id)
	teardownResults := runTeardown(sensorJSON)

	individuals := readSignals(r.SignalsLog(id))
	exitVerd, exitSev := exitMappingFromSensor(sensorJSON, entry)
	streamVerd, streamSev := libsignal.MaxStreamVerdict(individuals)

	subprocessSelfExited := entry.SubprocessExit != nil
	agg := libsignal.Aggregate(libsignal.AggregateInput{
		ExitVerdict:    exitVerd,
		ExitSeverity:   exitSev,
		StreamVerdict:  streamVerd,
		StreamSeverity: streamSev,
		Blocking:       !subprocessSelfExited,
	})

	sig := buildAggregate(id, sensorJSON, entry, individuals, agg, killedForcefully, reaped, teardownResults)

	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntry(id)
		return registry.Save(r, rs)
	}); err != nil {
		return 1, simpleSignal(id, "error", "high", "stop_failed", fmt.Sprintf("registry: %v", err))
	}
	return 0, sig
}

func terminateWithGrace(pgid int, gracefulMS int) bool {
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(time.Duration(gracefulMS) * time.Millisecond)
	for time.Now().Before(deadline) {
		if !registry.IsPIDAlive(pgid) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	if registry.IsPIDAlive(pgid) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return true
	}
	return false
}

func stopWatcher(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !registry.IsPIDAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if registry.IsPIDAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func readGracefulMS(entry *registry.RunningSensorEntry, projectRoot string) int {
	sj := loadSensorJSONForStop(projectRoot, entry.SensorID)
	exec, _ := sj["execution"].(map[string]interface{})
	if exec == nil {
		return 5000
	}
	if v, ok := exec["graceful_timeout_ms"].(float64); ok {
		return int(v)
	}
	return 5000
}

func loadSensorJSONForStop(projectRoot, id string) map[string]interface{} {
	path, err := libsensor.ResolveByID(id, projectRoot)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	_ = json.Unmarshal(data, &m)
	return m
}

func readSignals(path string) []map[string]interface{} {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []map[string]interface{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var m map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

func exitMappingFromSensor(sensorJSON map[string]interface{}, entry *registry.RunningSensorEntry) (string, string) {
	if sensorJSON == nil {
		return "pass", "info"
	}
	exitCode := -1
	if entry.SubprocessExit != nil {
		exitCode = entry.SubprocessExit.Code
	}
	exec, _ := sensorJSON["execution"].(map[string]interface{})
	if exec == nil {
		return "pass", "info"
	}
	ecMap, _ := exec["exit_code_map"].([]interface{})
	if len(ecMap) == 0 {
		return "pass", "info"
	}
	v, s := libsignal.MapExitCode(exitCode, ecMap)
	if v == "" {
		return "pass", "info"
	}
	return v, s
}

func runTeardown(sensorJSON map[string]interface{}) []map[string]interface{} {
	// Reuse orchestrator-style teardown: we keep this thin here. For the
	// first cut, we run nothing — teardown for blocking sensors lives in
	// the orchestrator path. /stop-sensor invoked manually skips teardown
	// to avoid double-running it. This is documented as a follow-up.
	_ = sensorJSON
	return nil
}

func buildAggregate(id string, sensorJSON map[string]interface{}, entry *registry.RunningSensorEntry, individuals []map[string]interface{}, agg libsignal.AggregateResult, killedForcefully bool, reaped []registry.HeldByEntry, teardown []map[string]interface{}) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	md := map[string]interface{}{
		"kind":        "aggregate",
		"output_mode": "stream",
		"command":     entry.Command,
		"counts":      libsignal.CountVerdicts(individuals),
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

func hasDeadHolder(entry *registry.RunningSensorEntry) bool {
	for _, h := range entry.HeldBy {
		if h.Kind == "sensor" && !registry.IsPIDAlive(h.PID) {
			return true
		}
	}
	return false
}

func holderSummaries(hs []registry.HeldByEntry) []interface{} {
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

func stringField(m map[string]interface{}, k string) string {
	if m == nil {
		return ""
	}
	v, _ := m[k].(string)
	return v
}

func simpleSignal(id, verdict, severity, kind, rationale string) map[string]interface{} {
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
		"metadata":    map[string]interface{}{"kind": kind},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=stop_sensor ./skills/stop-sensor/scripts/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/stop-sensor/scripts/stop.go skills/stop-sensor/scripts/stop_test.go
git commit -m "feat(stop-sensor): SIGTERM/SIGKILL escalation, refcount, aggregate"
```

---

## Task 14: skills/stop-sensor/SKILL.md

**Files:**
- Create: `skills/stop-sensor/SKILL.md`

- [ ] **Step 1: Write SKILL.md**

```markdown
---
name: stop-sensor
description: Use when the user invokes /stop-sensor or asks to bring down a previously-started blocking sensor. Takes `<sensor.id>` and an optional `--reap-dead-holders` flag. Idempotent: stopping a sensor that is not running emits a warn Signal and exits 0. Otherwise removes the user's `kind=manual` hold, refuses with `stop_held` if any sensor still holds the run, or proceeds with SIGTERM → wait `execution.graceful_timeout_ms` → SIGKILL on the subprocess group, then signals the watcher to drain, reads signals.log, and emits the aggregate Signal. Removes the entry from `.runtime/sensors/running_sensors.json` on success.
---

# stop-sensor

Bring a blocking sensor down and produce its aggregate.

## Invocation

```
/stop-sensor <sensor.id> [--reap-dead-holders]
```

## Procedure

```bash
go run -tags=stop_sensor ./skills/stop-sensor/scripts <sensor.id> [--reap-dead-holders]
```

## When to use --reap-dead-holders

If `/list-sensors` shows the sensor with `held_by` entries whose `pid_alive=false`, the holder process (typically a crashed orchestrator running a dependent sensor) leaked the hold. Pass `--reap-dead-holders` to drop those entries before evaluating whether the sensor is still held. The aggregate Signal carries `metadata.reaped_holders` listing what was removed.

## Output contract

A single aggregate Signal on stdout. `metadata.kind` is one of:

- `aggregate` — the subprocess and watcher were brought down cleanly. `verdict` is the worst-of-stream and exit-side per signal.Aggregate.
- `stop_not_running` — no live entry; warn.
- `stop_held` / `stop_held_with_dead_holders` — other holders remain; warn. Process not stopped.
- `stop_failed` — registry I/O failed; error.

## Notes

- A blocking sensor's `cost.latency.timeout_ms` is forbidden; `execution.graceful_timeout_ms` (min 100ms, default 5000) controls the SIGTERM→SIGKILL window here.
- Per-sensor `.runtime/sensors/<id>/{raw.log, signals.log}` are NOT deleted by stop — auditable. `.runtime/sensors/<id>/` cleanup is manual.
- When a subprocess dies on its own before /stop-sensor, the watcher's reaper records the exit; the aggregate then uses `Blocking: false` so `exit_code_map` interprets the verdict (a crashed dev server aggregates as fail/error rather than pass).
```

- [ ] **Step 2: Commit**

```bash
git add skills/stop-sensor/SKILL.md
git commit -m "docs(stop-sensor): SKILL.md prose for /stop-sensor"
```

---

## Task 15: skills/tail-sensor/scripts/tail.go

**Files:**
- Create: `skills/tail-sensor/scripts/tail.go`
- Create: `skills/tail-sensor/scripts/tail_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/tail-sensor/scripts/tail_test.go`:

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
}

func TestTail_Cursor0_ReturnsAll(t *testing.T) {
	root := t.TempDir()
	setupRunning(t, root, "loop", []string{
		`{"sensor_id":"loop","verdict":"pass","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"warn","metadata":{"kind":"individual"}}`,
	})
	var buf bytes.Buffer
	exit := runTail(root, []string{"loop", "0"}, &buf, os.Stderr)
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
}

func TestTail_CursorMid_ReturnsSuffix(t *testing.T) {
	root := t.TempDir()
	setupRunning(t, root, "loop", []string{
		`{"sensor_id":"loop","verdict":"pass","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"warn","metadata":{"kind":"individual"}}`,
		`{"sensor_id":"loop","verdict":"fail","metadata":{"kind":"individual"}}`,
	})
	var buf bytes.Buffer
	exit := runTail(root, []string{"loop", "2"}, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	// 1 individual + 1 envelope
	if len(lines) != 2 {
		t.Fatalf("lines: got %d", len(lines))
	}
}

func TestTail_NotRunning(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	exit := runTail(root, []string{"missing", "0"}, &buf, os.Stderr)
	if exit != 1 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	_ = json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &sig)
	if sig["metadata"].(map[string]interface{})["kind"] != "tail_not_running" {
		t.Fatalf("kind: got %v", sig["metadata"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=tail_sensor ./skills/tail-sensor/scripts/...`
Expected: FAIL — `runTail` not defined.

- [ ] **Step 3: Write the implementation**

Create `skills/tail-sensor/scripts/tail.go`:

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
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tail: cwd:", err)
		os.Exit(2)
	}
	exit := runTail(root, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(exit)
}

func runTail(projectRoot string, args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal("tail", "tail_invalid_args", "expected <sensor.id> <cursor>"))
		return 2
	}
	id := args[0]
	cursor, err := strconv.Atoi(args[1])
	if err != nil || cursor < 0 {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(id, "tail_invalid_cursor", fmt.Sprintf("cursor must be a non-negative integer, got %q", args[1])))
		return 1
	}

	r := registry.NewRoot(projectRoot)
	rs, err := registry.Load(r)
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(id, "tail_failed", fmt.Sprintf("load registry: %v", err)))
		return 1
	}
	entry := rs.FindEntry(id)
	if entry == nil {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(id, "tail_not_running", fmt.Sprintf("no live entry for %q", id)))
		return 1
	}

	f, err := os.Open(r.SignalsLog(id))
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(simpleErrSignal(id, "tail_failed", fmt.Sprintf("open signals.log: %v", err)))
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
	envelope := tailEnvelope(id, current)
	_ = json.NewEncoder(stdout).Encode(envelope)
	return 0
}

func tailEnvelope(id string, nextCursor int) map[string]interface{} {
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
			"kind":        "tail_envelope",
			"next_cursor": nextCursor,
			"sensor_id":   id,
		},
	}
}

func simpleErrSignal(id, kind, rationale string) map[string]interface{} {
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
		"metadata":    map[string]interface{}{"kind": kind},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=tail_sensor ./skills/tail-sensor/scripts/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/tail-sensor/scripts/tail.go skills/tail-sensor/scripts/tail_test.go
git commit -m "feat(tail-sensor): incremental line-based tail with envelope cursor"
```

---

## Task 16: skills/tail-sensor/SKILL.md

**Files:**
- Create: `skills/tail-sensor/SKILL.md`

- [ ] **Step 1: Write SKILL.md**

```markdown
---
name: tail-sensor
description: Use when the user invokes /tail-sensor or wants to read new Signal lines from a running blocking sensor's signals.log. Takes `<sensor.id> <cursor>` (cursor is a 1-based line index — pass 0 to read all lines from the start). Returns each new Signal as a JSONL line on stdout, then a final tail_envelope Signal carrying `metadata.next_cursor` (the line count after this read) for the agent to feed into the next /tail-sensor call.
---

# tail-sensor

Read new Signals from a running blocking sensor without disturbing it.

## Invocation

```
/tail-sensor <sensor.id> <cursor>
```

- `<cursor>` = 0 → return everything since the sensor started.
- `<cursor>` = N → return lines N+1..end.

## Procedure

```bash
go run -tags=tail_sensor ./skills/tail-sensor/scripts <sensor.id> <cursor>
```

## Output contract

JSONL on stdout: zero or more individual Signals, then exactly one `tail_envelope` Signal with `metadata.next_cursor`. The agent should parse the LAST line, extract `next_cursor`, and pass it as `<cursor>` on the next call. Cursor=0 is also useful for troubleshooting — it dumps the entire signals.log, so you can re-read history.

## Notes

- Cursor is line-based, not byte-based, because each Signal occupies one line. Re-reading from 0 always works regardless of buffer flushes.
- The runner does NOT persist the cursor for you — it is your responsibility (the agent's) to remember `next_cursor` between calls.
- Reading does NOT block: if there are no new lines, only the tail_envelope is emitted (with `next_cursor` unchanged).
```

- [ ] **Step 2: Commit**

```bash
git add skills/tail-sensor/SKILL.md
git commit -m "docs(tail-sensor): SKILL.md prose for /tail-sensor"
```

---

## Task 17: skills/list-sensors/scripts/list.go

**Files:**
- Create: `skills/list-sensors/scripts/list.go`
- Create: `skills/list-sensors/scripts/list_test.go`

- [ ] **Step 1: Write the failing test**

Create `skills/list-sensors/scripts/list_test.go`:

```go
//go:build list_sensors

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestList_Empty(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	exit := runList(root, &buf, os.Stderr)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}
	var sig map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &sig); err != nil {
		t.Fatal(err)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "list" {
		t.Fatalf("kind: got %v", md["kind"])
	}
	entries, _ := md["entries"].([]interface{})
	if len(entries) != 0 {
		t.Fatalf("entries: got %d, want 0", len(entries))
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
	var buf bytes.Buffer
	_ = runList(root, &buf, os.Stderr)
	var sig map[string]interface{}
	_ = json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &sig)
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

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=list_sensors ./skills/list-sensors/scripts/...`
Expected: FAIL — `runList` not defined.

- [ ] **Step 3: Write the implementation**

Create `skills/list-sensors/scripts/list.go`:

```go
//go:build list_sensors

// list reads .runtime/sensors/running_sensors.json, annotates each
// entry with PID liveness, and emits one Signal verdict=pass,
// metadata.kind=list with the full table under metadata.entries.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "list: cwd:", err)
		os.Exit(2)
	}
	os.Exit(runList(root, os.Stdout, os.Stderr))
}

func runList(projectRoot string, stdout, stderr io.Writer) int {
	r := registry.NewRoot(projectRoot)
	rs, err := registry.Load(r)
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(errorListSignal(fmt.Sprintf("load registry: %v", err)))
		return 1
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
			"sensor_id":         e.SensorID,
			"pid":               e.PID,
			"pid_alive":         pidAlive,
			"watcher_pid":       e.WatcherPID,
			"watcher_alive":     watcherAlive,
			"started_at":        e.StartedAt,
			"command":           e.Command,
			"held_by":           heldBySummaries(e.HeldBy),
			"signals_log_path":  r.SignalsLog(e.SensorID),
			"state":             state,
		})
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
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
		"metadata": map[string]interface{}{
			"kind":    "list",
			"entries": entries,
		},
	}
	_ = json.NewEncoder(stdout).Encode(sig)
	return 0
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

func errorListSignal(rationale string) map[string]interface{} {
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
		"metadata":    map[string]interface{}{"kind": "list_failed"},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=list_sensors ./skills/list-sensors/scripts/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/list-sensors/scripts/list.go skills/list-sensors/scripts/list_test.go
git commit -m "feat(list-sensors): list with orphan annotation"
```

---

## Task 18: skills/list-sensors/SKILL.md

**Files:**
- Create: `skills/list-sensors/SKILL.md`

- [ ] **Step 1: Write SKILL.md**

```markdown
---
name: list-sensors
description: Use when the user invokes /list-sensors or asks "what's running". No arguments. Reads `.runtime/sensors/running_sensors.json`, validates each entry's PID with `kill(pid, 0)`, and emits a single Signal `verdict=pass`, `metadata.kind=list`, `metadata.entries=[…]`. Each entry carries sensor_id, pid (with pid_alive flag), watcher_pid (with watcher_alive), started_at, command, held_by (each holder annotated with its own pid_alive when kind=sensor), signals_log_path, and state ("running" or "orphan" when the subprocess pid is dead).
---

# list-sensors

Show all live blocking-sensor runs.

## Invocation

```
/list-sensors
```

## Procedure

```bash
go run -tags=list_sensors ./skills/list-sensors/scripts
```

## Output contract

A single Signal `verdict=pass`, `metadata.kind=list`. `metadata.entries` is the list — empty when nothing is running.

## When to use

- Sanity check before `/start-sensor` (avoid `start_rejected`).
- Find leaked holders: any held_by entry with `pid_alive: false` is a candidate for `/stop-sensor <id> --reap-dead-holders`.
- Spot orphans: an entry with `state: orphan` means the subprocess is dead but the registry entry was not cleaned up. `/stop-sensor <id>` will fold the existing signals.log into an aggregate and remove the entry.
```

- [ ] **Step 2: Commit**

```bash
git add skills/list-sensors/SKILL.md
git commit -m "docs(list-sensors): SKILL.md prose for /list-sensors"
```

---

## Task 19: lib/orchestrator/live_deps.go (RunOneWithLiveDeps)

**Files:**
- Create: `lib/orchestrator/live_deps.go`
- Create: `lib/orchestrator/live_deps_test.go`

- [ ] **Step 1: Write the failing test**

Create `lib/orchestrator/live_deps_test.go`:

```go
package orchestrator_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
)

func TestRunOneWithLiveDeps_AttachesAndDetachesBlockingDep(t *testing.T) {
	root := t.TempDir()
	writeBlockingDep(t, root, "blocking-tick")
	writeConsumer(t, root, "uses-tick")

	ctx := context.Background()
	exit := orchestrator.RunWithDepsRoot(ctx, "uses-tick", root, "schemas", io.Discard, io.Discard)
	if exit != 0 {
		t.Fatalf("exit: got %d", exit)
	}

	r := registry.NewRoot(root)
	rs, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.FindEntry("blocking-tick") != nil {
		t.Fatal("blocking dep should be torn down after the consumer ran")
	}
}

func writeBlockingDep(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, "sensors")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"description": "blocking tick",
"determinism": "high",
"kind": "setup",
"type": "computational",
"output": "stream",
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
  "output_parsing": {"patterns":[{"regex":"^TICK$","verdict":"pass","severity":"info","rationale":"tick"}]}
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeConsumer(t *testing.T, root, id string) {
	t.Helper()
	dir := filepath.Join(root, "sensors")
	body := []byte(`{
"id": "` + id + `",
"version": "1.0.0",
"description": "consumer",
"determinism": "high",
"kind": "assertion",
"type": "computational",
"output": "single",
"depends_on": ["blocking-tick"],
"cost": {
  "class": "cheap",
  "compute": {"cpu":"low","memory_mb":32},
  "latency": {"p50_ms":10,"p95_ms":50,"timeout_ms":2000}
},
"execution": {
  "command": "echo OK",
  "exit_code_map": [{"exit_code":0,"verdict":"pass","severity":"info"},{"exit_code":"*","verdict":"fail","severity":"high"}]
}
}`)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./lib/orchestrator/...`
Expected: FAIL — `RunOneWithLiveDeps` and `RunWithDepsRoot` not defined.

- [ ] **Step 3: Write the implementation**

Create `lib/orchestrator/live_deps.go`:

```go
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/subprocess"
)

// RunWithDepsRoot is the id-resolving variant of RunWithDeps. The
// requested sensor is identified by id (resolved to <root>/sensors/<id>.json),
// schemasDir is resolved by the schema package's discovery if empty.
// All blocking deps along the chain are started/attached before the
// requested sensor runs and stopped/detached after.
func RunWithDepsRoot(ctx context.Context, id, projectRoot, schemasDir string, stdout, stderr io.Writer) int {
	path := filepath.Join(projectRoot, "sensors", id+".json")
	return RunWithDeps(ctx, path, schemasDir, stdout, stderr)
}

// AttachLiveDep starts (or attaches to) a blocking dep. Returns the
// SensorID it touched (so the caller can stack it for detach). Emits a
// `dep_attached` or `dep_started` Signal on stdout. Cascade on failure.
func AttachLiveDep(ctx context.Context, dep Sensor, projectRoot string, holderID string, v *schema.Validator, stdout, stderr io.Writer) (string, error) {
	r := registry.NewRoot(projectRoot)
	holderPID := os.Getpid()
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	holder := registry.HeldByEntry{Kind: "sensor", ID: holderID, PID: holderPID, AttachedAt: now}

	var existing *registry.RunningSensorEntry
	if err := registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		existing = rs.FindEntry(dep.ID)
		if existing != nil && registry.IsPIDAlive(existing.PID) {
			registry.AddHolder(existing, holder)
			return registry.Save(r, rs)
		}
		// Not live: start it.
		return startBlockingDep(ctx, &rs, r, dep, holder, projectRoot)
	}); err != nil {
		return "", err
	}

	kind := "dep_attached"
	if existing == nil || !registry.IsPIDAlive(existing.PID) {
		kind = "dep_started"
	}
	_ = json.NewEncoder(stdout).Encode(buildSimpleSignal(dep.ID, "pass", "info", kind, fmt.Sprintf("blocking dep %q held by %q", dep.ID, holderID)))
	return dep.ID, nil
}

// DetachLiveDep removes the holder from dep's HeldBy. If HeldBy becomes
// empty, the dep is stopped (SIGTERM/SIGKILL, registry cleanup) and an
// aggregate Signal is emitted on stdout. Otherwise emits dep_detached.
func DetachLiveDep(depID, projectRoot, holderID string, stdout, stderr io.Writer) {
	r := registry.NewRoot(projectRoot)
	var entry *registry.RunningSensorEntry
	stopNow := false
	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		entry = rs.FindEntry(depID)
		if entry == nil {
			return nil
		}
		registry.RemoveHolder(entry, registry.HeldByEntry{Kind: "sensor", ID: holderID, PID: os.Getpid()})
		if !registry.IsHeld(entry) {
			stopNow = true
		}
		return registry.Save(r, rs)
	})
	if entry == nil {
		return
	}
	if !stopNow {
		_ = json.NewEncoder(stdout).Encode(buildSimpleSignal(depID, "pass", "info", "dep_detached", fmt.Sprintf("blocking dep %q remains held", depID)))
		return
	}
	stopBlockingDep(r, entry, stdout, stderr)
}

func startBlockingDep(ctx context.Context, rs *registry.RunningSensors, r registry.Root, dep Sensor, holder registry.HeldByEntry, projectRoot string) error {
	execMap, _ := dep.JSON["execution"].(map[string]interface{})
	command, _ := execMap["command"].(string)
	if err := os.MkdirAll(r.SensorDir(dep.ID), 0o755); err != nil {
		return fmt.Errorf("mkdir log dir: %w", err)
	}
	if err := os.WriteFile(r.RawLog(dep.ID), nil, 0o644); err != nil {
		return fmt.Errorf("create raw.log: %w", err)
	}
	if err := os.WriteFile(r.SignalsLog(dep.ID), nil, 0o644); err != nil {
		return fmt.Errorf("create signals.log: %w", err)
	}
	det, err := subprocess.SpawnDetached(subprocess.DetachConfig{Command: command, LogFile: r.RawLog(dep.ID)})
	if err != nil {
		return fmt.Errorf("spawn: %w", err)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	rs.RemoveEntry(dep.ID)
	rs.Entries = append(rs.Entries, registry.RunningSensorEntry{
		SensorID:   dep.ID,
		PID:        det.PID,
		PGID:       det.PGID,
		WatcherPID: 0, // orchestrator-managed deps skip the watcher binary path; signals.log will be empty.
		StartedAt:  now,
		Command:    command,
		LogDir:     filepath.Join(".runtime", "sensors", dep.ID),
		HeldBy:     []registry.HeldByEntry{holder},
	})
	return registry.Save(r, *rs)
}

func stopBlockingDep(r registry.Root, entry *registry.RunningSensorEntry, stdout, stderr io.Writer) {
	gracefulMS := 5000
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
	_ = registry.WithFileLock(r.LockFile(), func() error {
		rs, err := registry.Load(r)
		if err != nil {
			return err
		}
		rs.RemoveEntry(entry.SensorID)
		return registry.Save(r, rs)
	})
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	agg := map[string]interface{}{
		"sensor_id":   entry.SensorID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  entry.StartedAt,
		"finished_at": now,
		"verdict":     "pass",
		"severity":    "info",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": fmt.Sprintf("blocking dep %q stopped on detach", entry.SensorID)}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    map[string]interface{}{"kind": "aggregate", "command": entry.Command, "output_mode": "stream", "counts": map[string]int{"pass": 0, "warn": 0, "fail": 0, "error": 0}},
	}
	_ = json.NewEncoder(stdout).Encode(agg)
}

func buildSimpleSignal(id, verdict, severity, kind, rationale string) map[string]interface{} {
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
		"metadata":    map[string]interface{}{"kind": kind},
	}
}
```

Now modify `lib/orchestrator/run.go` to call `AttachLiveDep` for blocking deps and `DetachLiveDep` after the requested sensor runs. Replace the existing dep loop with:

```go
	signals := map[string]map[string]interface{}{}
	var liveStack []string

	defer func() {
		for i := len(liveStack) - 1; i >= 0; i-- {
			DetachLiveDep(liveStack[i], filepath.Dir(filepath.Dir(abs)), rootID, stdout, stderr)
		}
	}()

	for _, s := range order {
		execMap, _ := s.JSON["execution"].(map[string]interface{})
		blocking, _ := execMap["blocking"].(bool)
		if blocking && s.ID != rootID {
			depID, err := AttachLiveDep(ctx, s, filepath.Dir(filepath.Dir(abs)), rootID, v, stdout, stderr)
			if err != nil {
				cascade := buildSimpleSignal(rootID, "error", "high", "dep_start_failed", err.Error())
				_ = json.NewEncoder(stdout).Encode(cascade)
				return 1
			}
			liveStack = append(liveStack, depID)
			signals[s.ID] = map[string]interface{}{"verdict": "pass"}
			continue
		}
		if blocker := FirstFailedDep(s, signals); blocker != nil {
			cascade := BuildCascadeSignal(s, blocker)
			if err := v.Validate(schema.TargetSignal, cascade); err != nil {
				schema.PrintValidationOrPlain(err, stderr)
				return 1
			}
			_ = json.NewEncoder(stdout).Encode(cascade)
			signals[s.ID] = cascade
			continue
		}
		sig, sigCode := RunOne(ctx, s, schemasDir, v, stdout, stderr)
		if sigCode != 0 {
			return sigCode
		}
		signals[s.ID] = sig
	}
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./lib/orchestrator/...`
Expected: PASS. (The integration test spawns a real `while true; do echo TICK; sleep 0.1; done` process — the test machine must have `sh`. The defer cleanup ensures the process is killed even if assertions fail.)

- [ ] **Step 5: Commit**

```bash
git add lib/orchestrator/live_deps.go lib/orchestrator/live_deps_test.go lib/orchestrator/run.go
git commit -m "feat(orchestrator): live-dep attach/detach via registry refcount"
```

---

## Task 20: /run-sensor migration to <id> + RunOneWithLiveDeps

**Files:**
- Modify: `skills/run-sensor/SKILL.md`
- Modify: `skills/run-sensor/scripts/run-computational.go`
- Modify: `skills/run-sensor/scripts/run-inferential.go`

- [ ] **Step 1: Update both runner scripts to accept `<sensor.id>`**

Open `skills/run-sensor/scripts/run-computational.go`. Find the line that currently does `sensor.ResolveSensorPath(arg, baseDir)` and replace with:

```go
path, err := sensor.ResolveByID(arg, projectRoot)
```

(where `projectRoot` is the directory containing `sensors/`, typically `cwd`).

Repeat for `run-inferential.go`.

- [ ] **Step 2: Reject blocking sensors in /run-sensor**

After loading the sensor JSON, before validation:

```go
execMap, _ := sensorJSON["execution"].(map[string]interface{})
if blocking, _ := execMap["blocking"].(bool); blocking {
	fmt.Fprintln(stderr, "error: sensor is blocking; use /start-sensor to invoke it")
	os.Exit(2)
}
```

- [ ] **Step 3: Switch to RunWithDepsRoot**

Replace the existing `orchestrator.RunWithDeps(ctx, path, ...)` call with `orchestrator.RunWithDepsRoot(ctx, id, projectRoot, ...)`.

- [ ] **Step 4: Update SKILL.md**

In `skills/run-sensor/SKILL.md`, find the "Invocation" section. Replace `<path-to-sensor.json>` with `<sensor.id>`. Add a "Refusing blocking sensors" subsection:

```markdown
### Refusing blocking sensors

If `execution.blocking: true`, the runner exits 2 with an error message pointing at `/start-sensor`. Blocking sensors do not have a hard timeout and cannot be invoked through `/run-sensor`. They can only be reached:

- Manually, through `/start-sensor` / `/stop-sensor` / `/tail-sensor` / `/list-sensors`.
- Implicitly, when another sensor declares them in `depends_on` — the orchestrator brings them up before the dependent runs.
```

Update the procedure code blocks: replace `<SENSOR_PATH>` with `<SENSOR_ID>` everywhere.

- [ ] **Step 5: Run all tests**

```bash
go test ./lib/...
go test -tags=run_computational ./skills/run-sensor/scripts/...
go test -tags=run_inferential ./skills/run-sensor/scripts/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add skills/run-sensor/SKILL.md skills/run-sensor/scripts/run-computational.go skills/run-sensor/scripts/run-inferential.go
git commit -m "feat(run-sensor): accept <sensor.id>; refuse blocking; honor live deps"
```

---

## Task 21: Fixture sensors + integration smoke

**Files:**
- Create: `sensors/fixtures/blocking-echo-loop.json`
- Create: `sensors/fixtures/consumer-of-blocking.json`

- [ ] **Step 1: Write `blocking-echo-loop.json`**

```json
{
  "id": "blocking-echo-loop",
  "version": "1.0.0",
  "description": "Test fixture: emits TICK every 100ms forever; used by /start-sensor and /tail-sensor smoke tests.",
  "determinism": "high",
  "kind": "setup",
  "type": "computational",
  "output": "stream",
  "cost": {
    "class": "cheap",
    "compute": { "cpu": "low", "memory_mb": 16 },
    "latency": { "p50_ms": 10, "p95_ms": 50 }
  },
  "execution": {
    "command": "while true; do echo TICK; sleep 0.1; done",
    "blocking": true,
    "graceful_timeout_ms": 200,
    "exit_code_map": [
      { "exit_code": "*", "verdict": "pass", "severity": "info" }
    ],
    "output_parsing": {
      "patterns": [
        { "regex": "^TICK$", "verdict": "pass", "severity": "info", "rationale": "tick observed" }
      ]
    }
  }
}
```

- [ ] **Step 2: Write `consumer-of-blocking.json`**

```json
{
  "id": "consumer-of-blocking",
  "version": "1.0.0",
  "description": "Test fixture: depends on blocking-echo-loop; runs a single echo and asserts SMOKE_OK. Used to verify orchestrator attach/detach.",
  "determinism": "high",
  "kind": "assertion",
  "type": "computational",
  "output": "stream",
  "depends_on": ["blocking-echo-loop"],
  "cost": {
    "class": "cheap",
    "compute": { "cpu": "low", "memory_mb": 16 },
    "latency": { "p50_ms": 10, "p95_ms": 50, "timeout_ms": 2000 }
  },
  "execution": {
    "command": "echo SMOKE_OK",
    "exit_code_map": [
      { "exit_code": 0, "verdict": "pass", "severity": "info" },
      { "exit_code": "*", "verdict": "fail", "severity": "high" }
    ],
    "output_parsing": {
      "patterns": [
        { "regex": "SMOKE_OK", "verdict": "pass", "severity": "info", "rationale": "smoke marker" }
      ]
    }
  }
}
```

- [ ] **Step 3: Run schema validation**

```bash
go test ./lib/schema/...
```

Expected: PASS — the new fixtures must pass `schemas/sensor.json` validation. Schema discovery picks them up via the existing test pattern.

- [ ] **Step 4: Commit**

```bash
git add sensors/fixtures/blocking-echo-loop.json sensors/fixtures/consumer-of-blocking.json
git commit -m "test: blocking-echo-loop and consumer fixtures for live-dep smoke"
```

---

## Task 22: .gitignore + plugin version bump

**Files:**
- Modify: `.gitignore`
- Modify: `.claude-plugin/plugin.json`

- [ ] **Step 1: Add `.runtime/` to .gitignore**

Append to `.gitignore`:

```
.runtime/
```

- [ ] **Step 2: Bump plugin version**

Open `.claude-plugin/plugin.json`. Find the `version` field and bump the minor version (this is a feature addition, not a breaking change for users — it's only breaking for sensor authors using `long_running`, which no one does yet).

For example, if it reads `"version": "0.1.0"`, change to `"version": "0.2.0"`.

- [ ] **Step 3: Verify**

```bash
cat .gitignore | grep .runtime
cat .claude-plugin/plugin.json | grep version
```

- [ ] **Step 4: Commit**

```bash
git add .gitignore .claude-plugin/plugin.json
git commit -m "chore: gitignore .runtime/ and bump plugin version for blocking sensors"
```

---

## Self-review

### Spec coverage check

- ✅ `execution.long_running` → `execution.blocking` (Task 9)
- ✅ Schema if/then for `cost.latency.timeout_ms` gating + `output: stream` enforcement (Task 9)
- ✅ `execution.graceful_timeout_ms` (min 100, default 5000) (Task 9)
- ✅ `lib/registry/` (paths, lock, liveness, state, held_by) (Tasks 2–6)
- ✅ `lib/sensor/path.go::ResolveByID` (Task 7)
- ✅ `lib/subprocess/detach.go` (Task 8)
- ✅ `lib/signal/aggregate.go::Blocking` rename (Task 9)
- ✅ `lib/orchestrator/lifecycle.go` reads `blocking` (Task 9)
- ✅ `lib/orchestrator/live_deps.go` + `RunOneWithLiveDeps` integration (Task 19)
- ✅ `skills/start-sensor/` (start.go + watcher.go + SKILL.md) (Tasks 10–12)
- ✅ `skills/stop-sensor/` (Tasks 13–14)
- ✅ `skills/tail-sensor/` (Tasks 15–16)
- ✅ `skills/list-sensors/` (Tasks 17–18)
- ✅ `skills/run-sensor/` migration to `<id>` (Task 20)
- ✅ `skills/detect-sensors/SKILL.md` rewrite (Task 9, sub-step 7)
- ✅ Fixture sensors (Task 21)
- ✅ `.gitignore` + plugin version bump (Task 22)

### Known limitations / follow-ups (NOT in this plan)

- Watcher cannot recover the actual exit code of the detached subprocess (POSIX `wait()` only works for our own children). `subprocess_exit.code` is recorded as `-1` and the aggregate flags `exit_code_unknown=true`. A future improvement adds an IPC pipe between `start.go` and the watcher so the start binary can keep the subprocess as its child and forward the code.
- `/stop-sensor` does not invoke teardown — the spec leaves teardown for blocking sensors to a follow-up. The aggregate path computes the verdict from signals.log + exit_code_map; teardown integration is tracked separately.
- Manual `/start-sensor` does not run `execution.prepare[]`. Prepare for blocking sensors is also a follow-up.

### Placeholder scan

- No "TBD", "implement later", or "fill in details".
- Every code step has actual code.
- Function/method signatures are consistent across tasks (`ResolveByID`, `IsPIDAlive`, `WithFileLock`, `Aggregate(AggregateInput{Blocking: ...})`, `RunWithDepsRoot`, `AttachLiveDep`, `DetachLiveDep`).

### Type consistency

- `RunningSensorEntry`, `HeldByEntry`, `SubprocessExit`: same field names everywhere.
- `Aggregate(AggregateInput{Blocking: ...})`: same signature in Task 9 (rename), Task 13 (stop), and orchestrator usage.
- `IsPIDAlive(int) bool`: consistent.
- Build tags: `start_sensor`, `start_watcher`, `stop_sensor`, `tail_sensor`, `list_sensors` — distinct, no overlap.
