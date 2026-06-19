// Package channel defines the abstraction for IM platform connections.
// Each IM platform (WeCom, Telegram, Discord, etc.) implements this interface.
package channel

import (
	"context"
	"time"
)

// MessageType categorizes incoming messages.
type MessageType string

const (
	MsgTypeText     MessageType = "text"
	MsgTypeImage    MessageType = "image"
	MsgTypeFile     MessageType = "file"
	MsgTypeEvent    MessageType = "event"
	MsgTypeEnterChat MessageType = "enter_chat"
)

// Message represents an incoming message from the IM platform.
type Message struct {
	ID        string
	UserID    string
	BotID     string
	Content   string
	MsgType   MessageType
	Timestamp time.Time
	ReqID     string // original request ID from platform, used for reply routing
	ChatID    string // the chat ID for proactive messages
	Raw       []byte

	// Quote fields: populated when the user quotes a previous message.
	QuoteContent  string      // text content of the quoted message, empty if no quote
	QuotedMsgType MessageType // type of the quoted message (text, image, etc.)
}

// MessageContent represents content to send back to the user.
type MessageContent struct {
	Text          string
	ReplyToID     string
	OriginalReqID string // if set, replies via aibot_respond_msg; otherwise uses aibot_send_msg
	ChatID        string // required for proactive messages (aibot_send_msg)
	StreamID      string // if set, use this stream ID instead of generating a new one
	StreamFinish  bool   // used with StreamID; the final frame should set this to true
}

// MessageHandler is called when a user message arrives.
type MessageHandler func(msg Message)

// EventHandler is called when a platform event occurs (e.g. enter_chat).
type EventHandler func(event Message)

// Channel represents a connection to an IM platform for a single Bot identity.
// Each Bot (control bot or worker bot) gets its own Channel instance.
type Channel interface {
	// Connect establishes a persistent connection to the IM platform.
	Connect(ctx context.Context) error

	// SendMessage sends a message to a specific user.
	SendMessage(ctx context.Context, userID string, content MessageContent) error

	// SendImage sends an image file as a reply to the message identified by reqID.
	// reqID must be the request ID of a message the user sent to this bot
	// recently (WeCom: within 24h) — WeCom does NOT allow images to be pushed
	// proactively, only sent as a reply. Platforms that don't support images
	// should return an error.
	SendImage(ctx context.Context, userID, reqID, imagePath string) error

	// OnMessage registers a handler for incoming user messages.
	OnMessage(handler MessageHandler)

	// OnEvent registers a handler for platform events (enter_chat, etc.).
	OnEvent(handler EventHandler)

	// PrepareClose signals the channel to stop accepting new messages and
	// returns a channel that closes when all internal goroutines have exited.
	// The caller should call Close() after the returned channel closes to
	// tear down the underlying connection.
	PrepareClose() <-chan struct{}

	// Close gracefully closes the connection.
	Close() error

	// BotID returns the bot identifier on the IM platform.
	BotID() string

	// IsConnected returns whether the connection is currently active.
	IsConnected() bool

	// OnConnectionChange registers a callback that fires when the WebSocket
	// connects (after initial auth or reconnection) or disconnects.
	OnConnectionChange(handler func(connected bool))

	// StreamTimeout returns the platform's hard limit on stream reply duration.
	// Returns 0 if the platform has no limit.
	StreamTimeout() time.Duration
}
