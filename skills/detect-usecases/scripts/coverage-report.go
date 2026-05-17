//go:build coverage_report

// Command coverage-report reads <project-root>/.harness/stack.yaml plus
// the persisted UseCases under <project-root>/.harness/usecases/<journey-id>/
// and prints a per-journey coverage matrix to stdout.
//
// Usage:
//
//	go run -tags=coverage_report ./skills/detect-usecases/scripts \
//	  --project-root=<dir> [--schemas-dir=<dir>] [--journey=<id>]
//
// Exit codes:
//
//	0 — every declared journey has at least one persisted UseCase
//	    (or stack has no journeys to verify),
//	1 — at least one declared journey has zero persisted UseCases,
//	2 — setup error (missing flag, stack absent, unknown --journey id,
//	    schema/parse failure).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/iurykrieger/harness-framework/lib/schema"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("coverage-report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var projectRoot, schemasDir, journeyFilter string
	fs.StringVar(&projectRoot, "project-root", "", "project root (required, holds .harness/stack.yaml)")
	fs.StringVar(&schemasDir, "schemas-dir", "", "schemas directory (unused today; reserved for symmetry with sibling scripts)")
	fs.StringVar(&journeyFilter, "journey", "", "limit verification to a single journey id (must exist in stack.journeys)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = schemasDir
	if projectRoot == "" {
		fmt.Fprintln(stderr, "error: --project-root is required")
		return 2
	}

	stk, code := loadStack(projectRoot, stderr)
	if code != 0 {
		return code
	}

	usecasesDir := filepath.Join(projectRoot, ".harness", "usecases")
	persisted, err := usecase.ListByJourney(usecasesDir)
	if err != nil {
		fmt.Fprintln(stderr, "error: scan usecases:", err)
		return 2
	}

	return emit(stdout, stderr, stk, persisted, journeyFilter)
}

func loadStack(projectRoot string, stderr io.Writer) (*stack.Stack, int) {
	stackPath := filepath.Join(projectRoot, ".harness", "stack.yaml")
	if _, err := os.Stat(stackPath); err != nil {
		fmt.Fprintf(stderr, "error: stack_missing: %s — run /detect-sensors first\n", stackPath)
		return nil, 2
	}
	body, err := schema.ReadAsJSON(stackPath)
	if err != nil {
		fmt.Fprintln(stderr, "error: read stack:", err)
		return nil, 2
	}
	var s stack.Stack
	if err := json.Unmarshal(body, &s); err != nil {
		fmt.Fprintln(stderr, "error: parse stack:", err)
		return nil, 2
	}
	return &s, 0
}

func emit(stdout, stderr io.Writer, stk *stack.Stack, persisted map[string][]string, journeyFilter string) int {
	if len(stk.Journeys) == 0 {
		fmt.Fprintln(stdout, "Coverage matrix: no journeys declared in .harness/stack.yaml; nothing to verify.")
		return 0
	}

	var selected []stack.Journey
	if journeyFilter != "" {
		for _, j := range stk.Journeys {
			if j.ID == journeyFilter {
				selected = append(selected, j)
				break
			}
		}
		if len(selected) == 0 {
			fmt.Fprintf(stderr, "error: --journey=%q not found in stack.journeys[]\n", journeyFilter)
			return 2
		}
	} else {
		selected = stk.Journeys
	}

	maxID := 0
	for _, j := range selected {
		if len(j.ID) > maxID {
			maxID = len(j.ID)
		}
	}

	covered, totalUC, uncovered := 0, 0, 0
	type row struct {
		id    string
		count int
	}
	rows := make([]row, 0, len(selected))
	for _, j := range selected {
		c := len(persisted[j.ID])
		totalUC += c
		if c == 0 {
			uncovered++
		} else {
			covered++
		}
		rows = append(rows, row{j.ID, c})
	}

	scope := fmt.Sprintf("%d of %d journeys covered", covered, len(selected))
	if journeyFilter != "" {
		scope = fmt.Sprintf("%d of %d selected journey covered", covered, len(selected))
	}
	fmt.Fprintf(stdout, "Coverage matrix (%s, %s):\n", scope, pluralUseCases(totalUC))
	for _, r := range rows {
		marker := "✅"
		suffix := ""
		if r.count == 0 {
			marker = "❌"
			suffix = "  ← BLOCKER"
		}
		fmt.Fprintf(stdout, "  %s %-*s  %s%s\n", marker, maxID, r.id, pluralUseCases(r.count), suffix)
	}

	declared := map[string]struct{}{}
	for _, j := range stk.Journeys {
		declared[j.ID] = struct{}{}
	}
	var orphanIDs []string
	for id := range persisted {
		if _, ok := declared[id]; !ok {
			orphanIDs = append(orphanIDs, id)
		}
	}
	sort.Strings(orphanIDs)
	if len(orphanIDs) > 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Orphan use cases (no matching journey in stack):")
		maxOrphan := 0
		for _, id := range orphanIDs {
			if len(id) > maxOrphan {
				maxOrphan = len(id)
			}
		}
		for _, id := range orphanIDs {
			files := persisted[id]
			fmt.Fprintf(stdout, "  ⚠️  %-*s  %s: %s\n",
				maxOrphan, id, pluralFiles(len(files)), join(files, ", "))
		}
	}

	fmt.Fprintln(stdout)
	if uncovered == 0 {
		fmt.Fprintln(stdout, "Full coverage achieved.")
		return 0
	}
	fmt.Fprintf(stdout, "Incomplete coverage: %d of %d journeys uncovered.\n", uncovered, len(selected))
	return 1
}

func pluralUseCases(n int) string {
	if n == 1 {
		return "1 use case"
	}
	return fmt.Sprintf("%d use cases", n)
}

func pluralFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

func join(ss []string, sep string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
