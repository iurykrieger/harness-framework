// Package transcripttest is a Go test helper for consumers of
// lib/transcript that need fixture-loaded entries. Follows the
// convention of net/http/httptest and testing/iotest: a sibling
// _test_helper_ package whose only client is *_test.go files in
// the broader codebase. Per project rule 11, this is the canonical
// way to share transcript fixtures across packages.
package transcripttest

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/transcript"
)

// Load reads a fixture JSONL file from lib/transcript/testdata/<name>
// (resolved relative to this package's source location) and returns
// the parsed Entry slice. Path resolution uses runtime.Caller so the
// helper works regardless of the test's working directory.
func Load(t *testing.T, name string) []transcript.Entry {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// file is .../lib/transcript/transcripttest/load.go
	// testdata is at .../lib/transcript/testdata/
	dir := filepath.Dir(filepath.Dir(file))
	path := filepath.Join(dir, "testdata", name)
	entries, err := transcript.Scan(path)
	if err != nil {
		t.Fatalf("transcripttest.Load(%q): %v", name, err)
	}
	return entries
}

// Path returns the absolute path of a testdata fixture without parsing
// it. Useful when a consumer needs to pass the path to a function that
// re-opens the file (e.g. setup-failure-detector's hook input).
func Path(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(filepath.Dir(file))
	return filepath.Join(dir, "testdata", name)
}
