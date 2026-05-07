// Package subprocess spawns a sensor's execution.command via `sh -c`,
// drains stdout and stderr concurrently, scans each line against the
// compiled output_parsing patterns, and emits one JSONL Signal per
// matching line. The returned StreamResult carries the bookkeeping the
// caller needs to build the aggregate Signal: exit code, timeout flag,
// elapsed time, the emitted individuals, and the original command string.
package subprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

// StreamConfig is the input to StreamSubprocess.
type StreamConfig struct {
	Command   string            // raw shell command, executed via sh -c
	Env       map[string]string // additional env vars
	TimeoutMS int               // hard cap; 0 means no timeout
	Patterns  []signal.Pattern  // compiled output_parsing.patterns
	Envelope  sensor.Envelope   // sensor_id, version, run_id, started_at, sensor_type
	Validator *schema.Validator // for per-individual signal validation; may be nil to skip
	Stdout    io.Writer         // JSONL goes here
	Stderr    io.Writer         // diagnostic messages (validation warnings, etc.)
}

// StreamResult holds what the caller needs to build the aggregate Signal.
type StreamResult struct {
	ExitCode    int
	TimedOut    bool
	ElapsedMS   int
	Individuals []map[string]interface{} // also already encoded onto Stdout
	CommandRun  string                   // exact string passed to sh -c
}

// StreamSubprocess spawns sh -c <Command>, scans merged stdout+stderr line by
// line, emits one JSONL Signal per matching line, and returns an aggregate-
// ready summary when the process exits or times out.
//
// It returns a non-nil error only for setup failures (missing command,
// pipe creation). A subprocess that fails to spawn (e.g. binary not found)
// is reported via a non-zero ExitCode, not an error.
func StreamSubprocess(ctx context.Context, cfg StreamConfig) (StreamResult, error) {
	if cfg.Command == "" {
		return StreamResult{}, errors.New("stream: empty command")
	}
	res := StreamResult{CommandRun: cfg.Command, ExitCode: -1}

	if cfg.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)
	if len(cfg.Env) > 0 {
		envList := append([]string{}, cmd.Environ()...)
		for k, v := range cfg.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = envList
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return res, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return res, fmt.Errorf("stderr pipe: %w", err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		// e.g. /bin/sh missing — extremely unlikely on POSIX hosts.
		res.ElapsedMS = int(time.Since(start) / time.Millisecond)
		return res, fmt.Errorf("start: %w", err)
	}

	// Drain stdout and stderr concurrently. Each goroutine pushes matched
	// individuals onto a shared buffered channel; main loop emits JSONL.
	type emit struct{ sig map[string]interface{} }
	emits := make(chan emit, 64)
	var wg sync.WaitGroup
	scan := func(r io.Reader) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			m, ok := signal.MatchLine(line, cfg.Patterns)
			if !ok {
				continue
			}
			emits <- emit{sig: buildIndividualSignal(cfg.Envelope, m)}
		}
	}
	wg.Add(2)
	go scan(stdoutPipe)
	go scan(stderrPipe)
	go func() { wg.Wait(); close(emits) }()

	for e := range emits {
		if cfg.Validator != nil {
			if err := cfg.Validator.Validate(schema.TargetSignal, e.sig); err != nil {
				fmt.Fprintf(cfg.Stderr, "warning: skipping invalid individual signal: %v\n", err)
				continue
			}
		}
		res.Individuals = append(res.Individuals, e.sig)
		_ = json.NewEncoder(cfg.Stdout).Encode(e.sig)
	}

	waitErr := cmd.Wait()
	res.ElapsedMS = int(time.Since(start) / time.Millisecond)
	res.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	_ = waitErr
	return res, nil
}

// buildIndividualSignal assembles a Signal-shaped map for one matched line.
// Fields with zero values are omitted (file/line_start/line_end/excerpt) so
// the Signal stays minimal when captures don't apply.
func buildIndividualSignal(env sensor.Envelope, m signal.PatternMatch) map[string]interface{} {
	ev := map[string]interface{}{"rationale": m.Rationale}
	if m.File != "" {
		ev["file"] = m.File
	}
	if m.LineStart != nil {
		ev["line_start"] = *m.LineStart
	}
	if m.LineEnd != nil {
		ev["line_end"] = *m.LineEnd
	}
	if m.Excerpt != "" {
		ev["excerpt"] = m.Excerpt
	}
	return map[string]interface{}{
		"sensor_id":   env.SensorID,
		"version":     env.Version,
		"run_id":      env.RunID,
		"started_at":  env.StartedAt,
		"finished_at": sensor.NowFn().Format("2006-01-02T15:04:05Z"),
		"verdict":     m.Verdict,
		"severity":    m.Severity,
		"confidence":  1.0,
		"evidence":    []interface{}{ev},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind": "individual",
			"line": m.Line,
		},
	}
}
