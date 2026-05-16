// devmsg sends a WeCom message to a user via the devbot.
//
// Usage:
//
//	devmsg -user <userid> <message>
//
// Credentials are read from botsecret.json (role=devbot).
//
// This tool is standalone and is NOT part of the main LinkCode binary.
// It exists so the AI assistant can page the developer during testing.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"linkcode/internal/channel"
	"linkcode/internal/channel/wecom"
)

type botEntry struct {
	BotID     string `json:"botId"`
	SecretKey string `json:"secretKey"`
	Name      string `json:"name"`
	Role      string `json:"role"`
}

func main() {
	userID := flag.String("user", "", "Target user ID (e.g., XuDeYi)")
	secretFile := flag.String("secrets", "botsecret.json", "Path to botsecret.json")
	flag.Parse()

	if *userID == "" || flag.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Usage: devmsg -user <userid> <message>\n")
		fmt.Fprintf(os.Stderr, "Example: devmsg -user XuDeYi '请给bot1发一条消息'\n")
		os.Exit(1)
	}
	text := flag.Arg(0)

	// Load and find devbot.
	botID, secret, err := findDevBot(*secretFile)
	if err != nil {
		log.Fatalf("find devbot: %v", err)
	}

	// Connect.
	ch := wecom.New(botID, secret)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := ch.Connect(ctx); err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer ch.Close()

	log.Printf("devbot connected, sending to %s: %s", *userID, text)

	// Send proactive message with ack wait.
	if err := ch.SendMessage(ctx, *userID, channel.MessageContent{
		Text:   text,
		ChatID: *userID,
	}); err != nil {
		log.Fatalf("send: %v", err)
	}

	log.Printf("message delivered to %s", *userID)

	// Wait a moment for WeCom to process, then clean exit.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigCh:
	case <-time.After(2 * time.Second):
	}
}

func findDevBot(path string) (botID, secret string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", path, err)
	}

	var bots []botEntry
	if err := json.Unmarshal(data, &bots); err != nil {
		return "", "", fmt.Errorf("parse %s: %w", path, err)
	}

	for _, b := range bots {
		if b.Role == "devbot" {
			return b.BotID, b.SecretKey, nil
		}
	}
	return "", "", fmt.Errorf("no devbot found in %s", path)
}
