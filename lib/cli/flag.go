// Package cli holds flag.Value helpers shared by the harness runners.
package cli

import "strings"

// MultiFlag implements flag.Value for repeatable string flags (--slot k=v --slot k2=v2).
type MultiFlag []string

func (m *MultiFlag) String() string     { return strings.Join(*m, ",") }
func (m *MultiFlag) Set(s string) error { *m = append(*m, s); return nil }
