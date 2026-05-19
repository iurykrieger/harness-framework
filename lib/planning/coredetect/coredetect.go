// Package coredetect scaffolds platform-primitive sensors that
// /create-sensors auto-creates on demand when a layer needs one and
// the catalog does not yet contain it. Each known primitive id is
// implemented in a sibling file via ScaffoldFunc; init() in each file
// registers itself.
//
// The same scaffolds are the source of truth for /detect-sensors —
// when that skill is refactored to consume this package, the two
// skills agree byte-for-byte on platform primitives.
package coredetect

import (
	"fmt"
	"sync"

	"github.com/iurykrieger/harness-framework/lib/sensor"
	"github.com/iurykrieger/harness-framework/lib/stack"
)

// ScaffoldFunc produces a Draft for one primitive sensor, parameterized
// by the project's stack. Returns nil when the primitive is not
// applicable to this stack (e.g. setup-postgres on a stack without
// role=db-client) — the caller treats nil as "skip".
type ScaffoldFunc func(s stack.Stack) *Draft

// Draft mirrors lib/planning/layer.Draft; redeclared here to keep the
// coredetect package independent of the layer package (which would
// otherwise pull recipe init() side-effects into coredetect tests).
// JSON tags mirror schemas/sensor.yaml so /create-sensors can pipe the
// emitted scaffold straight into lib/sensor.ValidateAndPersist.
// use_cases is omitted intentionally — core scaffolds are root-tier
// platform primitives and the write-sensor wrapper sets the field on
// emit (see [skills/create-sensors/scripts/plan-and-emit.go]).
type Draft struct {
	SensorID    string               `json:"id"`
	Version     string               `json:"version"`
	Name        string               `json:"name"`
	Kind        sensor.Kind          `json:"kind"`
	Type        sensor.Type          `json:"type"`
	Regulation  sensor.Regulation    `json:"regulation"`
	Phase       sensor.Phase         `json:"phase"`
	Determinism sensor.Determinism   `json:"determinism"`
	Output      sensor.Output        `json:"output"`
	Description string               `json:"description"`
	UseCases    []string             `json:"use_cases"`
	Requires    []sensor.Requirement `json:"requires,omitempty"`
	Cost        sensor.Cost          `json:"cost"`
	Triggers    []sensor.Trigger     `json:"triggers"`
	Execution   sensor.Execution     `json:"execution"`
}

var (
	mu       sync.RWMutex
	registry = map[string]ScaffoldFunc{}
)

// Register inserts a scaffold into the package registry. Panics on
// duplicate id. Intended for use from each scaffold file's init().
func Register(id string, fn ScaffoldFunc) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[id]; dup {
		panic("coredetect.Register: duplicate id " + id)
	}
	registry[id] = fn
}

// Get returns the scaffold for the given id, or nil when unregistered.
func Get(id string) ScaffoldFunc { mu.RLock(); defer mu.RUnlock(); return registry[id] }

// EnsureMissing maps the requested ids to drafts via the registry,
// skipping ids whose scaffold returns nil (not applicable to this
// stack) and erroring on unknown ids (caller bug — only known
// primitives should be requested).
func EnsureMissing(s stack.Stack, ids []string) ([]Draft, error) {
	out := make([]Draft, 0, len(ids))
	for _, id := range ids {
		fn := Get(id)
		if fn == nil {
			return nil, fmt.Errorf("coredetect: unknown primitive id %q", id)
		}
		if d := fn(s); d != nil {
			d.SensorID = id
			out = append(out, *d)
		}
	}
	return out, nil
}
