package coredetect

import (
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

func scaffoldCheckServerStartup(s stack.Stack) *Draft {
	hasHTTPServer := false
	for _, c := range s.Components {
		if string(c.Role) == "http-server" {
			hasHTTPServer = true
			break
		}
	}
	if !hasHTTPServer {
		return nil
	}
	timeoutMS := 30000
	return &Draft{
		Version:     "0.1.0",
		Name:        "check-server-startup",
		Kind:        sensor.KindObservation,
		Type:        sensor.TypeComputational,
		Regulation:  sensor.RegulationBehaviour,
		Phase:       sensor.PhaseOnDemand,
		Determinism: sensor.DeterminismHigh,
		Output:      sensor.OutputSingle,
		Description: "Probes the server's health endpoint to confirm startup.",
		Requires: []sensor.Requirement{
			{Kind: sensor.RequireSensor, ID: "run-project"},
		},
		Triggers: []sensor.Trigger{{On: sensor.TriggerManual}},
		Cost: sensor.Cost{
			Class:   sensor.CostClassCheap,
			Latency: sensor.Latency{P50MS: 100, P95MS: 2000, TimeoutMS: &timeoutMS},
			Compute: &sensor.Compute{CPU: sensor.CPULow, MemoryMB: 64},
		},
		Execution: sensor.Execution{
			Command: "echo 'TODO: curl --fail http://localhost:<port>/health' && false",
			ExitCodeMap: []sensor.ExitCodeMapEntry{
				{ExitCode: 0, Verdict: "pass", Severity: "info"},
				{ExitCode: "*", Verdict: "fail", Severity: "high"},
			},
		},
	}
}

func init() { Register("check-server-startup", scaffoldCheckServerStartup) }
