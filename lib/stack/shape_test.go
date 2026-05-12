package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadGoldenTyped(t *testing.T) Stack {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "golden-stack.json"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var s Stack
	if err := json.Unmarshal(body, &s); err != nil {
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
