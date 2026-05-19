package fixture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathEscapeError is returned by Write when the resolved target path
// escapes the fixtures root.
type PathEscapeError struct {
	Rel  string
	Root string
}

func (e *PathEscapeError) Error() string {
	return fmt.Sprintf("fixture path %q resolves outside %s", e.Rel, e.Root)
}

// Write atomically writes the payload to <projectRoot>/.harness/fixtures/<relPath>.
// relPath is relative to the fixtures root (e.g. "order-valid.json" or
// "orders/large.json"). Parent directories are created with mode 0o755. The
// write is atomic via tmp+rename. Idempotent: re-writing the same content
// is allowed. Rejects any relPath that, after cleaning, resolves outside the
// fixtures root or to the root itself (with *PathEscapeError).
// Payloads landing at a `.json` extension are pretty-printed (2-space
// indent, trailing newline, original key order preserved); invalid JSON
// is written unchanged.
// On success, returns the absolute path of the written file.
func Write(projectRoot, relPath string, payload []byte) (string, error) {
	if projectRoot == "" {
		return "", fmt.Errorf("fixture.Write: projectRoot is required")
	}
	if relPath == "" {
		return "", fmt.Errorf("fixture.Write: relPath is required")
	}
	root := filepath.Join(projectRoot, ".harness", "fixtures")
	target := filepath.Clean(filepath.Join(root, relPath))
	sep := string(os.PathSeparator)
	if target == root || !strings.HasPrefix(target+sep, root+sep) {
		return "", &PathEscapeError{Rel: relPath, Root: root}
	}
	payload = formatPayload(payload, filepath.Ext(target))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-fixture-*")
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", fmt.Errorf("sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return "", fmt.Errorf("rename: %w", err)
	}
	return target, nil
}

// formatPayload pretty-prints JSON payloads with 2-space indent and a
// trailing newline so fixtures land readable on disk and produce small,
// reviewable diffs. Only `.json` extensions are considered; original
// byte order is preserved (json.Indent, not json.MarshalIndent). Invalid
// JSON falls through unchanged — Write does not validate payload shape.
func formatPayload(payload []byte, ext string) []byte {
	if ext != ".json" {
		return payload
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, payload, "", "  "); err != nil {
		return payload
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte{'\n'}) {
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
