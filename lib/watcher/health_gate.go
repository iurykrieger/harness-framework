package watcher

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"time"
)

// HealthGateOutcome is the discrete result of WaitForReady.
type HealthGateOutcome string

const (
	OutcomeReady        HealthGateOutcome = "ready"
	OutcomeFailed       HealthGateOutcome = "failed"
	OutcomeDiedSilently HealthGateOutcome = "died_silently"
	OutcomeTimedOut     HealthGateOutcome = "timed_out"
)

// HealthGateOpts configures one WaitForReady call.
type HealthGateOpts struct {
	SignalsLogPath string
	SubprocessPID  int
	Timeout        time.Duration
	PollInterval   time.Duration
}

// HealthGateResult is what WaitForReady returns.
type HealthGateResult struct {
	Outcome HealthGateOutcome
	// Signal is the first individual signal observed in signals.log when
	// the outcome is Ready or Failed. Nil for DiedSilently and TimedOut.
	Signal map[string]interface{}
}

// WaitForReady polls a sensor's signals.log until one of four conditions
// holds and returns the corresponding outcome:
//
//   - Ready: an individual signal with verdict pass or warn appears.
//   - Failed: an individual signal with verdict fail or error appears.
//   - DiedSilently: the subprocess's PID stops being alive before any
//     individual signal appears.
//   - TimedOut: opts.Timeout elapses without either of the above; the
//     subprocess is still alive when the deadline hits.
//
// Envelope-shaped signals (those carrying metadata.kind in a closed set of
// non-individual kinds) are skipped — only individual matcher outputs count
// toward the ready/failed decision.
//
// Lines that are not valid JSON are skipped silently. signals.log may not yet
// exist when this is called; absence is treated as "no signals yet".
func WaitForReady(opts HealthGateOpts) HealthGateResult {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 100 * time.Millisecond
	}
	deadline := time.Now().Add(opts.Timeout)

	cursor := int64(0)
	for {
		sig, newCursor, _ := readNextIndividual(opts.SignalsLogPath, cursor)
		cursor = newCursor
		if sig != nil {
			verdict, _ := sig["verdict"].(string)
			switch verdict {
			case "pass", "warn":
				return HealthGateResult{Outcome: OutcomeReady, Signal: sig}
			case "fail", "error":
				return HealthGateResult{Outcome: OutcomeFailed, Signal: sig}
			}
			// Unknown verdict — keep scanning (treat as not-yet-decisive).
			continue
		}
		if opts.SubprocessPID > 0 && !isSubprocessAlive(opts.SubprocessPID) {
			// One last read after observing the dead PID, in case the
			// watcher wrote a final signal between our last read and
			// the death.
			sig, _, _ = readNextIndividual(opts.SignalsLogPath, cursor)
			if sig != nil {
				verdict, _ := sig["verdict"].(string)
				switch verdict {
				case "pass", "warn":
					return HealthGateResult{Outcome: OutcomeReady, Signal: sig}
				case "fail", "error":
					return HealthGateResult{Outcome: OutcomeFailed, Signal: sig}
				}
			}
			return HealthGateResult{Outcome: OutcomeDiedSilently}
		}
		if !time.Now().Before(deadline) {
			return HealthGateResult{Outcome: OutcomeTimedOut}
		}
		time.Sleep(opts.PollInterval)
	}
}

// readNextIndividual opens signalsLog, skips startOffset bytes, and returns
// the first complete JSONL line that decodes to an individual signal — i.e.
// has a "verdict" string field and is not envelope-shaped. The returned
// cursor advances past every line consumed, so subsequent calls resume from
// where this one stopped (including past skipped envelope lines).
//
// Missing file is not an error: returns (nil, startOffset, nil).
func readNextIndividual(signalsLog string, startOffset int64) (map[string]interface{}, int64, error) {
	f, err := os.Open(signalsLog)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, startOffset, nil
		}
		return nil, startOffset, err
	}
	defer f.Close()
	if _, err := f.Seek(startOffset, 0); err != nil {
		return nil, startOffset, err
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	cursor := startOffset
	for sc.Scan() {
		line := sc.Bytes()
		cursor += int64(len(line)) + 1 // include trailing \n
		if len(line) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if isIndividualSignal(m) {
			return m, cursor, nil
		}
	}
	return nil, cursor, nil
}

// isIndividualSignal reports whether m looks like a per-line matcher output
// (as opposed to envelope, aggregate, or other harness-internal kinds). The
// criterion is conservative: anything with metadata.kind in a known
// non-individual set is skipped; everything else with a string verdict
// counts.
func isIndividualSignal(m map[string]interface{}) bool {
	verdict, _ := m["verdict"].(string)
	if verdict == "" {
		return false
	}
	md, _ := m["metadata"].(map[string]interface{})
	if md == nil {
		return true
	}
	kind, _ := md["kind"].(string)
	switch kind {
	case "envelope", "aggregate", "cascade", "started", "dep_started",
		"dep_attached", "dep_detached", "dep_start_failed", "failed":
		return false
	}
	return true
}
