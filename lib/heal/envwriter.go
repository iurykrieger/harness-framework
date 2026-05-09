// lib/heal/envwriter.go
package heal

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteEnvVar appends NAME=VALUE to file (idempotent: no-op if the
// line is already present). Sets file permissions to 0600 on success.
// Returns Applied=false with a Reason when the file's directory is
// not gitignored — heal will not write secrets to a tracked location.
func WriteEnvVar(file, name, value string) ApplyResult {
	action := Action{Kind: "set-env-in-file", File: file, Name: name, Value: value}
	covered, err := isPathGitignored(file)
	if err != nil {
		return ApplyResult{Action: action, Reason: "gitignore check: " + err.Error()}
	}
	if !covered {
		return ApplyResult{Action: action, Reason: fmt.Sprintf("%s is not covered by a .gitignore — refusing to write a secret to a tracked path", file)}
	}

	if line, present, err := envFileHasLine(file, name, value); err != nil {
		return ApplyResult{Action: action, Reason: "read: " + err.Error()}
	} else if present {
		_ = os.Chmod(file, 0o600)
		return ApplyResult{Action: action, Applied: true, Reason: "already present: " + line}
	}

	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return ApplyResult{Action: action, Reason: "open: " + err.Error()}
	}
	defer f.Close()
	if _, err := f.WriteString(name + "=" + value + "\n"); err != nil {
		return ApplyResult{Action: action, Reason: "write: " + err.Error()}
	}
	_ = os.Chmod(file, 0o600)
	return ApplyResult{Action: action, Applied: true}
}

func envFileHasLine(path, name, value string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	want := name + "=" + value
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == want {
			return line, true, nil
		}
	}
	return "", false, scanner.Err()
}

// isPathGitignored returns true when path is matched by a .gitignore
// in path's directory, parent, or any ancestor up to the filesystem
// root or the first directory that is itself a git repo root.
func isPathGitignored(path string) (bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(abs)
	base := filepath.Base(abs)
	for i := 0; i < 64; i++ {
		gi := filepath.Join(dir, ".gitignore")
		if data, err := os.ReadFile(gi); err == nil {
			scanner := bufio.NewScanner(strings.NewReader(string(data)))
			for scanner.Scan() {
				pat := strings.TrimSpace(scanner.Text())
				if pat == "" || strings.HasPrefix(pat, "#") {
					continue
				}
				if matchGitignorePattern(pat, base, abs, dir) {
					return true, nil
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			// Reached the git root without a match.
			return false, nil
		}
		dir = parent
	}
	return false, errors.New("ancestor walk exceeded depth")
}

func matchGitignorePattern(pat, base, abs, dir string) bool {
	pat = strings.TrimPrefix(pat, "/")
	pat = strings.TrimSuffix(pat, "/")
	if pat == base {
		return true
	}
	if matched, _ := filepath.Match(pat, base); matched {
		return true
	}
	rel, err := filepath.Rel(dir, abs)
	if err == nil {
		if matched, _ := filepath.Match(pat, rel); matched {
			return true
		}
	}
	return false
}
