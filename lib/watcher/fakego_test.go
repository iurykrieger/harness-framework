package watcher

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// withFakeGo installs a temp directory at the front of $PATH containing
// a bash script masquerading as `go`. The script writes its argv (one
// per line, separated by ASCII unit-separator) to <argsFile>, writes
// the env block to <envFile>, sleeps for sleepMS milliseconds, then
// exits with exitCode. If stderr is non-empty, it is written verbatim
// to stderr before exit.
//
// Returns the temp dir so the test can read argsFile/envFile. The
// PATH change is reverted via t.Cleanup.
func withFakeGo(t *testing.T, sleepMS int, exitCode int, stderr string) (tmpDir, argsFile, envFile string) {
	t.Helper()
	tmpDir = t.TempDir()
	argsFile = filepath.Join(tmpDir, "args.txt")
	envFile = filepath.Join(tmpDir, "env.txt")

	script := fmt.Sprintf(`#!/bin/sh
# Record args separated by ASCII unit-separator (0x1F).
# Use octal \037 so /bin/sh-as-dash on Ubuntu CI accepts it; \x1f is a
# bash/zsh extension that dash silently passes through as literal text.
printf '%%s\037' "$@" > %q
# Record env.
env > %q
%s
sleep %f
exit %d
`,
		argsFile, envFile,
		shellStderr(stderr),
		float64(sleepMS)/1000.0,
		exitCode)

	goBin := filepath.Join(tmpDir, "go")
	if err := os.WriteFile(goBin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake go: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+origPath)
	return tmpDir, argsFile, envFile
}

func shellStderr(msg string) string {
	if msg == "" {
		return ""
	}
	// Quote single quotes by replacing ' with '\''.
	q := ""
	for _, r := range msg {
		if r == '\'' {
			q += `'\''`
		} else {
			q += string(r)
		}
	}
	return fmt.Sprintf("printf '%%s\\n' '%s' >&2", q)
}

func TestWithFakeGo_RecordsArgs(t *testing.T) {
	_, argsFile, envFile := withFakeGo(t, 0, 0, "")

	cmd := exec.Command("go", "-C", "/tmp", "version")
	cmd.Env = append(os.Environ(), "MARKER=value")
	if err := cmd.Run(); err != nil {
		t.Fatalf("fake go: %v", err)
	}

	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "-C") || !strings.Contains(string(args), "/tmp") || !strings.Contains(string(args), "version") {
		t.Errorf("args = %q, want to contain -C, /tmp, version", args)
	}
	env, _ := os.ReadFile(envFile)
	if !strings.Contains(string(env), "MARKER=value") {
		t.Errorf("env file missing MARKER=value: %s", env)
	}
}
