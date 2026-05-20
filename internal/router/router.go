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

	mu              sync.Mutex
	pendingQuestions map[int64]*agent.Question // sessionID -> pending question
}

// New creates a new Router.
func New(sessMgr *session.Manager, pool *botpool.Pool, runner agent.Runner, gw *gateway.Gateway, statusMgr *StatusManager) *Router {
	return &Router{
		sessionMgr:       sessMgr,
		botPool:          pool,
		agentRunner:      runner,
		gw:               gw,
		statusMgr:        statusMgr,
		pendingQuestions: make(map[int64]*agent.Question),
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

// streamJSONUserMsg builds a stream-json user message line.
type streamJSONUserMsg struct {
	Type    string              `json:"type"`
	Message streamJSONMsgBody   `json:"message"`
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

// HandleWorkerMessage processes a message from a user to a worker bot.
func (r *Router) HandleWorkerMessage(msg channel.Message) {
	log.Printf("[router] msg from %s: %s", msg.UserID, msg.Content)
	ctx := context.Background()

	sess, err := r.sessionMgr.GetByPlatformBotID(msg.BotID)
	if err != nil {
		log.Printf("[router] session not found for bot %s: %v", msg.BotID, err)
		r.sendReply(msg, "找不到对应的 Session，请通过总控 Bot 重新创建。")
		return
	}

	// Bootstrap status session (no message sent — state stays internal until first change).
	r.statusMgr.Send(StatusEvent{
		SessionID:   sess.ID,
		BotID:       msg.BotID,
		UserID:      msg.UserID,
		ChatID:      msg.ChatID,
		SessionName: sess.Name,
		State:       StateWaking,
	})

	if msg.Content == "/end" {
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateSleeped})

		// Send sleep bubble before tearing down the channel so the user
		// sees a proper goodbye instead of radio silence.
		streamID := fmt.Sprintf("stream_%s_%d", msg.ID, time.Now().UnixNano())
		sleepText := fmt.Sprintf("[💤] %s 我先睡啦 ZZZ", sess.Name)
		r.sendStreamReply(msg, sleepText, streamID, true)

		// Release bot and mark session as sleeped — no new messages will
		// be routed to this session.
		if sess.BoundBotID > 0 {
			if err := r.botPool.Release(sess.BoundBotID); err != nil {
				log.Printf("[router] release bot %d: %v", sess.BoundBotID, err)
			}
		}
		if err := r.sessionMgr.MarkSleeped(sess.ID); err != nil {
			log.Printf("[router] mark sleeped %d: %v", sess.ID, err)
		}

		// Signal prepare-to-close: cancel the context to stop heartbeat and
		// signal readLoop to exit. After readLoop has finished, give the
		// WeCom server 2s to render the sleep stream bubble, then tear down
		// the WebSocket. Without this delay, readLoop exits immediately when
		// the message handler returns (since ctx is already cancelled),
		// done closes within microseconds, and the server may close the
		// connection before it finishes rendering the stream.
		ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
		if ok {
			done := ch.PrepareClose()
			boundBotID := sess.BoundBotID
			go func() {
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					log.Printf("[router] prepareClose timeout for bot %d, closing anyway", boundBotID)
				}
				time.Sleep(2 * time.Second)
				r.gw.CloseWorkerChannel(boundBotID)
			}()
		}
		return
	}

	// Voice messages are not transcribed by WeCom — content is always empty.
	// Reply directly instead of launching Claude with empty input.
	if msg.MsgType == "voice" {
		r.sendReply(msg, "语音消息暂不支持，请发送文字消息。")
		return
	}

	// Save user message to history.
	contentToSave := msg.Content
	if msg.QuoteContent != "" {
		contentToSave = fmt.Sprintf("[引用: %s] %s", msg.QuoteContent, msg.Content)
	}
	_ = r.sessionMgr.AddMessage(sess.ID, "user", contentToSave, string(msg.MsgType))

	// Check for a pending question — if one exists, format the answer as text.
	r.mu.Lock()
	pendingQ := r.pendingQuestions[sess.ID]
	delete(r.pendingQuestions, sess.ID)
	r.mu.Unlock()

	var inputJSON string
	if pendingQ != nil {
		answerText := formatAnswerText(pendingQ, msg.Content)
		log.Printf("[router] answering pending question %s with: %s", pendingQ.ToolUseID, answerText)
		inputJSON = buildTextInput(answerText)
	} else {
		input := msg.Content
		if msg.QuoteContent != "" {
			input = fmt.Sprintf("[用户引用了以下消息]\n%s\n\n[用户的新消息]\n%s", msg.QuoteContent, msg.Content)
		}
		inputJSON = buildTextInput(input)
	}

	// Verify bot channel is healthy before launching agent.
	// If the WebSocket is dead, tell the user immediately instead of making them
	// wait for Claude to finish processing only to get a broken pipe.
	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok || !ch.IsConnected() {
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateReconnecting})
		r.sendReply(msg, "连接已断开，正在重连，请稍后重试。")
		return
	}

	// Resume or start agent process.
	agentSess, err := r.getOrCreateAgentSession(ctx, sess)
	if err != nil {
		log.Printf("[router] agent session: %v", err)
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})
		r.sendReply(msg, "启动/唤醒 Agent 失败，请重试。")
		return
	}

	_ = r.sessionMgr.Touch(sess.ID)

	// Send input to agent and stream output back.
	outputCh, err := agentSess.Send(ctx, inputJSON)
	if err != nil {
		log.Printf("[router] send to agent: %v", err)
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})
		r.sendReply(msg, "Agent 处理失败，请重试。")
		return
	}

	streamID := fmt.Sprintf("stream_%d", time.Now().UnixNano())
	spinnerRunes := []rune(spinnerFrames)
	// spinnerIconIdx advances only on ticker, giving a smooth fixed-cadence spin.
	spinnerIconIdx := 0
	spinnerDotIdx := 0 // advances per frame sent, for the dots animation
	spinnerInterval := spinnerMaxInterval
	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()

	var cache streamCache
	streamBroken := false

	// Send initial spinner frame.
	spinnerDotIdx++
	if !r.sendStreamReply(msg, spinPrefix(spinnerRunes, spinnerIconIdx, spinnerDotIdx, sess.Name), streamID, false) {
		streamBroken = true
	}

	for {
		select {
		case chunk, ok := <-outputCh:
			if !ok {
				goto done
			}
			spinnerDotIdx++
			log.Printf("[router] chunk kind=%s contentLen=%d", chunk.Kind, len(chunk.Content))
			switch chunk.Kind {
			case agent.KindError:
				log.Printf("[router] agent error: %s", chunk.Content)
				r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})
				r.sendStreamReply(msg, fmt.Sprintf("[💫] %s error\n\n%s", sess.Name, chunk.Content), streamID, true)
				return
			case agent.KindText:
				cache.textBuf.WriteString(chunk.Content)
				if !r.sendStreamReply(msg, spinPrefix(spinnerRunes, spinnerIconIdx, spinnerDotIdx, sess.Name)+cache.textBuf.String(), streamID, false) {
					streamBroken = true
				}
				spinnerInterval = spinnerMinInterval
				ticker.Reset(spinnerInterval)
			case agent.KindQuestion:
				cache.question = chunk.Question
			case agent.KindFinal:
				cache.fullResponse = chunk.Content
			case agent.KindThinking, agent.KindToolUse:
				if chunk.Content != "" {
					spinnerInterval = spinnerMinInterval
					ticker.Reset(spinnerInterval)
				}
			}
		case <-ticker.C:
			if streamBroken {
				continue
			}
			spinnerIconIdx++
			spinnerDotIdx++
			frameText := spinPrefix(spinnerRunes, spinnerIconIdx, spinnerDotIdx, sess.Name)
			if cache.textBuf.Len() > 0 {
				frameText += cache.textBuf.String()
			}
			// Decelerate: slow down toward max interval.
			if spinnerInterval < spinnerMaxInterval {
				spinnerInterval += spinnerDecelStep
				if spinnerInterval > spinnerMaxInterval {
					spinnerInterval = spinnerMaxInterval
				}
				ticker.Reset(spinnerInterval)
			}
			log.Printf("[router] ticker frame icon=%d dots=%d interval=%v", spinnerIconIdx%len(spinnerRunes), spinnerDotIdx%4, spinnerInterval)
			if !r.sendStreamReply(msg, frameText, streamID, false) {
				log.Printf("[router] ticker send failed, marking stream broken")
				streamBroken = true
			}
		}
	}

done:
	responseText := cache.textBuf.String()
	if responseText == "" {
		responseText = cache.fullResponse
	}

	if streamBroken {
		// Stream connection was lost. Send cached content as a regular message.
		if responseText != "" {
			r.sendReply(msg, responseText)
		}
		if cache.question != nil {
			r.mu.Lock()
			r.pendingQuestions[sess.ID] = cache.question
			r.mu.Unlock()
			r.sendQuestionMenu(msg, cache.question)
		}
	} else {
		// Normal path: send final stream frame.
		doneText := fmt.Sprintf("[✓] %s stand by\n\n%s", sess.Name, responseText)
		r.sendStreamReply(msg, doneText, streamID, true)

		if cache.question != nil {
			r.mu.Lock()
			r.pendingQuestions[sess.ID] = cache.question
			r.mu.Unlock()
			r.sendQuestionMenu(msg, cache.question)
		}
	}

	// Save agent response to history.
	if responseText != "" {
		_ = r.sessionMgr.AddMessage(sess.ID, "agent", responseText, "text")
	}
}

// getOrCreateAgentSession resumes or starts an agent session for the given LinkCode session.
func (r *Router) getOrCreateAgentSession(ctx context.Context, sess *session.Session) (agent.Session, error) {
	if sess.ClaudeSessionID != "" {
		agentSess, err := r.agentRunner.Resume(ctx, fmt.Sprintf("%d", sess.ID), sess.ClaudeSessionID)
		if err != nil {
			return nil, err
		}
		_ = r.sessionMgr.MarkWaked(sess.ID)
		return agentSess, nil
	}

	agentSess, err := r.agentRunner.Start(ctx, fmt.Sprintf("%d", sess.ID))
	if err != nil {
		return nil, err
	}
	if sid := agentSess.ClaudeSessionID(); sid != "" {
		if err := r.sessionMgr.SetClaudeSessionID(sess.ID, sid); err != nil {
			log.Printf("[router] WARNING: failed to persist claude session ID for session %d: %v", sess.ID, err)
		}
	}
	return agentSess, nil
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

		// For multiple questions, save each one with a modified ToolUseID.
		if len(q.Questions) > 1 && i > 0 {
			_ = i // TODO: multi-question support
		}
	}
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
