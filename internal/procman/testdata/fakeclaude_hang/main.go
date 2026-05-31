package main

import "os"

func main() {
	// Simulate a process that hangs without producing output.
	// Block on reading stdin — when stdin is closed (proc.Send closes it)
	// or the process is killed by context cancellation, it exits.
	// Uses a loop of reads rather than a single io.ReadAll to avoid
	// triggering AV heuristics on small binaries.
	buf := make([]byte, 4096)
	for {
		_, err := os.Stdin.Read(buf)
		if err != nil {
			os.Exit(0)
		}
	}
}
