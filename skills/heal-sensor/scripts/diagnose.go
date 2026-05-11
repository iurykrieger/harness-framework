//go:build heal_diagnose

// Command diagnose reads the failed Signal, the failing sensor's JSON,
// and the project root, and emits a structured "diagnosis input" JSON
// the calling agent uses to fill in the Setup Plan slots.
//
// The script does NOT do LLM reasoning. It collects the deterministic
// inputs (signal contents, declared requires[], README/CLAUDE/AGENTS
// excerpts, .env.example presence and contents) so the calling agent
// has them in one place and SKILL.md prose can deterministically
// reference what's available.
//
// Usage:
//
//	go run -tags=heal_diagnose ./skills/heal-sensor/scripts \
//	  --signal=PATH --sensor=PATH --root=DIR
//
// Exit codes: 0 emitted, 2 usage / I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var signalPath, sensorPath, root string
	fs.StringVar(&signalPath, "signal", "", "path to failing aggregate Signal JSON (required)")
	fs.StringVar(&sensorPath, "sensor", "", "path to failing sensor JSON (required)")
	fs.StringVar(&root, "root", "", "project root (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if signalPath == "" || sensorPath == "" || root == "" {
		fmt.Fprintln(stderr, "usage: diagnose --signal=PATH --sensor=PATH --root=DIR")
		return 2
	}

	signalBody, err := os.ReadFile(signalPath)
	if err != nil {
		fmt.Fprintln(stderr, "read signal:", err)
		return 2
	}
	sensorBody, err := os.ReadFile(sensorPath)
	if err != nil {
		fmt.Fprintln(stderr, "read sensor:", err)
		return 2
	}

	out := map[string]interface{}{
		"signal":        json.RawMessage(signalBody),
		"failed_sensor": json.RawMessage(sensorBody),
		"documents":     readDocuments(root),
		"templates":     listTemplates(root),
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(stderr, "encode:", err)
		return 2
	}
	return 0
}

func readDocuments(root string) map[string]string {
	docs := map[string]string{}
	for _, name := range []string{"README.md", "CLAUDE.md", "AGENTS.md", "GEMINI.md", "CONTRIBUTING.md"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		// Cap to ~16KB to keep the output bounded.
		if len(body) > 16*1024 {
			body = body[:16*1024]
		}
		docs[name] = string(body)
	}
	return docs
}

func listTemplates(root string) []map[string]string {
	out := []map[string]string{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == ".cache" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".example" || filepath.Base(path) == ".env.example" {
			body, _ := os.ReadFile(path)
			out = append(out, map[string]string{"path": path, "preview": truncate(string(body), 4096)})
		}
		return nil
	})
	return out
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
