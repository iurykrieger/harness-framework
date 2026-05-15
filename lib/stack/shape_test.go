package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

func loadGoldenTyped(t *testing.T) Stack {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "golden-stack.yaml"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	jb, err := yaml.YAMLToJSON(body)
	if err != nil {
		t.Fatalf("yaml→json: %v", err)
	}
	var s Stack
	if err := json.Unmarshal(jb, &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return s
}

func TestShapesByRole(t *testing.T) {
	s := loadGoldenTyped(t)
	loggers := s.ShapesByRole(RoleLogger)
	if len(loggers) != 1 || loggers[0].ID != "zap-prod-json" {
		t.Fatalf("ShapesByRole(logger) = %+v", loggers)
	}
	mw := s.ShapesByRole(RoleHTTPMiddleware)
	if len(mw) != 1 || mw[0].ID != "chi-access-log" {
		t.Fatalf("ShapesByRole(http-middleware) = %+v", mw)
	}
}

func TestShapesProducedBy(t *testing.T) {
	s := loadGoldenTyped(t)
	got := s.ShapesProducedBy("go.uber.org/zap")
	if len(got) != 1 || got[0].ID != "zap-prod-json" {
		t.Fatalf("ShapesProducedBy(zap) = %+v", got)
	}
	if len(s.ShapesProducedBy("nonexistent")) != 0 {
		t.Fatal("ShapesProducedBy(nonexistent) should be empty")
	}
}

func TestFieldsByMeaning(t *testing.T) {
	s := loadGoldenTyped(t)
	sev := s.LogShapes[0].FieldsByMeaning(MeaningSeverity)
	if len(sev) != 1 || sev[0].Key != "severity" {
		t.Fatalf("FieldsByMeaning(severity) = %+v", sev)
	}
}

func TestHasSeverity(t *testing.T) {
	s := loadGoldenTyped(t)
	if !s.LogShapes[0].HasSeverity() {
		t.Fatal("zap-prod-json should have severity")
	}
	if s.LogShapes[1].HasSeverity() {
		t.Fatal("chi-access-log should NOT have severity (combined-log-format)")
	}
}

func TestStack_FullRoundTrip(t *testing.T) {
	lineStart := 25
	in := Stack{
		Version:   "0.2.0",
		Languages: []Language{{Name: "go", Version: "1.25"}},
		Components: []Component{{
			Role:     RoleHTTPServer,
			Name:     "net/http",
			Evidence: []Evidence{{File: "cmd/server/main.go", LineStart: &lineStart, Rationale: "x"}},
		}},
		LogShapes:  []LogShape{{ID: "x", ProducedBy: []string{"net/http"}, Format: FormatPlain, Sample: "x"}},
		Purpose:    "HTTP API",
		Archetypes: []Archetype{ArchetypeHTTPAPI},
		Journeys: []Journey{{
			ID:        "user-registration",
			Name:      "User registration",
			Summary:   "POST /users",
			Archetype: ArchetypeHTTPAPI,
			EntryPoints: []EntryPoint{{
				Kind:     EntryPointHTTPRoute,
				Method:   "POST",
				Path:     "/users",
				Evidence: Evidence{File: "cmd/server/main.go", LineStart: &lineStart, Rationale: "handler"},
			}},
		}},
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Stack
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Purpose != "HTTP API" {
		t.Errorf("purpose = %q", out.Purpose)
	}
	if len(out.Archetypes) != 1 || out.Archetypes[0] != ArchetypeHTTPAPI {
		t.Errorf("archetypes = %v", out.Archetypes)
	}
	if len(out.Journeys) != 1 || out.Journeys[0].ID != "user-registration" {
		t.Errorf("journeys = %v", out.Journeys)
	}
}

func TestStack_OptionalFieldsOmitted(t *testing.T) {
	in := Stack{
		Version:    "0.1.0",
		Languages:  []Language{{Name: "go"}},
		Components: []Component{{Role: RoleLogger, Name: "x", Evidence: []Evidence{{File: "x.go", Rationale: "x"}}}},
		LogShapes:  []LogShape{{ID: "x", ProducedBy: []string{"x"}, Format: FormatPlain, Sample: "x"}},
	}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, key := range []string{`"purpose"`, `"archetypes"`, `"journeys"`} {
		if containsKey(s, key) {
			t.Errorf("expected %s to be omitted, got %s", key, s)
		}
	}
}

func containsKey(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
