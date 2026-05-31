package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

func main() {
	io.ReadAll(os.Stdin)

	initMsg, _ := json.Marshal(map[string]interface{}{
		"type":       "system",
		"subtype":    "init",
		"session_id": "slow-slow-slow-slow-slow-slow",
	})
	fmt.Println(string(initMsg))

	// Output 5 text chunks with delays between them.
	for i := 1; i <= 5; i++ {
		textMsg, _ := json.Marshal(map[string]interface{}{
			"type": "assistant",
			"message": map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": fmt.Sprintf("chunk %d of 5", i)},
				},
			},
		})
		fmt.Println(string(textMsg))
		time.Sleep(200 * time.Millisecond)
	}

	resultMsg, _ := json.Marshal(map[string]interface{}{
		"type":    "result",
		"subtype": "success",
		"result":  "slow stream complete",
	})
	fmt.Println(string(resultMsg))
}
