// Package subprocess spawns a sensor's execution.command via `sh -c`,
// drains stdout and stderr concurrently, scans each line against the
// compiled output_parsing patterns, and emits one JSONL Signal per
// matching line. The returned StreamResult carries the bookkeeping the
// caller needs to build the aggregate Signal: exit code, timeout flag,
// elapsed time, the emitted individuals, and the original command string.
package subprocess

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
)

// streamStderrExcerptCap bounds how much stderr text the streamer
// retains for downstream heuristics (heal_hint emission, etc.). Mirrors
// the cap used by RunStep so lifecycle and command captures are
// comparable.
const streamStderrExcerptCap = 4096

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
	Dir       string            // working directory for the subprocess (empty = inherit)

	// RunDir, when non-empty, points at .harness/runtime/<id>/<run-id>/.
	// The streamer tees subprocess stdout+stderr verbatim into <RunDir>/raw.log
	// and appends individual + aggregate Signals to <RunDir>/signals.log.
	// Empty preserves the legacy stdout-only behavior.
	RunDir string
}

// StreamResult holds what the caller needs to build the aggregate Signal.
type StreamResult struct {
	ExitCode    int
	TimedOut    bool
	ElapsedMS   int
	Individuals []map[string]interface{} // also already encoded onto Stdout
	CommandRun  string                   // exact string passed to sh -c
	// StdoutCapture holds the verbatim stdout the subprocess produced,
	// independent of pattern matching and of RunDir. Callers that need
	// the raw output (e.g. shell step output extraction) read it here
	// instead of round-tripping through raw.log.
	StdoutCapture string
	// StderrExcerpt holds up to streamStderrExcerptCap bytes of the
	// subprocess's stderr stream, captured verbatim regardless of
	// whether output_parsing patterns matched. Consumed by the
	// orchestrator to drive setup-shape heuristics (e.g. heal_hint
	// emission) for single-mode failures whose only useful signal is
	// the stderr text.
	StderrExcerpt string
}

// StreamHandle is the result of Start: a spawned subprocess whose
// PID/PGID are known, but whose stdout/stderr haven't been drained yet.
// The caller may set RunDir / Envelope (which control persistence and
// signal envelope fields) before calling Run(), which drains and waits.
//
// This two-phase split exists so the orchestrator can synthesize a
// run_id (using the just-spawned PID) and create the <run-id>/ directory
// BEFORE the streaming goroutines start opening raw.log / signals.log.
type StreamHandle struct {
	PID  int
	PGID int

	// Internal fields needed by Run()
	cmd        *exec.Cmd
	ctx        context.Context
	cancel     context.CancelFunc // non-nil when TimeoutMS > 0; invoked in Run()
	cfg        *StreamConfig      // pointer so SetRunDir/SetEnvelope can mutate
	stdoutPipe io.Reader
	stderrPipe io.Reader
	startedAt  time.Time
}

// SetRunDir updates the cfg's RunDir; must be called before Run().
func (h *StreamHandle) SetRunDir(dir string) { h.cfg.RunDir = dir }

// SetEnvelope updates the cfg's Envelope; must be called before Run().
func (h *StreamHandle) SetEnvelope(env sensor.Envelope) { h.cfg.Envelope = env }

// Kill kills the subprocess group. Used by the orchestrator if mkdir or
// registry insert fail between Start and Run.
func (h *StreamHandle) Kill() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.cmd != nil && h.cmd.Process != nil {
		// Kill the whole process group if available
		if pgid, err := syscall.Getpgid(h.cmd.Process.Pid); err == nil {
			if killErr := syscall.Kill(-pgid, syscall.SIGKILL); killErr == nil {
				return nil
			}
		}
		return h.cmd.Process.Kill()
	}
	return nil
}

// Start spawns sh -c <Command> and returns once cmd.Start() succeeds.
// Drains and waits are deferred until Run() is called on the returned
// handle. On error, no subprocess remains.
func Start(ctx context.Context, cfg StreamConfig) (*StreamHandle, error) {
	if cfg.Command == "" {
		return nil, errors.New("stream: empty command")
	}

	var cancel context.CancelFunc
	if cfg.TimeoutMS > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}
	if len(cfg.Env) > 0 {
		envList := append([]string{}, cmd.Environ()...)
		for k, v := range cfg.Env {
			envList = append(envList, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = envList
	}
	// Place the subprocess in its own process group so cancellation can
	// target the whole tree (-pgid). Mirrors what SpawnDetached does,
	// scoped to the streaming path.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// On ctx cancellation (parent timeout OR orchestrator cancelling
	// because a live blocking dep died mid-run), SIGTERM the whole
	// process group rather than only the head process. The default
	// CommandContext cancel sends SIGKILL to cmd.Process alone, which
	// leaves grandchildren (a sh wait-loop's curl, a docker compose's
	// container, …) running and ignores the orchestrator's intent. The
	// WaitDelay bound guarantees a SIGKILL fallback if the group ignores
	// SIGTERM, so cancellation is bounded.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if pgid, perr := syscall.Getpgid(cmd.Process.Pid); perr == nil {
			return syscall.Kill(-pgid, syscall.SIGTERM)
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, fmt.Errorf("start: %w", err)
	}
	pid := cmd.Process.Pid
	pgid, perr := syscall.Getpgid(pid)
	if perr != nil {
		pgid = pid // Setpgid implies pgid == pid by default
	}

	cfgCopy := cfg
	return &StreamHandle{
		PID:        pid,
		PGID:       pgid,
		cmd:        cmd,
		ctx:        ctx,
		cancel:     cancel,
		cfg:        &cfgCopy,
		stdoutPipe: stdoutPipe,
		stderrPipe: stderrPipe,
		startedAt:  startedAt,
	}, nil
}

// Run drains the subprocess and returns the StreamResult. Equivalent to
// the original StreamSubprocess body from cmd.Start onward.
func (h *StreamHandle) Run() StreamResult {
	if h.cancel != nil {
		defer h.cancel()
	}
	cfg := h.cfg
	res := StreamResult{CommandRun: cfg.Command, ExitCode: -1}

	// When RunDir is set, open raw.log in append mode so subprocess
	// stdout+stderr lines are teed verbatim alongside the JSONL stream.
	var rawLogF *os.File
	if cfg.RunDir != "" {
		f, ferr := os.OpenFile(
			filepath.Join(cfg.RunDir, "raw.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644,
		)
		if ferr != nil {
			// Wait for the subprocess to drain on its own to avoid leaks.
			_ = h.cmd.Wait()
			res.ElapsedMS = int(time.Since(h.startedAt) / time.Millisecond)
			if h.cmd.ProcessState != nil {
				res.ExitCode = h.cmd.ProcessState.ExitCode()
			}
			fmt.Fprintf(cfg.Stderr, "warning: open raw.log: %v\n", ferr)
			return res
		}
		rawLogF = f
		defer rawLogF.Close()
	}

	// When RunDir is set, open signals.log in append mode so each individual
	// Signal emitted to stdout is also persisted to disk.
	var signalsLogF *os.File
	if cfg.RunDir != "" {
		f, ferr := os.OpenFile(
			filepath.Join(cfg.RunDir, "signals.log"),
			os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644,
		)
		if ferr != nil {
			_ = h.cmd.Wait()
			res.ElapsedMS = int(time.Since(h.startedAt) / time.Millisecond)
			if h.cmd.ProcessState != nil {
				res.ExitCode = h.cmd.ProcessState.ExitCode()
			}
			fmt.Fprintf(cfg.Stderr, "warning: open signals.log: %v\n", ferr)
			return res
		}
		signalsLogF = f
		defer signalsLogF.Close()
	}

	// Drain stdout and stderr concurrently. Each goroutine pushes matched
	// individuals onto a shared buffered channel; main loop emits JSONL.
	// stderr is additionally tee'd into a capped byte buffer so the
	// orchestrator can inspect the verbatim text for setup-shape
	// heuristics (heal_hint emission) when output_parsing patterns
	// match nothing.
	type emit struct{ sig map[string]interface{} }
	emits := make(chan emit, 64)
	var wg sync.WaitGroup
	var stderrBuf, stdoutBuf bytes.Buffer
	var stderrMu, stdoutMu sync.Mutex
	var rawLogMu sync.Mutex
	scan := func(r io.Reader, captureStderr bool) {
		defer wg.Done()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if rawLogF != nil {
				rawLogMu.Lock()
				_, _ = rawLogF.WriteString(line + "\n")
				rawLogMu.Unlock()
			}
			if captureStderr {
				stderrMu.Lock()
				if remaining := streamStderrExcerptCap - stderrBuf.Len(); remaining > 0 {
					if len(line)+1 <= remaining {
						stderrBuf.WriteString(line)
						stderrBuf.WriteByte('\n')
					} else {
						stderrBuf.WriteString(line[:remaining])
					}
				}
				stderrMu.Unlock()
			} else {
				stdoutMu.Lock()
				stdoutBuf.WriteString(line)
				stdoutBuf.WriteByte('\n')
				stdoutMu.Unlock()
			}
			m, ok := signal.MatchLine(line, cfg.Patterns)
			if !ok {
				continue
			}
			emits <- emit{sig: buildIndividualSignal(cfg.Envelope, m)}
		}
	}
	wg.Add(2)
	go scan(h.stdoutPipe, false)
	go scan(h.stderrPipe, true)
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
		if signalsLogF != nil {
			_ = json.NewEncoder(signalsLogF).Encode(e.sig)
		}
	}

	waitErr := h.cmd.Wait()
	res.ElapsedMS = int(time.Since(h.startedAt) / time.Millisecond)
	res.TimedOut = errors.Is(h.ctx.Err(), context.DeadlineExceeded)
	if h.cmd.ProcessState != nil {
		res.ExitCode = h.cmd.ProcessState.ExitCode()
	}
	stderrMu.Lock()
	res.StderrExcerpt = stderrBuf.String()
	stderrMu.Unlock()
	stdoutMu.Lock()
	res.StdoutCapture = stdoutBuf.String()
	stdoutMu.Unlock()
	_ = waitErr
	return res
}

// StreamSubprocess spawns sh -c <Command>, scans merged stdout+stderr line by
// line, emits one JSONL Signal per matching line, and returns an aggregate-
// ready summary when the process exits or times out.
//
// It returns a non-nil error only for setup failures (missing command,
// pipe creation). A subprocess that fails to spawn (e.g. binary not found)
// is reported via a non-zero ExitCode, not an error.
//
// Thin wrapper over Start + Run; kept for backwards compatibility.
func StreamSubprocess(ctx context.Context, cfg StreamConfig) (StreamResult, error) {
	h, err := Start(ctx, cfg)
	if err != nil {
		return StreamResult{CommandRun: cfg.Command, ExitCode: -1}, err
	}
	return h.Run(), nil
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
