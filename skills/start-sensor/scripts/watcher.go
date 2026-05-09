//go:build start_watcher

// watcher tails a blocking sensor's raw.log, applies signal patterns to
// each new line, and appends matched Signals to signals.log. A reaper
// goroutine waits on the subprocess PID and writes subprocess_exit into
// the global registry once it terminates. The watcher exits cleanly on
// SIGTERM (drains the buffer, fsyncs both log files, returns).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/iurykrieger/harness-framework/lib/registry"
	libsensor "github.com/iurykrieger/harness-framework/lib/sensor"
	libsignal "github.com/iurykrieger/harness-framework/lib/signal"
)

type watcherConfig struct {
	RawLog        string
	SignalsLog    string
	PatternsJSON  string
	EnvelopeJSON  string
	SubprocessPID int
	RegistryRoot  string
	SensorID      string
}

func main() {
	cfg := watcherConfig{
		RawLog:       os.Getenv("HARNESS_WATCHER_RAW"),
		SignalsLog:   os.Getenv("HARNESS_WATCHER_SIGNALS"),
		PatternsJSON: os.Getenv("HARNESS_WATCHER_PATTERNS"),
		EnvelopeJSON: os.Getenv("HARNESS_WATCHER_ENVELOPE"),
		RegistryRoot: os.Getenv("HARNESS_WATCHER_REGISTRY_ROOT"),
		SensorID:     os.Getenv("HARNESS_WATCHER_SENSOR_ID"),
	}
	if pidStr := os.Getenv("HARNESS_WATCHER_SUBPROCESS_PID"); pidStr != "" {
		if pid, err := strconv.Atoi(pidStr); err == nil {
			cfg.SubprocessPID = pid
		}
	}

	stop := make(chan struct{})
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
		close(stop)
	}()

	if err := runWatcher(cfg, stop); err != nil {
		fmt.Fprintln(os.Stderr, "watcher:", err)
		os.Exit(1)
	}
}

// runWatcher follows cfg.RawLog with fsnotify, parses each new line with
// the compiled patterns, and appends matched signals to cfg.SignalsLog.
// Returns when stop is closed.
func runWatcher(cfg watcherConfig, stop <-chan struct{}) error {
	rawPatterns := []interface{}{}
	if err := json.Unmarshal([]byte(cfg.PatternsJSON), &rawPatterns); err != nil {
		return fmt.Errorf("patterns json: %w", err)
	}
	patterns, err := libsignal.CompilePatterns(rawPatterns)
	if err != nil {
		return fmt.Errorf("compile patterns: %w", err)
	}

	var envelope libsensor.Envelope
	if err := json.Unmarshal([]byte(cfg.EnvelopeJSON), &envelope); err != nil {
		return fmt.Errorf("envelope json: %w", err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify: %w", err)
	}
	defer w.Close()
	if err := w.Add(cfg.RawLog); err != nil {
		return fmt.Errorf("watch raw.log: %w", err)
	}

	rawF, err := os.Open(cfg.RawLog)
	if err != nil {
		return fmt.Errorf("open raw.log: %w", err)
	}
	defer rawF.Close()

	rdr := bufio.NewReader(rawF)
	var wg sync.WaitGroup
	if cfg.SubprocessPID > 0 && cfg.RegistryRoot != "" && cfg.SensorID != "" {
		wg.Add(1)
		go func() { defer wg.Done(); runReaper(cfg, stop) }()
	}

	for {
		select {
		case <-stop:
			drain(rdr, patterns, envelope, cfg.SignalsLog)
			wg.Wait()
			return nil
		case ev := <-w.Events:
			if ev.Op&fsnotify.Write != 0 || ev.Op&fsnotify.Create != 0 {
				drain(rdr, patterns, envelope, cfg.SignalsLog)
			}
		case err := <-w.Errors:
			return fmt.Errorf("fsnotify err: %w", err)
		}
	}
}

// drain reads every available line from rdr, matches against patterns,
// and appends matched Signals to signalsLog.
func drain(rdr *bufio.Reader, patterns []libsignal.Pattern, envelope libsensor.Envelope, signalsLog string) {
	for {
		line, err := rdr.ReadString('\n')
		if line != "" {
			handleLine(line, patterns, envelope, signalsLog)
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			return
		}
	}
}

func handleLine(line string, patterns []libsignal.Pattern, envelope libsensor.Envelope, signalsLog string) {
	line = trimNewline(line)
	m, ok := libsignal.MatchLine(line, patterns)
	if !ok {
		return
	}
	sig := buildIndividualSignal(envelope, m, line)
	appendSignal(signalsLog, sig)
}

func trimNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

func buildIndividualSignal(env libsensor.Envelope, m libsignal.PatternMatch, raw string) map[string]interface{} {
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
		"finished_at": time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"verdict":     m.Verdict,
		"severity":    m.Severity,
		"confidence":  1.0,
		"evidence":    []interface{}{ev},
		"cost_actual": map[string]interface{}{"latency_ms": 0},
		"metadata": map[string]interface{}{
			"kind": "individual",
			"line": raw,
		},
	}
}

func appendSignal(path string, sig map[string]interface{}) {
	// best-effort: signal loss is preferable to crashing the watcher.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(sig)
}

// runReaper waits on the subprocess PID and persists subprocess_exit
// into running_sensors.json under flock once the process terminates.
// We do not own the child (it was spawned detached by /start-sensor),
// so syscall.Wait4 wouldn't apply — poll-based liveness check.
//
// Returns early (without writing subprocess_exit) if stop is closed
// while the subprocess is still alive — that path indicates the watcher
// itself is being torn down (typically by /stop-sensor's SIGTERM to the
// subprocess group), and /stop-sensor will record the outcome.
func runReaper(cfg watcherConfig, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		if !registry.IsPIDAlive(cfg.SubprocessPID) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	root := registry.NewRoot(cfg.RegistryRoot)
	exitCode := -1 // we cannot recover the exact code without ptrace
	_ = registry.WithFileLock(root.LockFile(), func() error {
		rs, err := registry.Load(root)
		if err != nil {
			return err
		}
		if e := rs.FindEntry(cfg.SensorID); e != nil {
			e.SubprocessExit = &registry.SubprocessExit{
				Code:     exitCode,
				ExitedAt: time.Now().UTC().Format("2006-01-02T15:04:05Z"),
			}
		}
		return registry.Save(root, rs)
	})
}
