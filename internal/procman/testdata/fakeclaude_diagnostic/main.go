package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	// Writes a claude internal diagnostic line and a real error line to stderr,
	// then exits. Tests that [claude-code:...] diagnostic lines are filtered
	// from KindError output while plain stderr lines still forward.

	initMsg, _ := json.Marshal(map[string]interface{}{
		"type":       "system",
		"subtype":    "init",
		"session_id": "diag-diag-diag-diag-diag",
	})
	fmt.Println(string(initMsg))

	time.Sleep(100 * time.Millisecond)

	fmt.Fprintln(os.Stderr, `[claude-code:unrecognized_model] {"model":"deepseek-v4-flash","query_source":"sdk"}`)
	fmt.Fprintln(os.Stderr, "ERROR: real problem")
	os.Exit(1)
}
