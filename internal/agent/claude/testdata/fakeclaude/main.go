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
		"session_id": "clean-clean-clean-clean-clean",
	})
	fmt.Println(string(initMsg))

	textMsg, _ := json.Marshal(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "clean exit response"},
			},
		},
	})
	fmt.Println(string(textMsg))

	resultMsg, _ := json.Marshal(map[string]interface{}{
		"type":    "result",
		"subtype": "success",
		"result":  "done",
	})
	fmt.Println(string(resultMsg))
}
