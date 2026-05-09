//go:build heal_apply_safe

// Command apply-safe is a CLI wrapper that reads a Setup Plan and the
// failed sensor, then runs the Plan's auto_apply items through
// lib/heal.Apply (allowlist-gated idempotent file mutations). Emits a
// JSON report.
//
// Usage:
//
//	go run -tags=heal_apply_safe ./skills/heal-sensor/scripts \
//	  --plan=PATH --sensor=PATH --root=DIR
//
// Exit codes: 0 results emitted; 2 usage / I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("apply-safe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var planPath, sensorPath, root string
	fs.StringVar(&planPath, "plan", "", "Setup Plan JSON (required)")
	fs.StringVar(&sensorPath, "sensor", "", "failing sensor JSON (required)")
	fs.StringVar(&root, "root", "", "project root (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if planPath == "" || sensorPath == "" || root == "" {
		fmt.Fprintln(stderr, "usage: apply-safe --plan=PATH --sensor=PATH --root=DIR")
		return 2
	}

	planBody, err := os.ReadFile(planPath)
	if err != nil {
		fmt.Fprintln(stderr, "read plan:", err)
		return 2
	}
	plan, err := heal.ParsePlan(planBody)
	if err != nil {
		fmt.Fprintln(stderr, "parse plan:", err)
		return 2
	}
	sensorBody, err := os.ReadFile(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "read sensor:", err)
		return 2
	}
	failed, err := failedSensorView(sensorBody)
	if err != nil {
		fmt.Fprintln(stderr, "parse sensor:", err)
		return 2
	}

	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: failed}, plan.AutoApply)
	out := map[string]interface{}{"results": resultsForJSON(results)}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	return 0
}

func failedSensorView(body []byte) (heal.FailedSensor, error) {
	var v struct {
		ID       string `json:"id"`
		Requires struct {
			Env []struct {
				Name string `json:"name"`
			} `json:"env"`
			Tools   []string `json:"tools"`
			Context []string `json:"context"`
		} `json:"requires"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return heal.FailedSensor{}, err
	}
	envs := make([]string, 0, len(v.Requires.Env))
	for _, e := range v.Requires.Env {
		envs = append(envs, e.Name)
	}
	return heal.FailedSensor{ID: v.ID, EnvNames: envs, Tools: v.Requires.Tools, Context: v.Requires.Context}, nil
}

type resultJSON struct {
	Action     heal.Action `json:"action"`
	Applied    bool        `json:"applied"`
	NeedsInput bool        `json:"needs_input"`
	Reason     string      `json:"reason,omitempty"`
}

func resultsForJSON(in []heal.ApplyResult) []resultJSON {
	out := make([]resultJSON, 0, len(in))
	for _, r := range in {
		out = append(out, resultJSON{Action: r.Action, Applied: r.Applied, NeedsInput: r.NeedsInput, Reason: r.Reason})
	}
	return out
}
