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
		_ = filepath.Join // keep import
		t.Fatalf("reaped: got %d", len(reaped))
	}
}
