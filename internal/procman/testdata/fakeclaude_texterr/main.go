package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	// Output valid text, then crash with stderr + exit 1.
	// Tests that KindText + KindError coexist in output channel.

	initMsg, _ := json.Marshal(map[string]interface{}{
		"type":       "system",
		"subtype":    "init",
		"session_id": "texterr-texterr-texterr-texterr",
	})
	fmt.Println(string(initMsg))

	textMsg, _ := json.Marshal(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "partial response before crash"},
			},
		},
	})
	fmt.Println(string(textMsg))

	// Small delay so the reader goroutine has time to process.
	time.Sleep(100 * time.Millisecond)

	fmt.Fprintln(os.Stderr, "FATAL: simulated mid-response crash")
	os.Exit(1)
}
