package shell_test

import (
	"context"
	"strings"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/step"
	"github.com/iurykrieger/harness-framework/lib/step/shell"
)

func TestShell_HappyPath(t *testing.T) {
	cfg := sensor.StepConfig{
		ID:          "s",
		Type:        "shell",
		Run:         "echo hi && echo ok",
		ExitCodeMap: map[string]sensor.Verdict{"0": sensor.Verdict(signal.VerdictPass)},
	}
	s, err := shell.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ec := &step.ExecContext{Env: map[string]string{}}
	res := s.Execute(context.Background(), ec)
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v (err=%v)", res.Verdict, res.Err)
	}
	if res.Status != step.StatusCompleted {
		t.Fatalf("status = %q", res.Status)
	}
}

func TestShell_ExitCodeMapping(t *testing.T) {
	cfg := sensor.StepConfig{
		ID:   "fail",
		Type: "shell",
		Run:  "exit 2",
		ExitCodeMap: map[string]sensor.Verdict{
			"0": sensor.Verdict(signal.VerdictPass),
			"2": sensor.Verdict(signal.VerdictWarn),
		},
	}
	s, _ := shell.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if res.Verdict != signal.VerdictWarn {
		t.Fatalf("verdict = %v", res.Verdict)
	}
}

func TestShell_ParseEmitsIndividuals(t *testing.T) {
	cfg := sensor.StepConfig{
		ID:          "tail",
		Type:        "shell",
		Run:         "echo ERROR foo && echo WARN bar && echo done",
		ExitCodeMap: map[string]sensor.Verdict{"0": sensor.Verdict(signal.VerdictPass)},
		Parse: &sensor.ParseConfig{
			Patterns: []sensor.Pattern{
				{Regex: "ERROR", Verdict: string(signal.VerdictFail), Severity: string(signal.SeverityHigh)},
				{Regex: "WARN", Verdict: string(signal.VerdictWarn), Severity: string(signal.SeverityMedium)},
			},
		},
	}
	s, _ := shell.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{Env: map[string]string{}})
	if len(res.Signals) != 2 {
		t.Fatalf("expected 2 individuals, got %d", len(res.Signals))
	}
	if res.Verdict != signal.VerdictFail {
		t.Fatalf("verdict folded from parse should be fail, got %v", res.Verdict)
	}
}

func TestShell_WithFixtureInjectsEnv(t *testing.T) {
	cfg := sensor.StepConfig{
		ID:          "read",
		Type:        "shell",
		With:        map[string]interface{}{"fixture": map[string]interface{}{"fixture": "x.txt"}},
		Run:         `echo "$HARNESS_FIXTURE_PATH"`,
		ExitCodeMap: map[string]sensor.Verdict{"0": sensor.Verdict(signal.VerdictPass)},
	}
	s, _ := shell.New(cfg)
	res := s.Execute(context.Background(), &step.ExecContext{
		Fixtures: map[string]string{"x.txt": "/abs/x.txt"},
		Env:      map[string]string{},
	})
	if res.Verdict != signal.VerdictPass {
		t.Fatalf("verdict = %v err=%v", res.Verdict, res.Err)
	}
	if !strings.Contains(res.Stdout, "/abs/x.txt") {
		t.Fatalf("stdout did not echo fixture path: %q", res.Stdout)
	}
}

func TestShell_New_RejectsNonShellType(t *testing.T) {
	if _, err := shell.New(sensor.StepConfig{ID: "x", Type: "http"}); err == nil {
		t.Fatal("expected error for non-shell type")
	}
}

func TestShell_New_RejectsEmptyRun(t *testing.T) {
	if _, err := shell.New(sensor.StepConfig{ID: "x", Type: "shell"}); err == nil {
		t.Fatal("expected error for empty run")
	}
}
