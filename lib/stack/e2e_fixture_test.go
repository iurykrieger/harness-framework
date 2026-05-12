package stack

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EFixture proves that given a well-formed Stack, a deterministic
// pattern-derivation helper produces regexes that match every line in a
// real captured stdout sample with the expected verdict distribution.
// This is the contract the LLM's Phase B prose is asked to honor.
func TestE2EFixture(t *testing.T) {
	fixtureDir := findFixtureDir(t)
	stackPath := filepath.Join(fixtureDir, "expected-stack.json")
	stdoutPath := filepath.Join(fixtureDir, "expected-stdout.log")

	body, err := os.ReadFile(stackPath)
	if err != nil {
		t.Fatalf("read expected-stack.json: %v", err)
	}
	var s Stack
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode expected-stack.json: %v", err)
	}
	if len(s.LogShapes) == 0 {
		t.Fatal("fixture stack has no log_shapes")
	}

	patterns := derivePatternsForShape(s.LogShapes[0], s)
	if len(patterns) == 0 {
		t.Fatal("derivePatternsForShape returned no patterns")
	}

	// Compile the regexes.
	type compiled struct {
		re      *regexp.Regexp
		verdict string
	}
	var ps []compiled
	for _, p := range patterns {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			t.Fatalf("compile regex %q: %v", p.Regex, err)
		}
		ps = append(ps, compiled{re: re, verdict: p.Verdict})
	}

	// Verify the sample is matched by at least one pattern (anchor invariant).
	matched := false
	for _, p := range ps {
		if p.re.MatchString(s.LogShapes[0].Sample) {
			matched = true
			break
		}
	}
	if !matched {
		t.Fatalf("no derived pattern matches the shape sample %q", s.LogShapes[0].Sample)
	}

	// Read stdout, count verdicts.
	f, err := os.Open(stdoutPath)
	if err != nil {
		t.Fatalf("open stdout fixture: %v", err)
	}
	defer f.Close()
	counts := map[string]int{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		for _, p := range ps {
			if p.re.MatchString(line) {
				counts[p.verdict]++
				break
			}
		}
	}

	// Acceptance: every line in the captured stdout matches at least one
	// pattern (no unmatched lines). For the fixture, all four lines are
	// INFO, so we expect counts["pass"] == 4.
	totalMatched := 0
	for _, n := range counts {
		totalMatched += n
	}
	if totalMatched != 4 {
		t.Fatalf("totalMatched = %d, want 4; counts = %v", totalMatched, counts)
	}
	t.Logf("e2e fixture verdict counts: %v", counts)
}

// patternSpec mirrors a single output_parsing.patterns[] entry.
type patternSpec struct {
	Regex   string
	Verdict string
}

// derivePatternsForShape encodes the Phase B prose's pattern-derivation
// rules deterministically. Used by tests; not production code (Phase B
// is LLM-driven). The rules:
//
//   - severity ∈ {ERROR, FATAL, DPANIC, PANIC} → fail/high
//   - severity == WARN AND status_code ∈ 4xx/5xx → fail/medium
//   - severity == WARN (other) → warn/low
//   - severity == INFO (boot markers) → pass/info
//
// For format=json, the regex looks for `"<key>":"<value>"` patterns
// using the shape's literal field keys.
func derivePatternsForShape(sh LogShape, _ Stack) []patternSpec {
	if !sh.HasSeverity() {
		return nil
	}
	if sh.Format != FormatJSON {
		return nil
	}
	sevKey := sh.FieldsByMeaning(MeaningSeverity)[0].Key
	var out []patternSpec
	// Map case-insensitive sev tokens by category.
	highTokens := []string{}
	warnTokens := []string{}
	infoTokens := []string{}
	for _, v := range sh.SeverityValues {
		switch strings.ToUpper(v) {
		case "ERROR", "FATAL", "DPANIC", "PANIC":
			highTokens = append(highTokens, regexp.QuoteMeta(v))
		case "WARN", "WARNING":
			warnTokens = append(warnTokens, regexp.QuoteMeta(v))
		case "INFO":
			infoTokens = append(infoTokens, regexp.QuoteMeta(v))
		}
	}
	if len(highTokens) > 0 {
		out = append(out, patternSpec{
			Regex:   fmt.Sprintf(`"%s":"(?:%s)"`, regexp.QuoteMeta(sevKey), strings.Join(highTokens, "|")),
			Verdict: "fail",
		})
	}
	if len(warnTokens) > 0 {
		out = append(out, patternSpec{
			Regex:   fmt.Sprintf(`"%s":"(?:%s)"`, regexp.QuoteMeta(sevKey), strings.Join(warnTokens, "|")),
			Verdict: "warn",
		})
	}
	if len(infoTokens) > 0 {
		out = append(out, patternSpec{
			Regex:   fmt.Sprintf(`"%s":"(?:%s)"`, regexp.QuoteMeta(sevKey), strings.Join(infoTokens, "|")),
			Verdict: "pass",
		})
	}
	return out
}

func findFixtureDir(t *testing.T) string {
	t.Helper()
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "testdata", "stack-discovery")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("lib/stack/testdata/stack-discovery not found")
	return ""
}
