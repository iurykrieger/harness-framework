package sensor_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

func TestWriteFixture_HappyPath(t *testing.T) {
	root := t.TempDir()
	rel := ".harness/sensors/fixtures/assert-x/pass.txt"
	abs, err := sensor.WriteFixture(root, rel, []byte("200\n"))
	if err != nil {
		t.Fatal(err)
	}
	if abs != filepath.Join(root, rel) {
		t.Fatalf("abs=%q", abs)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "200\n" {
		t.Fatalf("body=%q", body)
	}
}

func TestWriteFixture_CreatesNestedParents(t *testing.T) {
	root := t.TempDir()
	rel := ".harness/sensors/fixtures/deeply/nested/case/pass.txt"
	if _, err := sensor.WriteFixture(root, rel, []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFixture_PathEscape_Rejected(t *testing.T) {
	root := t.TempDir()
	cases := []string{
		".harness/sensors/fixtures/../escape.txt",
		".harness/sensors/escape.txt",
		"/etc/passwd",
		"../outside.txt",
		".harness/sensors/fixtures", // exactly the root
	}
	for _, bad := range cases {
		_, err := sensor.WriteFixture(root, bad, []byte("x"))
		if err == nil {
			t.Fatalf("expected error for %q, got nil", bad)
		}
		var fpe *sensor.FixturePathEscapeError
		if !errors.As(err, &fpe) {
			t.Fatalf("expected *FixturePathEscapeError for %q, got %T: %v", bad, err, err)
		}
	}
}

func TestWriteFixture_IdempotentRewrite(t *testing.T) {
	root := t.TempDir()
	rel := ".harness/sensors/fixtures/assert-z/pass.txt"
	for i := 0; i < 2; i++ {
		if _, err := sensor.WriteFixture(root, rel, []byte("same")); err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
	}
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "same" {
		t.Fatalf("body=%q", body)
	}
}

func TestWriteFixture_MissingArgs(t *testing.T) {
	if _, err := sensor.WriteFixture("", "a.txt", []byte("x")); err == nil {
		t.Fatal("expected error for empty projectRoot")
	}
	if _, err := sensor.WriteFixture(t.TempDir(), "", []byte("x")); err == nil {
		t.Fatal("expected error for empty relPath")
	}
}
