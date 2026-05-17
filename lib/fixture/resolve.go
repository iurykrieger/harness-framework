package fixture

import "fmt"

// Resolve returns the absolute path of the named fixture, or an error
// citing the pool's available count when the name is unknown.
func Resolve(pool Pool, name string) (string, error) {
	if path, ok := pool[name]; ok {
		return path, nil
	}
	return "", fmt.Errorf("fixture %q not found in pool (have %d fixtures)", name, len(pool))
}
