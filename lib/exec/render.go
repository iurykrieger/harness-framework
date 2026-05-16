package exec

// Template rendering is intentionally not centralized in this package.
// Each step type (lib/step/shell, lib/step/http, lib/step/assert,
// lib/step/sensor) holds its own buildActionsContext helper and calls
// template.RenderActions directly against the fields it actually
// expands (run:, url:, headers, body templates, expect.value, …). The
// engine's job is to sequence steps and fold verdicts, not to
// second-guess which fields each step will template.
//
// This file is intentionally bare so the package layout matches the
// design (engine.go, render.go, aggregate.go). If a shared rendering
// helper earns its place, it belongs here.
