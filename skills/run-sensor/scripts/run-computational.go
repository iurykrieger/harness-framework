//go:build run_computational

// Command run-computational runs a streaming computational sensor end-to-end,
// resolving and executing its requires[kind=sensor] dependency graph in topological order.
//
// Usage:
//
//	go run -tags=run_computational ./skills/run-sensor/scripts <sensor-id>
//
// Stdout is JSONL: every dep's aggregate Signal first, then the requested
// sensor's individual Signals (one per matched output line), terminated by
// the requested sensor's aggregate Signal as the LAST line. Exit codes:
// 0 ok (root ran and emitted its aggregate), 1 root did not run (DAG
// failure or dep cascade — a cascade Signal is still emitted to stdout),
// 2 usage or I/O error.
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

	"github.com/iurykrieger/harness-framework/lib/orchestrator"
	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// signalCancellableContext returns a context that is cancelled on SIGINT
// or SIGTERM. The orchestrator's RunOne/RunOneWithRoot inspects ctx.Err()
// when building the aggregate and flips metadata.terminated_externally to
// true when non-nil. exec.CommandContext propagates the cancellation to
// the subprocess via SIGKILL, so the runner exits cleanly instead of
// leaving an orphaned subprocess (and, when persistence is on, a stale
// registry entry).
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

func main() {
	cwd, _ := os.Getwd()
	projectRoot := resolveProjectRoot(cwd)
	os.Exit(run(os.Args[1:], projectRoot, os.Stdout, os.Stderr))
}

// run is the testable entry point. projectRoot is the directory from which
// sensor ids are resolved (sensors/<id>.json); pass os.Getwd() for production.
func run(args []string, projectRoot string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run-computational", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var schemasDir string
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (default: walk up from cwd)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "usage: run-computational [--schemas-dir=DIR] <sensor-id>")
		return 2
	}

	arg := rest[0]

	// Accept either a bare sensor id (^[a-z][a-z0-9-]*$) or an absolute
	// file path. Absolute paths are resolved directly; ids are looked up
	// under <projectRoot>/sensors/<id>.json.
	var resolvedPath string
	if filepath.IsAbs(arg) {
		resolvedPath = arg
	} else {
		p, err := sensor.ResolveByID(arg, projectRoot)
		if err != nil {
			fmt.Fprintln(stderr, "error: resolve:", err)
			return 2
		}
		resolvedPath = p
	}

	var sensorJSON map[string]interface{}
	if b, rerr := os.ReadFile(resolvedPath); rerr != nil {
		fmt.Fprintln(stderr, "error: read:", rerr)
		return 2
	} else if jerr := json.Unmarshal(b, &sensorJSON); jerr != nil {
		fmt.Fprintln(stderr, "error: parse:", jerr)
		return 2
	}

	if execMap, _ := sensorJSON["execution"].(map[string]interface{}); execMap != nil {
		if blocking, _ := execMap["blocking"].(bool); blocking {
			fmt.Fprintln(stderr, "error: sensor is blocking; use /start-sensor instead")
			return 2
		}
	}

	ctx, cancel := signalCancellableContext()
	defer cancel()
	// When an absolute path was given, use the path-based variant so that
	// projectRoot is derived from the sensor's location on disk (not from the
	// runner's own cwd, which is the plugin root when invoked via go -C).
	if filepath.IsAbs(arg) {
		return orchestrator.RunWithDeps(ctx, resolvedPath, schemasDir, stdout, stderr)
	}
	return orchestrator.RunWithDepsRoot(ctx, arg, projectRoot, schemasDir, stdout, stderr)
}
