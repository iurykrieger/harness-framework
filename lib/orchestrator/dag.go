// Package orchestrator resolves and runs sensor dependency graphs. A
// sensor's `requires[]` entries of `kind=sensor` declare ids of other
// sensors that must run and pass before it. This package walks that closure,
// sorts topologically (deps first), and runs each sensor's
// requires[kind=step] → command → teardown lifecycle. Failures cascade:
// dependents of a failed sensor never run and emit cascade Signals instead.
package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iurykrieger/harness-framework/lib/sensor"
)

// Sensor is the parsed-JSON form of one sensor along the dependency path.
type Sensor struct {
	ID   string
	Path string
	JSON map[string]interface{}
}

// Resolve loads the sensor identified by rootID from projectRoot, walks
// its requires[kind=sensor] transitively, and returns the slice topo-sorted (leaves
// first, rootID last). Cycles (including self-loops A → A) and missing
// dependency files cause an error and an empty slice.
func Resolve(rootID, projectRoot string) ([]Sensor, error) {
	sensors := map[string]Sensor{}
	deps := map[string][]string{}
	if err := loadRecursive(rootID, projectRoot, sensors, deps, map[string]bool{}); err != nil {
		return nil, err
	}
	return topoSort(rootID, sensors, deps)
}

func loadRecursive(id, projectRoot string, sensors map[string]Sensor, deps map[string][]string, visiting map[string]bool) error {
	if _, ok := sensors[id]; ok {
		return nil
	}
	if visiting[id] {
		return fmt.Errorf("dependency cycle detected at sensor %q", id)
	}
	visiting[id] = true
	defer delete(visiting, id)

	path, err := sensor.Resolve(id, projectRoot)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read sensor %q: %w", id, err)
	}
	var s map[string]interface{}
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("parse sensor %q: %w", id, err)
	}
	abs, _ := filepath.Abs(path)
	sensors[id] = Sensor{ID: id, Path: abs, JSON: s}

	depIDs := readDepsArray(s)
	deps[id] = depIDs
	for _, depID := range depIDs {
		if depID == id {
			return fmt.Errorf("dependency cycle detected at sensor %q (self-loop)", id)
		}
		if err := loadRecursive(depID, projectRoot, sensors, deps, visiting); err != nil {
			return err
		}
	}
	return nil
}

func readDepsArray(s map[string]interface{}) []string {
	items := sensor.Project(s, "sensor")
	out := make([]string, 0, len(items))
	for _, item := range items {
		if id, ok := item["id"].(string); ok && id != "" {
			out = append(out, id)
		}
	}
	return out
}

// topoSort runs Kahn's algorithm starting from leaves and ending at rootID.
func topoSort(rootID string, sensors map[string]Sensor, deps map[string][]string) ([]Sensor, error) {
	indegree := map[string]int{}
	dependents := map[string][]string{}
	for id := range sensors {
		indegree[id] = 0
	}
	for id, ds := range deps {
		for _, d := range ds {
			indegree[id]++
			dependents[d] = append(dependents[d], id)
		}
	}

	var queue []string
	for id, deg := range indegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	out := make([]Sensor, 0, len(sensors))
	for len(queue) > 0 {
		// Pop the lexicographically smallest id for stable ordering.
		minIdx := 0
		for i := range queue {
			if queue[i] < queue[minIdx] {
				minIdx = i
			}
		}
		id := queue[minIdx]
		queue = append(queue[:minIdx], queue[minIdx+1:]...)
		out = append(out, sensors[id])
		for _, dep := range dependents[id] {
			indegree[dep]--
			if indegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}
	if len(out) != len(sensors) {
		return nil, fmt.Errorf("dependency cycle detected (resolved %d of %d sensors)", len(out), len(sensors))
	}
	if out[len(out)-1].ID != rootID {
		return nil, fmt.Errorf("internal: topo sort did not end at root %q", rootID)
	}
	return out, nil
}
