// Package router handles bidirectional message forwarding between
// IM channels (user <-> bot) and agent processes.
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"linkcode/internal/agent"
	"linkcode/internal/botpool"
	"linkcode/internal/channel"
	"linkcode/internal/gateway"
	"linkcode/internal/session"
)

const (
	// spinnerFrames defines the 8-frame Braille spinner sequence.
	spinnerFrames = "⣷⣯⣟⡿⢿⣻⣽⣾"
	// spinnerMinInterval is the fastest spin rate, used when tokens are flowing.
	spinnerMinInterval = 200 * time.Millisecond
	// spinnerMaxInterval is the slowest spin rate, used when idle.
	spinnerMaxInterval = 1 * time.Second
	// spinnerDecelStep is how much the interval grows per tick without a chunk.
	spinnerDecelStep = 200 * time.Millisecond
)

// streamCache holds the important parts of a streaming reply so that
// on stream failure the content can be resent as a regular (non-stream) message.
type streamCache struct {
	textBuf      strings.Builder
	fullResponse string
	question     *agent.Question
}

// Router forwards messages between users and agent processes.
type Router struct {
	sessionMgr  *session.Manager
	botPool     *botpool.Pool
	agentRunner agent.Runner
	gw          *gateway.Gateway
	statusMgr   *StatusManager

	mu                  sync.Mutex
	pendingQuestions    map[int64]*agent.Question // sessionID -> pending question
	interruptedSessions map[int64]bool            // sessionID -> process was /stop'd
}

// New creates a new Router.
func New(sessMgr *session.Manager, pool *botpool.Pool, runner agent.Runner, gw *gateway.Gateway, statusMgr *StatusManager) *Router {
	return &Router{
		sessionMgr:         sessMgr,
		botPool:            pool,
		agentRunner:        runner,
		gw:                 gw,
		statusMgr:          statusMgr,
		pendingQuestions:   make(map[int64]*agent.Question),
		interruptedSessions: make(map[int64]bool),
	}
}

// HandleWorkerEvent handles events from worker bots (enter_chat, etc.).
func (r *Router) HandleWorkerEvent(msg channel.Message) {
	if msg.MsgType != channel.MsgTypeEnterChat {
		return
	}

	sess, err := r.sessionMgr.GetByPlatformBotID(msg.BotID)
	if err != nil || sess == nil {
		return
	}

	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok {
		return
	}
	ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
		Text: fmt.Sprintf("你好，我是你的 %s「%s」，有什么任务要交给我吗？", sess.AgentType, sess.Name),
	})
}

// HandleWorkerMessage is the dispatcher. It looks up the session from the bot ID,
// classifies the message, and routes it to the appropriate handler.
func (r *Router) HandleWorkerMessage(msg channel.Message) {
	log.Printf("[router] msg from %s: %s", msg.UserID, msg.Content)

	sess, err := r.sessionMgr.GetByPlatformBotID(msg.BotID)
	if err != nil {
		log.Printf("[router] session not found for bot %s: %v", msg.BotID, err)
		r.sendReply(msg, "找不到对应的 Session，请通过总控 Bot 重新创建。")
		return
	}

	switch classifyMessage(msg) {
	case msgKindVoice:
		r.handleVoice(msg, sess)
	case msgKindCommand:
		r.handleCommand(msg, sess)
	default:
		r.handleLLM(msg, sess)
	}
}

// message classification
type msgKind int

const (
	msgKindText    msgKind = iota
	msgKindCommand
	msgKindVoice
)

// classifyMessage returns the type of an incoming message.
func classifyMessage(msg channel.Message) msgKind {
	if msg.MsgType == "voice" {
		return msgKindVoice
	}
	if parseCommand(msg.Content) != "" {
		return msgKindCommand
	}
	return msgKindText
}

// stream JSON helpers

type streamJSONUserMsg struct {
	Type    string            `json:"type"`
	Message streamJSONMsgBody `json:"message"`
}

type streamJSONMsgBody struct {
	Role    string                 `json:"role"`
	Content []streamJSONContentPart `json:"content"`
}

type streamJSONContentPart struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ToolUseID  string `json:"tool_use_id,omitempty"`
	Content    string `json:"content,omitempty"`
}

// buildTextInput formats a plain text message as a stream-json line.
func buildTextInput(text string) string {
	msg := streamJSONUserMsg{
		Type: "user",
		Message: streamJSONMsgBody{
			Role: "user",
			Content: []streamJSONContentPart{
				{Type: "text", Text: text},
			},
		},
	}
	b, _ := json.Marshal(msg)
	return string(b)
}

// formatAnswerText converts a user's raw answer into a descriptive text for Claude.
// It parses numeric answers and maps them to option labels when possible.
func formatAnswerText(q *agent.Question, rawAnswer string) string {
	answer := strings.TrimSpace(rawAnswer)

	var sb strings.Builder
	sb.WriteString("[用户回答]\n")

	for i, qi := range q.Questions {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(fmt.Sprintf("问题: %s\n", qi.Question))
		resolved := resolveAnswer(qi, answer)
		sb.WriteString(fmt.Sprintf("回答: %s", resolved))
	}

	return sb.String()
}

// resolveAnswer maps a user input to an option label.
// If the input is a number, it maps to the corresponding option.
// If it's comma-separated numbers (multi-select), it maps each one.
// Otherwise returns the input as-is.
func resolveAnswer(qi agent.QuestionItem, input string) string {
	parts := strings.Split(input, ",")
	var labels []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if n, err := strconv.Atoi(p); err == nil && n >= 1 && n <= len(qi.Options) {
			labels = append(labels, qi.Options[n-1].Label)
		} else {
			labels = append(labels, p)
		}
	}
	return strings.Join(labels, ", ")
}

