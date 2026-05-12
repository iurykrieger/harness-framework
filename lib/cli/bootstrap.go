package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/iurykrieger/harness-framework/lib/registry"
	"github.com/iurykrieger/harness-framework/lib/schema"
)

// BootstrapResult is the standardized return from Bootstrap. When ExitCode
// is non-zero, signals have already been emitted to stdout — the caller
// should exit with that code immediately.
type BootstrapResult struct {
	Res       registry.Result
	Validator *schema.Validator
	Diagnose  map[string]interface{}
	ExitCode  int
}

// Bootstrap performs the standard setup for skills that touch the sensor
// registry: resolves cwd, discovers the registry root (with sanitization),
// emits discovery_error and registry_migrated signals when applicable, and
// initializes the schema validator.
//
// The caller uses the returned BootstrapResult to access registry.Result,
// the validator, and a pre-built diagnose map. If ExitCode != 0, the caller
// invokes os.Exit(ExitCode) without emitting anything else (signals have
// already been emitted).
func Bootstrap(skillName string, stdout, stderr io.Writer) BootstrapResult {
	startDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(stderr, "%s: cwd: %v\n", skillName, err)
		return BootstrapResult{ExitCode: 2}
	}
	res, reports, err := registry.LookupSanitized(startDir)
	if err != nil {
		_ = json.NewEncoder(stdout).Encode(registry.DiscoveryErrorSignal(err, skillName))
		return BootstrapResult{ExitCode: 1}
	}
	if len(reports) > 0 {
		_ = json.NewEncoder(stdout).Encode(registry.RegistryMigratedSignal(res, reports, skillName))
	}
	v, code := schema.LoadValidator("", stderr)
	if code != 0 {
		return BootstrapResult{Res: res, ExitCode: code, Diagnose: registry.DiagnoseMetadata(res)}
	}
	return BootstrapResult{
		Res:       res,
		Validator: v,
		Diagnose:  registry.DiagnoseMetadata(res),
		ExitCode:  0,
	}
}
