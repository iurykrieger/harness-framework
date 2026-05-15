//go:build replay_fixture

// Command replay-fixture exercises a sensor against a fixture file as a
// regular run: it loads the sensor JSON, swaps execution.command for
// "cat <fixture>", and feeds the modified sensor through the orchestrator
// just like /run-sensor would. The run is registered under the project's
// real .harness/runtime/<sensor-id>/<run-id>/ tree so replay attempts sit
// alongside any other valid run of that sensor in the runtime history.
//
// sensor.id is preserved verbatim — the runtime directory naturally aligns
// with the sensor it exercises rather than being shoved into a synthetic
// "replay-*" namespace.
//
// Usage:
//
//	go run -tags=replay_fixture ./skills/detect-sensors/scripts \
//	  --sensor=PATH --fixture=PATH
//
// Exit codes mirror the orchestrator: 0 on a clean aggregate, 1 on
// schema/DAG/cascade failure, 2 on usage or I/O error before the
// orchestrator is invoked.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay-fixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sensorPath, fixturePath, schemasDir string
	fs.StringVar(&sensorPath, "sensor", "", "path to the sensor JSON (required)")
	fs.StringVar(&fixturePath, "fixture", "", "path to the fixture file (required)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if sensorPath == "" || fixturePath == "" {
		fmt.Fprintln(stderr, "usage: replay-fixture --sensor=PATH --fixture=PATH [--schemas-dir=DIR]")
		return 2
	}

	body, err := schema.ReadAsJSON(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "read sensor:", err)
		return 2
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		fmt.Fprintln(stderr, "parse sensor:", err)
		return 2
	}
	absFixture, err := filepath.Abs(fixturePath)
	if err != nil {
		fmt.Fprintln(stderr, "abs fixture:", err)
		return 2
	}
	execBlock, ok := raw["execution"].(map[string]interface{})
	if !ok {
		fmt.Fprintln(stderr, "sensor.execution is not an object")
		return 2
	}
	execBlock["command"] = fmt.Sprintf("cat %q", absFixture)

	projectRoot, err := resolveProjectRoot(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "resolve project root:", err)
		return 2
	}

	tempSensor, err := os.CreateTemp("", "replay-sensor-*.yaml")
	if err != nil {
		fmt.Fprintln(stderr, "create temp sensor:", err)
		return 2
	}
	tempPath := tempSensor.Name()
	defer func() {
		if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(stderr, "cleanup temp sensor:", err)
		}
	}()
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		tempSensor.Close()
		fmt.Fprintln(stderr, "marshal temp sensor:", err)
		return 2
	}
	yamlBytes, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		tempSensor.Close()
		fmt.Fprintln(stderr, "convert temp sensor to YAML:", err)
		return 2
	}
	if _, err := tempSensor.Write(yamlBytes); err != nil {
		tempSensor.Close()
		fmt.Fprintln(stderr, "write temp sensor:", err)
		return 2
	}
	if err := tempSensor.Close(); err != nil {
		fmt.Fprintln(stderr, "close temp sensor:", err)
		return 2
	}

	ctx, cancel := signalCancellableContext()
	defer cancel()
	return orchestrator.RunWithDepsRoot(ctx, tempPath, projectRoot, schemasDir, stdout, stderr)
}

// resolveProjectRoot finds the user's project root so the orchestrator
// persists this run under <projectRoot>/.harness/runtime/<sensor-id>/.
// Preference order:
//
//  1. registry.Lookup(cwd) — honors HARNESS_REGISTRY_ROOT, then walks up
//     from cwd looking for the .harness/ marker. This is the canonical
//     discovery path other skills use.
//  2. Three Dir() calls above sensorPath, assuming the sensor lives at
//     the canonical <projectRoot>/.harness/sensors/<id>.yaml location.
//     Useful when invoked from outside the project tree (e.g. CI).
//
// Both candidates are required to be existing directories before they
// are accepted; otherwise the next strategy is tried.
func resolveProjectRoot(sensorPath string) (string, error) {
	cwd, _ := os.Getwd()
	if res, err := registry.Lookup(cwd); err == nil {
		return res.ProjectRoot, nil
	}
	abs, err := filepath.Abs(sensorPath)
	if err != nil {
		return "", fmt.Errorf("abs(%q): %w", sensorPath, err)
	}
	candidate := filepath.Dir(filepath.Dir(filepath.Dir(abs)))
	info, err := os.Stat(filepath.Join(candidate, ".harness"))
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("no project root found: cwd %q is not inside a harness project, and %q has no .harness/ child", cwd, candidate)
	}
	return candidate, nil
}

func signalCancellableContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
