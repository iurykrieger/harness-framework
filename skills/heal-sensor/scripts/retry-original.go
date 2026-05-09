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
	"path/filepath"
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

	body, err := os.ReadFile(sensorPath)
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
	cmd := exec.Command("go", "run", "-tags="+tag, "./skills/run-sensor/scripts", sensorPath)
	if root := repoRoot(); root != "" {
		cmd.Dir = root
	}
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

// repoRoot walks up from cwd looking for a directory that contains
// both go.mod and skills/run-sensor/scripts (the runner package). When
// found, returns its absolute path; otherwise returns "" so the caller
// can fall back to cwd. Keeps the script invocable from anywhere
// inside the harness checkout (including its own scripts/ dir, where
// `go test` runs).
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
