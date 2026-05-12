//go:build replay_fixture

// Command replay-fixture runs a sensor against a fixture file without
// polluting the project's .harness/runtime/ tree. It loads the sensor
// JSON, overrides execution.command to "cat <fixture>", and invokes
// the runner (run-computational | run-inferential) with
// HARNESS_REGISTRY_ROOT pointed at an ephemeral tempdir. The runner's
// stdout/stderr stream through; the temp tree is removed on exit.
//
// The sensor.id field is preserved verbatim — earlier versions of this
// step (a shell snippet in skills/detect-sensors/SKILL.md) mutated id
// to "replay-" + id, which leaked into .harness/runtime/replay-<id>/
// once runtime persistence shipped (issue #28).
//
// Usage:
//
//	go run -tags=replay_fixture ./skills/detect-sensors/scripts \
//	  --sensor=PATH --fixture=PATH
//
// Exit codes: same as the underlying runner. 2 on usage or I/O error
// before the runner is spawned.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay-fixture", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sensorPath, fixturePath string
	fs.StringVar(&sensorPath, "sensor", "", "path to the sensor JSON (required)")
	fs.StringVar(&fixturePath, "fixture", "", "path to the fixture file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if sensorPath == "" || fixturePath == "" {
		fmt.Fprintln(stderr, "usage: replay-fixture --sensor=PATH --fixture=PATH")
		return 2
	}

	body, err := os.ReadFile(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "read sensor:", err)
		return 2
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		fmt.Fprintln(stderr, "parse sensor:", err)
		return 2
	}
	sensorType, _ := raw["type"].(string)
	tag := tagForType(sensorType)
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

	tempRoot, err := os.MkdirTemp("", "harness-replay-")
	if err != nil {
		fmt.Fprintln(stderr, "mkdtemp:", err)
		return 2
	}
	defer func() {
		if err := os.RemoveAll(tempRoot); err != nil {
			fmt.Fprintln(stderr, "cleanup:", err)
		}
	}()

	tempSensor, err := os.CreateTemp(tempRoot, "sensor-*.json")
	if err != nil {
		fmt.Fprintln(stderr, "create temp sensor:", err)
		return 2
	}
	enc := json.NewEncoder(tempSensor)
	enc.SetIndent("", "  ")
	if err := enc.Encode(raw); err != nil {
		tempSensor.Close()
		fmt.Fprintln(stderr, "marshal temp sensor:", err)
		return 2
	}
	if err := tempSensor.Close(); err != nil {
		fmt.Fprintln(stderr, "close temp sensor:", err)
		return 2
	}

	cmd := exec.Command("go", "run", "-tags="+tag, "./skills/run-sensor/scripts", tempSensor.Name())
	if root := repoRoot(); root != "" {
		cmd.Dir = root
	}
	cmd.Env = append(os.Environ(), "HARNESS_REGISTRY_ROOT="+tempRoot)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(stderr, "exec:", err)
		return 2
	}
	return 0
}

// tagForType selects the runner's build tag based on sensor.type.
// Defaults to run_computational when the field is empty or unknown —
// the schema's discriminator enforces the inferential branch, so
// any non-"inferential" value the runner sees will fail validation
// downstream regardless of our pick here.
func tagForType(sensorType string) string {
	if sensorType == "inferential" {
		return "run_inferential"
	}
	return "run_computational"
}

// repoRoot walks up from cwd looking for a directory that contains
// both go.mod and skills/run-sensor/scripts (the runner package).
// Mirrors skills/heal-sensor/scripts/retry-original.go::repoRoot
// — duplicated rather than coupled per project rule #4. Returns
// "" if no ancestor matches; the caller may fall back to cwd.
func repoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := cwd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "skills", "run-sensor", "scripts")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}
