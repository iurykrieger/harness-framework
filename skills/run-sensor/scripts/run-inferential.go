//go:build run_inferential

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "run-inferential: not yet implemented under streaming model (see Task 8)")
	os.Exit(1)
}
