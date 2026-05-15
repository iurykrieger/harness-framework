//go:build heal_retry_original

// Command retry-original re-invokes the original sensor's runner
// exactly once. Reads the sensor's `type` (computational | inferential)
// to pick the build tag, shells out to
// `go run -tags=<tag> ./skills/run-sensor/scripts <sensor>`, and pipes
// stdout/stderr through.
//
// Usage:
//
//	go run -tags=heal_retry_original ./skills/heal-sensor/scripts \
//	  --sensor=PATH
//
// Exit codes: same as the underlying runner.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("retry-original", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var sensorPath string
	fs.StringVar(&sensorPath, "sensor", "", "path to the sensor JSON to retry (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if sensorPath == "" {
		fmt.Fprintln(stderr, "usage: retry-original --sensor=PATH")
		return 2
	}

	body, err := schema.ReadAsJSON(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "read sensor:", err)
		return 2
	}
	var v struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		fmt.Fprintln(stderr, "parse sensor:", err)
		return 2
	}
	tag := "run_computational"
	if v.Type == "inferential" {
		tag = "run_inferential"
	}

	pluginRoot := os.Getenv("CLAUDE_PLUGIN_ROOT")
	if pluginRoot == "" {
		fmt.Fprintln(stderr, "error: CLAUDE_PLUGIN_ROOT not set")
		return 2
	}

	cmd := exec.Command("go", "-C", pluginRoot, "run", "-tags="+tag, "./skills/run-sensor/scripts", sensorPath)
	cmd.Env = append(os.Environ(), "GOWORK=off")
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
