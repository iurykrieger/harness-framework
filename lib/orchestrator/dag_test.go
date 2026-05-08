package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSensorJSON(t *testing.T, root, id string, depsOn []string) {
	t.Helper()
	deps := "[]"
	if len(depsOn) > 0 {
		var s string
		for i, d := range depsOn {
			if i > 0 {
				s += ","
			}
			s += `"` + d + `"`
		}
		deps = "[" + s + "]"
	}
	body := `{"id":"` + id + `","depends_on":` + deps + `}`
	if err := os.WriteFile(filepath.Join(root, id+".json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolve_Linear(t *testing.T) {
	root := t.TempDir()
	writeSensorJSON(t, root, "a", nil)
	writeSensorJSON(t, root, "b", []string{"a"})
	writeSensorJSON(t, root, "c", []string{"b"})

	order, err := Resolve("c", root)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, s := range order {
		got = append(got, s.ID)
	}
	want := []string{"a", "b", "c"}
	if !equal(got, want) {
		t.Fatalf("topo order = %v, want %v", got, want)
	}
}

func TestResolve_Diamond(t *testing.T) {
	// d → b, c ; b → a ; c → a   ⇒   a before b,c  ; b,c before d
	root := t.TempDir()
	writeSensorJSON(t, root, "a", nil)
	writeSensorJSON(t, root, "b", []string{"a"})
	writeSensorJSON(t, root, "c", []string{"a"})
	writeSensorJSON(t, root, "d", []string{"b", "c"})

	order, err := Resolve("d", root)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, s := range order {
		pos[s.ID] = i
	}
	if pos["a"] >= pos["b"] || pos["a"] >= pos["c"] || pos["b"] >= pos["d"] || pos["c"] >= pos["d"] {
		t.Fatalf("diamond order violates dependencies: %v", pos)
	}
	if len(order) != 4 {
		t.Fatalf("expected 4 sensors in order, got %d", len(order))
	}
}

func TestResolve_Cycle(t *testing.T) {
	root := t.TempDir()
	writeSensorJSON(t, root, "a", []string{"b"})
	writeSensorJSON(t, root, "b", []string{"a"})

	if _, err := Resolve("a", root); err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestResolve_SelfLoop(t *testing.T) {
	root := t.TempDir()
	writeSensorJSON(t, root, "a", []string{"a"})

	if _, err := Resolve("a", root); err == nil {
		t.Fatal("expected self-loop to be rejected")
	}
}

func TestResolve_MissingDep(t *testing.T) {
	root := t.TempDir()
	writeSensorJSON(t, root, "a", []string{"ghost"})

	if _, err := Resolve("a", root); err == nil {
		t.Fatal("expected missing dep error")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
