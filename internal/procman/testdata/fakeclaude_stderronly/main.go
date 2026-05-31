package main

import (
	"fmt"
	"os"
)

func main() {
	// Only write to stderr, then exit 1. Zero stdout output.
	fmt.Fprintln(os.Stderr, "ERROR: configuration file corrupted")
	fmt.Fprintln(os.Stderr, "ERROR: cannot initialize session")
	os.Exit(1)
}
