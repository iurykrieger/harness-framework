package sensor

// Project returns all elements of requires[] whose `kind` equals the given
// kind, preserving array order. Returns nil when requires is absent, empty,
// or no entry matches. Schema validation is the caller's responsibility —
// Project silently skips entries that are not JSON objects or whose kind
// field is missing/non-string.
//
// TRANSITIONAL: while v1 sensors are still on disk, Project also accepts
// the v1 shape (top-level depends_on, requires as object, execution.prepare)
// and synthesizes the v2 array internally. The synthesis lives at the top
// of this function in a single dispatch on the runtime type of requires;
// commit 5 of the unification PR removes it.
func Project(sensor map[string]interface{}, kind string) []map[string]interface{} {
	v2 := asV2Array(sensor)
	if len(v2) == 0 {
		return nil
	}
	var out []map[string]interface{}
	for _, raw := range v2 {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		k, ok := item["kind"].(string)
		if !ok || k != kind {
			continue
		}
		out = append(out, item)
	}
	return out
}

// asV2Array returns the requires[] array, synthesizing one from v1 fields
// when needed. The synthesis is the single transitional point; commit 5
// removes it and inlines `requires, _ := sensor["requires"].([]interface{})`.
func asV2Array(sensor map[string]interface{}) []interface{} {
	if arr, ok := sensor["requires"].([]interface{}); ok {
		return arr
	}
	return synthesizeV2(sensor)
}

// synthesizeV2 builds a v2 requires[] from v1 fields. Order is stable:
// sensor → tool → env → context → permission → step. Step entries are
// never deduplicated. Used only while v1 sensors coexist (commits 1–4).
func synthesizeV2(sensor map[string]interface{}) []interface{} {
	out := []interface{}{}

	if deps, ok := sensor["depends_on"].([]interface{}); ok {
		for _, d := range deps {
			id, ok := d.(string)
			if !ok {
				continue
			}
			out = append(out, map[string]interface{}{"kind": "sensor", "id": id})
		}
	}

	reqObj, _ := sensor["requires"].(map[string]interface{})
	if reqObj != nil {
		if tools, ok := reqObj["tools"].([]interface{}); ok {
			for _, t := range tools {
				name, ok := t.(string)
				if !ok {
					continue
				}
				out = append(out, map[string]interface{}{"kind": "tool", "name": name})
			}
		}
		if envs, ok := reqObj["env"].([]interface{}); ok {
			for _, e := range envs {
				em, ok := e.(map[string]interface{})
				if !ok {
					continue
				}
				entry := map[string]interface{}{"kind": "env"}
				for _, k := range []string{"name", "description", "optional"} {
					if v, ok := em[k]; ok {
						entry[k] = v
					}
				}
				out = append(out, entry)
			}
		}
		if ctxs, ok := reqObj["context"].([]interface{}); ok {
			for _, c := range ctxs {
				p, ok := c.(string)
				if !ok {
					continue
				}
				out = append(out, map[string]interface{}{"kind": "context", "path": p})
			}
		}
		if perms, ok := reqObj["permissions"].([]interface{}); ok {
			for _, p := range perms {
				s, ok := p.(string)
				if !ok {
					continue
				}
				out = append(out, map[string]interface{}{"kind": "permission", "scope": s})
			}
		}
	}

	if exec, ok := sensor["execution"].(map[string]interface{}); ok {
		if steps, ok := exec["prepare"].([]interface{}); ok {
			for _, st := range steps {
				sm, ok := st.(map[string]interface{})
				if !ok {
					continue
				}
				entry := map[string]interface{}{"kind": "step"}
				for _, k := range []string{"command", "timeout_ms", "exit_code_map"} {
					if v, ok := sm[k]; ok {
						entry[k] = v
					}
				}
				out = append(out, entry)
			}
		}
	}

	return out
}
