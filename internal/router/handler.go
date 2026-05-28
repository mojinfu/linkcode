package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"linkcode/internal/agent"
	"linkcode/internal/botpool"
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
	case "/new":
		r.handleNew(msg, sess)
	case "/resetdefaultworkdir":
		r.handleResetDefaultWorkDir(msg, sess)
	case "/workdir":
		r.handleWorkDir(msg, sess)
	case "/help":
		r.handleHelp(msg)
	default:
		r.sendReply(msg, fmt.Sprintf("未知命令: %s。发送 /help 查看可用命令。", cmd))
	}
}

// handleStop interrupts the running agent process for this session.
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
	delete(r.pendingMessages, sess.ID)
	delete(r.thinkingStartedAt, sess.ID)
	r.mu.Unlock()
	r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateSleeped})

	// Create a new session reusing the agent name.
	newSess, err := r.sessionMgr.Create(sess.Name, sess.AgentType, "", 0)
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

	// Resolve work directory and send welcome.
	bot, _ := r.botPool.GetByID(sess.BoundBotID)
	botWD := ""
	if bot != nil {
		botWD = bot.WorkDir
	}
	wd, source := r.botPool.ResolveWorkDir(botWD)

	var welcome string
	claudeSid := sess.ClaudeSessionID
	b := r.styler.Bold
	if hadClaudeSession {
		welcome = fmt.Sprintf("对话已重置。\n旧 Claude Session：%s\n新 LinkCode Session：%s（Claude Session 将在下条消息时创建）\n\n工作目录：%s（%s）\n开始新的对话吧！",
			b(claudeSid), b(fmt.Sprintf("%d", newSess.ID)), wd, source)
	} else {
		welcome = fmt.Sprintf("你好，我是你的 Agent「%s」，有什么任务要交给我吗？\n工作目录：%s（%s）\n发送 %s 查看可用命令。", sess.Name, wd, source, b("/help"))
	}
	r.sendReply(msg, welcome)

	log.Printf("[router] handleNew: session %d (claude: %s) -> %d (bot %d)", oldSessionID, claudeSid, newSess.ID, sess.BoundBotID)
}

// handleResetDefaultWorkDir sets the bot-level default working directory.
// Takes effect after /new (next session start).
func (r *Router) handleResetDefaultWorkDir(msg channel.Message, sess *session.Session) {
	parts := strings.Fields(msg.Content)
	if len(parts) < 2 {
		bot, _ := r.botPool.GetByID(sess.BoundBotID)
		botWD := ""
		if bot != nil {
			botWD = bot.WorkDir
		}
		wd, source := r.botPool.ResolveWorkDir(botWD)
		r.sendReply(msg, fmt.Sprintf("当前 Claude 启动时的默认工作目录：%s（%s）\n\n修改方式：%s <路径>\n修改后 /new 才会生效。", wd, source, r.styler.Bold("/resetdefaultworkdir")))
		return
	}

	newPath := parts[1]

	if !botpool.DirExists(newPath) {
		r.sendReply(msg, fmt.Sprintf("目录不存在或无法访问：%s\n请检查路径是否正确。", newPath))
		return
	}

	if err := r.botPool.UpdateWorkDir(sess.BoundBotID, newPath); err != nil {
		log.Printf("[router] /resetdefaultworkdir update bot %d: %v", sess.BoundBotID, err)
		r.sendReply(msg, fmt.Sprintf("更新失败：%v", err))
		return
	}

	r.sendReply(msg, fmt.Sprintf("默认工作目录已更新：%s\n此修改在 /new 后生效。", newPath))
}

// handleWorkDir asks Claude about its current working directory.
func (r *Router) handleWorkDir(msg channel.Message, sess *session.Session) {
	msg.Content = "请告诉我你当前所在的工作目录路径是什么？用一句话回答。"
	r.handleLLM(msg, sess)
}

// handleHelp sends a list of available worker bot commands to the user.
func (r *Router) handleHelp(msg channel.Message) {
	help := r.styler.Box("Agent 命令",
		"\"/new\"                 重置对话，清空上下文\n"+
			"\"/stop\"                中断 Agent 正在进行的思考\n"+
			"\"/workdir\"             让 Claude 回答当前实际的工作目录\n"+
			"\"/resetdefaultworkdir\" 设定此 Agent 默认工作目录\n"+
			"\"/help\"                显示本帮助\n"+
			"\n直接发文字消息即可与 Agent 对话")
	r.sendReply(msg, help)
}

// handleLLM forwards a text message to the agent and streams the response back.
func (r *Router) handleLLM(msg channel.Message, sess *session.Session) {
	ctx := context.Background()

	// Verify bot channel first — no point doing anything if we can't reply.
	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok || !ch.IsConnected() {
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateReconnecting})
		r.sendReply(msg, "连接已断开，正在重连，请稍后重试。")
		return
	}

	// Resolve work directory for this session.
	bot, _ := r.botPool.GetByID(sess.BoundBotID)
	botWD := ""
	if bot != nil {
		botWD = bot.WorkDir
	}
	workDir, _ := r.botPool.ResolveWorkDir(botWD)

	// Resume or start agent process.
	agentSess, err := r.getOrCreateAgentSession(ctx, sess, workDir)
	if err != nil {
		log.Printf("[router] agent session: %v", err)
		if errors.Is(err, agent.ErrBusy) {
			r.enqueueMessage(sess, msg)
			return
		}
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})
		r.sendReply(msg, "启动/唤醒 Agent 失败，请重试。")
		return
	}

	r.mu.Lock()
	r.thinkingStartedAt[sess.ID] = time.Now()
	r.mu.Unlock()

	_ = r.sessionMgr.Touch(sess.ID)

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

	outputCh, err := agentSess.Send(ctx, inputJSON)
	if err != nil {
		log.Printf("[router] send to agent: %v", err)
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})
		r.sendReply(msg, "Agent 处理失败，请重试。")
		return
	}

	r.streamToUser(msg, sess, outputCh)
	r.drainPendingMessages(sess)
}

// buildUserInput constructs the stream-json text to send to the agent.
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

	streamTimeout := time.Duration(0)
	if ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID); ok {
		streamTimeout = ch.StreamTimeout()
	}

	buildStatusBar := func() string {
		timeStr, warning := r.streamStatus(sess.ID, streamTimeout)
	title := spinPrefix(spinnerRunes, spinnerIconIdx, spinnerDotIdx, "") + timeStr + " " + spinDots(spinnerDotIdx) + r.queueIndicator(sess.ID)
	if warning != "" {
		return r.styler.Box(title, warning)
	}
	return r.styler.Bar(title) + r.styler.DiffSuffix(spinnerDotIdx)
	}

	// Send initial spinner frame.
	spinnerDotIdx++
	if !r.sendStreamReply(msg, buildStatusBar(), streamID, false) {
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
				r.sendStreamReply(msg, r.styler.Bar("[💫] error")+"\n"+chunk.Content, streamID, true)
				return
			case agent.KindText:
				cache.textBuf.WriteString(chunk.Content)
				if !r.sendStreamReply(msg, buildStatusBar()+"\n"+cache.textBuf.String(), streamID, false) {
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
			frameText := buildStatusBar()
			if cache.textBuf.Len() > 0 {
				frameText += "\n" + cache.textBuf.String()
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
		stopText := r.styler.Bar("[✗] interrupted, stand by")
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
			if ok && ch.IsConnected() {
				prefix := quotePrefix(r.styler, msg.Content, 30)
				doneText := r.styler.Bar("[✓] stand by") + "\n\n" + prefix + "\n" + responseText
				ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
					Text:   doneText,
					ChatID: msg.ChatID,
				})
			} else if responseText != "" {
				log.Printf("[router] stream broken and channel dead for bot %s, response saved to session %d", msg.BotID, sess.ID)
			}
		}
		if cache.question != nil {
			r.mu.Lock()
			r.pendingQuestions[sess.ID] = cache.question
			r.mu.Unlock()
			r.sendQuestionMenu(msg, cache.question)
		}
	} else {
		doneText := r.styler.Bar("[✓] stand by") + "\n\n" + responseText
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
func (r *Router) getOrCreateAgentSession(ctx context.Context, sess *session.Session, workDir string) (agent.Session, error) {
	if sess.ClaudeSessionID != "" {
		agentSess, err := r.agentRunner.Resume(ctx, fmt.Sprintf("%d", sess.ID), sess.ClaudeSessionID, workDir)
		if err != nil {
			return nil, err
		}
		_ = r.sessionMgr.MarkWaked(sess.ID)
		return agentSess, nil
	}

	agentSess, err := r.agentRunner.Start(ctx, fmt.Sprintf("%d", sess.ID), workDir)
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

// enqueueMessage saves the message to the pending queue and notifies the user.
func (r *Router) enqueueMessage(sess *session.Session, msg channel.Message) {
	r.mu.Lock()
	r.pendingMessages[sess.ID] = append(r.pendingMessages[sess.ID], msg)
	queued := len(r.pendingMessages[sess.ID])
	startedAt, ok := r.thinkingStartedAt[sess.ID]
	r.mu.Unlock()

	elapsed := time.Duration(0)
	if ok {
		elapsed = time.Since(startedAt)
	}
	r.sendReply(msg, r.styler.Box("排队中",
		fmt.Sprintf("Agent 正在思考（已过 %s）\n"+
			"排队消息：%d 条\n"+
			"\n回复 \"/stop\" 中断当前任务\n"+
			"或等待思考完成后逐一处理",
			formatDuration(elapsed), queued)))
}

// drainPendingMessages processes queued messages one by one after the current
// agent process finishes.
func (r *Router) drainPendingMessages(sess *session.Session) {
	time.Sleep(300 * time.Millisecond)

	r.mu.Lock()
	if len(r.pendingMessages[sess.ID]) == 0 {
		delete(r.thinkingStartedAt, sess.ID)
		r.mu.Unlock()
		return
	}
	msg := r.pendingMessages[sess.ID][0]
	r.pendingMessages[sess.ID] = r.pendingMessages[sess.ID][1:]
	delete(r.pendingQuestions, sess.ID)
	r.mu.Unlock()

	log.Printf("[router] draining pending message for session %d", sess.ID)
	r.handleLLM(msg, sess)
}

// parseCommand returns the first word of content if it starts with "/", otherwise empty.
func parseCommand(content string) string {
	if len(content) < 2 {
		return ""
	}
	if content[0] != '/' {
		return ""
	}
	if content[1] == ' ' {
		return ""
	}
	for i, c := range content {
		if c == ' ' {
			return content[:i]
		}
	}
	return content
}
