// streamtest tests WeCom streaming: sends multiple frames with the same
// stream_id (Finish:false...Finish:true) and observes how the client renders them.
//
// Usage:
//
//	go build -o streamtest ./cmd/streamtest/
//	./streamtest -secrets botsecret.json
//
// Then send any message to the devbot in WeCom to trigger the test.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const wssURL = "wss://openws.work.weixin.qq.com"

type botEntry struct {
	BotID     string `json:"botId"`
	SecretKey string `json:"secretKey"`
	Name      string `json:"name"`
	Role      string `json:"role"`
}

func main() {
	secretFile := flag.String("secrets", "botsecret.json", "Path to botsecret.json")
	flag.Parse()

	botID, secret, err := findDevBot(*secretFile)
	if err != nil {
		log.Fatalf("find devbot: %v", err)
	}

	log.Printf("streamtest: using devbot %s", botID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := connect(ctx, botID, secret)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close()

	log.Println("streamtest: connected. Send any message to the devbot in WeCom to trigger the test.")

	// Read messages, reply with multi-chunk stream when a text message arrives.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go heartbeatLoop(ctx, conn)

	for {
		select {
		case <-sigCh:
			log.Println("streamtest: shutting down")
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("read: %v", err)
			// Try reconnect.
			conn, err = connect(ctx, botID, secret)
			if err != nil {
				log.Fatalf("reconnect: %v", err)
			}
			continue
		}

		var env wecomEnvelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}

		if env.Cmd == "aibot_msg_callback" {
			var body wecomMsgCallback
			if err := json.Unmarshal(env.Body, &body); err != nil {
				continue
			}
			if body.MsgType == "text" && body.Text.Content != "" {
				log.Printf("received: %q from %s (req_id=%s)", body.Text.Content, body.From.UserID, env.Headers.ReqID)
				runStreamTest(conn, env.Headers.ReqID)
			}
		}
	}
}

func runStreamTest(conn *websocket.Conn, reqID string) {
	streamID := fmt.Sprintf("test_stream_%d", time.Now().UnixNano())

	// Incremental chunks — deliberately cross Markdown boundaries.
	chunks := []string{
		"【测试说明】\n\n",
		"下面是一段跨 chunk 的 Markdown：\n\n",
		"我需要**强调",
		"这个**重要内容。\n\n如果这段文字中 **强调这个** 能正确加粗，说明流式拼接成功。",
		"\n\n如果出现了 ** 原始字符，说明每个 chunk 被独立渲染了。\n\n---\n✅ streamID: " + streamID + "\n✅ 共 " + fmt.Sprintf("%d", 6) + " 个帧（含本结束帧）",
	}

	var builder strings.Builder
	totalChunks := len(chunks)

	for i, chunk := range chunks {
		builder.WriteString(chunk)
		fullContent := builder.String()
		isFinish := i == totalChunks-1

		resp := wecomEnvelope{
			Cmd: "aibot_respond_msg",
			Headers: wecomHeaders{
				ReqID: reqID,
			},
			Body: json.RawMessage(mustMarshal(wecomRespondMsg{
				MsgType: "stream",
				Stream: wecomStream{
					ID:      streamID,
					Finish:  isFinish,
					Content: fullContent,
				},
			})),
		}

		if err := conn.WriteJSON(resp); err != nil {
			log.Printf("send chunk %d: %v", i+1, err)
			return
		}
		log.Printf("sent chunk %d/%d (finish=%v, full=%d bytes, added=%d bytes)",
			i+1, totalChunks, isFinish, len(fullContent), len(chunk))
		time.Sleep(200 * time.Millisecond)
	}

	log.Println("streamtest: all chunks sent. Check WeCom.")
}

func connect(ctx context.Context, botID, secret string) (*websocket.Conn, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.Dial(wssURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	reqID := fmt.Sprintf("streamtest_%d", time.Now().UnixNano())
	subReq := wecomEnvelope{
		Cmd: "aibot_subscribe",
		Headers: wecomHeaders{
			ReqID: reqID,
		},
		Body: json.RawMessage(mustMarshal(wecomSubscribe{
			BotID:  botID,
			Secret: secret,
		})),
	}

	if err := conn.WriteJSON(subReq); err != nil {
		conn.Close()
		return nil, fmt.Errorf("subscribe: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read auth response: %w", err)
	}

	var resp wecomEnvelope
	if err := json.Unmarshal(msg, &resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("parse auth response: %w", err)
	}

	if resp.ErrCode != 0 {
		conn.Close()
		return nil, fmt.Errorf("auth failed: errcode=%d errmsg=%s", resp.ErrCode, resp.ErrMsg)
	}

	log.Printf("authenticated: %s", botID)
	conn.SetReadDeadline(time.Time{})
	return conn, nil
}

func heartbeatLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	seq := int64(0)
	for {
		select {
		case <-ticker.C:
			seq++
			ping := wecomEnvelope{
				Cmd: "ping",
				Headers: wecomHeaders{
					ReqID: fmt.Sprintf("ping_%d_%d", time.Now().UnixNano(), seq),
				},
			}
			conn.WriteJSON(ping)
		case <-ctx.Done():
			return
		}
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

func mustMarshal(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// --- JSON wire types ---

type wecomEnvelope struct {
	Cmd     string          `json:"cmd"`
	Headers wecomHeaders    `json:"headers"`
	Body    json.RawMessage `json:"body,omitempty"`
	ErrCode int             `json:"errcode,omitempty"`
	ErrMsg  string          `json:"errmsg,omitempty"`
}

type wecomHeaders struct {
	ReqID string `json:"req_id"`
}

type wecomSubscribe struct {
	BotID  string `json:"bot_id"`
	Secret string `json:"secret"`
}

type wecomRespondMsg struct {
	MsgType string      `json:"msgtype"`
	Stream  wecomStream `json:"stream,omitempty"`
}

type wecomStream struct {
	ID      string `json:"id"`
	Finish  bool   `json:"finish"`
	Content string `json:"content"`
}

type wecomMsgCallback struct {
	MsgID      string    `json:"msgid"`
	AIBotID    string    `json:"aibotid"`
	ChatID     string    `json:"chatid"`
	ChatType   string    `json:"chattype"`
	From       wecomUser `json:"from"`
	MsgType    string    `json:"msgtype"`
	CreateTime int64     `json:"create_time"`
	Text       wecomText `json:"text,omitempty"`
}

type wecomUser struct {
	UserID string `json:"userid"`
	Name   string `json:"name"`
}

type wecomText struct {
	Content string `json:"content"`
}
