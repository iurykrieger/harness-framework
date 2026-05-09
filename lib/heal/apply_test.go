// lib/heal/apply_test.go
package heal_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/heal"
)

func TestApply_CopyTemplate_Success(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env.example")
	dst := filepath.Join(dir, ".env")
	os.WriteFile(src, []byte("FOO=bar\n"), 0o644)

	results := heal.Apply(heal.ApplyContext{Root: dir, FailedSensor: heal.FailedSensor{Context: []string{dir}}}, []heal.Action{
		{Kind: "copy-template", Src: src, Dst: dst},
	})
	if len(results) != 1 || !results[0].Applied {
		t.Fatalf("expected applied; got %#v", results)
	}
	body, _ := os.ReadFile(dst)
	if string(body) != "FOO=bar\n" {
		t.Fatalf("dst content = %q", body)
	}
}

func TestApply_CopyTemplate_DstAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, ".env.example")
	dst := filepath.Join(dir, ".env")
	os.WriteFile(src, []byte("FOO=bar\n"), 0o644)
	os.WriteFile(dst, []byte("EXISTING=true\n"), 0o644)

	results := heal.Apply(heal.ApplyContext{Root: dir, FailedSensor: heal.FailedSensor{Context: []string{dir}}}, []heal.Action{
		{Kind: "copy-template", Src: src, Dst: dst},
	})
	if results[0].Applied {
		t.Fatal("dst exists; must NOT auto-apply")
	}
	body, _ := os.ReadFile(dst)
	if string(body) != "EXISTING=true\n" {
		t.Fatal("dst was overwritten; must be left alone")
	}
}

func TestApply_Mkdir_PathInContext(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "tmp-cache")
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{Context: []string{root}}}, []heal.Action{
		{Kind: "mkdir", Dir: dir},
	})
	if !results[0].Applied {
		t.Fatal("mkdir must succeed when dir is under context")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestApply_Mkdir_PathOutsideContext(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir() // separate, not in context
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{Context: []string{root}}}, []heal.Action{
		{Kind: "mkdir", Dir: filepath.Join(other, "x")},
	})
	if results[0].Applied {
		t.Fatal("dir outside requires.context must be rejected")
	}
}

func TestApply_Touch_PathInContext(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "marker")
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{Context: []string{root}}}, []heal.Action{
		{Kind: "touch", File: file},
	})
	if !results[0].Applied {
		t.Fatal("touch must succeed under context")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("marker not created")
	}
}

func TestApply_UnknownKind(t *testing.T) {
	root := t.TempDir()
	results := heal.Apply(heal.ApplyContext{Root: root}, []heal.Action{{Kind: "rm-rf-everything"}})
	if results[0].Applied {
		t.Fatal("unknown kind must be rejected")
	}
}

func TestApply_SetEnvInFile_RequiresValue(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(root, ".env")
	os.WriteFile(envFile, []byte(""), 0o644)
	// value_source=ask-user with no Value → must NOT apply (caller fills in via AskUserQuestion).
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{EnvNames: []string{"FOO"}}}, []heal.Action{
		{Kind: "set-env-in-file", File: envFile, Name: "FOO", ValueSource: "ask-user"},
	})
	if results[0].Applied {
		t.Fatal("ask-user without Value must defer (Applied=false, NeedsInput=true)")
	}
	if !results[0].NeedsInput {
		t.Fatal("expected NeedsInput=true so caller knows to prompt")
	}
}

func TestApply_SetEnvInFile_WithLiteralValue(t *testing.T) {
	root := t.TempDir()
	envFile := filepath.Join(root, ".env")
	os.WriteFile(envFile, []byte(""), 0o644)
	// .env must be gitignored so WriteEnvVar (Task 14) does not refuse the write.
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".env\n"), 0o644)
	results := heal.Apply(heal.ApplyContext{Root: root, FailedSensor: heal.FailedSensor{EnvNames: []string{"FOO"}}}, []heal.Action{
		{Kind: "set-env-in-file", File: envFile, Name: "FOO", Value: "bar"},
	})
	if !results[0].Applied {
		t.Fatalf("expected applied; got %#v", results[0])
	}
	body, _ := os.ReadFile(envFile)
	if string(body) != "FOO=bar\n" {
		t.Fatalf("env content = %q", body)
	}
}
