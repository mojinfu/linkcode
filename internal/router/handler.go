package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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
	case "/img":
		r.handleSendImage(msg, sess)
	case "/cmd":
		r.handleRunFile(msg)
	case "/help":
		r.handleHelp(msg)
	default:
		r.sendReply(msg, fmt.Sprintf("未知命令: %s。发送 /help 查看可用命令。", cmd))
	}
}

const (
	// cmdExecTimeout bounds a /cmd run so a hanging script can't wedge the router.
	cmdExecTimeout = 5 * time.Minute
	// cmdOutputMaxLen caps the result message length for WeCom.
	cmdOutputMaxLen = 4000
)

// handleRunFile executes a local file triggered by /cmd <path>. It acknowledges
// immediately, then runs the file in a goroutine (so a slow script can't block
// message draining) and proactively pushes the full result when done. Proactive
// push is used because the reply stream (aibot_respond_msg) tied to the original
// message can expire (846608) during a long run.
func (r *Router) handleRunFile(msg channel.Message) {
	path := strings.Trim(strings.TrimSpace(strings.TrimPrefix(msg.Content, "/cmd")), `"'`)
	if path == "" {
		r.sendReply(msg, "用法：/cmd <文件路径>\n"+cmdSupportText())
		return
	}
	shell, args, ok := cmdInvocation(path)
	if !ok {
		r.sendReply(msg, fmt.Sprintf("不支持的文件类型：%s\n%s", filepath.Ext(path), cmdSupportText()))
		return
	}
	if shell == "" {
		shell = path // direct execution (Windows .exe; non-Windows any executable)
	}

	r.sendReply(msg, fmt.Sprintf("开始执行 %s ...", path))
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), cmdExecTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, shell, args...)
		cmd.Dir = filepath.Dir(path) // scripts resolve relative paths from their own dir
		out, err := cmd.CombinedOutput()
		text := buildCmdResult(path, out, err)
		ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
		if ok && ch.IsConnected() {
			if err := ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
				Text:   text,
				ChatID: msg.ChatID,
			}); err != nil {
				log.Printf("[router] /cmd result push: %v", err)
			}
		}
	}()
}

// cmdInvocation maps a file to how it should be executed. An empty shell means
// the file is run directly (executable). ok is false for unsupported extensions.
// On Windows the extension decides (scripts run via their shell, .exe directly);
// on other platforms any file is run directly, relying on shebang + exec bit.
func cmdInvocation(path string) (shell string, args []string, ok bool) {
	if runtime.GOOS != "windows" {
		return "", nil, true // run directly (shebang/exec bit)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ps1":
		return "powershell", []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path}, true
	case ".bat", ".cmd":
		// Go's CreateProcess auto-quotes args containing spaces, so cmd /c
		// receives the path correctly quoted even with spaces in it.
		return "cmd", []string{"/c", path}, true
	case ".exe":
		return "", nil, true
	}
	return "", nil, false
}

// cmdSupportText describes the /cmd file types accepted on this platform.
func cmdSupportText() string {
	if runtime.GOOS == "windows" {
		return "支持 .ps1 / .bat / .cmd / .exe"
	}
	return "支持任意可执行文件（脚本需含 shebang 且有执行权限）"
}

// buildCmdResult formats the combined output of a /cmd run for a WeCom message,
// truncating overly long output and appending a note.
func buildCmdResult(path string, out []byte, err error) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "执行失败：%v\n\n", err)
	}
	b.Write(out)
	text := b.String()
	if len(text) > cmdOutputMaxLen {
		text = text[:cmdOutputMaxLen] + fmt.Sprintf("\n...(已截断，共 %d 字节)", len(out))
	}
	return text
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
	delete(r.pendingFragments, sess.ID)
	delete(r.thinkingStartedAt, sess.ID)
	delete(r.sessionUsage, sess.ID)
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
	wd, _ := r.botPool.ResolveWorkDir(botWD)

	var welcome string
	claudeSid := sess.ClaudeSessionID
	b := r.styler.Bold
	if hadClaudeSession {
		welcome = fmt.Sprintf("对话已重置。\n旧 Claude Session：%s\n新 LinkCode Session：%s（Claude Session 将在下条消息时创建）\n\n%s\n%s\n开始新的对话吧！",
			b(claudeSid), b(fmt.Sprintf("%d", newSess.ID)),
			r.styler.Bold("工作目录"),
			r.styler.Box("path", wd))
	} else {
		welcome = fmt.Sprintf("你好，我是你的 Agent「%s」，有什么任务要交给我吗？\n\n%s\n%s\n\n发送 %s 查看可用命令。", sess.Name,
			r.styler.Bold("工作目录"),
			r.styler.Box("path", wd),
			b("/help"))
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
			"\"/img\"                 发送图片：/img 图片路径（仅本机 png/jpg/gif）\n"+
			"\"/cmd\"                 执行本机文件：/cmd <路径>\n"+
			"\"/help\"                显示本帮助\n"+
			"\n直接发文字消息即可与 Agent 对话")
	r.sendReply(msg, help)
}

// handleSendImage sends a local image file as a WeCom image reply. If the
// argument isn't an exact path, it fuzzy-matches image filenames under the
// session's working dir; multiple hits are listed as 1..N and the user picks by
// replying a number (see consumeImageChoice).
// WeCom only allows images as replies (aibot_respond_msg with the incoming
// message's reqID), never proactive pushes.
func (r *Router) handleSendImage(msg channel.Message, sess *session.Session) {
	query := strings.TrimSpace(strings.TrimPrefix(msg.Content, "/img"))
	query = strings.Trim(query, "\"'")
	if query == "" {
		r.sendReply(msg, "用法：/img <图片路径或名称关键字>\n示例：/img D:\\x.png  或  /img logo\n支持 png/jpg/jpeg/gif，≤10MB")
		return
	}
	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok || !ch.IsConnected() {
		r.sendReply(msg, "连接已断开，正在重连，请稍后重试。")
		return
	}
	// Exact path → send directly.
	if _, err := os.Stat(query); err == nil {
		r.sendImageFile(ch, msg, query)
		return
	}
	// Otherwise fuzzy-match image filenames under the working dir.
	wd := r.workDirFor(sess)
	hits := fuzzyMatchImages(wd, query)
	if len(hits) == 0 {
		r.sendReply(msg, fmt.Sprintf("未找到匹配“%s”的图片，请用完整路径或换个关键字。", query))
		return
	}
	if len(hits) == 1 {
		r.sendImageFile(ch, msg, hits[0])
		return
	}
	// Multiple hits → list and wait for a numeric pick.
	r.mu.Lock()
	r.pendingImageChoices[sess.ID] = hits
	r.mu.Unlock()
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("找到 %d 张相似图片，回复数字选择：\n", len(hits)))
	for i, h := range hits {
		rel, err := filepath.Rel(wd, h)
		if err != nil {
			rel = h
		}
		sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, rel))
	}
	r.sendReply(msg, sb.String())
}

// consumeImageChoice handles a numeric reply that selects a fuzzy-matched image.
// Returns true if the message was consumed as an image pick (a valid number while
// a choice is pending); otherwise clears any stale pending choice and returns
// false so normal handling proceeds.
func (r *Router) consumeImageChoice(msg channel.Message, sess *session.Session) bool {
	r.mu.Lock()
	hits := r.pendingImageChoices[sess.ID]
	if hits == nil {
		r.mu.Unlock()
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(msg.Content))
	if err != nil || n < 1 || n > len(hits) {
		delete(r.pendingImageChoices, sess.ID)
		r.mu.Unlock()
		return false
	}
	chosen := hits[n-1]
	delete(r.pendingImageChoices, sess.ID)
	r.mu.Unlock()

	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok || !ch.IsConnected() {
		r.sendReply(msg, "连接已断开，正在重连，请稍后重试。")
		return true
	}
	r.sendImageFile(ch, msg, chosen)
	return true
}

// sendImageFile sends one image file as a reply and confirms via text.
func (r *Router) sendImageFile(ch channel.Channel, msg channel.Message, path string) {
	if err := ch.SendImage(context.Background(), msg.UserID, msg.ReqID, path); err != nil {
		r.sendReply(msg, fmt.Sprintf("发送图片失败：%v", err))
		return
	}
	r.sendReply(msg, "已发送："+filepath.Base(path))
}

// workDirFor returns the effective working directory for a session's bot.
func (r *Router) workDirFor(sess *session.Session) string {
	bot, _ := r.botPool.GetByID(sess.BoundBotID)
	botWD := ""
	if bot != nil {
		botWD = bot.WorkDir
	}
	wd, _ := r.botPool.ResolveWorkDir(botWD)
	return wd
}

// fuzzyMatchImages walks dir and returns up to 10 image files whose name
// contains query (case-insensitive). Only image extensions are considered.
func fuzzyMatchImages(dir, query string) []string {
	q := strings.ToLower(query)
	var hits []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".png", ".jpg", ".jpeg", ".gif":
		default:
			return nil
		}
		if strings.Contains(strings.ToLower(info.Name()), q) {
			hits = append(hits, p)
		}
		return nil
	})
	sort.Strings(hits)
	if len(hits) > 10 {
		hits = hits[:10]
	}
	return hits
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
	idleIdx := 0 // index into idleRefreshSchedule; advances as the agent stays idle
	ticker := time.NewTicker(idleRefreshSchedule[0])
	defer ticker.Stop()

	var cache streamCache
	streamBroken := false
	hadContent := false
	var turnCostUSD float64

	streamTimeout := time.Duration(0)
	if ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID); ok {
		streamTimeout = ch.StreamTimeout()
	}

	processTimeout := time.After(10 * time.Minute)

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
			processTimeout = time.After(10 * time.Minute)
			spinnerDotIdx++
			log.Printf("[router] chunk kind=%s contentLen=%d", chunk.Kind, len(chunk.Content))
			switch chunk.Kind {
			case agent.KindError:
				log.Printf("[router] agent error: %s", chunk.Content)
				r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})
				r.sendStreamReply(msg, r.styler.Bar("[💫] error")+"\n"+chunk.Content, streamID, true)
				return
			case agent.KindText:
				hadContent = true
				cache.textBuf.WriteString(chunk.Content)
				if !r.sendStreamReply(msg, buildStatusBar()+"\n"+cache.textBuf.String(), streamID, false) {
					streamBroken = true
				}
				// Tokens are flowing again — restart the schedule from the fast end.
				idleIdx = 0
				ticker.Reset(idleRefreshSchedule[0])
			case agent.KindQuestion:
				cache.question = chunk.Question
			case agent.KindFinal:
				hadContent = true
				cache.fullResponse = chunk.Content
				if chunk.TokenUsage != nil && chunk.TokenUsage.InputTokens > 0 {
					r.mu.Lock()
					u := r.sessionUsage[sess.ID]
					if u == nil {
						u = &sessionUsageRec{accCost: sess.TotalCost}
						r.sessionUsage[sess.ID] = u
					}
					prevIn := u.inputTokens
					prevOut := u.outputTokens
					prevCache := u.cacheReadTokens
					u.inputTokens += chunk.TokenUsage.InputTokens
					u.outputTokens += chunk.TokenUsage.OutputTokens
					u.cacheReadTokens += chunk.TokenUsage.CacheReadTokens
					if chunk.TokenUsage.Model != "" {
						u.model = chunk.TokenUsage.Model
					}
					turnCost := r.pricingCalc.Cost(u.model, u.inputTokens-prevIn, u.cacheReadTokens-prevCache, u.outputTokens-prevOut)
					u.accCost += turnCost
					turnCostUSD = turnCost
					r.mu.Unlock()
					_ = r.sessionMgr.SetCost(sess.ID, u.accCost)
				}
				goto done // final=success: ignore subsequent non-zero exit chunk (e.g. BigModel compat)
			case agent.KindThinking, agent.KindToolUse:
				if chunk.Content != "" {
					idleIdx = 0
					ticker.Reset(idleRefreshSchedule[0])
				}
			}
		case <-processTimeout:
			if streamBroken {
				continue
			}
			// Warn the user but don't force-kill — the user decides whether to /stop.
			r.sendStreamReply(msg, r.styler.Bar("[⏰] 超时")+"\nAgent 已运行 10 分钟仍未结束，如需终止请回复 /stop", streamID, false)
			processTimeout = time.After(10 * time.Minute)

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
			// Advance through the schedule and loop back to the fast start once
			// exhausted. One full loop (30 frames / ~62s ≈ 29/min) stays under
			// WeCom's 30-msgs/min, and looping keeps the cadence lively instead
			// of sticking at the slow tail.
			idleIdx = (idleIdx + 1) % len(idleRefreshSchedule)
			ticker.Reset(idleRefreshSchedule[idleIdx])
			log.Printf("[router] ticker frame icon=%d dots=%d interval=%v", spinnerIconIdx%len(spinnerRunes), spinnerDotIdx%4, idleRefreshSchedule[idleIdx])
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

	if !hadContent {
		r.statusMgr.Send(StatusEvent{SessionID: sess.ID, State: StateDizzy})
		doneText := r.styler.Bar("[💫] 无响应") + "\nAgent 进程异常退出，请稍后重试"
		r.sendStreamReply(msg, doneText, streamID, true)
		return
	}

	responseText := cache.textBuf.String()
	if responseText == "" {
		responseText = cache.fullResponse
	}

	costLine := r.buildCostLine(sess.ID, turnCostUSD)

	if streamBroken {
		if responseText != "" {
			ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
			if ok && ch.IsConnected() {
				prefix := quotePrefix(r.styler, msg.Content, 30)
				doneText := r.styler.Bar("[✓] stand by"+costLine) + "\n\n" + prefix + "\n" + responseText
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
		doneText := r.styler.Bar("[✓] stand by"+costLine) + "\n\n" + responseText
		r.sendStreamReply(msg, doneText, streamID, true)

		if cache.question != nil {
			r.mu.Lock()
			r.pendingQuestions[sess.ID] = cache.question
			r.mu.Unlock()
			r.sendQuestionMenu(msg, cache.question)
		} else {
			// Task finished with no pending question — proactively push a
			// "done" so the phone gets a notification. The [✓] stand by
			// reply above only updates the chat bubble and doesn't push.
			r.sendDonePush(msg)
		}
	}

	if responseText != "" {
		_ = r.sessionMgr.AddMessage(sess.ID, "agent", responseText, "text")
	}
}

// sendDonePush proactively pushes a short "done" message after a turn completes
// normally. The [✓] stand by reply (aibot_respond_msg) only updates the chat
// bubble and doesn't trigger a phone notification; a proactive aibot_send_msg
// does. It runs in a goroutine so a slow ack never blocks queued-message
// draining. The ZWSP suffix varies per push so WeCom doesn't dedup identical
// "done" messages.
func (r *Router) sendDonePush(msg channel.Message) {
	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok || !ch.IsConnected() {
		return
	}
	seq := r.doneSeq.Add(1)
	text := "done" + r.styler.DiffSuffix(int(seq))
	go func() {
		if err := ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
			Text:   text,
			ChatID: msg.ChatID,
		}); err != nil {
			log.Printf("[router] sendDonePush: %v", err)
		}
	}()
}

// buildCostLine returns a cost display string for the standby bar.
// prevCost is the cumulative cost before this turn, turnCost is this turn's cost.
// Returns empty string if no cost data is available.
func (r *Router) buildCostLine(sessionID int64, turnCostUSD float64) string {
	r.mu.Lock()
	u := r.sessionUsage[sessionID]
	r.mu.Unlock()
	if u == nil {
		return ""
	}
	if u.accCost <= 0 {
		return ""
	}
	prevCost := u.accCost - turnCostUSD
	symbol := r.pricingCalc.Symbol(u.model)
	return ", " + r.styler.Cost(prevCost, turnCostUSD, symbol)
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
