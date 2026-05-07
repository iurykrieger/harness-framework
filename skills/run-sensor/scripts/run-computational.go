//go:build run_computational

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "run-computational: not yet implemented under streaming model (see Task 7)")
	os.Exit(1)
}
