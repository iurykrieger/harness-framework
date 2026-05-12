//go:build stop_sensor

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/iurykrieger/harness-framework/lib/cli"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

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
	// Reap the helper as soon as it exits so IsPIDAlive (signal-0
	// probe) doesn't keep returning true for the zombie. Without this,
	// stopWatcher's poll loop times out even when SIGTERM killed the
	// helper instantly.
	waited := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(waited)
	}()
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waited
	})
	// Give the helper a moment to install its signal handler.
	time.Sleep(150 * time.Millisecond)
	return cmd.Process.Pid
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

// bootstrapFor wraps a registry.Result in a cli.BootstrapResult suitable for
// passing to runStop in tests. The schema validator is loaded so that signal
// validation runs; errors are written to errBuf.
func bootstrapFor(t *testing.T, res registry.Result, errBuf *bytes.Buffer) cli.BootstrapResult {
	t.Helper()
	v, _ := schema.LoadValidator("", errBuf)
	return cli.BootstrapResult{
		Res:       res,
		Validator: v,
		Diagnose:  registry.DiagnoseMetadata(res),
	}
}

func TestStop_RegistryFileAbsent_Error(t *testing.T) {
	root := t.TempDir()
	res := resultFor(t, root, false)
	var errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit, sig := runStop(b, []string{"missing"}, false)
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
	var errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit, sig := runStop(b, []string{"missing"}, false)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	if sig["verdict"] != "warn" || sig["metadata"].(map[string]interface{})["kind"] != "not_running" {
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
				SensorID: "live", RunID: "1-aa", Blocking: true,
				PID: registry.SelfPID(), PGID: registry.SelfPID(), HeldBy: []registry.HeldByEntry{
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
	var errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	exit, sig := runStop(b, []string{"live"}, false)
	if exit != 0 {
		t.Fatalf("exit: got %d, want 0", exit)
	}
	md := sig["metadata"].(map[string]interface{})
	if md["kind"] != "held" {
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
				RunID:    "1-bb",
				Blocking: true,
				PID:      3_999_998,
				PGID:     3_999_998,
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
	var errBuf bytes.Buffer
	b := bootstrapFor(t, res, &errBuf)
	_, sig := runStop(b, []string{"live"}, true)
	md := sig["metadata"].(map[string]interface{})
	reaped, _ := md["reaped_holders"].([]interface{})
	if len(reaped) != 1 {
		_ = json.NewEncoder(os.Stdout).Encode(sig)
		_ = filepath.Join // keep import
		t.Fatalf("reaped: got %d", len(reaped))
	}
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
	// Give the parent's Wait goroutine a moment to reap the killed
	// helper so IsPIDAlive (signal-0 probe) stops returning true for
	// the zombie.
	deadline := time.Now().Add(500 * time.Millisecond)
	for registry.IsPIDAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
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

func TestStop_BlockingFalse_TerminatesRunnerSubprocess(t *testing.T) {
	proj := t.TempDir()
	// Spawn a real, long-running subprocess we'll target. Use Setpgid so
	// kill(-pgid, …) reaches only the subprocess group, not the test
	// binary's group.
	sub := exec.Command("sleep", "30")
	sub.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := sub.Start(); err != nil {
		t.Skipf("cannot spawn sleep: %v", err)
	}
	defer func() { _ = sub.Process.Kill(); _, _ = sub.Process.Wait() }()

	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{{
		SensorID:  "alpha",
		RunID:     fmt.Sprintf("%d-runX", sub.Process.Pid),
		Blocking:  false,
		PID:       sub.Process.Pid,
		PGID:      sub.Process.Pid,
		StartedAt: "2026-05-11T00:00:00Z",
	}}}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	res := resultFor(t, proj, true)
	exit, sig := runStop(res, []string{"alpha"}, false)
	if exit != 0 {
		t.Fatalf("exit=%d, sig=%+v", exit, sig)
	}
	// Subprocess should be reaped (Wait returns once it exits).
	done := make(chan struct{})
	go func() { _, _ = sub.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("subprocess pid=%d did not exit after stop", sub.Process.Pid)
	}
	// Entry should be removed.
	after, _ := registry.Load(r)
	if len(after.Entries) != 0 {
		t.Errorf("entry not removed: %+v", after.Entries)
	}
	if sig != nil {
		md, _ := sig["metadata"].(map[string]interface{})
		if md != nil && md["kind"] != "stopped" && md["kind"] != "aggregate" {
			t.Errorf("metadata.kind: got %v, want stopped or aggregate", md["kind"])
		}
	}
}

func TestStop_BlockingPreferred_WhenMixedActives(t *testing.T) {
	// Two entries for "alpha": one blocking:true (real PID), one blocking:false.
	// /stop-sensor alpha (no run-id) must target the blocking:true one.
	proj := t.TempDir()
	blockingSub := exec.Command("sleep", "30")
	blockingSub.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := blockingSub.Start(); err != nil {
		t.Skipf("spawn: %v", err)
	}
	defer func() { _ = blockingSub.Process.Kill(); _, _ = blockingSub.Process.Wait() }()
	nonBlockingSub := exec.Command("sleep", "30")
	nonBlockingSub.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := nonBlockingSub.Start(); err != nil {
		t.Skipf("spawn: %v", err)
	}
	defer func() { _ = nonBlockingSub.Process.Kill(); _, _ = nonBlockingSub.Process.Wait() }()

	r := registry.NewRoot(proj)
	if err := os.MkdirAll(r.SensorsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	rs := registry.RunningSensors{Version: 1, Entries: []registry.RunningSensorEntry{
		{
			SensorID: "alpha", RunID: "nb-1", Blocking: false,
			PID: nonBlockingSub.Process.Pid, PGID: nonBlockingSub.Process.Pid,
			StartedAt: "2026-05-11T00:00:00Z",
		},
		{
			SensorID: "alpha", RunID: "b-1", Blocking: true,
			PID: blockingSub.Process.Pid, PGID: blockingSub.Process.Pid,
			WatcherPID: 1, StartedAt: "2026-05-11T00:00:00Z",
		},
	}}
	if err := registry.Save(r, rs); err != nil {
		t.Fatal(err)
	}

	res := resultFor(t, proj, true)
	exit, _ := runStop(res, []string{"alpha"}, false)
	if exit != 0 {
		t.Fatalf("exit=%d", exit)
	}
	after, _ := registry.Load(r)
	var leftBlocking, leftNonBlocking int
	for _, e := range after.Entries {
		if e.Blocking {
			leftBlocking++
		} else {
			leftNonBlocking++
		}
	}
	if leftBlocking != 0 {
		t.Errorf("blocking entry not removed: %+v", after.Entries)
	}
	if leftNonBlocking != 1 {
		t.Errorf("non-blocking entry mistakenly removed: %+v", after.Entries)
	}
}
