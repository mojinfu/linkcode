package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	// Write one init line then close stdout immediately.
	// This simulates the scenario where readOutput hits EOF before
	// the process actually exits — the original channel-close race.
	fmt.Println(`{"type":"system","subtype":"init","session_id":"race-race-race-race-race"}`)

	// Close stdout. readOutput's scanner will hit EOF, triggering wg.Done().
	// Without the WaitGroup fix, this defer close(p.output) fires before
	// waitForExit can send the exit error.
	os.Stdout.Close()

	// Process stays alive for 2 seconds while waitForExit is blocked on cmd.Wait().
	time.Sleep(2 * time.Second)

	// Now exit. cmd.Wait() returns. waitForExit should send the exit error
	// BEFORE closing the output channel.
	os.Exit(1)
}
