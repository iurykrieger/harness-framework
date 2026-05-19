// Package layer is the deterministic /create-sensors planner. Each
// validation lens is a Go file in this package implementing LayerRecipe;
// init() in each file registers itself into the package registry. The
// enum mirrors lib/sensor.Layer and schemas/sensor.yaml::properties.layer.enum.
package layer

import (
	"sort"
	"sync"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
	"github.com/iurykrieger/harness-framework/lib/usecase"
)

// Layer mirrors lib/sensor.Layer; redeclared here as an alias so this
// package does not need to translate types at recipe-author time.
type Layer = sensor.Layer

// LayerRecipe is the per-layer interface. Implementations live in
// sibling files; each file's init() registers itself.
type LayerRecipe interface {
	// Name returns the canonical layer slug.
	Name() Layer

	// Applicable reports whether this layer makes sense for the given
	// stack + usecase + existing catalog. The string is the reason
	// when the layer is NOT applicable (surfaced in the /create-sensors
	// plan report).
	Applicable(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) (bool, string)

	// Plan returns 1..N draft sensors that, together, validate the
	// usecase through this layer's lens. Each draft carries layer=Name().
	// Drafts MUST be topologically ordered (leaves before composites).
	Plan(s stack.Stack, uc usecase.UseCase, cat []sensor.Sensor) []Draft
}

// Draft is the planner's neutral representation of one sensor to emit.
// /create-sensors converts each Draft into a sensor.yaml via write-sensor.
// JSON tags mirror schemas/sensor.yaml so write-sensor.go can pipe the
// emitted draft straight into lib/sensor.ValidateAndPersist.
type Draft struct {
	SensorID    string               `json:"id"`
	Version     string               `json:"version"`
	Name        string               `json:"name"`
	Layer       Layer                `json:"layer,omitempty"`
	Kind        sensor.Kind          `json:"kind"`
	Type        sensor.Type          `json:"type"`
	Regulation  sensor.Regulation    `json:"regulation"`
	Phase       sensor.Phase         `json:"phase"`
	Determinism sensor.Determinism   `json:"determinism"`
	Output      sensor.Output        `json:"output"`
	Description string               `json:"description"`
	UseCases    []string             `json:"use_cases"`
	Requires    []sensor.Requirement `json:"requires,omitempty"`
	Execution   sensor.Execution     `json:"execution"`
	Cost        sensor.Cost          `json:"cost"`
	Triggers    []sensor.Trigger     `json:"triggers"`
	Calibration *sensor.Calibration  `json:"calibration,omitempty"`
}

var (
	mu       sync.RWMutex
	registry = map[Layer]LayerRecipe{}
)

// Register inserts a recipe into the package registry. Panics if the
// layer is not a known enum value or if it is already registered.
// Intended for use from each recipe file's init().
func Register(l Layer, r LayerRecipe) {
	mu.Lock()
	defer mu.Unlock()
	if !validLayer(l) {
		panic("layer.Register: unknown layer " + string(l))
	}
	if _, dup := registry[l]; dup {
		panic("layer.Register: duplicate registration for " + string(l))
	}
	registry[l] = r
}

// Get returns the recipe for a layer. Returns nil if unregistered.
func Get(l Layer) LayerRecipe { mu.RLock(); defer mu.RUnlock(); return registry[l] }

// AllLayers returns the registered Layer values sorted alphabetically
// so iteration order is deterministic across processes.
func AllLayers() []Layer {
	mu.RLock()
	out := make([]Layer, 0, len(registry))
	for l := range registry {
		out = append(out, l)
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

// validLayer is the closed-enum guard. Add a new Layer constant in
// lib/sensor/shape.go AND the matching enum value in
// schemas/sensor.yaml AND a recipe file in this package together —
// they are versioned as one unit.
func validLayer(l Layer) bool {
	switch l {
	case sensor.LayerUnitTest, sensor.LayerIntegrationTest, sensor.LayerContractTest,
		sensor.LayerE2E,
		sensor.LayerDBState, sensor.LayerLogTrace, sensor.LayerMetric,
		sensor.LayerEventEmission, sensor.LayerEventConsumption,
		sensor.LayerPerformance, sensor.LayerResilience,
		sensor.LayerCodeQuality, sensor.LayerArchitecture, sensor.LayerSecurity,
		sensor.LayerDependencyHealth,
		sensor.LayerDBSchema, sensor.LayerAccessibility:
		return true
	}
	return false
}
