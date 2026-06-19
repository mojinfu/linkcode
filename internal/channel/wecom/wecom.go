// Package wecom implements the channel.Channel interface for 企业微信 AI Bot.
//
// Protocol reference:
//
//	https://developer.work.weixin.qq.com/document/path/101463
//
// Connection: wss://openws.work.weixin.qq.com
// Auth: aibot_subscribe (bot_id + secret)
// Messages: aibot_msg_callback / aibot_event_callback
// Reply: aibot_respond_msg (supports streaming)
package wecom

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"linkcode/internal/channel"
)

const (
	DefaultWSSURL        = "wss://openws.work.weixin.qq.com"
	heartbeatInterval    = 30 * time.Second
	reconnectDelay       = 5 * time.Second
	maxReconnectAttempts = 10
	dialTimeout          = 10 * time.Second
	authTimeout          = 10 * time.Second
	ackTimeout           = 5 * time.Second
		// streamTimeout is the hard limit WeCom imposes on a single stream reply.
		// From the moment the user sends a message, WeCom will keep the stream
		// alive for at most 10 minutes before forcibly ending it.
		// Ref: https://developer.work.weixin.qq.com/document/path/100719
		streamTimeout = 10 * time.Minute

	// readWait is the read deadline for ReadMessage.
	// If no data (heartbeat response, message, etc.) arrives within this window,
	// the connection is considered dead and will be reconnected.
	// Set to 3x heartbeatInterval to tolerate transient network delays.
	readWait = 90 * time.Second
)

// Channel implements channel.Channel for 企业微信.
type Channel struct {
	wssURL string
	botID  string
	secret string

	conn      *websocket.Conn
	connMu    sync.Mutex
	connected bool

	msgHandler   channel.MessageHandler
	eventHandler channel.EventHandler

	connChangeHandler func(connected bool)

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	reqIDSeq int64

	// pendingAcks tracks proactive messages waiting for server ack.
	ackMu       sync.Mutex
	pendingAcks map[string]chan error // req_id -> ack result

	// uploadResps stores response bodies for media upload commands.
	uploadMu    sync.RWMutex
	uploadResps map[string]json.RawMessage // req_id -> response body

	// streamErrReqID tracks req_ids that received WeCom error 846608
	// (stream message update expired). The next sendReply with a matching
	// OriginalReqID will fail fast so the router can fall back to proactive.
	streamErrMu    sync.Mutex
	streamErrReqID string
}

// New creates a new WeCom Channel.
func New(botID, secret string) *Channel {
	return &Channel{
		wssURL:      DefaultWSSURL,
		botID:       botID,
		secret:      secret,
		pendingAcks: make(map[string]chan error),
		uploadResps: make(map[string]json.RawMessage),
	}
}

// BotID returns the bot identifier.
func (c *Channel) BotID() string { return c.botID }

// StreamTimeout returns the WeCom hard limit on stream reply duration.
func (c *Channel) StreamTimeout() time.Duration { return streamTimeout }

// IsConnected returns whether the WebSocket is currently active.
func (c *Channel) IsConnected() bool {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.connected
}

// OnMessage registers a handler for incoming user messages.
func (c *Channel) OnMessage(handler channel.MessageHandler) {
	c.msgHandler = handler
}

// OnEvent registers a handler for platform events.
func (c *Channel) OnEvent(handler channel.EventHandler) {
	c.eventHandler = handler
}

// OnConnectionChange registers a callback for WebSocket connection state changes.
func (c *Channel) OnConnectionChange(handler func(connected bool)) {
	c.connChangeHandler = handler
}

// Connect establishes a WebSocket connection and authenticates.
func (c *Channel) Connect(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})

	if err := c.connect(); err != nil {
		return err
	}

	go c.heartbeatLoop()
	go c.readLoop()

	return nil
}

// PrepareClose signals the channel to stop accepting new messages.
// It cancels the context (stopping heartbeat and readLoop) and returns a
// channel that closes when readLoop has exited. After the channel closes,
// the caller should call Close() to tear down the WebSocket.
func (c *Channel) PrepareClose() <-chan struct{} {
	if c.cancel != nil {
		c.cancel()
	}
	return c.done
}

// Close gracefully closes the WebSocket connection.
func (c *Channel) Close() error {
	if c.cancel != nil {
		c.cancel()
	}

	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.connected = false

	if c.done != nil {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
	return nil
}

// SendMessage sends a text message to a user.
// If content.OriginalReqID is set, it replies via aibot_respond_msg (tied to an incoming message).
// Otherwise, it sends proactively via aibot_send_msg and waits for the server ack.
func (c *Channel) SendMessage(ctx context.Context, userID string, content channel.MessageContent) error {
	if content.OriginalReqID != "" {
		return c.sendReply(content)
	}
	return c.sendProactive(userID, content)
}

func (c *Channel) sendReply(content channel.MessageContent) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if !c.connected || c.conn == nil {
		return fmt.Errorf("wecom: not connected")
	}

	// If a previous stream update for this req_id received 846608, fail fast
	// so the router can switch to proactive (non-stream) delivery.
	c.streamErrMu.Lock()
	if c.streamErrReqID == content.OriginalReqID && c.streamErrReqID != "" {
		c.streamErrReqID = ""
		c.streamErrMu.Unlock()
		return fmt.Errorf("wecom: stream message update expired (846608)")
	}
	c.streamErrMu.Unlock()

	streamID := content.StreamID
	if streamID == "" {
		streamID = fmt.Sprintf("stream_%d", time.Now().UnixNano())
	}
	finish := true
	if content.StreamID != "" {
		finish = content.StreamFinish
	}

	resp := wecomEnvelope{
		Cmd: "aibot_respond_msg",
		Headers: wecomHeaders{
			ReqID: content.OriginalReqID,
		},
		Body: json.RawMessage(mustMarshal(wecomRespondMsg{
			MsgType: "stream",
			Stream: wecomStream{
				ID:      streamID,
				Finish:  finish,
				Content: content.Text,
			},
		})),
	}
	if err := c.conn.WriteJSON(resp); err != nil {
		c.markBrokenLocked()
		return err
	}
	return nil
}

func (c *Channel) sendProactive(userID string, content channel.MessageContent) error {
	c.connMu.Lock()
	if !c.connected || c.conn == nil {
		c.connMu.Unlock()
		return fmt.Errorf("wecom: not connected")
	}

	chatID := content.ChatID
	if chatID == "" {
		chatID = userID
	}

	reqID := c.nextReqID()

	// aibot_send_msg only supports markdown/rich_text, not stream.
	body := []byte(mustMarshal(wecomSendMsg{
		ChatID:  chatID,
		MsgType: "markdown",
		Markdown: wecomMarkdown{
			Content: content.Text,
		},
	}))

	resp := wecomEnvelope{
		Cmd:     "aibot_send_msg",
		Headers: wecomHeaders{
			ReqID: reqID,
		},
		Body: json.RawMessage(body),
	}

	// Register ack waiter before sending.
	ackCh := make(chan error, 1)
	c.ackMu.Lock()
	c.pendingAcks[reqID] = ackCh
	c.ackMu.Unlock()

	if err := c.conn.WriteJSON(resp); err != nil {
		c.markBrokenLocked()
		c.connMu.Unlock()
		c.ackMu.Lock()
		delete(c.pendingAcks, reqID)
		c.ackMu.Unlock()
		return fmt.Errorf("wecom: write proactive: %w", err)
	}
	c.connMu.Unlock()

	// Wait for ack.
	select {
	case err := <-ackCh:
		return err
	case <-time.After(ackTimeout):
		c.ackMu.Lock()
		delete(c.pendingAcks, reqID)
		c.ackMu.Unlock()
		return fmt.Errorf("wecom: ack timeout for proactive message %s", reqID)
	}
}

// SendImage implements channel.Channel. It uploads the image via the chunked
// temporary-material API over the long connection, then replies with it via
// aibot_respond_msg. reqID must be a recent incoming message's request ID —
// WeCom does NOT allow images to be pushed proactively, only sent as a reply.
// Ref: https://developer.work.weixin.qq.com/document/path/101463 (上传临时素材, 图片消息)
func (c *Channel) SendImage(ctx context.Context, userID, reqID, imagePath string) error {
	if reqID == "" {
		return fmt.Errorf("wecom: SendImage requires reqID of a user message to reply to")
	}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return fmt.Errorf("read image %s: %w", imagePath, err)
	}
	if err := validateImage(imagePath, data); err != nil {
		return err
	}
	mediaID, err := c.uploadMedia(filepath.Base(imagePath), data)
	if err != nil {
		return err
	}
	return c.sendImageReply(reqID, mediaID)
}

// uploadMedia uploads image data via the chunked temporary-material API and
// returns the resulting media_id (valid for 3 days). The whole exchange runs
// over the existing WebSocket long connection — no access_token needed.
func (c *Channel) uploadMedia(filename string, data []byte) (string, error) {
	const chunkSize = 512 * 1024 // per WeCom: each chunk ≤512KB BEFORE base64
	total := len(data)
	totalChunks := total / chunkSize
	if total%chunkSize != 0 {
		totalChunks++
	}
	md5sum := fmt.Sprintf("%x", md5.Sum(data))

	// 1. init -> upload_id
	initResp, err := c.sendCmdWait(c.nextReqID(), "aibot_upload_media_init", map[string]interface{}{
		"type": "image", "filename": filename,
		"total_size": total, "total_chunks": totalChunks, "md5": md5sum,
	})
	if err != nil {
		return "", fmt.Errorf("upload init: %w", err)
	}
	var initRes struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(initResp, &initRes); err != nil || initRes.UploadID == "" {
		return "", fmt.Errorf("upload init: bad response %s: %v", string(initResp), err)
	}
	uploadID := initRes.UploadID

	// 2. chunks (index from 0; may be uploaded out of order, idempotent)
	for i := 0; i < totalChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > total {
			end = total
		}
		if _, err := c.sendCmdWait(c.nextReqID(), "aibot_upload_media_chunk", map[string]interface{}{
			"upload_id":   uploadID,
			"chunk_index": i,
			"base64_data": base64.StdEncoding.EncodeToString(data[start:end]),
		}); err != nil {
			return "", fmt.Errorf("upload chunk %d: %w", i, err)
		}
	}

	// 3. finish -> media_id
	finResp, err := c.sendCmdWait(c.nextReqID(), "aibot_upload_media_finish", map[string]interface{}{
		"upload_id": uploadID,
	})
	if err != nil {
		return "", fmt.Errorf("upload finish: %w", err)
	}
	var finRes struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(finResp, &finRes); err != nil || finRes.MediaID == "" {
		return "", fmt.Errorf("upload finish: bad response %s: %v", string(finResp), err)
	}
	return finRes.MediaID, nil
}

// sendImageReply sends an aibot_respond_msg with msgtype=image, replying to the
// message identified by reqID. Like sendReply, it does not wait for an ack.
func (c *Channel) sendImageReply(reqID, mediaID string) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	if !c.connected || c.conn == nil {
		return fmt.Errorf("wecom: not connected")
	}
	resp := wecomEnvelope{
		Cmd:     "aibot_respond_msg",
		Headers: wecomHeaders{ReqID: reqID},
		Body: json.RawMessage(mustMarshal(wecomRespondMsg{
			MsgType: "image",
			Image:   &wecomSendImage{MediaID: mediaID},
		})),
	}
	if err := c.conn.WriteJSON(resp); err != nil {
		c.markBrokenLocked()
		return err
	}
	return nil
}

// sendCmdWait sends a command that expects a response body (e.g. media upload)
// and blocks until the ack arrives, then returns the response body. It reuses
// the pendingAcks mechanism; the body is pulled from uploadResps, where
// dispatchAck stores every response, and cleaned up after reading.
func (c *Channel) sendCmdWait(reqID, cmd string, body interface{}) (json.RawMessage, error) {
	c.connMu.Lock()
	if !c.connected || c.conn == nil {
		c.connMu.Unlock()
		return nil, fmt.Errorf("wecom: not connected")
	}

	ackCh := make(chan error, 1)
	c.ackMu.Lock()
	c.pendingAcks[reqID] = ackCh
	c.ackMu.Unlock()

	env := wecomEnvelope{
		Cmd:     cmd,
		Headers: wecomHeaders{ReqID: reqID},
		Body:    json.RawMessage(mustMarshal(body)),
	}
	if err := c.conn.WriteJSON(env); err != nil {
		c.markBrokenLocked()
		c.connMu.Unlock()
		c.ackMu.Lock()
		delete(c.pendingAcks, reqID)
		c.ackMu.Unlock()
		return nil, fmt.Errorf("wecom: write %s: %w", cmd, err)
	}
	c.connMu.Unlock()

	select {
	case err := <-ackCh:
		if err != nil {
			return nil, err
		}
		c.uploadMu.Lock()
		respBody := c.uploadResps[reqID]
		delete(c.uploadResps, reqID)
		c.uploadMu.Unlock()
		return respBody, nil
	case <-time.After(ackTimeout):
		c.ackMu.Lock()
		delete(c.pendingAcks, reqID)
		c.ackMu.Unlock()
		return nil, fmt.Errorf("wecom: ack timeout for %s %s", cmd, reqID)
	}
}

// validateImage checks the file extension and size against WeCom limits.
func validateImage(path string, data []byte) error {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif":
	default:
		return fmt.Errorf("wecom: unsupported image type %q (png/jpg/jpeg/gif only)", ext)
	}
	if len(data) > 10*1024*1024 {
		return fmt.Errorf("wecom: image too large: %d bytes (max 10MB)", len(data))
	}
	return nil
}

func (c *Channel) dispatchAck(env wecomEnvelope) {
	reqID := env.Headers.ReqID

	// Track 846608 (stream message update expired) so sendReply can fail fast.
	if env.ErrCode == 846608 {
		c.streamErrMu.Lock()
		c.streamErrReqID = reqID
		c.streamErrMu.Unlock()
	}

	c.uploadMu.Lock()
	c.uploadResps[reqID] = env.Body
	c.uploadMu.Unlock()

	c.ackMu.Lock()
	ch, ok := c.pendingAcks[reqID]
	if ok {
		delete(c.pendingAcks, reqID)
	}
	c.ackMu.Unlock()

	if !ok {
		return
	}

	if env.ErrCode != 0 {
		ch <- fmt.Errorf("wecom: errcode=%d errmsg=%s", env.ErrCode, env.ErrMsg)
	} else {
		ch <- nil
	}
}

// connect performs a single WebSocket connection + subscription.
func (c *Channel) connect() error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	dialer := websocket.Dialer{
		HandshakeTimeout: dialTimeout,
	}

	log.Printf("[wecom] bot %s: dialing %s...", c.botID, c.wssURL)
	conn, _, err := dialer.Dial(c.wssURL, nil)
	if err != nil {
		return fmt.Errorf("wecom: dial %s: %w", c.wssURL, err)
	}

	// Send subscription auth.
	reqID := c.nextReqID()
	subReq := wecomEnvelope{
		Cmd: "aibot_subscribe",
		Headers: wecomHeaders{
			ReqID: reqID,
		},
		Body: json.RawMessage(mustMarshal(wecomSubscribe{
			BotID:  c.botID,
			Secret: c.secret,
		})),
	}

	if err := conn.WriteJSON(subReq); err != nil {
		conn.Close()
		return fmt.Errorf("wecom: send subscribe: %w", err)
	}

	// Wait for subscription response.
	conn.SetReadDeadline(time.Now().Add(authTimeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		return fmt.Errorf("wecom: read subscribe response: %w", err)
	}

	var resp wecomEnvelope
	if err := json.Unmarshal(msg, &resp); err != nil {
		conn.Close()
		return fmt.Errorf("wecom: parse subscribe response: %w", err)
	}

	if resp.ErrCode != 0 {
		conn.Close()
		return fmt.Errorf("wecom: subscribe failed: errcode=%d errmsg=%s (raw: %s)", resp.ErrCode, resp.ErrMsg, string(msg))
	}

	log.Printf("[wecom] bot %s: authenticated successfully", c.botID)

	// Set initial read deadline. If the JSON heartbeat response doesn't
	// arrive within readWait, the connection is considered dead and will
	// be reconnected by readLoop.
	conn.SetReadDeadline(time.Now().Add(readWait))

	c.conn = conn
	c.connected = true

	if c.connChangeHandler != nil {
		go c.connChangeHandler(true)
	}

	return nil
}

func (c *Channel) heartbeatLoop() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.sendPing()
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Channel) sendPing() {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if !c.connected || c.conn == nil {
		return
	}

	reqID := c.nextReqID()
	ping := wecomEnvelope{
		Cmd: "ping",
		Headers: wecomHeaders{
			ReqID: reqID,
		},
	}
	if err := c.conn.WriteJSON(ping); err != nil {
		log.Printf("[wecom] bot %s: heartbeat write error: %v", c.botID, err)
		c.markBrokenLocked()
	}
}

// markBrokenLocked closes the current connection and marks it as disconnected.
// Must be called with connMu held. The readLoop will pick up conn==nil and reconnect.
func (c *Channel) markBrokenLocked() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	wasConnected := c.connected
	c.connected = false
	// Notify outside the lock to avoid reentrancy issues.
	if wasConnected && c.connChangeHandler != nil {
		go c.connChangeHandler(false)
	}
}

func (c *Channel) readLoop() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[wecom] bot %s: readLoop panic recovered: %v", c.botID, r)
		}
		select {
		case <-c.done:
			// Already closed.
		default:
			close(c.done)
		}
	}()

	attempt := 0

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		c.connMu.Lock()
		conn := c.conn
		c.connMu.Unlock()

		if conn == nil {
			if !c.reconnect(attempt) {
				return
			}
			attempt = 0
			continue
		}

		// Set read deadline so a dead connection is detected within readWait.
		conn.SetReadDeadline(time.Now().Add(readWait))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[wecom] bot %s: read error: %v", c.botID, err)
			c.connMu.Lock()
			if c.conn != nil {
				c.conn.Close()
				c.conn = nil
			}
			wasConnected := c.connected
			c.connected = false
			c.connMu.Unlock()
			if wasConnected && c.connChangeHandler != nil {
				go c.connChangeHandler(false)
			}

			if !c.reconnect(attempt) {
				return
			}
			attempt++
			continue
		}
		attempt = 0
		go c.handleMessage(msg)
	}
}

func (c *Channel) reconnect(attempt int) bool {
	if attempt > maxReconnectAttempts {
		log.Printf("[wecom] bot %s: max reconnect attempts reached", c.botID)
		return false
	}

	delay := reconnectDelay * time.Duration(attempt)
	log.Printf("[wecom] bot %s: reconnecting in %v (attempt %d)", c.botID, delay, attempt)

	select {
	case <-time.After(delay):
	case <-c.ctx.Done():
		return false
	}

	if err := c.connect(); err != nil {
		log.Printf("[wecom] bot %s: reconnect failed: %v", c.botID, err)
		return c.reconnect(attempt + 1)
	}

	log.Printf("[wecom] bot %s: reconnected successfully", c.botID)
	return true
}

func (c *Channel) handleMessage(raw []byte) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[wecom] bot %s: handleMessage panic recovered: %v", c.botID, r)
		}
	}()
	log.Printf("[wecom] bot %s: recv %d bytes, raw: %s", c.botID, len(raw), string(raw))

	var env wecomEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		log.Printf("[wecom] bot %s: parse envelope: %v", c.botID, err)
		return
	}

	switch env.Cmd {
	case "pong":
		return

	case "aibot_msg_callback":
		c.handleMsgCallback(env)

	case "aibot_event_callback":
		c.handleEventCallback(env)

	default:
		// Non-cmd frames: auth responses, heartbeat acks, send_msg acks.
		// Route to pending ack waiter if one is waiting for this req_id.
		c.dispatchAck(env)

		if env.ErrCode != 0 {
			log.Printf("[wecom] bot %s: error frame: errcode=%d errmsg=%s", c.botID, env.ErrCode, env.ErrMsg)
		}
	}
}

func (c *Channel) handleMsgCallback(env wecomEnvelope) {
	var body wecomMsgCallback
	if err := json.Unmarshal(env.Body, &body); err != nil {
		log.Printf("[wecom] bot %s: parse msg body: %v", c.botID, err)
		return
	}

	msg := channel.Message{
		ID:        body.MsgID,
		UserID:    body.From.UserID,
		BotID:     body.AIBotID,
		MsgType:   channel.MessageType(body.MsgType),
		Timestamp: time.Unix(body.CreateTime, 0),
		ReqID:     env.Headers.ReqID,
		ChatID:    body.ChatID,
		Raw:       env.Body,
	}

	switch body.MsgType {
	case "text":
		msg.Content = body.Text.Content
	case "mixed":
		for _, item := range body.Mixed.MsgItem {
			if item.MsgType == "text" {
				msg.Content += item.Text.Content
			}
		}
	case "image":
		msg.Content = "[图片]"
	case "voice":
		msg.Content = body.Voice.Content
	case "file":
		msg.Content = "[文件]"
	}

	if body.Quote != nil {
		msg.QuotedMsgType = channel.MessageType(body.Quote.MsgType)
		msg.QuoteContent = extractQuoteContent(body.Quote)
	}

	if c.msgHandler != nil {
		c.msgHandler(msg)
	}
}

func (c *Channel) handleEventCallback(env wecomEnvelope) {
	var body wecomEventCallback
	if err := json.Unmarshal(env.Body, &body); err != nil {
		log.Printf("[wecom] bot %s: parse event body: %v", c.botID, err)
		return
	}

	msg := channel.Message{
		ID:        body.MsgID,
		UserID:    body.From.UserID,
		BotID:     body.AIBotID,
		MsgType:   channel.MsgTypeEvent,
		Timestamp: time.Unix(body.CreateTime, 0),
		ReqID:     env.Headers.ReqID,
		ChatID:    body.ChatID,
		Raw:       env.Body,
	}

	if body.Event.EventType == "enter_chat" {
		msg.MsgType = channel.MsgTypeEnterChat
		msg.Content = "enter_chat"
	}

	if c.eventHandler != nil {
		c.eventHandler(msg)
	}
}

func (c *Channel) nextReqID() string {
	seq := atomic.AddInt64(&c.reqIDSeq, 1)
	return fmt.Sprintf("linkcode_%d_%d", time.Now().UnixNano(), seq)
}

// extractQuoteContent extracts human-readable text from a quoted message.
func extractQuoteContent(q *wecomQuote) string {
	switch q.MsgType {
	case "text":
		return q.Text.Content
	case "mixed":
		var parts []string
		for _, item := range q.Mixed.MsgItem {
			if item.MsgType == "text" {
				parts = append(parts, item.Text.Content)
			}
		}
		return strings.Join(parts, "")
	case "voice":
		return q.Voice.Content
	default:
		return "" // image/file have no text
	}
}

func mustMarshal(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// --- JSON wire types (official WeCom AI Bot protocol) ---

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

type wecomMsgCallback struct {
	MsgID      string     `json:"msgid"`
	AIBotID    string     `json:"aibotid"`
	ChatID     string     `json:"chatid"`
	ChatType   string     `json:"chattype"`
	From       wecomUser  `json:"from"`
	MsgType    string     `json:"msgtype"`
	CreateTime int64      `json:"create_time"`
	Text       wecomText  `json:"text,omitempty"`
	Image      wecomImage `json:"image,omitempty"`
	Voice      wecomVoice `json:"voice,omitempty"`
	Mixed      wecomMixed  `json:"mixed,omitempty"`
	File       wecomFile   `json:"file,omitempty"`
	Quote      *wecomQuote `json:"quote,omitempty"`
}

type wecomEventCallback struct {
	MsgID      string     `json:"msgid"`
	AIBotID    string     `json:"aibotid"`
	ChatID     string     `json:"chatid"`
	ChatType   string     `json:"chattype"`
	From       wecomUser  `json:"from"`
	MsgType    string     `json:"msgtype"`
	CreateTime int64      `json:"create_time"`
	Event      wecomEvent `json:"event"`
}

type wecomUser struct {
	UserID string `json:"userid"`
	Name   string `json:"name"`
}

type wecomText struct {
	Content string `json:"content"`
}

type wecomImage struct {
	URL string `json:"url"`
}

type wecomVoice struct {
	Content string `json:"content"`
}

type wecomMixed struct {
	MsgItem []wecomMixedItem `json:"msg_item"`
}

type wecomMixedItem struct {
	MsgType string    `json:"msgtype"`
	Text    wecomText `json:"text,omitempty"`
	Image   wecomImage `json:"image,omitempty"`
}

type wecomFile struct {
	FileName string `json:"file_name"`
	URL      string `json:"url"`
}

// wecomQuote represents a quoted message inside a WeCom callback.
type wecomQuote struct {
	MsgType string    `json:"msgtype"`
	Text    wecomText  `json:"text,omitempty"`
	Image   wecomImage `json:"image,omitempty"`
	Voice   wecomVoice `json:"voice,omitempty"`
	Mixed   wecomMixed `json:"mixed,omitempty"`
}

type wecomEvent struct {
	EventType string `json:"eventtype"`
}

type wecomRespondMsg struct {
	MsgType string          `json:"msgtype"`
	Stream  wecomStream     `json:"stream,omitempty"`
	Image   *wecomSendImage `json:"image,omitempty"`
}

// wecomSendImage is the image payload for an aibot_respond_msg reply.
// media_id is obtained from the chunked upload (aibot_upload_media_*).
type wecomSendImage struct {
	MediaID string `json:"media_id"`
}

type wecomStream struct {
	ID      string `json:"id"`
	Finish  bool   `json:"finish"`
	Content string `json:"content"`
}

type wecomSendMsg struct {
	ChatID   string        `json:"chatid"`
	MsgType  string        `json:"msgtype"`
	Markdown wecomMarkdown `json:"markdown,omitempty"`
	Stream   *wecomStream  `json:"stream,omitempty"`
}

type wecomMarkdown struct {
	Content string `json:"content"`
}
