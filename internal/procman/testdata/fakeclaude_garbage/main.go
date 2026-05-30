package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	_, err := os.Stat("fakeclaude_garbage_marker")
	firstRun := os.IsNotExist(err)

	if firstRun {
		os.WriteFile("fakeclaude_garbage_marker", []byte("1"), 0644)
		// Non-JSON garbage output — simulates Claude outputting a panic dump or
		// unformatted error text instead of stream-json.
		fmt.Fprintln(os.Stderr, "ERROR: simulated panic: nil pointer dereference")
		fmt.Println("panic: runtime error: invalid memory address or nil pointer dereference")
		fmt.Println("[signal 0xc0000005 code=0x0 addr=0x0 pc=0x7ffa1b2c3d4e]")
		fmt.Println()
		fmt.Println("goroutine 1 [running]:")
		fmt.Println("main.main()")
		fmt.Println("        /app/main.go:42 +0x1a3")
		os.Exit(2)
	}

	io.ReadAll(os.Stdin)

	// Second run: valid stream-json.
	fmt.Println(`{"type":"system","subtype":"init","session_id":"bbbbbbbb-cccc-dddd-eeee-ffffffffffff"}`)
	fmt.Println(`{"type":"assistant","message":{"content":[{"type":"text","text":"recovered and working"}]}}`)
	fmt.Println(`{"type":"result","subtype":"success","result":"ok"}`)
}
