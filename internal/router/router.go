// Package router handles bidirectional message forwarding between
// IM channels (user <-> bot) and agent processes.
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"linkcode/internal/agent"
	"linkcode/internal/botpool"
	"linkcode/internal/channel"
	"linkcode/internal/gateway"
	"linkcode/internal/pricing"
	"linkcode/internal/session"
)

const (
	// spinnerFrames defines the 8-frame Braille spinner sequence.
	spinnerFrames = "⣷⣯⣟⡿⢿⣻⣽⣾"
)

// idleRefreshSchedule controls the cadence of spinner refresh frames sent to
// WeCom while the agent is idle/thinking. It starts fast (0.5s — responsive feel
// right after the user sends a message) and progressively slows down (1s, then
// 4.7s) to keep the refresh rate under WeCom's 30-msgs/min limit during long
// thinking periods. That way the final `finish` frame ([✓] stand by) lands in
// the sparse tail and isn't rejected as 846607 — which is what made the standby
// checkmark sometimes fail to show after a long thinking.
// The schedule loops: after the slow tail it returns to the fast start, keeping
// the cadence lively. One full loop is 30 frames / ~62s ≈ 29/min — and any 60s
// sliding window holds at most 29 frames, under WeCom's 30-msgs/min limit.
// Composition: 0.5s ×10, 1s ×10, 4.7s ×10.
var idleRefreshSchedule = func() []time.Duration {
	var s []time.Duration
	for i := 0; i < 10; i++ {
		s = append(s, 500*time.Millisecond)
	}
	for i := 0; i < 10; i++ {
		s = append(s, 1*time.Second)
	}
	for i := 0; i < 10; i++ {
		s = append(s, 4700*time.Millisecond)
	}
	return s
}()

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
	styler      channel.Styler

	mu                  sync.Mutex
	pendingQuestions    map[int64]*agent.Question   // sessionID -> pending question
	interruptedSessions map[int64]bool              // sessionID -> process was /stop'd
	pendingMessages     map[int64][]channel.Message // sessionID -> queued messages while process is busy
	pendingFragments    map[int64][]string          // sessionID -> buffered "still speaking" fragments, joined on next non-continuation message
	thinkingStartedAt   map[int64]time.Time         // sessionID -> when the current thinking started
	sessionUsage        map[int64]*sessionUsageRec  // sessionID -> cumulative token usage
	pendingImageChoices map[int64][]string          // sessionID -> fuzzy-matched image candidates awaiting a numeric pick
	pricingCalc         pricing.Calculator
}

// sessionUsageRec accumulates token usage across turns for a session.
type sessionUsageRec struct {
	inputTokens     int
	outputTokens    int
	cacheReadTokens int
	model           string  // last used model
	accCost         float64 // accumulated calculated cost
}

// New creates a new Router.
func New(sessMgr *session.Manager, pool *botpool.Pool, runner agent.Runner, gw *gateway.Gateway, statusMgr *StatusManager, styler channel.Styler, pricingCalc pricing.Calculator) *Router {
	return &Router{
		sessionMgr:          sessMgr,
		botPool:             pool,
		agentRunner:         runner,
		gw:                  gw,
		statusMgr:           statusMgr,
		styler:              styler,
		pricingCalc:         pricingCalc,
		pendingQuestions:    make(map[int64]*agent.Question),
		interruptedSessions: make(map[int64]bool),
		pendingMessages:     make(map[int64][]channel.Message),
		pendingFragments:    make(map[int64][]string),
		thinkingStartedAt:   make(map[int64]time.Time),
		sessionUsage:        make(map[int64]*sessionUsageRec),
		pendingImageChoices: make(map[int64][]string),
	}
}

// PendingCount returns the number of queued messages for a session.
func (r *Router) PendingCount(sessionID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pendingMessages[sessionID])
}

// SessionUsage returns the accumulated token usage and calculated cost for a session.
// known is false when the model is not configured in pricing (cost should display "?").
func (r *Router) SessionUsage(sessionID int64) (inputTokens, outputTokens int, costUSD float64, known bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	u := r.sessionUsage[sessionID]
	if u == nil {
		return 0, 0, 0, false
	}
	known = u.model != "" && !math.IsNaN(r.pricingCalc.Cost(u.model, 0, 0, 0))
	return u.inputTokens, u.outputTokens, u.accCost, known
}

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
		Text: fmt.Sprintf("你好，我是你的 %s「%s」\n工作目录：%s\n发送 %s 查看可用命令。",
			displayAgentType(sess.AgentType), sess.Name, wd, r.styler.Bold("/help")),
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
		if r.consumeImageChoice(msg, sess) {
			return
		}
		finalContent, buffering := r.collectFragments(sess.ID, msg.Content)
		if buffering {
			r.sendReply(msg, "请继续，我接着听")
			return
		}
		msg.Content = finalContent
		r.handleLLM(msg, sess)
	}
}

// message classification
type msgKind int

const (
	msgKindText msgKind = iota
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

// continuationPhrases 列出表示"用户还没说完"的结尾短语；命中则该条消息暂存、不发给 Claude。
// 英文短语不区分大小写（见 endsWithContinuation）。
var continuationPhrases = []string{
	"我接着说", "我来接着说", "我继续说", "我来继续说", "我还没说完",
	"not done yet", "more to come",
}

// endsWithContinuation 判断消息（去首尾空白后）是否以续说短语结尾。
// 严格匹配：短语后不能有任何字符（含标点）；短语前可有正文。
// 英文短语不区分大小写（中文不受影响）。
func endsWithContinuation(content string) bool {
	s := strings.ToLower(strings.TrimSpace(content))
	for _, p := range continuationPhrases {
		if strings.HasSuffix(s, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// collectFragments 处理"还没说完"的暂存与拼接。
// 命中续说短语 -> 追加暂存，返回 ("", true)，调用方应回复提示且不进 Claude。
// 否则 -> 将已暂存片段与本条按换行拼齐、清空暂存，返回 (拼接内容, false)。
func (r *Router) collectFragments(sessionID int64, content string) (finalContent string, buffering bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if endsWithContinuation(content) {
		r.pendingFragments[sessionID] = append(r.pendingFragments[sessionID], content)
		return "", true
	}

	frags := r.pendingFragments[sessionID]
	if len(frags) == 0 {
		return content, false
	}
	delete(r.pendingFragments, sessionID)
	return strings.Join(append(frags, content), "\n"), false
}

// stream JSON helpers

type streamJSONUserMsg struct {
	Type    string            `json:"type"`
	Message streamJSONMsgBody `json:"message"`
}

type streamJSONMsgBody struct {
	Role    string                  `json:"role"`
	Content []streamJSONContentPart `json:"content"`
}

type streamJSONContentPart struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
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

// quotePrefix truncates text to n runes and wraps it in platform-specific quote markup.
func quotePrefix(styler channel.Styler, text string, n int) string {
	runes := []rune(text)
	if len(runes) > n {
		return styler.Quote(string(runes[:n])+"...") + "\n"
	}
	return styler.Quote(text) + "\n"
}

// spinPrefix builds the stream prefix for the current spinner frame.
func spinPrefix(runes []rune, iconIdx int, dotIdx int, name string) string {
	icon := string(runes[iconIdx%len(runes)])
	if name != "" {
		return fmt.Sprintf("[%s] %s thinking", icon, name)
	}
	return fmt.Sprintf("[%s] thinking", icon)
}

func spinDots(dotIdx int) string {
	return strings.Repeat(".", (dotIdx%4)+1)
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

// streamStatus returns the elapsed time string (for the Bar title) and an optional
// timeout warning (which goes into the Box body to keep the title short).
func (r *Router) streamStatus(sessionID int64, streamTimeout time.Duration) (timeStr string, warning string) {
	r.mu.Lock()
	startedAt, ok := r.thinkingStartedAt[sessionID]
	r.mu.Unlock()
	if !ok || streamTimeout <= 0 {
		return "", ""
	}
	elapsed := time.Since(startedAt)
	timeStr = fmt.Sprintf("（%s）", formatDurationShort(elapsed))
	if elapsed > streamTimeout-30*time.Second {
		remaining := streamTimeout - elapsed
		if remaining < 0 {
			remaining = 0
		}
		warning = r.styler.StreamWarning(remaining)
	}
	return
}

func formatDurationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
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
