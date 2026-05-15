// lib/sensor/catalog.go
package sensor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/iurykrieger/harness-framework/lib/schema"
)

// CatalogWarn names a sensor file that was skipped during Catalog,
// either because the file's JSON failed to parse or because schema
// validation rejected it. Catalog itself does not emit signals or write
// to stdout; the caller decides how to surface skipped entries.
type CatalogWarn struct {
	File   string // base name of the skipped file
	Reason string // human-readable explanation
}

// Catalog is the single entrypoint for enumerating sensor JSON files
// under sensorsDir. Returns one *Sensor per validly-parsed, schema-valid
// file (sorted alphabetically by filename) and one CatalogWarn per
// skipped file. Schema validation is always performed: pass a validator
// constructed for the relevant schemas dir.
//
// When sensorsDir does not exist, returns (nil, nil, nil) — an empty
// catalog. Other read-dir failures are returned as an error.
func Catalog(sensorsDir string, v *schema.Validator) ([]*Sensor, []CatalogWarn, error) {
	if v == nil {
		return nil, nil, fmt.Errorf("Catalog: validator is required")
	}
	entries, err := os.ReadDir(sensorsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read dir %s: %w", sensorsDir, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var out []*Sensor
	var warns []CatalogWarn
	for _, name := range names {
		fpath := filepath.Join(sensorsDir, name)
		body, err := schema.ReadAsJSON(fpath)
		if err != nil {
			warns = append(warns, CatalogWarn{File: name, Reason: fmt.Sprintf("read %s: %v", fpath, err)})
			continue
		}
		// Validate the raw shape against the schema first; schema validation
		// is stricter than json.Unmarshal into *Sensor (which silently
		// ignores unknown keys and type errors that the schema catches).
		var asMap map[string]interface{}
		if err := json.Unmarshal(body, &asMap); err != nil {
			warns = append(warns, CatalogWarn{File: name, Reason: fmt.Sprintf("parse %s: %v", fpath, err)})
			continue
		}
		if err := v.Validate(schema.TargetSensor, asMap); err != nil {
			warns = append(warns, CatalogWarn{File: name, Reason: fmt.Sprintf("schema-invalid %s: %v", fpath, err)})
			continue
		}
		var s Sensor
		if err := json.Unmarshal(body, &s); err != nil {
			warns = append(warns, CatalogWarn{File: name, Reason: fmt.Sprintf("decode %s: %v", fpath, err)})
			continue
		}
		out = append(out, &s)
	}
	return out, warns, nil
}
