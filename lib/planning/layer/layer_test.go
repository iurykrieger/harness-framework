package layer

import (
	"sort"
	"testing"
)

func TestRegistryHasAllSeventeenLayers(t *testing.T) {
	got := AllLayers()
	if len(got) != 17 {
		t.Fatalf("expected 17 layers, got %d: %v", len(got), got)
	}
	seen := map[Layer]bool{}
	for _, l := range got {
		if seen[l] {
			t.Fatalf("duplicate registration: %s", l)
		}
		seen[l] = true
	}
}

func TestAllLayersSortedDeterministic(t *testing.T) {
	first := AllLayers()
	second := AllLayers()
	if len(first) != len(second) {
		t.Fatalf("non-deterministic length")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("non-deterministic order at %d: %s vs %s", i, first[i], second[i])
		}
	}
	if !sort.SliceIsSorted(first, func(i, j int) bool { return string(first[i]) < string(first[j]) }) {
		t.Fatalf("AllLayers() must return sorted slice")
	}
}

func TestRegisterRejectsUnknownLayer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic registering unknown layer")
		}
	}()
	Register("not-a-real-layer", nil)
}
