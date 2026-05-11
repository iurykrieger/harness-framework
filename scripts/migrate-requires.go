// scripts/migrate-requires.go
//
// One-shot migration tool that converts v1 sensor JSON files to the v2
// requires[] discriminated-union shape defined in schemas/sensor.json.
// Idempotent: already-v2 files are left untouched. Fail-fast on ambiguity
// (sensor mixes v1 and v2 shapes).
//
// Usage:
//
//	migrate-requires <sensor.json>...
//	migrate-requires --root <dir>
//	migrate-requires --dry-run [...]
//
// Exit codes: 0 success, 1 ambiguity / validation failure, 2 usage / I/O.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "print diff to stdout instead of writing")
	root := flag.String("root", "", "walk this directory recursively for sensor JSON files")
	flag.Parse()

	var paths []string
	if *root != "" {
		walked, err := walkSensorJSONs(*root)
		if err != nil {
			fmt.Fprintln(os.Stderr, "walk:", err)
			os.Exit(2)
		}
		paths = walked
	}
	paths = append(paths, flag.Args()...)

	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "usage: migrate-requires [--root <dir>] [--dry-run] <sensor.json>...")
		os.Exit(2)
	}

	exit := 0
	for _, p := range paths {
		if err := migrateFile(p, *dryRun); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", p, err)
			exit = 1
		}
	}
	os.Exit(exit)
}

func migrateFile(path string, dryRun bool) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changed, err := convert(body)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if dryRun {
		fmt.Printf("--- %s\n+++ %s (migrated)\n%s\n", path, path, string(out))
		return nil
	}
	return os.WriteFile(path, out, 0o644)
}

// convert returns the migrated body, whether anything changed, and any
// ambiguity error. The function is pure (no I/O).
func convert(body []byte) ([]byte, bool, error) {
	var s map[string]interface{}
	if err := json.Unmarshal(body, &s); err != nil {
		return nil, false, fmt.Errorf("parse: %w", err)
	}

	hasV2Array := false
	if _, ok := s["requires"].([]interface{}); ok {
		hasV2Array = true
	}

	_, hasDepends := s["depends_on"]
	_, hasReqObj := s["requires"].(map[string]interface{})
	hasPrepare := false
	if exec, ok := s["execution"].(map[string]interface{}); ok {
		if _, ok := exec["prepare"]; ok {
			hasPrepare = true
		}
	}

	hasV1 := hasDepends || hasReqObj || hasPrepare

	if hasV2Array && hasV1 {
		return nil, false, fmt.Errorf("sensor mixes v1 and v2 shapes (refusing to guess)")
	}
	if hasV2Array {
		return body, false, nil // already v2, nothing to do
	}
	if !hasV1 {
		return body, false, nil // nothing to migrate
	}

	// Build the v2 array in stable order: sensor → tool → env → context → permission → step.
	requires := []interface{}{}

	if deps, ok := s["depends_on"].([]interface{}); ok {
		for _, d := range deps {
			if id, ok := d.(string); ok {
				requires = append(requires, map[string]interface{}{"kind": "sensor", "id": id})
			}
		}
	}
	if obj, ok := s["requires"].(map[string]interface{}); ok {
		if tools, ok := obj["tools"].([]interface{}); ok {
			for _, t := range tools {
				if name, ok := t.(string); ok {
					requires = append(requires, map[string]interface{}{"kind": "tool", "name": name})
				}
			}
		}
		if envs, ok := obj["env"].([]interface{}); ok {
			for _, e := range envs {
				em, ok := e.(map[string]interface{})
				if !ok {
					continue
				}
				entry := map[string]interface{}{"kind": "env"}
				for _, k := range []string{"name", "description", "optional"} {
					if v, present := em[k]; present {
						entry[k] = v
					}
				}
				requires = append(requires, entry)
			}
		}
		if ctxs, ok := obj["context"].([]interface{}); ok {
			for _, c := range ctxs {
				if p, ok := c.(string); ok {
					requires = append(requires, map[string]interface{}{"kind": "context", "path": p})
				}
			}
		}
		if perms, ok := obj["permissions"].([]interface{}); ok {
			for _, p := range perms {
				if scope, ok := p.(string); ok {
					requires = append(requires, map[string]interface{}{"kind": "permission", "scope": scope})
				}
			}
		}
	}
	if exec, ok := s["execution"].(map[string]interface{}); ok {
		if steps, ok := exec["prepare"].([]interface{}); ok {
			for _, st := range steps {
				sm, ok := st.(map[string]interface{})
				if !ok {
					continue
				}
				entry := map[string]interface{}{"kind": "step"}
				for _, k := range []string{"command", "timeout_ms", "exit_code_map"} {
					if v, present := sm[k]; present {
						entry[k] = v
					}
				}
				requires = append(requires, entry)
			}
		}
		delete(exec, "prepare")
	}

	delete(s, "depends_on")
	s["requires"] = requires

	if v, ok := s["version"].(string); ok {
		s["version"] = bumpPatch(v)
	}

	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, false, err
	}
	out = append(out, '\n')
	return out, true, nil
}

func bumpPatch(v string) string {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return v
	}
	patchOnly := parts[2]
	for i, r := range patchOnly {
		if r != '-' && r != '+' {
			continue
		}
		patchOnly = patchOnly[:i]
		break
	}
	n, err := strconv.Atoi(patchOnly)
	if err != nil {
		return v
	}
	parts[2] = strconv.Itoa(n + 1)
	return strings.Join(parts, ".")
}

func walkSensorJSONs(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var probe map[string]interface{}
		if err := json.Unmarshal(body, &probe); err != nil {
			return nil
		}
		if _, hasID := probe["id"]; !hasID {
			return nil
		}
		if _, hasExec := probe["execution"]; !hasExec {
			return nil
		}
		out = append(out, path)
		return nil
	})
	return out, err
}
