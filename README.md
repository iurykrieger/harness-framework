# harness-framework

A Claude Code plugin that implements a **sensor harness** for AI coding agents. Sensors observe the system after the agent acts and emit Signals optimized for self-correction.

## Requirements

- [Claude Code](https://claude.com/claude-code) with plugin support
- Go 1.20+ on PATH (Claude Code's bundled toolchain works)

No binaries are shipped or built. Scripts run on demand via `go run`.

## Installation

Install through Claude Code's plugin manager (see Claude Code docs). The plugin lives in a directory the user does not need to touch.

## Quick start

Inside any project where you want a harness:

```bash
# Create your first sensor (auto-detects archetype):
/detect-sensors

# Run it:
/run-sensor <sensor-id>

# For blocking sensors (long-running processes):
/start-sensor <sensor-id>
/tail-sensor <sensor-id> 0
/stop-sensor <sensor-id>
```

All commands resolve sensors as `sensors/<id>.yaml` under the user's project root.

## Architecture

See [`CLAUDE.md`](./CLAUDE.md) for the full architecture, schema overview, and project rules. The two schemas (`schemas/sensor.yaml` and `schemas/signal.yaml`) are the source of truth for sensor definitions and signal output.

## Invocation contract

The plugin's skills invoke scripts via:

```bash
HARNESS_REGISTRY_ROOT="$(pwd)" GOWORK=off \
  go run -C "${CLAUDE_PLUGIN_ROOT}" -tags=<tag> \
  ./skills/<name>/scripts <args>
```

The `-C` flag isolates the plugin's Go module from your project's `go.mod`/`go.work`. The env vars preserve the project root for sensor discovery and subprocess cwd. See `CLAUDE.md` for the full explanation.

## Upgrading from a JSON-era version

This plugin moved sensors, use cases, and the stack manifest from JSON to YAML. On upgrade, downstream projects must:

```bash
rm -f .harness/stack.json
rm -f .harness/sensors/*.json
rm -f .harness/usecases/*.json
```

then re-run:

- `/detect-sensors`
- `/detect-usecases`

Both commands are idempotent regenerators — they will produce fresh YAML artifacts. No data is migrated; the framework treats `*.json` files in those directories as foreign content.

Runtime state (`.harness/runtime/running_sensors.json`, `.harness/runtime/auto-issues.json`), the streaming protocol (`signals.log`, runner stdout JSONL, hook IO), and the plugin manifest (`.claude-plugin/plugin.json`) remain JSON and require no action.

## License

MIT — see [`LICENSE`](./LICENSE).
