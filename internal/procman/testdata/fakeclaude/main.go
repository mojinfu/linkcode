package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

func main() {
	_, err := os.Stat("fakeclaude_marker")
	firstRun := os.IsNotExist(err)

	if firstRun {
		os.WriteFile("fakeclaude_marker", []byte("1"), 0644)
		fmt.Fprintln(os.Stderr, "ERROR: simulated Claude crash - version too high")
		out, _ := json.Marshal(map[string]interface{}{
			"type":    "result",
			"subtype": "error",
			"result":  "simulated crash",
		})
		fmt.Println(string(out))
		os.Exit(1)
	}

	io.ReadAll(os.Stdin)

	initMsg, _ := json.Marshal(map[string]interface{}{
		"type":       "system",
		"subtype":    "init",
		"session_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	})
	fmt.Println(string(initMsg))

	textMsg, _ := json.Marshal(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": "hello from fake claude"},
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
