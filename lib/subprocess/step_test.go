package subprocess

import (
	"context"
	"strings"
	"testing"
)

func TestRunStep_Pass(t *testing.T) {
	res, err := RunStep(context.Background(), StepConfig{Command: "true", TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d want 0", res.ExitCode)
	}
	if res.TimedOut {
		t.Fatal("did not expect timeout")
	}
}

func TestRunStep_NonZeroExit(t *testing.T) {
	res, err := RunStep(context.Background(), StepConfig{Command: "false", TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode == 0 {
		t.Fatal("expected non-zero exit")
	}
}

func TestRunStep_StderrExcerpt(t *testing.T) {
	res, err := RunStep(context.Background(), StepConfig{Command: "echo woops 1>&2; exit 7", TimeoutMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit=%d want 7", res.ExitCode)
	}
	if !strings.Contains(res.StderrExcerpt, "woops") {
		t.Fatalf("stderr excerpt = %q", res.StderrExcerpt)
	}
}

func TestRunStep_Timeout(t *testing.T) {
	res, err := RunStep(context.Background(), StepConfig{Command: "sleep 5", TimeoutMS: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatal("expected timeout")
	}
}

func TestRunStep_EmptyCommandError(t *testing.T) {
	if _, err := RunStep(context.Background(), StepConfig{Command: "", TimeoutMS: 1000}); err == nil {
		t.Fatal("expected empty-command error")
	}
}
