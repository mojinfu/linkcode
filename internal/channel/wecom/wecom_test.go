package wecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"linkcode/internal/channel"
)

// wsTestServer runs a local WebSocket server for testing.
// It handles the WeCom auth handshake and records all received messages.
type wsTestServer struct {
	url      string
	server   *httptest.Server
	mu       sync.Mutex
	messages []wsRecordedMsg // messages received in order
}

type wsRecordedMsg struct {
	msgType int    // websocket.TextMessage, etc.
	data    []byte
	when    time.Time
}

func startWSTestServer(t *testing.T) *wsTestServer {
	t.Helper()

	wts := &wsTestServer{}
	wts.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("[test-server] upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// Step 1: Read auth subscribe message from channel.
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			t.Logf("[test-server] read auth error: %v", err)
			return
		}
		wts.record(msgType, msg)

		var env wecomEnvelope
		if err := json.Unmarshal(msg, &env); err != nil || env.Cmd != "aibot_subscribe" {
			t.Logf("[test-server] unexpected auth message: %s", string(msg))
			return
		}

		// Step 2: Send auth success response.
		authResp := wecomEnvelope{ErrCode: 0}
		authRespBytes, _ := json.Marshal(authResp)
		if err := conn.WriteMessage(websocket.TextMessage, authRespBytes); err != nil {
			t.Logf("[test-server] write auth response error: %v", err)
			return
		}

		// Step 3: Read all subsequent messages until close.
		// For aibot_send_msg (proactive messages), send back an ack.
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) ||
					strings.Contains(err.Error(), "close") ||
					strings.Contains(err.Error(), "EOF") {
					return
				}
				t.Logf("[test-server] read error: %v", err)
				return
			}
			wts.record(msgType, msg)

			// If this is a proactive message, send back an ack with errcode=0.
			var incoming wecomEnvelope
			if json.Unmarshal(msg, &incoming) == nil && incoming.Cmd == "aibot_send_msg" {
				ack := wecomEnvelope{
					ErrCode: 0,
					Headers: wecomHeaders{
						ReqID: incoming.Headers.ReqID,
					},
				}
				ackBytes, _ := json.Marshal(ack)
				if err := conn.WriteMessage(websocket.TextMessage, ackBytes); err != nil {
					t.Logf("[test-server] write ack error: %v", err)
					return
				}
			}
		}
	}))

	// Extract the ws:// URL from the http:// test server URL.
	wts.url = "ws" + strings.TrimPrefix(wts.server.URL, "http")
	return wts
}

func (wts *wsTestServer) record(msgType int, data []byte) {
	wts.mu.Lock()
	defer wts.mu.Unlock()
	wts.messages = append(wts.messages, wsRecordedMsg{
		msgType: msgType,
		data:    append([]byte{}, data...),
		when:    time.Now(),
	})
}

func (wts *wsTestServer) close() {
	wts.server.Close()
}

// receivedMessages returns all received messages as decoded WeCom envelopes.
func (wts *wsTestServer) receivedEnvelopes(t *testing.T) []wecomEnvelope {
	t.Helper()
	wts.mu.Lock()
	defer wts.mu.Unlock()

	var envs []wecomEnvelope
	for _, m := range wts.messages {
		var env wecomEnvelope
		if err := json.Unmarshal(m.data, &env); err != nil {
			t.Logf("[test-server] skipping non-envelope message: %s", string(m.data))
			continue
		}
		envs = append(envs, env)
	}
	return envs
}

// TestEndFlowEnsureReplyBeforeClose verifies the fix for the /end handler:
// when a reply is sent and then the channel is closed, the reply message
// MUST arrive at the server before the WebSocket close frame is processed.
//
// This simulates the exact flow in router.HandleWorkerMessage for "/end":
//  1. sendStreamReply (sleep message)
//  2. PrepareClose (cancel context)
//  3. Wait for readLoop done, then Close
func TestEndFlowEnsureReplyBeforeClose(t *testing.T) {
	wts := startWSTestServer(t)
	defer wts.close()

	ch := New("test-bot-id", "test-secret")
	// Override URL to connect to local test server.
	ch.wssURL = wts.url

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ch.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Wait for the auth handshake to complete (goroutine reads auth response).
	time.Sleep(100 * time.Millisecond)

	if !ch.IsConnected() {
		t.Fatal("channel should be connected after auth")
	}

	// Simulate the /end handler sequence:
	// Step 1: Send sleep reply (simulated via SendMessage with OriginalReqID).
	sleepText := "[💤] test-bot 我先睡啦 ZZZ"
	streamID := "stream_end_test_001"
	err := ch.SendMessage(context.Background(), "test-user", channel.MessageContent{
		Text:          sleepText,
		OriginalReqID: "simulated-req-id-001",
		StreamID:      streamID,
		StreamFinish:  true,
	})
	if err != nil {
		t.Fatalf("SendMessage (sleep reply) failed: %v", err)
	}

	// Step 2: PrepareClose (cancel context to stop heartbeat and readLoop).
	// readLoop is blocked in ReadMessage (90s deadline), so done won't close
	// until the WebSocket is actually closed. This is the real behavior.
	done := ch.PrepareClose()

	// Step 3: Wait for readLoop to exit (give it time to detect context
	// cancellation and process the close), OR timeout and close anyway.
	// This mirrors the exact pattern in router.HandleWorkerMessage's /end handler.
	select {
	case <-done:
		t.Log("readLoop exited on its own before timeout")
	case <-time.After(2 * time.Second):
		t.Log("PrepareClose timed out (expected: readLoop blocked in ReadMessage), closing anyway")
	}
	if err := ch.Close(); err != nil {
		t.Logf("Close returned error (may be expected): %v", err)
	}

	// Give the test server a moment to process all received data.
	time.Sleep(200 * time.Millisecond)

	// Verify: the server should have received the sleep reply.
	envs := wts.receivedEnvelopes(t)
	var foundSleep bool
	for _, env := range envs {
		if env.Cmd != "aibot_respond_msg" {
			continue
		}
		var body wecomRespondMsg
		if err := json.Unmarshal(env.Body, &body); err != nil {
			continue
		}
		if body.Stream.ID == streamID && body.Stream.Content == sleepText {
			foundSleep = true
			break
		}
	}

	if !foundSleep {
		t.Errorf("sleep reply was NOT received by the test server before close")
		t.Logf("received %d envelopes:", len(envs))
		for i, env := range envs {
			t.Logf("  [%d] cmd=%s body=%s", i, env.Cmd, string(env.Body))
		}
	} else {
		t.Log("sleep reply was successfully received by the test server before close")
	}
}

// TestSendReplyWithOriginalReqID verifies that SendMessage with OriginalReqID
// sends a proper aibot_respond_msg with stream format.
func TestSendReplyWithOriginalReqID(t *testing.T) {
	wts := startWSTestServer(t)
	defer wts.close()

	ch := New("bot-reply", "secret-reply")
	ch.wssURL = wts.url

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ch.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if !ch.IsConnected() {
		t.Fatal("channel should be connected")
	}

	// Send a reply to a simulated incoming message.
	err := ch.SendMessage(context.Background(), "user1", channel.MessageContent{
		Text:          "Hello, this is a stream reply",
		OriginalReqID: "incoming-req-001",
		StreamID:      "stream-test-001",
		StreamFinish:  false,
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Shutdown channel — PrepareClose cancels context, but readLoop is
	// blocked in ReadMessage, so we use a timeout before calling Close.
	done := ch.PrepareClose()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	ch.Close()
	time.Sleep(200 * time.Millisecond)

	// Verify the reply was received with correct format.
	envs := wts.receivedEnvelopes(t)
	var foundReply bool
	for _, env := range envs {
		if env.Cmd != "aibot_respond_msg" {
			continue
		}
		var body wecomRespondMsg
		if err := json.Unmarshal(env.Body, &body); err != nil {
			t.Logf("unmarshal body error: %v", err)
			continue
		}
		if body.MsgType != "stream" {
			t.Errorf("expected msgtype 'stream', got '%s'", body.MsgType)
		}
		if body.Stream.ID != "stream-test-001" {
			t.Errorf("expected stream ID 'stream-test-001', got '%s'", body.Stream.ID)
		}
		if body.Stream.Content != "Hello, this is a stream reply" {
			t.Errorf("unexpected content: %s", body.Stream.Content)
		}
		if body.Stream.Finish {
			t.Error("expected finish=false (not the final frame)")
		}
		foundReply = true
		break
	}
	if !foundReply {
		t.Error("aibot_respond_msg not found in received messages")
	}
}

// TestSendReplyWithoutOriginalReqID uses proactive send (aibot_send_msg).
func TestSendReplyWithoutOriginalReqID(t *testing.T) {
	wts := startWSTestServer(t)
	defer wts.close()

	ch := New("bot-proactive", "secret-proactive")
	ch.wssURL = wts.url

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ch.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if !ch.IsConnected() {
		t.Fatal("channel should be connected")
	}

	// Proactive message: no OriginalReqID.
	err := ch.SendMessage(context.Background(), "user1", channel.MessageContent{
		Text:   "Proactive markdown message",
		ChatID: "chat-001",
	})
	if err != nil {
		t.Fatalf("SendMessage proactive failed: %v", err)
	}

	done := ch.PrepareClose()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
	ch.Close()
	time.Sleep(200 * time.Millisecond)

	envs := wts.receivedEnvelopes(t)
	// The proactive message uses aibot_send_msg with markdown format.
	var foundSendMsg bool
	for _, env := range envs {
		if env.Cmd != "aibot_send_msg" {
			continue
		}
		var body wecomSendMsg
		if err := json.Unmarshal(env.Body, &body); err != nil {
			t.Logf("unmarshal body error: %v", err)
			continue
		}
		if body.MsgType != "markdown" {
			t.Errorf("expected msgtype 'markdown', got '%s'", body.MsgType)
		}
		if body.Markdown.Content != "Proactive markdown message" {
			t.Errorf("unexpected content: %s", body.Markdown.Content)
		}
		foundSendMsg = true
		break
	}
	if !foundSendMsg {
		t.Error("aibot_send_msg (proactive) not found in received messages")
	}
}

// TestEndFlowMessageToCloseTiming measures the time between writing the sleep
// message and closing the WebSocket. The current PrepareClose → done → Close
// flow closes within microseconds when readLoop is not blocked (i.e. when
// the message handler runs inside readLoop). This leaves insufficient time
// for the WeCom server to process the stream reply before the connection dies.
func TestEndFlowMessageToCloseTiming(t *testing.T) {
	wts := startWSTestServer(t)
	defer wts.close()

	ch := New("timing-bot", "timing-secret")
	ch.wssURL = wts.url

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ch.Connect(ctx); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	if !ch.IsConnected() {
		t.Fatal("channel should be connected")
	}

	// Measure: time from write to Close.
	writeTime := time.Now()

	// Step 1: Send sleep message.
	sleepText := "[💤] timing-bot 我先睡啦 ZZZ"
	err := ch.SendMessage(context.Background(), "test-user", channel.MessageContent{
		Text:          sleepText,
		OriginalReqID: "simulated-req-id-timing",
		StreamID:      "stream_timing_test",
		StreamFinish:  true,
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// Step 2: PrepareClose.
	done := ch.PrepareClose()

	// Step 3: Simulate what happens in readLoop: message handler returns,
	// readLoop checks ctx.Done() at top of loop → fires → returns → done closes.
	// In real life this is near-instant (<1ms). Force it here.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}

	// Step 4: Close — no extra delay.
	closeTime := time.Now()
	ch.Close()

	elapsed := closeTime.Sub(writeTime)
	t.Logf("Time from write to Close: %v", elapsed)

	// The message was written before close, so TCP ordering ensures delivery.
	// However, in production, the WeCom server may not process the stream reply
	// before the close frame arrives, especially if elapsed < server processing time.
	// A production-safe fix would ensure elapsed >= 2 seconds.
	if elapsed < 1*time.Second {
		t.Logf("NOTE: close happened <1s after write (elapsed=%v). "+
			"Production WeCom server may drop pending stream display.", elapsed)
	}
}

// TestVoiceMessageParsing verifies that a voice message callback is correctly
// parsed and dispatched through the message handler without panicking.
func TestVoiceMessageParsing(t *testing.T) {
	ch := New("voice-bot", "voice-secret")

	receivedCh := make(chan channel.Message, 1)
	ch.msgHandler = func(msg channel.Message) {
		receivedCh <- msg
	}

	// Simulate a WeCom voice message callback with transcribed text.
	voiceJSON := `{
		"cmd": "aibot_msg_callback",
		"headers": {"req_id": "voice-test-req-001"},
		"body": {
			"msgid": "msg-voice-001",
			"aibotid": "voice-bot",
			"chatid": "chat-voice-001",
			"chattype": "single",
			"from": {"userid": "user-voice-001"},
			"msgtype": "voice",
			"create_time": 1716200000,
			"voice": {"content": "这是语音转文字的内容"}
		}
	}`

	ch.handleMessage([]byte(voiceJSON))

	select {
	case msg := <-receivedCh:
		if msg.MsgType != "voice" {
			t.Errorf("expected MsgType 'voice', got '%s'", msg.MsgType)
		}
		if msg.Content != "这是语音转文字的内容" {
			t.Errorf("expected Content '这是语音转文字的内容', got '%s'", msg.Content)
		}
		if msg.UserID != "user-voice-001" {
			t.Errorf("expected UserID 'user-voice-001', got '%s'", msg.UserID)
		}
		if msg.ReqID != "voice-test-req-001" {
			t.Errorf("expected ReqID 'voice-test-req-001', got '%s'", msg.ReqID)
		}
		if msg.ChatID != "chat-voice-001" {
			t.Errorf("expected ChatID 'chat-voice-001', got '%s'", msg.ChatID)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("message handler was not called for voice message")
	}
}

// TestVoiceMessageEmptyText verifies that a voice message without transcription
// (empty text) does not panic.
func TestVoiceMessageEmptyText(t *testing.T) {
	ch := New("voice-empty-bot", "voice-empty-secret")

	receivedCh := make(chan channel.Message, 1)
	ch.msgHandler = func(msg channel.Message) {
		receivedCh <- msg
	}

	// Voice message with no text (e.g., not yet transcribed by WeCom).
	voiceJSON := `{
		"cmd": "aibot_msg_callback",
		"headers": {"req_id": "voice-empty-req"},
		"body": {
			"msgid": "msg-voice-empty",
			"aibotid": "voice-empty-bot",
			"chatid": "chat-empty",
			"chattype": "single",
			"from": {"userid": "user-empty"},
			"msgtype": "voice",
			"create_time": 1716200000,
			"voice": {"content": ""}
		}
	}`

	// Should not panic.
	ch.handleMessage([]byte(voiceJSON))

	select {
	case msg := <-receivedCh:
		if msg.Content != "" {
			t.Logf("voice message has non-empty content: %s", msg.Content)
		}
		t.Log("empty voice message parsed without panic")
	case <-time.After(1 * time.Second):
		t.Fatal("message handler was not called for empty voice message")
	}
}

// TestVoiceMessageMissingVoiceField verifies that a voice message without
// the voice field (malformed) does not panic.
func TestVoiceMessageMissingVoiceField(t *testing.T) {
	ch := New("voice-missing-bot", "voice-missing-secret")

	receivedCh := make(chan channel.Message, 1)
	ch.msgHandler = func(msg channel.Message) {
		receivedCh <- msg
	}

	// Voice msgtype but missing the voice field entirely.
	voiceJSON := `{
		"cmd": "aibot_msg_callback",
		"headers": {"req_id": "voice-missing-req"},
		"body": {
			"msgid": "msg-voice-missing",
			"aibotid": "voice-missing-bot",
			"chatid": "chat-missing",
			"chattype": "single",
			"from": {"userid": "user-missing"},
			"msgtype": "voice",
			"create_time": 1716200000
		}
	}`

	// Should not panic even with missing voice field.
	ch.handleMessage([]byte(voiceJSON))

	select {
	case msg := <-receivedCh:
		t.Logf("voice message with missing voice field parsed, content=%q", msg.Content)
	case <-time.After(1 * time.Second):
		t.Fatal("message handler was not called for voice message without voice field")
	}
}
