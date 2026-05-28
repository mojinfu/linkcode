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
	pendingQuestions    map[int64]*agent.Question   // sessionID -> pending question
	interruptedSessions map[int64]bool              // sessionID -> process was /stop'd
	pendingMessages     map[int64][]channel.Message // sessionID -> queued messages while process is busy
	thinkingStartedAt   map[int64]time.Time         // sessionID -> when the current thinking started
}

// New creates a new Router.
func New(sessMgr *session.Manager, pool *botpool.Pool, runner agent.Runner, gw *gateway.Gateway, statusMgr *StatusManager) *Router {
	return &Router{
		sessionMgr:         sessMgr,
		botPool:            pool,
		agentRunner:        runner,
		gw:                 gw,
		statusMgr:          statusMgr,
		pendingQuestions:     make(map[int64]*agent.Question),
		interruptedSessions:  make(map[int64]bool),
		pendingMessages:      make(map[int64][]channel.Message),
		thinkingStartedAt:    make(map[int64]time.Time),
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

	bot, _ := r.botPool.GetByID(sess.BoundBotID)
	botWD := ""
	if bot != nil {
		botWD = bot.WorkDir
	}
	wd, _ := r.botPool.ResolveWorkDir(botWD)

	ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
		Text: fmt.Sprintf("你好，我是你的 %s「%s」\n工作目录：%s\n有什么任务要交给我吗？\n发送 /help 查看可用命令。", displayAgentType(sess.AgentType), sess.Name, wd),
	})
}

// HandleWorkerMessage is the dispatcher. It looks up the session from the bot ID,
// classifies the message, and routes it to the appropriate handler.
func (r *Router) HandleWorkerMessage(msg channel.Message) {
	log.Printf("[router] msg from %s: %s", msg.UserID, msg.Content)

	// /help works without a session lookup.
	if parseCommand(msg.Content) == "/help" {
		r.handleHelp(msg)
		return
	}

	sess, err := r.sessionMgr.GetByPlatformBotID(msg.BotID)
	if err != nil {
		log.Printf("[router] session not found for bot %s: %v", msg.BotID, err)
		r.sendReply(msg, "找不到对应的 Session，请联系管理员。")
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

// sendQuestionMenu formats a structured question as an IM menu and sends it.
func (r *Router) sendQuestionMenu(msg channel.Message, q *agent.Question) {
	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok {
		return
	}

	for i, qi := range q.Questions {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("📋 %s\n\n", qi.Question))
		for j, opt := range qi.Options {
			sb.WriteString(fmt.Sprintf("%d. %s", j+1, opt.Label))
			if opt.Description != "" {
				sb.WriteString(fmt.Sprintf(" - %s", opt.Description))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n回复数字选择")
		if qi.MultiSelect {
			sb.WriteString("（可多选，用逗号分隔）")
		}
		sb.WriteString("，或引用此消息回复。")

		ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
			Text:          sb.String(),
			ReplyToID:     msg.ID,
			OriginalReqID: msg.ReqID,
			ChatID:        msg.ChatID,
		})

		if len(q.Questions) > 1 && i > 0 {
			_ = i // TODO: multi-question support
		}
	}
}

// buildQuotePrefix formats the first n characters of the user's original message
// as a markdown blockquote prefix, so the fallback proactive message gives context
// about which user message it is responding to.
func buildQuotePrefix(userMsg string, n int) string {
	runes := []rune(userMsg)
	if len(runes) > n {
		return fmt.Sprintf("> %s...\n\n", string(runes[:n]))
	}
	return fmt.Sprintf("> %s\n\n", userMsg)
}

// spinPrefix builds the stream prefix for the current spinner frame.
func spinPrefix(runes []rune, iconIdx int, dotIdx int, name string) string {
	icon := string(runes[iconIdx%len(runes)])
	dots := strings.Repeat(".", (dotIdx%4)+1)
	return fmt.Sprintf("[%s] %s thinking%s\n\n", icon, name, dots)
}

func (r *Router) sendStreamReply(msg channel.Message, text string, streamID string, finish bool) bool {
	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok {
		log.Printf("[router] no channel for bot %s", msg.BotID)
		return false
	}
	if err := ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
		Text:          text,
		ReplyToID:     msg.ID,
		OriginalReqID: msg.ReqID,
		ChatID:        msg.ChatID,
		StreamID:      streamID,
		StreamFinish:  finish,
	}); err != nil {
		log.Printf("[router] send stream reply: %v", err)
		return false
	}
	return true
}

func displayAgentType(t string) string {
	switch t {
	case "claude-code":
		return "Claude Code"
	default:
		return t
	}
}

var subscriptDigits = []rune{'₀', '₁', '₂', '₃', '₄', '₅', '₆', '₇', '₈', '₉'}

func subscriptNum(n int) string {
	if n == 0 {
		return "₀"
	}
	var runes []rune
	for n > 0 {
		runes = append([]rune{subscriptDigits[n%10]}, runes...)
		n /= 10
	}
	return string(runes)
}

func (r *Router) queueIndicator(sessionID int64) string {
	r.mu.Lock()
	n := len(r.pendingMessages[sessionID])
	r.mu.Unlock()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("  [waiting %s]", subscriptNum(n))
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%d 小时 %d 分", h, m)
}

func (r *Router) sendReply(msg channel.Message, text string) {
	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok {
		log.Printf("[router] no channel for bot %s", msg.BotID)
		return
	}
	if err := ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
		Text:          text,
		ReplyToID:     msg.ID,
		OriginalReqID: msg.ReqID,
		ChatID:        msg.ChatID,
	}); err != nil {
		log.Printf("[router] send reply: %v", err)
	}
}
