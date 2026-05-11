# Registry PID invariant and /stop-sensor robustness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce non-negative PID invariants in `lib/registry` via `Save`-time validation and `Load`-time self-heal, surface migrations as a `warn` Signal in the four registry-touching skills, and harden `/stop-sensor`'s watcher termination with measured/reported escalation. Closes issue [#11](https://github.com/iurykrieger/harness-framework/issues/11).

**Architecture:** A new file `lib/registry/sanitize.go` owns the invariant and the migration. `Save` validates each entry via `ValidateEntry`; a new additive `LoadSanitized` calls `SanitizeAll` and best-effort re-persists under flock. A new additive `LookupSanitized` (companion of `Lookup`) is the skill-facing entry point. The four registry-touching skills (`/list-sensors`, `/tail-sensor`, `/stop-sensor`, `/start-sensor`) migrate to it and emit a precedence `warn` Signal (`metadata.kind=registry_migrated`) ahead of their main Signal when migration occurred. In parallel, `stopWatcher` widens its return to `(killedForcefully bool, latencyMS int)` so `/stop-sensor`'s aggregate Signal carries `watcher_kill_forced` and `watcher_kill_latency_ms` for regression diagnostics. The watcher's stderr is captured into a new per-sensor `watcher.log` so its signal-handler log line survives. The e2e tests drop their defensive `SIGKILL` so any future regression in watcher signal handling fails CI loudly.

**Tech Stack:** Go 1.25, stdlib only (`testing`, `os`, `os/exec`, `os/signal`, `syscall`, `time`), `github.com/google/uuid`, `github.com/santhosh-tekuri/jsonschema/v5` (transitively for skills), `golang.org/x/sys/unix` (transitively via `lib/registry/liveness.go`). Schema unchanged. Build tags per script: `start_sensor`, `start_watcher`, `stop_sensor`, `list_sensors`, `tail_sensor`. e2e tests have no build tag.

---

## File Structure

### New files

- `lib/registry/sanitize.go` — types `InvalidEntryError`, `SanitizeReport`; functions `ValidateEntry`, `SanitizeAll`, `RegistryMigratedSignal`.
- `lib/registry/sanitize_test.go` — unit tests for the three.

### Modified files

- `lib/registry/state.go` — `Save` validates each entry first; new `LoadSanitized`.
- `lib/registry/state_test.go` — new test cases.
- `lib/registry/root.go` — new `LookupSanitized`.
- `lib/registry/root_test.go` — new test cases.
- `skills/list-sensors/scripts/list.go` — call `LookupSanitized`; emit precedence warn when reports non-empty.
- `skills/list-sensors/scripts/list_test.go` — new test asserting precedence warn + sanitized entry.
- `skills/tail-sensor/scripts/tail.go` — call `LookupSanitized`; emit precedence warn when reports non-empty.
- `skills/stop-sensor/scripts/stop.go` — call `LookupSanitized`; emit precedence warn; `stopWatcher` returns `(killedForcefully bool, latencyMS int)`; aggregate Signal carries the two new metadata fields.
- `skills/stop-sensor/scripts/stop_test.go` — new tests for the wider `stopWatcher` signature using a subprocess helper.
- `skills/start-sensor/scripts/start.go` — replace `os.Getwd()` with `registry.LookupSanitized`; emit precedence warn; redirect watcher subprocess stderr to `<r.SensorDir(id)>/watcher.log`.
- `skills/start-sensor/scripts/watcher.go` — log `"watcher: <signal> received, draining"` to stderr in the signal-handler goroutine.
- `skills/start-sensor/scripts/watcher_test.go` — new test asserting the log line.
- `test/registry-discovery-e2e/registry_discovery_e2e_test.go` — remove `killWatcherIfAlive` helper and its two callers; add new test `TestSanitize_LegacyMinusOneViaListSensors`.

### Untouched

- `schemas/sensor.json`, `schemas/signal.json` — no schema changes.
- `lib/orchestrator/live_deps.go`, watcher reaper in `watcher.go` — stay on `Load` (no migration noise on the runtime fast path).
- All other `lib/`, `skills/`, and `test/` files.

---

## Task 1: ValidateEntry types + function

**Files:**
- Create: `lib/registry/sanitize.go`
- Create: `lib/registry/sanitize_test.go`

- [ ] **Step 1: Write the failing tests**

Write `lib/registry/sanitize_test.go`:

```go
package registry_test

import (
	"errors"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/registry"
)

func validEntry() registry.RunningSensorEntry {
	return registry.RunningSensorEntry{
		SensorID:   "ok",
		PID:        100,
		PGID:       100,
		WatcherPID: 101,
		StartedAt:  "2026-05-11T00:00:00Z",
		Command:    "true",
		LogDir:     ".runtime/sensors/ok",
		HeldBy: []registry.HeldByEntry{
			{Kind: "manual", AttachedAt: "2026-05-11T00:00:00Z"},
		},
	}
}

func TestValidateEntry_AcceptsValid(t *testing.T) {
	cases := []registry.RunningSensorEntry{
		validEntry(),
		func() registry.RunningSensorEntry {
			e := validEntry()
			e.WatcherPID = 0 // orchestrator path
			return e
		}(),
		func() registry.RunningSensorEntry {
			e := validEntry()
			e.HeldBy = []registry.HeldByEntry{
				{Kind: "sensor", ID: "dep", PID: 99, AttachedAt: "2026-05-11T00:00:00Z"},
			}
			return e
		}(),
	}
	for i, c := range cases {
		if err := registry.ValidateEntry(c); err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
}

func TestValidateEntry_RejectsNegative(t *testing.T) {
	type tc struct {
		name  string
		mut   func(*registry.RunningSensorEntry)
		field string
		value int
	}
	cases := []tc{
		{"pid zero", func(e *registry.RunningSensorEntry) { e.PID = 0 }, "pid", 0},
		{"pid negative", func(e *registry.RunningSensorEntry) { e.PID = -1 }, "pid", -1},
		{"pgid zero", func(e *registry.RunningSensorEntry) { e.PGID = 0 }, "pgid", 0},
		{"pgid negative", func(e *registry.RunningSensorEntry) { e.PGID = -7 }, "pgid", -7},
		{"watcher_pid negative", func(e *registry.RunningSensorEntry) { e.WatcherPID = -1 }, "watcher_pid", -1},
		{"sensor holder pid zero", func(e *registry.RunningSensorEntry) {
			e.HeldBy = []registry.HeldByEntry{{Kind: "sensor", ID: "x", PID: 0, AttachedAt: "t"}}
		}, "held_by[0].pid", 0},
		{"manual holder pid negative", func(e *registry.RunningSensorEntry) {
			e.HeldBy = []registry.HeldByEntry{{Kind: "manual", PID: -1, AttachedAt: "t"}}
		}, "held_by[0].pid", -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validEntry()
			c.mut(&e)
			err := registry.ValidateEntry(e)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			var ie *registry.InvalidEntryError
			if !errors.As(err, &ie) {
				t.Fatalf("expected *InvalidEntryError, got %T: %v", err, err)
			}
			if ie.Field != c.field || ie.Value != c.value {
				t.Errorf("got field=%q value=%d, want field=%q value=%d", ie.Field, ie.Value, c.field, c.value)
			}
			if ie.SensorID != "ok" {
				t.Errorf("SensorID: got %q, want %q", ie.SensorID, "ok")
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./lib/registry/...`

Expected: compile error (`registry.ValidateEntry` undefined, `registry.InvalidEntryError` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `lib/registry/sanitize.go`:

```go
package registry

import "fmt"

// InvalidEntryError is returned by ValidateEntry when a RunningSensorEntry
// violates a PID non-negativity invariant. Save propagates this unwrapped
// so callers can errors.As(err, new(*InvalidEntryError)).
type InvalidEntryError struct {
	SensorID string
	Field    string // "pid" | "pgid" | "watcher_pid" | "held_by[i].pid"
	Value    int
}

func (e *InvalidEntryError) Error() string {
	return fmt.Sprintf("registry: invalid %s=%d for sensor %q", e.Field, e.Value, e.SensorID)
}

// ValidateEntry enforces the PID non-negativity invariant.
// Returns nil if valid; otherwise *InvalidEntryError naming the first
// offending field.
//
// Rules:
//   - PID must be > 0
//   - PGID must be > 0
//   - WatcherPID must be >= 0 (0 means "no watcher", as in the orchestrator path)
//   - HeldBy[i].PID must be >= 0 always; when Kind == "sensor", it must be > 0.
func ValidateEntry(e RunningSensorEntry) error {
	if e.PID < 1 {
		return &InvalidEntryError{SensorID: e.SensorID, Field: "pid", Value: e.PID}
	}
	if e.PGID < 1 {
		return &InvalidEntryError{SensorID: e.SensorID, Field: "pgid", Value: e.PGID}
	}
	if e.WatcherPID < 0 {
		return &InvalidEntryError{SensorID: e.SensorID, Field: "watcher_pid", Value: e.WatcherPID}
	}
	for i, h := range e.HeldBy {
		if h.PID < 0 {
			return &InvalidEntryError{SensorID: e.SensorID, Field: fmt.Sprintf("held_by[%d].pid", i), Value: h.PID}
		}
		if h.Kind == "sensor" && h.PID < 1 {
			return &InvalidEntryError{SensorID: e.SensorID, Field: fmt.Sprintf("held_by[%d].pid", i), Value: h.PID}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./lib/registry/...`

Expected: PASS (all tests, including pre-existing ones).

- [ ] **Step 5: Run vet**

Run: `go vet ./lib/registry/...`

Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add lib/registry/sanitize.go lib/registry/sanitize_test.go
git commit -m "$(cat <<'EOF'
feat(registry): add ValidateEntry + InvalidEntryError

Enforces non-negative PID invariants for RunningSensorEntry fields.
First piece of the issue #11 fix.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: SanitizeAll

**Files:**
- Modify: `lib/registry/sanitize.go`
- Modify: `lib/registry/sanitize_test.go`

- [ ] **Step 1: Append failing tests to `lib/registry/sanitize_test.go`**

Append at end of file:

```go
func TestSanitizeAll_RewritesWatcherPID(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "x", PID: 10, PGID: 10, WatcherPID: -1, StartedAt: "t", Command: "c", LogDir: "d"},
		},
	}
	reports := registry.SanitizeAll(rs)
	if len(reports) != 1 {
		t.Fatalf("reports: got %d, want 1", len(reports))
	}
	r := reports[0]
	if r.SensorID != "x" || r.Field != "watcher_pid" || r.OldValue != -1 || r.Dropped {
		t.Errorf("unexpected report: %+v", r)
	}
	if rs.Entries[0].WatcherPID != 0 {
		t.Errorf("WatcherPID: got %d, want 0", rs.Entries[0].WatcherPID)
	}
}

func TestSanitizeAll_DropsHolderWithBadSensorPID(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID: "x", PID: 10, PGID: 10, WatcherPID: 11,
				StartedAt: "t", Command: "c", LogDir: "d",
				HeldBy: []registry.HeldByEntry{
					{Kind: "sensor", ID: "dep", PID: -1, AttachedAt: "t"},
					{Kind: "manual", AttachedAt: "t"},
				},
			},
		},
	}
	reports := registry.SanitizeAll(rs)
	if len(reports) != 1 {
		t.Fatalf("reports: got %d, want 1", len(reports))
	}
	r := reports[0]
	if r.Field != "held_by[0].pid" || !r.Dropped || r.OldValue != -1 {
		t.Errorf("unexpected report: %+v", r)
	}
	if len(rs.Entries[0].HeldBy) != 1 || rs.Entries[0].HeldBy[0].Kind != "manual" {
		t.Errorf("HeldBy after drop: %+v", rs.Entries[0].HeldBy)
	}
}

func TestSanitizeAll_DropsEntryWithBadPID(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "bad", PID: -1, PGID: 10, WatcherPID: 11, StartedAt: "t", Command: "c", LogDir: "d"},
			{SensorID: "good", PID: 10, PGID: 10, WatcherPID: 11, StartedAt: "t", Command: "c", LogDir: "d"},
		},
	}
	reports := registry.SanitizeAll(rs)
	if len(reports) != 1 {
		t.Fatalf("reports: got %d, want 1", len(reports))
	}
	r := reports[0]
	if r.SensorID != "bad" || r.Field != "pid" || !r.Dropped || r.OldValue != -1 {
		t.Errorf("unexpected report: %+v", r)
	}
	if len(rs.Entries) != 1 || rs.Entries[0].SensorID != "good" {
		t.Errorf("Entries after drop: %+v", rs.Entries)
	}
}

func TestSanitizeAll_Idempotent(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "x", PID: 10, PGID: 10, WatcherPID: -1, StartedAt: "t", Command: "c", LogDir: "d"},
		},
	}
	if got := registry.SanitizeAll(rs); len(got) != 1 {
		t.Fatalf("first call: got %d reports, want 1", len(got))
	}
	if got := registry.SanitizeAll(rs); len(got) != 0 {
		t.Fatalf("second call: got %d reports, want 0", len(got))
	}
}

func TestSanitizeAll_NoOpOnHealthy(t *testing.T) {
	rs := &registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "x", PID: 10, PGID: 10, WatcherPID: 11, StartedAt: "t", Command: "c", LogDir: "d"},
		},
	}
	if got := registry.SanitizeAll(rs); len(got) != 0 {
		t.Fatalf("got %d reports, want 0", len(got))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./lib/registry/... -run TestSanitizeAll`

Expected: compile error (`registry.SanitizeAll` undefined, `registry.SanitizeReport` undefined).

- [ ] **Step 3: Implement `SanitizeAll` in `lib/registry/sanitize.go`**

Append to `lib/registry/sanitize.go`:

```go
// SanitizeReport records one mutation performed by SanitizeAll.
type SanitizeReport struct {
	SensorID string `json:"sensor_id"`
	Field    string `json:"field"`     // "watcher_pid" | "held_by[i].pid" | "pid" | "pgid"
	OldValue int    `json:"old_value"`
	Dropped  bool   `json:"dropped"`   // entry or holder discarded entirely
}

// SanitizeAll rewrites legacy invalid PID fields in rs to safe values.
// Mutation is in-memory; caller persists via Save (which will succeed
// because ValidateEntry passes on the sanitized state).
//
// Rules, applied per entry:
//   - WatcherPID < 0       → rewrite to 0, report (Dropped: false).
//   - HeldByEntry.PID < 0 with Kind == "manual" → rewrite to 0, report (Dropped: false).
//   - HeldByEntry.PID < 1 with Kind == "sensor" → drop the holder, report (Dropped: true).
//   - PID < 1 or PGID < 1  → drop the entire entry, report (Dropped: true).
//
// Returns an empty slice when nothing changed.
func SanitizeAll(rs *RunningSensors) []SanitizeReport {
	if rs == nil {
		return nil
	}
	reports := make([]SanitizeReport, 0)
	keep := rs.Entries[:0]
	for _, e := range rs.Entries {
		if e.PID < 1 {
			reports = append(reports, SanitizeReport{SensorID: e.SensorID, Field: "pid", OldValue: e.PID, Dropped: true})
			continue
		}
		if e.PGID < 1 {
			reports = append(reports, SanitizeReport{SensorID: e.SensorID, Field: "pgid", OldValue: e.PGID, Dropped: true})
			continue
		}
		if e.WatcherPID < 0 {
			reports = append(reports, SanitizeReport{SensorID: e.SensorID, Field: "watcher_pid", OldValue: e.WatcherPID, Dropped: false})
			e.WatcherPID = 0
		}
		newHolders := e.HeldBy[:0]
		for i, h := range e.HeldBy {
			switch {
			case h.Kind == "sensor" && h.PID < 1:
				reports = append(reports, SanitizeReport{
					SensorID: e.SensorID,
					Field:    fmt.Sprintf("held_by[%d].pid", i),
					OldValue: h.PID,
					Dropped:  true,
				})
				continue
			case h.PID < 0:
				reports = append(reports, SanitizeReport{
					SensorID: e.SensorID,
					Field:    fmt.Sprintf("held_by[%d].pid", i),
					OldValue: h.PID,
					Dropped:  false,
				})
				h.PID = 0
			}
			newHolders = append(newHolders, h)
		}
		e.HeldBy = newHolders
		keep = append(keep, e)
	}
	rs.Entries = keep
	return reports
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./lib/registry/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/sanitize.go lib/registry/sanitize_test.go
git commit -m "$(cat <<'EOF'
feat(registry): add SanitizeAll for legacy PID self-heal

Mutates RunningSensors in place: rewrites negative WatcherPID and
manual-holder PIDs to 0; drops sensor-holders and entries whose PID
is unrecoverable. Returns SanitizeReport list for callers to surface
as a warn Signal.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: RegistryMigratedSignal helper

**Files:**
- Modify: `lib/registry/sanitize.go`
- Modify: `lib/registry/sanitize_test.go`

- [ ] **Step 1: Append failing test**

Append to `lib/registry/sanitize_test.go`:

```go
func TestRegistryMigratedSignal_Shape(t *testing.T) {
	res := registry.Result{
		Root:        registry.NewRoot("/tmp/proj"),
		ProjectRoot: "/tmp/proj",
		Source:      registry.SourceWalkUp,
		Exists:      true,
	}
	reports := []registry.SanitizeReport{
		{SensorID: "run-api-local", Field: "watcher_pid", OldValue: -1, Dropped: false},
	}
	sig := registry.RegistryMigratedSignal(res, reports, "list-sensors")
	if got, _ := sig["verdict"].(string); got != "warn" {
		t.Errorf("verdict: got %q, want warn", got)
	}
	if got, _ := sig["severity"].(string); got != "low" {
		t.Errorf("severity: got %q, want low", got)
	}
	if got, _ := sig["sensor_id"].(string); got != "list-sensors" {
		t.Errorf("sensor_id: got %q, want list-sensors", got)
	}
	md, _ := sig["metadata"].(map[string]interface{})
	if md == nil {
		t.Fatal("metadata: nil")
	}
	if got, _ := md["kind"].(string); got != "registry_migrated" {
		t.Errorf("metadata.kind: got %q, want registry_migrated", got)
	}
	if got, _ := md["registry_path"].(string); got == "" {
		t.Errorf("metadata.registry_path missing")
	}
	rpts, ok := md["reports"].([]registry.SanitizeReport)
	if !ok || len(rpts) != 1 || rpts[0].Field != "watcher_pid" {
		t.Errorf("metadata.reports: got %v", md["reports"])
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./lib/registry/... -run TestRegistryMigratedSignal_Shape`

Expected: compile error (undefined).

- [ ] **Step 3: Implement helper in `lib/registry/sanitize.go`**

Append to `lib/registry/sanitize.go` (also add `"time"` and `uuid` to the import block):

```go
import (
	"fmt"
	"time"

	"github.com/google/uuid"
)
```

Replace the import block with the above. Then append:

```go
// RegistryMigratedSignal builds the precedence warn Signal emitted by
// the four registry-touching skills when SanitizeAll returns non-empty
// reports. The Signal carries DiagnoseMetadata fields plus the
// migration report list under metadata.reports, and is structured to
// pass signal.json validation.
//
// sensorID is the skill name ("list-sensors", "tail-sensor",
// "stop-sensor", "start-sensor") — same convention as DiscoveryErrorSignal.
func RegistryMigratedSignal(res Result, reports []SanitizeReport, sensorID string) map[string]interface{} {
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	rewritten := 0
	dropped := 0
	for _, r := range reports {
		if r.Dropped {
			dropped++
		} else {
			rewritten++
		}
	}
	rationale := fmt.Sprintf("rewrote %d invalid PID field(s) and dropped %d entry/holder(s) in running_sensors.json", rewritten, dropped)
	md := DiagnoseMetadata(res)
	md["kind"] = "registry_migrated"
	md["reports"] = reports
	return map[string]interface{}{
		"sensor_id":   sensorID,
		"version":     "0.0.0",
		"run_id":      uuid.NewString(),
		"started_at":  now,
		"finished_at": now,
		"verdict":     "warn",
		"severity":    "low",
		"confidence":  1.0,
		"evidence":    []interface{}{map[string]interface{}{"rationale": rationale}},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata":    md,
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/registry/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/sanitize.go lib/registry/sanitize_test.go
git commit -m "$(cat <<'EOF'
feat(registry): RegistryMigratedSignal helper

Shared constructor for the precedence warn Signal that the four
registry-touching skills emit when SanitizeAll surfaces reports.
Reuses DiagnoseMetadata for registry_path/source/exists.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Save validates each entry

**Files:**
- Modify: `lib/registry/state.go:67-83` (the `Save` function body)
- Modify: `lib/registry/state_test.go`

- [ ] **Step 1: Append failing test to `lib/registry/state_test.go`**

```go
func TestSave_RejectsInvalidEntry(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)

	rs := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{
				SensorID:   "bad",
				PID:        1234,
				PGID:       1234,
				WatcherPID: -1, // invalid
				StartedAt:  "t",
				Command:    "c",
				LogDir:     "d",
				HeldBy:     []registry.HeldByEntry{{Kind: "manual", AttachedAt: "t"}},
			},
		},
	}
	err := registry.Save(r, rs)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var ie *registry.InvalidEntryError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InvalidEntryError, got %T: %v", err, err)
	}
	if ie.Field != "watcher_pid" || ie.SensorID != "bad" {
		t.Errorf("got field=%q sensor=%q, want watcher_pid/bad", ie.Field, ie.SensorID)
	}
	if _, statErr := os.Stat(r.RegistryFile()); statErr == nil {
		t.Errorf("registry file should NOT exist after rejected Save")
	}
}
```

`errors` is already in the imports (from the existing test file). Verify:

```bash
grep '"errors"' lib/registry/state_test.go
```

Expected: matches. If not, add `"errors"` to the import block.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./lib/registry/... -run TestSave_RejectsInvalidEntry`

Expected: FAIL with "expected error, got nil" (current `Save` doesn't validate).

- [ ] **Step 3: Modify `lib/registry/state.go::Save` to validate**

Replace lines 65-83 (the entire `Save` function) with:

```go
// Save writes running_sensors.json atomically (temp + rename). Each
// entry is validated via ValidateEntry before any bytes are written;
// the first invalid entry causes Save to return *InvalidEntryError
// without touching the file. The caller is expected to be holding the
// registry flock.
func Save(r Root, rs RunningSensors) error {
	for _, e := range rs.Entries {
		if err := ValidateEntry(e); err != nil {
			return err
		}
	}
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/registry/...`

Expected: PASS (all tests). The pre-existing `TestSaveLoad_RoundTrip` continues to work because its fixture uses valid PIDs.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/state.go lib/registry/state_test.go
git commit -m "$(cat <<'EOF'
feat(registry): Save validates entries via ValidateEntry

A Save call now returns *InvalidEntryError without writing the
running_sensors.json file when any entry has a negative PID/PGID/
WatcherPID/HeldBy[i].PID. Centralizes the invariant where future
callers cannot bypass it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: LoadSanitized companion of Load

**Files:**
- Modify: `lib/registry/state.go`
- Modify: `lib/registry/state_test.go`

- [ ] **Step 1: Append failing tests**

Append to `lib/registry/state_test.go`:

```go
func TestLoadSanitized_MigratesLegacy(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "entries": [
    {
      "sensor_id": "run-api-local",
      "pid": 90006,
      "pgid": 90006,
      "watcher_pid": -1,
      "started_at": "2026-05-09T13:51:38Z",
      "command": "docker compose up",
      "log_dir": ".runtime/sensors/run-api-local",
      "held_by": [{"kind": "manual", "attached_at": "2026-05-09T13:51:38Z"}]
    }
  ]
}`)
	if err := os.WriteFile(r.RegistryFile(), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	rs, reports, err := registry.LoadSanitized(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Field != "watcher_pid" {
		t.Errorf("reports: %+v", reports)
	}
	if rs.Entries[0].WatcherPID != 0 {
		t.Errorf("WatcherPID in memory: got %d, want 0", rs.Entries[0].WatcherPID)
	}
	// Re-Save persisted on disk:
	rs2, err := registry.Load(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs2.Entries[0].WatcherPID != 0 {
		t.Errorf("WatcherPID on disk: got %d, want 0", rs2.Entries[0].WatcherPID)
	}
}

func TestLoadSanitized_NoOpOnHealthy(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	healthy := registry.RunningSensors{
		Version: 1,
		Entries: []registry.RunningSensorEntry{
			{SensorID: "ok", PID: 100, PGID: 100, WatcherPID: 101,
				StartedAt: "t", Command: "c", LogDir: "d",
				HeldBy: []registry.HeldByEntry{{Kind: "manual", AttachedAt: "t"}}},
		},
	}
	if err := registry.Save(r, healthy); err != nil {
		t.Fatal(err)
	}
	statBefore, _ := os.Stat(r.RegistryFile())

	rs, reports, err := registry.LoadSanitized(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Errorf("reports: got %d, want 0", len(reports))
	}
	if len(rs.Entries) != 1 || rs.Entries[0].WatcherPID != 101 {
		t.Errorf("Entries: %+v", rs.Entries)
	}
	statAfter, _ := os.Stat(r.RegistryFile())
	if statBefore != nil && statAfter != nil && !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("mtime changed on no-op LoadSanitized")
	}
}

func TestLoadSanitized_ReturnsEmptyOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	rs, reports, err := registry.LoadSanitized(r)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Version != 1 || len(rs.Entries) != 0 {
		t.Errorf("rs: got %+v", rs)
	}
	if len(reports) != 0 {
		t.Errorf("reports: got %d, want 0", len(reports))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./lib/registry/... -run TestLoadSanitized`

Expected: compile error (`registry.LoadSanitized` undefined).

- [ ] **Step 3: Implement LoadSanitized**

Append to `lib/registry/state.go` (immediately after the existing `LoadOrEmpty` function):

```go
// LoadSanitized loads running_sensors.json, applies SanitizeAll, and
// best-effort re-persists when any mutation occurred. Returns the
// sanitized in-memory state plus the migration reports so callers can
// surface a warn Signal.
//
// A failure to re-Save the sanitized state is silenced: the in-memory
// state is still correct, persistence retries on the next invocation.
// A Load failure (parse error, I/O error) returns (zero, nil, err)
// untouched.
func LoadSanitized(r Root) (RunningSensors, []SanitizeReport, error) {
	rs, err := Load(r)
	if err != nil {
		return rs, nil, err
	}
	reports := SanitizeAll(&rs)
	if len(reports) > 0 {
		_ = WithFileLock(r.LockFile(), func() error { return Save(r, rs) })
	}
	return rs, reports, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/registry/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/state.go lib/registry/state_test.go
git commit -m "$(cat <<'EOF'
feat(registry): LoadSanitized companion of Load

Combines Load + SanitizeAll + best-effort re-Save under flock.
Returns the migration reports for the caller to surface as a warn
Signal. Load and LoadOrEmpty unchanged so the runtime fast path
(orchestrator, watcher reaper) stays migration-free.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: LookupSanitized companion of Lookup

**Files:**
- Modify: `lib/registry/root.go`
- Modify: `lib/registry/root_test.go`

- [ ] **Step 1: Append failing tests**

Append to `lib/registry/root_test.go`:

```go
func TestLookupSanitized_MigratesAndReturnsReports(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	// Pre-seed registry with a legacy -1 entry.
	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "entries": [
    {
      "sensor_id": "x", "pid": 1234, "pgid": 1234,
      "watcher_pid": -1, "started_at": "t", "command": "c", "log_dir": "d",
      "held_by": [{"kind": "manual", "attached_at": "t"}]
    }
  ]
}`)
	if err := os.WriteFile(r.RegistryFile(), legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	res, reports, err := registry.LookupSanitized("/tmp/anything")
	if err != nil {
		t.Fatal(err)
	}
	if res.ProjectRoot != proj {
		t.Errorf("ProjectRoot: got %q, want %q", res.ProjectRoot, proj)
	}
	if res.Source != registry.SourceEnv {
		t.Errorf("Source: got %q, want %q", res.Source, registry.SourceEnv)
	}
	if !res.Exists {
		t.Errorf("Exists: got false, want true")
	}
	if len(reports) != 1 || reports[0].Field != "watcher_pid" {
		t.Errorf("reports: %+v", reports)
	}
	if res.State.Entries[0].WatcherPID != 0 {
		t.Errorf("WatcherPID: got %d, want 0", res.State.Entries[0].WatcherPID)
	}
}

func TestLookupSanitized_NoRegistryNoReports(t *testing.T) {
	parent := t.TempDir()
	proj := makeProjectTree(t, parent)
	t.Setenv("HARNESS_REGISTRY_ROOT", proj)

	res, reports, err := registry.LookupSanitized("/tmp/anything")
	if err != nil {
		t.Fatal(err)
	}
	if res.Exists {
		t.Errorf("Exists: got true, want false")
	}
	if len(reports) != 0 {
		t.Errorf("reports: got %d, want 0", len(reports))
	}
}

func TestLookupSanitized_DiscoveryFailurePropagates(t *testing.T) {
	parent := t.TempDir()
	// No sensors/ marker, no env var.
	t.Setenv("HARNESS_REGISTRY_ROOT", "")
	_, _, err := registry.LookupSanitized(parent)
	if err == nil {
		t.Fatal("expected discovery error, got nil")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./lib/registry/... -run TestLookupSanitized`

Expected: compile error (`registry.LookupSanitized` undefined).

- [ ] **Step 3: Implement LookupSanitized**

Append to `lib/registry/root.go` (immediately after the existing `Lookup` function):

```go
// LookupSanitized is the skill-facing entry point that combines root
// discovery with PID-invariant sanitation. Same Result, same error
// semantics as Lookup. The []SanitizeReport return is non-empty when
// LoadSanitized rewrote or dropped one or more entries from
// running_sensors.json; the caller surfaces it as a warn Signal.
//
// When the registry file does not exist on disk, returns Exists=false
// with an empty state — same as Lookup — and an empty reports slice.
func LookupSanitized(startDir string) (Result, []SanitizeReport, error) {
	root, source, err := Discover(startDir)
	if err != nil {
		return Result{}, nil, err
	}
	r := NewRoot(root)
	state, reports, err := LoadSanitized(r)
	if err != nil {
		return Result{}, nil, err
	}
	// Existence is derived from whether running_sensors.json is on
	// disk. LoadSanitized does not surface this directly, so stat it.
	exists := true
	if _, statErr := os.Stat(r.RegistryFile()); statErr != nil {
		exists = false
	}
	return Result{
		Root:        r,
		ProjectRoot: root,
		Source:      source,
		Exists:      exists,
		State:       state,
	}, reports, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./lib/registry/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add lib/registry/root.go lib/registry/root_test.go
git commit -m "$(cat <<'EOF'
feat(registry): LookupSanitized companion of Lookup

Skill-facing entry point that funnels root discovery + LoadSanitized
into one call, returning the migration reports alongside the Result.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Migrate /list-sensors

**Files:**
- Modify: `skills/list-sensors/scripts/list.go`
- Modify: `skills/list-sensors/scripts/list_test.go`

- [ ] **Step 1: Append failing test to `list_test.go`**

Append:

```go
func TestRunList_EmitsRegistryMigratedSignalFirst(t *testing.T) {
	dir := t.TempDir()
	r := registry.NewRoot(dir)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "entries": [
    {
      "sensor_id": "x", "pid": 1234, "pgid": 1234,
      "watcher_pid": -1, "started_at": "t", "command": "c",
      "log_dir": ".runtime/sensors/x",
      "held_by": [{"kind": "manual", "attached_at": "t"}]
    }
  ]
}`)
	if err := os.WriteFile(r.RegistryFile(), legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	// Ensure marker dir exists for walk-up.
	if err := os.MkdirAll(filepath.Join(dir, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HARNESS_REGISTRY_ROOT", dir)

	res, reports, err := registry.LookupSanitized(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) == 0 {
		t.Fatal("expected sanitize reports, got none")
	}
	var buf bytes.Buffer
	exit := runList(res, reports, &buf, io.Discard)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d JSONL line(s); want 2 (warn + list). Output:\n%s", len(lines), buf.String())
	}

	var warn map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &warn); err != nil {
		t.Fatal(err)
	}
	if warn["verdict"] != "warn" {
		t.Errorf("line 0 verdict: %v", warn["verdict"])
	}
	md, _ := warn["metadata"].(map[string]interface{})
	if md["kind"] != "registry_migrated" {
		t.Errorf("line 0 metadata.kind: %v", md["kind"])
	}

	var main map[string]interface{}
	if err := json.Unmarshal([]byte(lines[1]), &main); err != nil {
		t.Fatal(err)
	}
	if main["verdict"] != "pass" {
		t.Errorf("line 1 verdict: %v", main["verdict"])
	}
	mainMD, _ := main["metadata"].(map[string]interface{})
	entries, _ := mainMD["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("entries: got %d, want 1", len(entries))
	}
	e0, _ := entries[0].(map[string]interface{})
	if pid, _ := e0["watcher_pid"].(float64); pid != 0 {
		t.Errorf("entry watcher_pid: got %v, want 0", e0["watcher_pid"])
	}
}
```

Adjust imports at the top of `list_test.go` to include `"bytes"`, `"encoding/json"`, `"io"`, `"os"`, `"path/filepath"`, `"strings"`, `"testing"`, and `"github.com/iurykrieger/harness-framework/lib/registry"`. Existing tests already import several; check before adding.

- [ ] **Step 2: Run to verify failure**

Run: `go test -tags=list_sensors ./skills/list-sensors/scripts/... -run TestRunList_EmitsRegistryMigratedSignalFirst`

Expected: compile error — `runList`'s signature does not yet accept `reports`.

- [ ] **Step 3: Modify `list.go`**

Replace the entire `main` function (lines 24-36 in current `list.go`) with:

```go
func main() {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "list: cwd:", err)
		os.Exit(2)
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, "list-sensors"))
		os.Exit(1)
	}
	os.Exit(runList(res, reports, os.Stdout, os.Stderr))
}
```

Then change `runList`'s signature (currently `func runList(res registry.Result, stdout, stderr io.Writer) int`) to accept reports, and emit the precedence warn before the rest of its body runs:

Find the line `func runList(res registry.Result, stdout, stderr io.Writer) int {` and replace with:

```go
func runList(res registry.Result, reports []registry.SanitizeReport, stdout, stderr io.Writer) int {
	if len(reports) > 0 {
		_ = json.NewEncoder(stdout).Encode(registry.RegistryMigratedSignal(res, reports, "list-sensors"))
	}
```

(The `{` and the rest of the function body remain unchanged. The new lines are inserted right at the top of `runList`.)

- [ ] **Step 4: Run all tests for the skill**

Run: `go test -tags=list_sensors ./skills/list-sensors/scripts/...`

Expected: PASS (all tests, including pre-existing ones).

- [ ] **Step 5: Vet**

Run: `go vet -tags=list_sensors ./skills/list-sensors/...`

Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add skills/list-sensors/scripts/list.go skills/list-sensors/scripts/list_test.go
git commit -m "$(cat <<'EOF'
feat(list-sensors): emit registry_migrated warn signal

Migrate from registry.Lookup to registry.LookupSanitized; emit the
precedence warn signal as the first JSONL line when sanitize reports
are non-empty.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: Migrate /tail-sensor

**Files:**
- Modify: `skills/tail-sensor/scripts/tail.go`

- [ ] **Step 1: Modify `tail.go`'s main**

Replace `main` (lines 26-39 in current `tail.go`) with:

```go
func main() {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tail: cwd:", err)
		os.Exit(2)
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	if len(reports) > 0 {
		_ = json.NewEncoder(os.Stdout).Encode(registry.RegistryMigratedSignal(res, reports, "tail-sensor"))
	}
	exit := runTail(res, os.Args[1:], os.Stdout, os.Stderr)
	os.Exit(exit)
}
```

Rationale: unlike `list-sensors`, `tail-sensor`'s `runTail` already takes `res` only and we emit the warn from `main` directly. This avoids threading `reports` through `runTail`'s argument list.

- [ ] **Step 2: Build to verify it compiles**

Run: `go build -tags=tail_sensor ./skills/tail-sensor/scripts/...`

Expected: succeeds.

- [ ] **Step 3: Run existing tests for the skill**

Run: `go test -tags=tail_sensor ./skills/tail-sensor/scripts/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/tail-sensor/scripts/tail.go
git commit -m "$(cat <<'EOF'
feat(tail-sensor): emit registry_migrated warn signal

Migrate from registry.Lookup to registry.LookupSanitized; emit the
precedence warn signal as the first JSONL line when sanitize reports
are non-empty.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: Migrate /stop-sensor's Lookup call

**Files:**
- Modify: `skills/stop-sensor/scripts/stop.go`

- [ ] **Step 1: Modify `stop.go`'s main**

Replace `main` (lines 26-49 in current `stop.go`) with:

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
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	if len(reports) > 0 {
		_ = json.NewEncoder(os.Stdout).Encode(registry.RegistryMigratedSignal(res, reports, "stop-sensor"))
	}
	exit, sig := runStop(res, fs.Args(), reap)
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build -tags=stop_sensor ./skills/stop-sensor/scripts/...`

Expected: succeeds.

- [ ] **Step 3: Run existing tests**

Run: `go test -tags=stop_sensor ./skills/stop-sensor/scripts/...`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/stop-sensor/scripts/stop.go
git commit -m "$(cat <<'EOF'
feat(stop-sensor): emit registry_migrated warn signal

Migrate from registry.Lookup to registry.LookupSanitized; emit the
precedence warn signal as the first JSONL line when sanitize reports
are non-empty.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: stopWatcher returns latency + escalation flag

**Files:**
- Modify: `skills/stop-sensor/scripts/stop.go`
- Modify: `skills/stop-sensor/scripts/stop_test.go`

The widening also flows into the aggregate Signal metadata.

- [ ] **Step 1: Append failing tests**

Append to `skills/stop-sensor/scripts/stop_test.go`:

```go
// helperKey is the env var that puts the test binary into helper mode.
// When set, the binary becomes a "fake watcher" instead of running
// the test suite.
const helperKey = "HARNESS_STOP_TEST_HELPER"

const (
	helperRespectSIGTERM = "respect_sigterm"
	helperIgnoreSIGTERM  = "ignore_sigterm"
)

// TestMain dispatches into helper mode when the env var is set.
// Standard pattern for spawning the test binary as a fake subprocess.
func TestMain(m *testing.M) {
	switch os.Getenv(helperKey) {
	case helperRespectSIGTERM:
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		os.Exit(0)
	case helperIgnoreSIGTERM:
		signal.Ignore(syscall.SIGTERM)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func spawnHelper(t *testing.T, mode string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperKey+"="+mode)
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn helper: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	// Give the helper a moment to install its signal handler.
	time.Sleep(150 * time.Millisecond)
	return cmd.Process.Pid
}

func TestStopWatcher_NormalSIGTERM(t *testing.T) {
	pid := spawnHelper(t, helperRespectSIGTERM)
	killedForcefully, latencyMS := stopWatcher(pid)
	if killedForcefully {
		t.Errorf("killedForcefully: got true, want false")
	}
	if latencyMS < 0 || latencyMS > 500 {
		t.Errorf("latencyMS: got %d, want in [0, 500]", latencyMS)
	}
	if registry.IsPIDAlive(pid) {
		t.Errorf("helper still alive after stopWatcher")
	}
}

func TestStopWatcher_RequiresSIGKILL(t *testing.T) {
	pid := spawnHelper(t, helperIgnoreSIGTERM)
	killedForcefully, latencyMS := stopWatcher(pid)
	if !killedForcefully {
		t.Errorf("killedForcefully: got false, want true")
	}
	if latencyMS < 950 || latencyMS > 1500 {
		t.Errorf("latencyMS: got %d, want in [950, 1500]", latencyMS)
	}
	if registry.IsPIDAlive(pid) {
		t.Errorf("helper still alive after stopWatcher")
	}
}

func TestStopWatcher_NonPositivePID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		killedForcefully, latencyMS := stopWatcher(pid)
		if killedForcefully || latencyMS != 0 {
			t.Errorf("pid=%d: got (%v, %d), want (false, 0)", pid, killedForcefully, latencyMS)
		}
	}
}
```

Adjust the imports of `stop_test.go` to include `"os"`, `"os/exec"`, `"os/signal"`, `"syscall"`, `"time"`, and `"github.com/iurykrieger/harness-framework/lib/registry"`. Check for existing imports first.

- [ ] **Step 2: Run to verify failure**

Run: `go test -tags=stop_sensor ./skills/stop-sensor/scripts/... -run TestStopWatcher`

Expected: compile error — `stopWatcher` returns no values today.

- [ ] **Step 3: Widen `stopWatcher` in `stop.go`**

Replace the existing `stopWatcher` (lines 163-178 in current `stop.go`) with:

```go
// stopWatcher sends SIGTERM to the watcher pid, polls for up to one
// second, and escalates to SIGKILL if needed. Returns
// (killedForcefully, latencyMS):
//   - killedForcefully = true when the SIGTERM wait timed out and we
//     fell through to SIGKILL.
//   - latencyMS is wall-clock elapsed from the first signal to either
//     observed death or the SIGKILL send-time.
// Pid <= 0 is a no-op (returns false, 0).
func stopWatcher(pid int) (killedForcefully bool, latencyMS int) {
	if pid <= 0 {
		return false, 0
	}
	start := time.Now()
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := start.Add(time.Second)
	for time.Now().Before(deadline) {
		if !registry.IsPIDAlive(pid) {
			return false, int(time.Since(start) / time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if registry.IsPIDAlive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		return true, int(time.Since(start) / time.Millisecond)
	}
	return false, int(time.Since(start) / time.Millisecond)
}
```

- [ ] **Step 4: Update the only caller**

In `stop.go`, find the caller (currently `stopWatcher(entry.WatcherPID)` at line 111) and replace with:

```go
watcherKillForced, watcherKillLatencyMS := stopWatcher(entry.WatcherPID)
```

Then fold these into the aggregate Signal's metadata. Locate `buildAggregate(...)` (line 129). Look at its signature in `stop.go` (search `func buildAggregate`). It currently passes the aggregate verdict-related arguments. **Bring the two new values into the aggregate by extending `buildAggregate` or — simpler — by injecting them post-build.**

Append, right after the line `sig := buildAggregate(res, id, sensorJSON, entry, individuals, agg, killedForcefully, reaped, teardownResults)` (around line 129), this block:

```go
if md, ok := sig["metadata"].(map[string]interface{}); ok {
	md["watcher_kill_forced"] = watcherKillForced
	md["watcher_kill_latency_ms"] = watcherKillLatencyMS
}
```

- [ ] **Step 5: Run tests**

Run: `go test -tags=stop_sensor ./skills/stop-sensor/scripts/...`

Expected: PASS.

Note: macOS may run slower than the 950-1500 ms window of `TestStopWatcher_RequiresSIGKILL`. If it does, widen to e.g. `[950, 2000]` after observing the actual range.

- [ ] **Step 6: Commit**

```bash
git add skills/stop-sensor/scripts/stop.go skills/stop-sensor/scripts/stop_test.go
git commit -m "$(cat <<'EOF'
feat(stop-sensor): stopWatcher returns latency + escalation flag

Widen stopWatcher's return to (killedForcefully, latencyMS) so the
aggregate Signal can carry watcher_kill_forced and
watcher_kill_latency_ms for regression diagnostics on macOS SIGTERM
survival. Tests use a self-spawning helper pattern.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: Migrate /start-sensor to LookupSanitized + capture watcher.log

**Files:**
- Modify: `skills/start-sensor/scripts/start.go`

- [ ] **Step 1: Replace `main` and `runStart` signature**

Replace `main` (lines 28-39 of current `start.go`) with:

```go
func main() {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start: cwd:", err)
		os.Exit(2)
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(os.Stdout).Encode(registry.DiscoveryErrorSignal(err, ""))
		os.Exit(1)
	}
	if len(reports) > 0 {
		_ = json.NewEncoder(os.Stdout).Encode(registry.RegistryMigratedSignal(res, reports, "start-sensor"))
	}
	exit, sig := runStart(res.ProjectRoot, os.Args[1:])
	if sig != nil {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
	}
	os.Exit(exit)
}
```

`runStart`'s signature does not change (still takes `projectRoot string`); we just feed it `res.ProjectRoot`.

- [ ] **Step 2: Redirect watcher stderr to `watcher.log`**

Locate the `os.StartProcess(watcherPath, ...)` block (lines 200-212 of current `start.go`). Insert immediately *before* that block:

```go
		watcherLogPath := filepath.Join(r.SensorDir(id), "watcher.log")
		watcherLogFile, err := os.OpenFile(watcherLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open watcher.log: %w", err)
		}
```

Then change the `Files` line of the `os.ProcAttr`:

Before:
```go
			Files: []*os.File{nil, nil, nil},
```

After:
```go
			Files: []*os.File{nil, nil, watcherLogFile},
```

And immediately after the `os.StartProcess` success path (after `_ = watcherProc.Release()` on line 220), add:

```go
		_ = watcherLogFile.Close() // parent's handle; child keeps its own fd open.
```

Place the close call on the line after `_ = watcherProc.Release()`.

- [ ] **Step 3: Build to verify it compiles**

Run: `go build -tags=start_sensor ./skills/start-sensor/scripts/...`

Expected: succeeds.

- [ ] **Step 4: Run existing tests**

Run: `go test -tags=start_sensor ./skills/start-sensor/scripts/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add skills/start-sensor/scripts/start.go
git commit -m "$(cat <<'EOF'
feat(start-sensor): use LookupSanitized; capture watcher stderr

Replace os.Getwd()+NewRoot with registry.LookupSanitized so /start-sensor
joins /list-sensors, /stop-sensor, and /tail-sensor on the
documented cwd-independent root path. Emit the registry_migrated warn
signal when applicable.

Also redirect the watcher subprocess's stderr to a new per-sensor
watcher.log so diagnostic output (signal-handler log line, etc.)
survives.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: Watcher logs the signal it received

**Files:**
- Modify: `skills/start-sensor/scripts/watcher.go`
- Modify: `skills/start-sensor/scripts/watcher_test.go`

- [ ] **Step 1: Append failing test**

Append to `skills/start-sensor/scripts/watcher_test.go`:

```go
func TestWatcher_LogsSignalToStderr(t *testing.T) {
	rPipe, wPipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rPipe.Close()
	origStderr := os.Stderr
	os.Stderr = wPipe
	t.Cleanup(func() { os.Stderr = origStderr; _ = wPipe.Close() })

	stop := make(chan struct{})
	go func() {
		// Simulate the watcher's signal goroutine in isolation.
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		s := <-ch
		fmt.Fprintf(os.Stderr, "watcher: %s received, draining\n", s)
		close(stop)
	}()

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	<-stop
	_ = wPipe.Close()

	buf, _ := io.ReadAll(rPipe)
	if !strings.Contains(string(buf), "received, draining") {
		t.Errorf("stderr did not contain expected log: %q", buf)
	}
}
```

Imports: ensure `"fmt"`, `"io"`, `"os"`, `"os/signal"`, `"strings"`, `"syscall"`, `"testing"` are present.

Note: this test does NOT run the watcher binary end-to-end — that would require spinning up a subprocess. It exercises the signal-handler goroutine pattern that the watcher uses, so any regression in the pattern fails here.

- [ ] **Step 2: Run to verify**

Run: `go test -tags=start_watcher ./skills/start-sensor/scripts/... -run TestWatcher_LogsSignalToStderr`

Expected: PASS (the test re-implements the pattern inline; it asserts the pattern works, not the watcher.go code yet — so it should pass already if the test is written correctly).

Then modify the watcher to use the pattern. In `skills/start-sensor/scripts/watcher.go`, replace the signal goroutine (lines 55-60 of current `watcher.go`) with:

```go
	stop := make(chan struct{})
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		s := <-ch
		fmt.Fprintf(os.Stderr, "watcher: %s received, draining\n", s)
		close(stop)
	}()
```

(The block was previously: `s := <-ch` was just `<-ch`, no fmt.Fprintf. The change is to capture the received signal and log it.)

Imports: confirm `"fmt"` is already in `watcher.go`'s import block (it is — used by `Fprintln` elsewhere).

- [ ] **Step 3: Build and re-test**

Run:
```bash
go build -tags=start_watcher ./skills/start-sensor/scripts/...
go test -tags=start_watcher ./skills/start-sensor/scripts/...
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add skills/start-sensor/scripts/watcher.go skills/start-sensor/scripts/watcher_test.go
git commit -m "$(cat <<'EOF'
feat(start-sensor): watcher logs the signal it received

The watcher's signal-handler goroutine now logs the received signal
to stderr (captured into watcher.log via start-sensor changes), so a
future macOS SIGTERM survival regression is diagnosable.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: Remove defensive SIGKILL from e2e; add migration e2e test

**Files:**
- Modify: `test/registry-discovery-e2e/registry_discovery_e2e_test.go`

- [ ] **Step 1: Remove the `killWatcherIfAlive` helper**

Delete lines 170-185 of `test/registry-discovery-e2e/registry_discovery_e2e_test.go` (the comment block + helper function). Use:

```bash
# Confirm the bytes you intend to remove first:
sed -n '170,185p' test/registry-discovery-e2e/registry_discovery_e2e_test.go
```

Then remove with your editor (the Edit tool, manual delete, etc.) so the block is gone.

- [ ] **Step 2: Remove the two `t.Cleanup` callers**

Locate the lines around 281-296 and 378-395 in `registry_discovery_e2e_test.go`. Each block looks like:

```go
	// Capture watcher PID so we can SIGKILL it if stop-sensor leaves it alive.
	if v, ok := startMD["watcher_pid"].(float64); ok {
		watcherPID = int(v)
	}
	t.Cleanup(func() {
		// Defensive: if stop-sensor failed to kill the watcher, SIGKILL it
		// to avoid leaking processes in the test runner.
		killWatcherIfAlive(watcherPID)
	})
```

Delete both blocks entirely — both the watcher_pid capture and the `t.Cleanup`. The remaining test body does not reference `watcherPID`, so removing the local variable is safe. If your editor complains about an unused `watcherPID`, also remove its declaration.

- [ ] **Step 3: Verify e2e tests still build**

Run: `go vet ./test/registry-discovery-e2e/...`

Expected: no output, no unused-variable errors.

- [ ] **Step 4: Add the new migration e2e test**

Append a new test at the end of `registry_discovery_e2e_test.go`:

```go
// TestSanitize_LegacyMinusOneViaListSensors verifies that a legacy
// running_sensors.json containing watcher_pid: -1 is sanitized in place
// by /list-sensors, and that the skill emits the precedence
// registry_migrated warn signal ahead of its main list signal.
func TestSanitize_LegacyMinusOneViaListSensors(t *testing.T) {
	repo := repoRoot(t)
	parent := t.TempDir()
	proj := filepath.Join(parent, "proj")
	if err := os.MkdirAll(filepath.Join(proj, "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".runtime", "sensors"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{
  "version": 1,
  "entries": [
    {
      "sensor_id": "run-api-local", "pid": 90006, "pgid": 90006,
      "watcher_pid": -1, "started_at": "2026-05-09T13:51:38Z",
      "command": "docker compose up", "log_dir": ".runtime/sensors/run-api-local",
      "held_by": [{"kind": "manual", "attached_at": "2026-05-09T13:51:38Z"}]
    }
  ]
}`)
	regFile := filepath.Join(proj, ".runtime", "sensors", "running_sensors.json")
	if err := os.WriteFile(regFile, legacy, 0o644); err != nil {
		t.Fatal(err)
	}

	binary := buildSkillBinary(t, repo, "list-sensors")
	stdout, stderr, exit := runIn(t, binary, proj, nil, nil)
	if exit != 0 {
		t.Fatalf("list-sensors exit=%d\nstdout:\n%s\nstderr:\n%s", exit, stdout, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d JSONL lines, want 2 (warn + list).\nstdout:\n%s", len(lines), stdout)
	}
	var warn map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &warn); err != nil {
		t.Fatalf("parse line 0: %v\nline: %s", err, lines[0])
	}
	if warn["verdict"] != "warn" {
		t.Errorf("line 0 verdict: %v", warn["verdict"])
	}
	if md, _ := warn["metadata"].(map[string]interface{}); md["kind"] != "registry_migrated" {
		t.Errorf("line 0 metadata.kind: %v", md["kind"])
	}
	var main map[string]interface{}
	if err := json.Unmarshal([]byte(lines[1]), &main); err != nil {
		t.Fatalf("parse line 1: %v\nline: %s", err, lines[1])
	}
	if main["verdict"] != "pass" {
		t.Errorf("line 1 verdict: %v", main["verdict"])
	}

	// File on disk now has watcher_pid = 0.
	migrated, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(migrated, &parsed); err != nil {
		t.Fatal(err)
	}
	entries, _ := parsed["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("entries: got %d, want 1", len(entries))
	}
	e0, _ := entries[0].(map[string]interface{})
	if pid, _ := e0["watcher_pid"].(float64); pid != 0 {
		t.Errorf("on-disk watcher_pid after migration: got %v, want 0", e0["watcher_pid"])
	}
}
```

This uses two helpers that already exist in the file (`repoRoot`, `runIn`) and one helper (`buildSkillBinary`) that the file uses elsewhere — confirm the name; check existing tests for the exact name. If the existing helper is named differently (e.g. `buildBinary`), adjust the call to match. Likely candidates:

```bash
grep -n "func build" test/registry-discovery-e2e/registry_discovery_e2e_test.go
```

Use whatever name returns the path to a compiled skill binary, e.g. `buildSkill(t, repo, "list-sensors")` or `buildSkillBinary(t, repo, "list-sensors")`.

- [ ] **Step 5: Run e2e**

Run: `go test ./test/registry-discovery-e2e/...`

Expected: PASS, including the new `TestSanitize_LegacyMinusOneViaListSensors` and the two existing tests whose defensive SIGKILL was removed.

- [ ] **Step 6: Final full-tree sanity**

Run:
```bash
go test ./lib/...
go test -tags=list_sensors ./skills/list-sensors/...
go test -tags=tail_sensor ./skills/tail-sensor/...
go test -tags=stop_sensor ./skills/stop-sensor/...
go test -tags=start_sensor ./skills/start-sensor/...
go test -tags=start_watcher ./skills/start-sensor/...
go test ./test/...
go vet -tags=run_computational ./...
go vet -tags=run_inferential ./...
go vet -tags=start_sensor ./...
go vet -tags=start_watcher ./...
go vet -tags=stop_sensor ./...
go vet -tags=list_sensors ./...
go vet -tags=tail_sensor ./...
```

Expected: all PASS, no vet output.

- [ ] **Step 7: Commit**

```bash
git add test/registry-discovery-e2e/registry_discovery_e2e_test.go
git commit -m "$(cat <<'EOF'
test(e2e): drop defensive SIGKILL; add legacy -1 migration test

Remove killWatcherIfAlive helper and its two t.Cleanup callers. With
the stopWatcher hardening from earlier in this PR, a watcher that
survives /stop-sensor's SIGTERM is now a real test failure — the
defensive SIGKILL was hiding regressions.

Add TestSanitize_LegacyMinusOneViaListSensors: end-to-end legacy
state migration through /list-sensors, asserting the precedence warn
signal + on-disk file rewrite.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Self-review checklist

- [x] Spec coverage:
  - `lib/registry/sanitize.go` types + ValidateEntry + SanitizeAll → Tasks 1, 2
  - `RegistryMigratedSignal` helper → Task 3
  - `Save` validates → Task 4
  - `LoadSanitized` → Task 5
  - `LookupSanitized` → Task 6
  - 4 skills migrate + precedence warn signal → Tasks 7, 8, 9, 11
  - `stopWatcher` widened signature + aggregate metadata → Task 10
  - Watcher `watcher.log` redirect + signal log → Tasks 11, 12
  - e2e SIGKILL helper removed + migration e2e test → Task 13
- [x] Types/signatures consistent across tasks:
  - `ValidateEntry(e RunningSensorEntry) error` — Task 1, used by Task 4
  - `SanitizeAll(rs *RunningSensors) []SanitizeReport` — Task 2, used by Task 5
  - `LoadSanitized(r Root) (RunningSensors, []SanitizeReport, error)` — Task 5, used by Task 6
  - `LookupSanitized(startDir string) (Result, []SanitizeReport, error)` — Task 6, used by Tasks 7, 8, 9, 11
  - `RegistryMigratedSignal(res Result, reports []SanitizeReport, sensorID string) map[string]interface{}` — Task 3, used by Tasks 7, 8, 9, 11
  - `stopWatcher(pid int) (killedForcefully bool, latencyMS int)` — Task 10, used in Task 10
- [x] No "TBD" / "implement later" / "add error handling" placeholders.
- [x] Every code-changing step has actual code blocks, not directions.

## Risks and notes

- **Helper subprocess pattern in Task 10** (test binary re-exec) is standard Go but requires `TestMain` discipline. If `stop_test.go` already has a `TestMain` for another reason, merge cases rather than duplicate.
- **macOS latency window** in `TestStopWatcher_RequiresSIGKILL` is `[950, 1500]`. On a heavily loaded macOS CI machine, this can be widened — the assertion is about ordering, not millisecond precision.
- **`buildSkillBinary` helper name in Task 13** is best confirmed by grepping the e2e test file before the task runs; adjust the call site to match the actual helper name.
- **Concurrent migration** (two skills running on the same registry with a `-1` entry): `LoadSanitized`'s re-`Save` is under flock; the loser does a redundant write with identical content. Documented as acceptable in the spec.
- **`watcher.log`'s mode is `O_APPEND`**: a sensor restarted multiple times accumulates lines. No rotation — diagnostic only; manual cleanup if it grows.
