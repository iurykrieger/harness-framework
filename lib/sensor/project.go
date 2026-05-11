package sensor

// Project returns all elements of requires[] whose `kind` equals the given
// kind, preserving array order. Returns nil when requires is absent, empty,
// or no entry matches. Schema validation is the caller's responsibility —
// Project silently skips entries that are not JSON objects or whose kind
// field is missing/non-string.
func Project(sensor map[string]interface{}, kind string) []map[string]interface{} {
	arr, ok := sensor["requires"].([]interface{})
	if !ok || len(arr) == 0 {
		return nil
	}
	var out []map[string]interface{}
	for _, raw := range arr {
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
