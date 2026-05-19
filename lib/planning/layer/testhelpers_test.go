package layer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
	"sigs.k8s.io/yaml"
)

func loadUsecase(t *testing.T, name string) usecase.UseCase {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var uc usecase.UseCase
	if err := yaml.Unmarshal(body, &uc); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return uc
}

func filterOutRole(in []stack.Component, role string) []stack.Component {
	out := make([]stack.Component, 0, len(in))
	for _, c := range in {
		if string(c.Role) != role {
			out = append(out, c)
		}
	}
	return out
}
