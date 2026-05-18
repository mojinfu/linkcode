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
	"encoding/json"
	"fmt"
	"log"
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

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	reqIDSeq int64

	// pendingAcks tracks proactive messages waiting for server ack.
	ackMu       sync.Mutex
	pendingAcks map[string]chan error // req_id -> ack result
}

// New creates a new WeCom Channel.
func New(botID, secret string) *Channel {
	return &Channel{
		wssURL:      DefaultWSSURL,
		botID:       botID,
		secret:      secret,
		pendingAcks: make(map[string]chan error),
	}
}

// BotID returns the bot identifier.
func (c *Channel) BotID() string { return c.botID }

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
	resp := wecomEnvelope{
		Cmd: "aibot_send_msg",
		Headers: wecomHeaders{
			ReqID: reqID,
		},
		Body: json.RawMessage(mustMarshal(wecomSendMsg{
			ChatID:  chatID,
			MsgType: "markdown",
			Markdown: wecomMarkdown{
				Content: content.Text,
			},
		})),
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

func (c *Channel) dispatchAck(env wecomEnvelope) {
	reqID := env.Headers.ReqID
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
	c.connected = false
}

func (c *Channel) readLoop() {
	defer func() {
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
			c.connected = false
			c.conn = nil
			c.connMu.Unlock()

			if !c.reconnect(attempt) {
				return
			}
			attempt++
			continue
		}
		attempt = 0
		c.handleMessage(msg)
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
		msg.Content = body.Voice.Text
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
		return q.Voice.Text
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
	Text string `json:"text"`
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
	MsgType string      `json:"msgtype"`
	Stream  wecomStream `json:"stream,omitempty"`
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
}

type wecomMarkdown struct {
	Content string `json:"content"`
}
