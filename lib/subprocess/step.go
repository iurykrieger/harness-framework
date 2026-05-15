package subprocess

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// StepConfig is the input to RunStep.
type StepConfig struct {
	Command   string
	Env       map[string]string
	TimeoutMS int
	Dir       string // working directory for the subprocess (empty = inherit)
}

// StepResult captures everything a lifecycle phase needs to fold into the
// aggregate Signal: exit code, elapsed time, timeout flag, and a short
// stderr excerpt (helpful for surfacing why a prepare/teardown step
// failed).
type StepResult struct {
	ExitCode      int
	ElapsedMS     int
	TimedOut      bool
	StderrExcerpt string
}

const stderrExcerptCap = 4096

// RunStep spawns sh -c <Command>, captures stderr fully (truncated at
// stderrExcerptCap), discards stdout, and returns the result. Patterns
// are NOT applied — this is for prepare/teardown only.
func RunStep(ctx context.Context, cfg StepConfig) (StepResult, error) {
	if cfg.Command == "" {
		return StepResult{}, errors.New("step: empty command")
	}
	if cfg.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Command)
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	}
	if len(cfg.Env) > 0 {
		envList := append([]string{}, cmd.Environ()...)
		for k, v := range cfg.Env {
			envList = append(envList, k+"="+v)
		}
		cmd.Env = envList
	}
	// Run in a fresh process group so SIGTERM/SIGKILL on ctx cancellation
	// reach the whole tree, not just sh -c. Matches the stream path.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	start := time.Now()
	runErr := cmd.Run()
	elapsed := int(time.Since(start) / time.Millisecond)

	res := StepResult{
		ElapsedMS:     elapsed,
		TimedOut:      errors.Is(ctx.Err(), context.DeadlineExceeded),
		StderrExcerpt: truncate(stderr.String(), stderrExcerptCap),
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	} else if runErr != nil {
		res.ExitCode = -1
	}
	return res, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
