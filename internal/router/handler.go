package router

import (
	"context"
	"fmt"
	"log"
	"time"

	"linkcode/internal/agent"
	"linkcode/internal/channel"
	"linkcode/internal/session"
)

// handleVoice rejects voice messages early, before any state change or Claude launch.
func (r *Router) handleVoice(msg channel.Message, _ *session.Session) {
	r.sendReply(msg, "语音消息暂不支持，请发送文字消息。")
}

// handleCommand dispatches a slash-command message to the appropriate handler.
func (r *Router) handleCommand(msg channel.Message, sess *session.Session) {
	cmd := parseCommand(msg.Content)
	switch cmd {
	case "/stop":
		r.handleStop(msg, sess)
	case "/end":
		r.handleEnd(msg, sess)
	default:
		r.sendReply(msg, fmt.Sprintf("未知命令: %s。可用命令: /stop /end", cmd))
	}
}

// handleStop interrupts the running agent process for this session.
func (r *Router) handleStop(msg channel.Message, sess *session.Session) {
	if r.agentRunner.Interrupt(fmt.Sprintf("%d", sess.ID)) {
		r.mu.Lock()
		r.interruptedSessions[sess.ID] = true
		r.mu.Unlock()
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateSleeped})
		r.sendReply(msg, fmt.Sprintf("[✗] %s interrupted, stand by", sess.Name))
	} else {
		r.sendReply(msg, "当前没有正在思考的进程")
	}
}

// handleEnd tears down the worker bot connection and marks the session as sleeped.
func (r *Router) handleEnd(msg channel.Message, sess *session.Session) {
	r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateSleeped})

	streamID := fmt.Sprintf("stream_%s_%d", msg.ID, time.Now().UnixNano())
	sleepText := fmt.Sprintf("[💤] %s 我先睡啦 ZZZ", sess.Name)
	r.sendStreamReply(msg, sleepText, streamID, true)

	if sess.BoundBotID > 0 {
		if err := r.botPool.Release(sess.BoundBotID); err != nil {
			log.Printf("[router] release bot %d: %v", sess.BoundBotID, err)
		}
	}
	if err := r.sessionMgr.MarkSleeped(sess.ID); err != nil {
		log.Printf("[router] mark sleeped %d: %v", sess.ID, err)
	}

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
}

// handleLLM forwards a text message to the agent and streams the response back.
func (r *Router) handleLLM(msg channel.Message, sess *session.Session) {
	ctx := context.Background()

	// Bootstrap status session.
	r.statusMgr.Send(StatusEvent{
		SessionID:   sess.ID,
		BotID:       msg.BotID,
		UserID:      msg.UserID,
		ChatID:      msg.ChatID,
		SessionName: sess.Name,
		State:       StateWaking,
	})

	// Save user message to history.
	contentToSave := msg.Content
	if msg.QuoteContent != "" {
		contentToSave = fmt.Sprintf("[引用: %s] %s", msg.QuoteContent, msg.Content)
	}
	_ = r.sessionMgr.AddMessage(sess.ID, "user", contentToSave, string(msg.MsgType))

	// Build the input JSON for the agent.
	inputJSON := r.buildUserInput(msg, sess)

	// Verify bot channel is healthy.
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

	outputCh, err := agentSess.Send(ctx, inputJSON)
	if err != nil {
		log.Printf("[router] send to agent: %v", err)
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})
		r.sendReply(msg, "Agent 处理失败，请重试。")
		return
	}

	r.streamToUser(msg, sess, outputCh)
}

// buildUserInput constructs the stream-json text to send to the agent.
// It resolves pending questions and applies quote context when present.
func (r *Router) buildUserInput(msg channel.Message, sess *session.Session) string {
	r.mu.Lock()
	pendingQ := r.pendingQuestions[sess.ID]
	delete(r.pendingQuestions, sess.ID)
	r.mu.Unlock()

	if pendingQ != nil {
		answerText := formatAnswerText(pendingQ, msg.Content)
		log.Printf("[router] answering pending question %s with: %s", pendingQ.ToolUseID, answerText)
		return buildTextInput(answerText)
	}

	input := msg.Content
	if msg.QuoteContent != "" {
		input = fmt.Sprintf("[用户引用了以下消息]\n%s\n\n[用户的新消息]\n%s", msg.QuoteContent, msg.Content)
	}
	return buildTextInput(input)
}

// streamToUser reads agent output chunks and streams them to the user.
func (r *Router) streamToUser(msg channel.Message, sess *session.Session, outputCh <-chan agent.OutputChunk) {
	streamID := fmt.Sprintf("stream_%d", time.Now().UnixNano())
	spinnerRunes := []rune(spinnerFrames)
	spinnerIconIdx := 0
	spinnerDotIdx := 0
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
	r.mu.Lock()
	interrupted := r.interruptedSessions[sess.ID]
	delete(r.interruptedSessions, sess.ID)
	r.mu.Unlock()
	if interrupted {
		return
	}

	responseText := cache.textBuf.String()
	if responseText == "" {
		responseText = cache.fullResponse
	}

	if streamBroken {
		if responseText != "" {
			ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
			if ok {
				prefix := buildQuotePrefix(msg.Content, 30)
				doneText := fmt.Sprintf("%s[✓] %s stand by\n\n%s", prefix, sess.Name, responseText)
				ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
					Text:   doneText,
					ChatID: msg.ChatID,
				})
			}
		}
		if cache.question != nil {
			r.mu.Lock()
			r.pendingQuestions[sess.ID] = cache.question
			r.mu.Unlock()
			r.sendQuestionMenu(msg, cache.question)
		}
	} else {
		doneText := fmt.Sprintf("[✓] %s stand by\n\n%s", sess.Name, responseText)
		r.sendStreamReply(msg, doneText, streamID, true)

		if cache.question != nil {
			r.mu.Lock()
			r.pendingQuestions[sess.ID] = cache.question
			r.mu.Unlock()
			r.sendQuestionMenu(msg, cache.question)
		}
	}

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

// parseCommand returns the first word of content if it starts with "/", otherwise empty.
// "hello" → "", "/stop" → "/stop", "/ " → ""
func parseCommand(content string) string {
	if len(content) < 2 {
		return "" // too short for a valid /command
	}
	if content[0] != '/' {
		return ""
	}
	// Reject "/ " (slash immediately followed by space).
	if content[1] == ' ' {
		return ""
	}
	// Return the first word (up to first space).
	for i, c := range content {
		if c == ' ' {
			return content[:i]
		}
	}
	return content
}
