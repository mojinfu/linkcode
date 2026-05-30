package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	io.ReadAll(os.Stdin)

	initMsg, _ := json.Marshal(map[string]interface{}{
		"type":       "system",
		"subtype":    "init",
		"session_id": "large-large-large-large-large",
	})
	fmt.Println(string(initMsg))

	// Output 600 lines of valid stream-json text chunks.
	for i := 1; i <= 600; i++ {
		textMsg, _ := json.Marshal(map[string]interface{}{
			"type": "assistant",
			"message": map[string]interface{}{
				"content": []map[string]interface{}{
					{"type": "text", "text": fmt.Sprintf("line %d: all work and no play makes claude a dull bot", i)},
				},
			},
		})
		fmt.Println(string(textMsg))
	}

	resultMsg, _ := json.Marshal(map[string]interface{}{
		"type":    "result",
		"subtype": "success",
		"result":  "large output complete",
	})
	fmt.Println(string(resultMsg))
}
