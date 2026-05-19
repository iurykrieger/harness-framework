package fixture_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/fixture"
)

func TestWrite_AtomicAndIdempotent(t *testing.T) {
	root := t.TempDir()
	abs, err := fixture.Write(root, "order-valid.json", []byte(`{"id":"x"}`))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if abs != filepath.Join(root, ".harness/fixtures/order-valid.json") {
		t.Fatalf("abs = %q", abs)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	const wantPretty = "{\n  \"id\": \"x\"\n}\n"
	if string(body) != wantPretty {
		t.Fatalf("body = %q want %q", body, wantPretty)
	}
	if _, err := fixture.Write(root, "order-valid.json", []byte(`{"id":"x"}`)); err != nil {
		t.Fatalf("idempotent rewrite: %v", err)
	}
}

func TestWrite_FormatsJSONExtension(t *testing.T) {
	root := t.TempDir()
	abs, err := fixture.Write(root, "nested/order.json", []byte(`{"b":2,"a":[1,2,3]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"b\": 2,\n  \"a\": [\n    1,\n    2,\n    3\n  ]\n}\n"
	if string(body) != want {
		t.Fatalf("body = %q\nwant   %q", body, want)
	}
}

func TestWrite_FormatsJSONAlreadyPretty(t *testing.T) {
	root := t.TempDir()
	already := "{\n  \"id\": \"x\"\n}\n"
	abs, err := fixture.Write(root, "already.json", []byte(already))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != already {
		t.Fatalf("body = %q\nwant   %q", body, already)
	}
}

func TestWrite_NonJSONExtensionUntouched(t *testing.T) {
	root := t.TempDir()
	raw := []byte("plain\ntext\nno-reformat")
	abs, err := fixture.Write(root, "notes.txt", raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(raw) {
		t.Fatalf("body = %q want %q", body, raw)
	}
}

func TestWrite_InvalidJSONFallsThrough(t *testing.T) {
	root := t.TempDir()
	raw := []byte("not really json {{{")
	abs, err := fixture.Write(root, "broken.json", raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(raw) {
		t.Fatalf("body = %q want %q", body, raw)
	}
}

func TestWrite_FormatsYAML(t *testing.T) {
	root := t.TempDir()
	abs, err := fixture.Write(root, "config.yaml", []byte(`{a: 1, b: [1, 2, 3]}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	want := "a: 1\nb:\n- 1\n- 2\n- 3\n"
	if string(body) != want {
		t.Fatalf("body = %q\nwant   %q", body, want)
	}
}

func TestWrite_FormatsXML(t *testing.T) {
	root := t.TempDir()
	abs, err := fixture.Write(root, "soap-response.xml", []byte(`<root><item id="1">a</item><item id="2">b</item></root>`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	want := "<root>\n  <item id=\"1\">a</item>\n  <item id=\"2\">b</item>\n</root>\n"
	if string(body) != want {
		t.Fatalf("body = %q\nwant   %q", body, want)
	}
}

func TestWrite_JSONLNotReformatted(t *testing.T) {
	root := t.TempDir()
	raw := []byte(`{"a":1}` + "\n" + `{"a":2}` + "\n")
	abs, err := fixture.Write(root, "stream.jsonl", raw)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(raw) {
		t.Fatalf("body = %q want %q", body, raw)
	}
}

func TestWrite_CreatesNestedParents(t *testing.T) {
	root := t.TempDir()
	abs, err := fixture.Write(root, "deeply/nested/case/pass.txt", []byte("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(root, ".harness/fixtures/deeply/nested/case/pass.txt")
	if abs != expected {
		t.Fatalf("abs = %q want %q", abs, expected)
	}
}

func TestWrite_RejectEscape(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		"../escape.txt",
		"../../etc/passwd",
		"sub/../../escape.txt",
		".", // resolves exactly to fixtures root
		"",  // empty triggers "relPath required", but check the dedicated test below
	}
	for _, bad := range cases {
		if bad == "" {
			// Covered by TestWrite_MissingArgs.
			continue
		}
		_, err := fixture.Write(root, bad, []byte("x"))
		if err == nil {
			t.Fatalf("expected error for %q, got nil", bad)
		}
		var esc *fixture.PathEscapeError
		if !errors.As(err, &esc) {
			t.Fatalf("expected *PathEscapeError for %q, got %T: %v", bad, err, err)
		}
	}
}

func TestWrite_MissingArgs(t *testing.T) {
	if _, err := fixture.Write("", "a.txt", []byte("x")); err == nil {
		t.Fatal("expected error for empty projectRoot")
	}
	if _, err := fixture.Write(t.TempDir(), "", []byte("x")); err == nil {
		t.Fatal("expected error for empty relPath")
	}
}

func TestWrite_RewriteUpdatesContent(t *testing.T) {
	root := t.TempDir()
	abs, err := fixture.Write(root, "a.txt", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Write(root, "a.txt", []byte("second")); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "second" {
		t.Fatalf("body = %q want %q", body, "second")
	}
}
