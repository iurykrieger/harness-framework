package signal

// MapExitCode resolves an exit code via sensor.execution.exit_code_map.
// "*" is the wildcard fallback. Returns ("error", "high") if no entry matches
// and no wildcard is present.
func MapExitCode(code int, ecMap []interface{}) (verdict, severity string) {
	var fallbackV, fallbackS string
	haveFallback := false
	for _, item := range ecMap {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		switch ec := m["exit_code"].(type) {
		case float64:
			if int(ec) == code {
				v, _ := m["verdict"].(string)
				s, _ := m["severity"].(string)
				return v, s
			}
		case string:
			if ec == "*" {
				fallbackV, _ = m["verdict"].(string)
				fallbackS, _ = m["severity"].(string)
				haveFallback = true
			}
		}
	}
	if haveFallback {
		return fallbackV, fallbackS
	}
	return "error", "high"
}
