package router

import (
	"context"
	"fmt"
	"log"
	"os"
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
	case "/new":
		r.handleNew(msg, sess)
	case "/help":
		r.handleHelp(msg)
	default:
		r.sendReply(msg, fmt.Sprintf("未知命令: %s。发送 /help 查看可用命令。", cmd))
	}
}

// handleStop interrupts the running agent process for this session.
// The stream loop at done: handles the visual update; we only set the flag here
// so it knows this was a /stop rather than a crash.
func (r *Router) handleStop(msg channel.Message, sess *session.Session) {
	if r.agentRunner.Interrupt(fmt.Sprintf("%d", sess.ID)) {
		r.mu.Lock()
		r.interruptedSessions[sess.ID] = true
		r.mu.Unlock()
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateSleeped})
	} else {
		r.sendReply(msg, "当前没有正在思考的进程")
	}
}

// handleEnd marks the session as sleeped without closing the WebSocket,
// so the bot stays online and can receive /new to start a fresh session.
func (r *Router) handleEnd(msg channel.Message, sess *session.Session) {
	r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateSleeped})

	streamID := fmt.Sprintf("stream_%s_%d", msg.ID, time.Now().UnixNano())
	sleepText := fmt.Sprintf("[💤] %s 我先睡啦 ZZZ\n\n发送 /new 开始新对话，发送 /help 查看更多命令", sess.Name)
	r.sendStreamReply(msg, sleepText, streamID, true)

	if err := r.sessionMgr.MarkSleeped(sess.ID); err != nil {
		log.Printf("[router] mark sleeped %d: %v", sess.ID, err)
	}
}

// handleNew resets the worker bot by preserving the old session and creating a new one,
// rebinding the same bot without closing the WebSocket connection.
func (r *Router) handleNew(msg channel.Message, sess *session.Session) {
	oldSessionID := sess.ID
	hadClaudeSession := sess.ClaudeSessionID != ""

	// Interrupt if process is running.
	if sess.ProcessStatus == "waked" {
		r.agentRunner.Interrupt(fmt.Sprintf("%d", sess.ID))
		r.mu.Lock()
		delete(r.interruptedSessions, sess.ID)
		r.mu.Unlock()
	}

	// Release bot from old session (makes bot idle in DB).
	if err := r.botPool.Release(sess.BoundBotID); err != nil {
		log.Printf("[router] handleNew: release bot %d: %v", sess.BoundBotID, err)
	}

	// Mark old session as sleeped.
	if err := r.sessionMgr.MarkSleeped(sess.ID); err != nil {
		log.Printf("[router] handleNew: mark sleeped %d: %v", sess.ID, err)
	}

	// Clean up old session state.
	r.mu.Lock()
	delete(r.pendingQuestions, sess.ID)
	delete(r.interruptedSessions, sess.ID)
	r.mu.Unlock()
	r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateSleeped})

	// Create a new session with auto-generated name.
	newName := fmt.Sprintf("Agent-%d", time.Now().Unix())
	newSess, err := r.sessionMgr.Create(newName, "claude-code", "", 0)
	if err != nil {
		log.Printf("[router] handleNew: create session: %v", err)
		r.sendReply(msg, fmt.Sprintf("创建新 Session 失败：%v", err))
		return
	}

	// Rebind the same bot to the new session.
	if err := r.botPool.BindToSession(sess.BoundBotID, newSess.ID); err != nil {
		log.Printf("[router] handleNew: bind bot %d to session %d: %v", sess.BoundBotID, newSess.ID, err)
		r.sendReply(msg, fmt.Sprintf("绑定 Bot 失败：%v", err))
		return
	}

	// Send welcome message.
	wd := r.workDir
	if wd == "" {
		wd, _ = os.Getwd()
	}
	var welcome string
	if hadClaudeSession {
		welcome = fmt.Sprintf("之前的对话内容已经清空，保存至 [%d]，开始新的对话吧，有什么任务要交给我吗？\n工作目录：%s", oldSessionID, wd)
	} else {
		welcome = fmt.Sprintf("你好，我是你的 work bot %s，有什么任务要交给我吗？\n工作目录：%s", sess.Name, wd)
	}
	r.sendReply(msg, welcome)

	log.Printf("[router] handleNew: session %d -> %d (bot %d)", oldSessionID, newSess.ID, sess.BoundBotID)
}

// handleHelp sends a list of available worker bot commands to the user.
func (r *Router) handleHelp(msg channel.Message) {
	help := `可用命令：

/new  - 清空当前对话，开始新 Session
/stop - 中断 Agent 正在进行的思考
/end  - 结束当前对话（可随时 /new 重新开始）
/help - 显示本帮助信息

直接发送文字消息即可与 Agent 对话。`
	r.sendReply(msg, help)
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
		stopText := fmt.Sprintf("[✗] %s interrupted, stand by", sess.Name)
		if !r.sendStreamReply(msg, stopText, streamID, true) {
			r.sendReply(msg, stopText)
		}
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
