// lib/heal/apply.go
package heal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ApplyContext is the deterministic context Apply needs: the project
// root (used to bound mkdir/touch) and the failed sensor's declared
// requires.* surfaces.
type ApplyContext struct {
	Root         string
	FailedSensor FailedSensor
}

// ApplyResult records the outcome of one Action.
type ApplyResult struct {
	Action     Action
	Applied    bool
	NeedsInput bool   // true when the action needs an ask-user value
	Reason     string // why Applied=false (when not applied)
}

// Apply walks the actions and runs only those allowed by the
// hardcoded allowlist, in order. Side-effecting and idempotent.
//
// kinds:
//   - "copy-template": cp src dst when src exists AND dst does not
//   - "mkdir": mkdir -p dir when dir is under one of FailedSensor.Context paths
//   - "touch": create empty file when file is under context
//   - "set-env-in-file": append <NAME>=<VALUE> when var is declared and absent;
//     defers (NeedsInput=true) when ValueSource=ask-user and Value is empty
func Apply(ctx ApplyContext, actions []Action) []ApplyResult {
	out := make([]ApplyResult, 0, len(actions))
	for _, a := range actions {
		out = append(out, applyOne(ctx, a))
	}
	return out
}

func applyOne(ctx ApplyContext, a Action) ApplyResult {
	switch a.Kind {
	case "copy-template":
		return applyCopyTemplate(a)
	case "mkdir":
		return applyMkdir(ctx, a)
	case "touch":
		return applyTouch(ctx, a)
	case "set-env-in-file":
		return applySetEnvInFile(ctx, a)
	default:
		return ApplyResult{Action: a, Reason: fmt.Sprintf("kind %q not in allowlist", a.Kind)}
	}
}

func applyCopyTemplate(a Action) ApplyResult {
	if a.Src == "" || a.Dst == "" {
		return ApplyResult{Action: a, Reason: "copy-template requires src and dst"}
	}
	if _, err := os.Stat(a.Src); err != nil {
		return ApplyResult{Action: a, Reason: "src does not exist"}
	}
	if _, err := os.Stat(a.Dst); err == nil {
		return ApplyResult{Action: a, Applied: true, Reason: "dst already in place (idempotent)"}
	}
	body, err := os.ReadFile(a.Src)
	if err != nil {
		return ApplyResult{Action: a, Reason: "read src: " + err.Error()}
	}
	if err := os.WriteFile(a.Dst, body, 0o600); err != nil {
		return ApplyResult{Action: a, Reason: "write dst: " + err.Error()}
	}
	return ApplyResult{Action: a, Applied: true}
}

func applyMkdir(ctx ApplyContext, a Action) ApplyResult {
	if a.Dir == "" {
		return ApplyResult{Action: a, Reason: "mkdir requires dir"}
	}
	if !pathUnderAny(a.Dir, ctx.FailedSensor.Context) {
		return ApplyResult{Action: a, Reason: "dir not under requires.context"}
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		return ApplyResult{Action: a, Reason: "mkdir: " + err.Error()}
	}
	return ApplyResult{Action: a, Applied: true}
}

func applyTouch(ctx ApplyContext, a Action) ApplyResult {
	if a.File == "" {
		return ApplyResult{Action: a, Reason: "touch requires file"}
	}
	if !pathUnderAny(a.File, ctx.FailedSensor.Context) {
		return ApplyResult{Action: a, Reason: "file not under requires.context"}
	}
	if _, err := os.Stat(a.File); err == nil {
		return ApplyResult{Action: a, Applied: true} // already exists; idempotent
	}
	f, err := os.OpenFile(a.File, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ApplyResult{Action: a, Reason: "create: " + err.Error()}
	}
	f.Close()
	return ApplyResult{Action: a, Applied: true}
}

func applySetEnvInFile(ctx ApplyContext, a Action) ApplyResult {
	if a.File == "" || a.Name == "" {
		return ApplyResult{Action: a, Reason: "set-env-in-file requires file and name"}
	}
	declared := false
	for _, n := range ctx.FailedSensor.EnvNames {
		if n == a.Name {
			declared = true
			break
		}
	}
	if !declared {
		return ApplyResult{Action: a, Reason: "var " + a.Name + " not in requires.env"}
	}
	if _, err := os.Stat(a.File); err != nil {
		return ApplyResult{Action: a, Reason: "target file does not exist"}
	}
	if a.Value == "" && a.ValueSource == "ask-user" {
		return ApplyResult{Action: a, NeedsInput: true, Reason: "value pending — ask user"}
	}
	if a.Value == "" {
		return ApplyResult{Action: a, Reason: "no value supplied"}
	}
	return WriteEnvVar(a.File, a.Name, a.Value)
}

func pathUnderAny(target string, roots []string) bool {
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	for _, r := range roots {
		ra, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(ra, abs)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") {
			return true
		}
	}
	return false
}
