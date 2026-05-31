package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	// Emit init message.
	initMsg, _ := json.Marshal(map[string]interface{}{
		"type":       "system",
		"subtype":    "init",
		"session_id": "stop-stop-stop-stop-stop-stop-s",
	})
	fmt.Println(string(initMsg))

	// Emit text chunks every 500ms for up to 60 chunks (30s total).
	// The process stops when stdin is closed (proc.Send closes stdin)
	// or when killed by proc.Stop().
	for i := 1; i <= 60; i++ {
		textMsg, _ := json.Marshal(map[string]interface{}{
			"type": "assistant",
			"message": map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": fmt.Sprintf("thinking chunk %d", i)},
				},
			},
		})
		fmt.Println(string(textMsg))
		time.Sleep(500 * time.Millisecond)

		// Check if stdin is closed (proc was stopped).
		select {
		case <-time.After(0):
			// Non-blocking check: try reading a byte from stdin.
			// If stdin is closed, Read returns EOF (0 bytes, nil error on some platforms).
		default:
		}
	}

	// Final result (only reached if not killed).
	resultMsg, _ := json.Marshal(map[string]interface{}{
		"type":    "result",
		"subtype": "success",
		"result":  "all chunks delivered",
	})
	fmt.Println(string(resultMsg))

	// Drain stdin to detect closure.
	buf := make([]byte, 1)
	os.Stdin.Read(buf)
}
