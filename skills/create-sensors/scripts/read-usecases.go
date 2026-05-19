//go:build read_usecases

// Command read-usecases resolves a set of usecase identifiers (by id
// or by journey) under <projectRoot>/.harness/usecases/ and emits a
// JSON ledger on stdout. Read-only. The catalog projection walks
// .harness/sensors/ recursively so root-tier primitives AND per-
// usecase folders are both included.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/signal"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

type Ledger struct {
	Usecases    []usecase.UseCase `json:"usecases"`
	Stack       map[string]any    `json:"stack,omitempty"`
	Catalog     []catalogEntry    `json:"catalog,omitempty"`
	ProjectRoot string            `json:"project_root"`
}

type catalogEntry struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	Output   string `json:"output"`
	Blocking bool   `json:"blocking"`
	Layer    string `json:"layer,omitempty"`
	Path     string `json:"path"`
}

type indexLedger struct {
	Usecases []listEntry `json:"usecases"`
}

type listEntry struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Tags []string `json:"tags,omitempty"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("read-usecases", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		projectRoot, usecases, journey string
		listOnly, includeStack         bool
		includeCatalog                 bool
	)
	fs.StringVar(&projectRoot, "project-root", "", "project root (default: registry.Lookup from cwd)")
	fs.StringVar(&usecases, "usecases", "", "comma-separated usecase ids")
	fs.StringVar(&journey, "journey", "", "journey id (loads all usecases under that journey)")
	fs.BoolVar(&listOnly, "list-only", false, "emit thin index (id+name+tags) only")
	fs.BoolVar(&includeStack, "include-stack", false, "include .harness/stack.yaml in the ledger")
	fs.BoolVar(&includeCatalog, "include-catalog", false, "include existing sensor catalog in the ledger")
	if err := fs.Parse(args); err != nil {
		emit(stdout, errSignal("usage", err.Error()))
		return 2
	}
	if listOnly && (includeStack || includeCatalog) {
		emit(stdout, errSignal("usage", "--list-only mutually exclusive with --include-stack and --include-catalog"))
		return 2
	}
	if projectRoot == "" {
		cwd, _ := os.Getwd()
		res, err := registry.Lookup(cwd)
		if err != nil {
			emit(stdout, registry.DiscoveryErrorSignal(err, "read-usecases"))
			return 2
		}
		projectRoot = res.ProjectRoot
	}

	ids, err := resolveIDs(projectRoot, usecases, journey)
	if err != nil {
		emit(stdout, errSignal("usage", err.Error()))
		return 2
	}

	var validatorErr bytes.Buffer
	validator, code := schema.LoadValidator("", &validatorErr)
	if code != 0 {
		detail := strings.TrimSpace(validatorErr.String())
		emit(stdout, errSignal("schema_validator_init_failed", "schema validator init failed: "+detail))
		return 2
	}

	loaded, warns, missing := loadUsecases(projectRoot, ids, validator)
	for _, w := range warns {
		emit(stdout, w)
	}
	if len(missing) > 0 {
		emit(stdout, errSignal("usecase_not_found", "usecases not found: "+strings.Join(missing, ", ")))
		return 1
	}

	if listOnly {
		idx := indexLedger{Usecases: []listEntry{}}
		for _, uc := range loaded {
			idx.Usecases = append(idx.Usecases, listEntry{ID: uc.ID, Name: uc.Name, Tags: uc.Tags})
		}
		emit(stdout, idx)
		return 0
	}

	lg := Ledger{Usecases: loaded, ProjectRoot: projectRoot}
	if includeStack {
		if stackMap, ok := loadStack(projectRoot); ok {
			lg.Stack = stackMap
		}
	}
	if includeCatalog {
		lg.Catalog = loadCatalog(projectRoot, validator)
	}
	emit(stdout, lg)
	return 0
}

func resolveIDs(projectRoot, usecasesCSV, journey string) ([]string, error) {
	if usecasesCSV != "" && journey != "" {
		return nil, errors.New("pass either --usecases or --journey, not both")
	}
	if usecasesCSV == "" && journey == "" {
		return nil, errors.New("one of --usecases or --journey is required")
	}
	if usecasesCSV != "" {
		raw := strings.Split(usecasesCSV, ",")
		out := make([]string, 0, len(raw))
		for _, r := range raw {
			if r = strings.TrimSpace(r); r != "" {
				out = append(out, r)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	dir := filepath.Join(projectRoot, ".harness", "usecases", journey)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("journey %q: %w", journey, err)
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(ids)
	return ids, nil
}

func loadUsecases(projectRoot string, ids []string, validator *schema.Validator) (loaded []usecase.UseCase, warns []map[string]interface{}, missing []string) {
	usecasesRoot := filepath.Join(projectRoot, ".harness", "usecases")
	pathByID := indexUsecaseFiles(usecasesRoot)
	for _, id := range ids {
		path, ok := pathByID[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			warns = append(warns, warnSignal("usecase_read_failed", fmt.Sprintf("read %s: %v", path, err)))
			continue
		}
		var instance interface{}
		if err := yaml.Unmarshal(body, &instance); err != nil {
			warns = append(warns, warnSignal("usecase_parse_failed", fmt.Sprintf("%s: parse: %v", path, err)))
			continue
		}
		if err := validator.Validate(schema.TargetUseCase, instance); err != nil {
			warns = append(warns, warnSignal("usecase_schema_invalid", fmt.Sprintf("%s: %v", path, err)))
			continue
		}
		var uc usecase.UseCase
		if err := yaml.Unmarshal(body, &uc); err != nil {
			warns = append(warns, warnSignal("usecase_parse_failed", fmt.Sprintf("%s: decode: %v", path, err)))
			continue
		}
		loaded = append(loaded, uc)
	}
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].ID < loaded[j].ID })
	return loaded, warns, missing
}

func indexUsecaseFiles(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		id := strings.TrimSuffix(info.Name(), ".yaml")
		out[id] = path
		return nil
	})
	return out
}

func loadStack(projectRoot string) (map[string]any, bool) {
	body, err := os.ReadFile(filepath.Join(projectRoot, ".harness", "stack.yaml"))
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := yaml.Unmarshal(body, &out); err != nil {
		return nil, false
	}
	return out, true
}

// loadCatalog walks .harness/sensors/ RECURSIVELY so root-tier
// platform primitives AND per-usecase bundles both contribute. Each
// entry's Path is relative to projectRoot.
func loadCatalog(projectRoot string, validator *schema.Validator) []catalogEntry {
	sensorsRoot := filepath.Join(projectRoot, ".harness", "sensors")
	out := []catalogEntry{}
	_ = filepath.Walk(sensorsRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".yaml") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var instance interface{}
		if err := yaml.Unmarshal(body, &instance); err != nil {
			return nil
		}
		if err := validator.Validate(schema.TargetSensor, instance); err != nil {
			return nil
		}
		var s sensor.Sensor
		if err := yaml.Unmarshal(body, &s); err != nil {
			return nil
		}
		rel, _ := filepath.Rel(projectRoot, path)
		out = append(out, catalogEntry{
			ID: s.ID, Kind: string(s.Kind), Type: string(s.Type),
			Output: string(s.Output), Blocking: s.Execution.Blocking,
			Layer: string(s.Layer), Path: rel,
		})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func emit(w io.Writer, v interface{}) {
	body, _ := json.Marshal(v)
	fmt.Fprintln(w, string(body))
}

func warnSignal(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("read-usecases", "0.1.0").
		WithVerdict("warn", "medium").WithKind(kind).WithRationale(rationale).Build()
}

func errSignal(kind, rationale string) map[string]interface{} {
	return signal.NewBuilder("read-usecases", "0.1.0").
		WithVerdict("error", "high").WithKind(kind).WithRationale(rationale).Build()
}
