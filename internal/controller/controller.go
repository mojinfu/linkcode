// Package controller implements the main control bot's menu-based interaction logic.
package controller

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"linkcode/internal/botpool"
	"linkcode/internal/channel"
	"linkcode/internal/gateway"
	"linkcode/internal/session"
)

// MenuState tracks where a user is in the menu flow.
type MenuState int

const (
	MenuNone             MenuState = iota
	MenuMain
	MenuCreateAgentName   // waiting for agent name
	MenuCreateAgentBotID  // waiting for bot ID
	MenuCreateAgentSecret // waiting for secret
	MenuCreateAgentType   // waiting for agent type
	MenuSetDefaultWorkDir // waiting for new work_dir path
	MenuList              // viewing agent list, reply with number to reconnect
)

type listSessionItem struct {
	sessionID int64
	botID     int64
	name      string
	connected bool
}

type userState struct {
	state        MenuState
	updatedAt    time.Time
	tmpAgentName string // agent name being created
	tmpBotID     string // platform bot ID being created
	tmpSecret    string // decrypted secret being created
	listSessions []listSessionItem // cached for reconnect
}

// Controller orchestrates the main control bot.
type Controller struct {
	sessionMgr   *session.Manager
	botPool      *botpool.Pool
	gw           *gateway.Gateway
	styler       channel.Styler
	claudeCodePath string // path to claude CLI, used by /restart

	mu         sync.Mutex
	userStates map[string]*userState
}

// New creates a new Controller.
func New(sessMgr *session.Manager, pool *botpool.Pool, gw *gateway.Gateway, styler channel.Styler, claudeCodePath string) *Controller {
	return &Controller{
		sessionMgr:    sessMgr,
		botPool:       pool,
		gw:            gw,
		styler:        styler,
		claudeCodePath: claudeCodePath,
		userStates:    make(map[string]*userState),
	}
}

// HandleMessage processes an incoming message to the control bot.
func (c *Controller) HandleMessage(ctx context.Context, msg channel.Message) string {
	text := strings.TrimSpace(msg.Content)
	userID := msg.UserID

	// If the user quoted a bot message, detect the menu stage from the quote.
	if msg.QuoteContent != "" {
		if state := detectMenuStage(msg.QuoteContent); state != MenuNone {
			c.setState(userID, state)
		}
	}

	switch {
	case strings.HasPrefix(text, "/start"):
		return c.showMainMenu(userID)
	case strings.HasPrefix(text, "/help"):
		return c.handleHelp()
	case strings.HasPrefix(text, "/addbot"):
		return c.handleAddBot(userID, text)
	case strings.HasPrefix(text, "/list"):
		return c.handleList(userID)
	case strings.HasPrefix(text, "/restart"):
		return c.handleRestart(ctx, msg)
	}

	state := c.getState(userID)
	switch state.state {
	case MenuMain:
		return c.handleMainMenuChoice(userID, text)
	case MenuCreateAgentName:
		return c.handleCreateAgentName(userID, text)
	case MenuCreateAgentBotID:
		return c.handleCreateAgentBotID(userID, text)
	case MenuCreateAgentSecret:
		return c.handleCreateAgentSecret(userID, text)
	case MenuCreateAgentType:
		return c.handleCreateAgentType(ctx, userID, text)
	case MenuSetDefaultWorkDir:
		return c.handleSetDefaultWorkDir(userID, text)
	case MenuList:
		return c.handleListChoice(ctx, userID, text)
	default:
		return c.showMainMenu(userID)
	}
}

func (c *Controller) showMainMenu(userID string) string {
	c.setState(userID, MenuMain)
	return c.styler.Box("菜单",
		"\"1. 创建 Agent\"   —  录入 Bot 凭证\n"+
			"\"2. 查看 Agent\"   —  运行状态与工作目录\n"+
			"\"3. 默认工作目录\" —  新建时使用此路径\n"+
			"\n回复 1-3")
}

func (c *Controller) handleHelp() string {
	help := "\"/start\"   显示主菜单\n" +
		"\"/addbot\"  快速创建 Agent：/addbot 名称 BotID Secret\n" +
		"\"/list\"    查看所有 Agent 的状态与工作目录\n" +
		"\"/restart\" 重启 LinkCode\n" +
		"\"/help\"    显示本帮助\n" +
		"\n推荐通过主菜单分步创建 Agent"

	return c.styler.Box("总控命令", help)
}

func (c *Controller) handleMainMenuChoice(userID, text string) string {
	switch text {
	case "1":
		return c.enterCreateAgentName(userID)
	case "2":
		return c.handleList(userID)
	case "3":
		return c.enterSetDefaultWorkDir(userID)
	default:
		return c.styler.Box("菜单", "请回复数字 1-3，或发送 /start 重新开始\n\n💡 试试引用本条消息后回复数字")
	}
}

// ============================================================================
// create agent (step-by-step flow)
// ============================================================================

func (c *Controller) enterCreateAgentName(userID string) string {
	c.setState(userID, MenuCreateAgentName)
	return c.styler.Box("创建 Agent", "请输入 Agent 名称：\n\n（回复 0 取消）")
}

func (c *Controller) handleCreateAgentName(userID, text string) string {
	text = strings.TrimSpace(text)
	if text == "0" {
		return c.showMainMenu(userID)
	}
	name := text
	if name == "" {
		return c.styler.Box("创建 Agent", "名称不能为空，请重新输入：\n\n（回复 0 取消）")
	}

	st := c.getState(userID)
	st.tmpAgentName = name
	c.setState(userID, MenuCreateAgentBotID)

	return c.styler.Box("创建 Agent", fmt.Sprintf("\"Agent 名称：「%s」\n\n请输入企微 BotID：\n\n💡 企业微信管理后台 → 应用管理 → AI 机器人 → 详情页\n（回复 0 取消）", name))
}

func (c *Controller) handleCreateAgentBotID(userID, text string) string {
	text = strings.TrimSpace(text)
	if text == "0" {
		return c.showMainMenu(userID)
	}
	botID := text
	if botID == "" {
		return c.styler.Box("创建 Agent", "BotID 不能为空，请重新输入：\n\n（回复 0 取消）")
	}

	st := c.getState(userID)
	if st == nil {
		return c.showMainMenu(userID)
	}
	st.tmpBotID = botID
	c.setState(userID, MenuCreateAgentSecret)

	return c.styler.Box("创建 Agent", fmt.Sprintf("\"Agent 名称：「%s」\nBotID：%s\n\n请输入 Bot Secret：\n\n💡 与 BotID 在同一页面，点击「查看 Secret」获取\n（回复 0 取消）", st.tmpAgentName, botID))
}

func (c *Controller) handleCreateAgentSecret(userID, text string) string {
	text = strings.TrimSpace(text)
	if text == "0" {
		return c.showMainMenu(userID)
	}
	secret := text
	if secret == "" {
		return c.styler.Box("创建 Agent", "Secret 不能为空，请重新输入：\n\n（回复 0 取消）")
	}

	st := c.getState(userID)
	if st == nil {
		return c.showMainMenu(userID)
	}
	st.tmpSecret = secret
	c.setState(userID, MenuCreateAgentType)

	return c.styler.Box("创建 Agent", fmt.Sprintf("\"Agent 名称：「%s」\nBotID：%s\nSecret：已输入\n\n请选择 Agent 类型：\n1. Claude Code\n\n请回复数字 1\n（回复 0 取消）", st.tmpAgentName, st.tmpBotID))
}

func (c *Controller) handleCreateAgentType(ctx context.Context, userID, text string) string {
	text = strings.TrimSpace(text)
	if text == "0" {
		return c.showMainMenu(userID)
	}

	var agentType string
	switch text {
	case "1":
		agentType = "claude-code"
	default:
		return c.styler.Box("创建 Agent", "请回复数字 1 选择 Claude Code\n\n（回复 0 取消）")
	}

	st := c.getState(userID)
	if st == nil {
		return c.showMainMenu(userID)
	}

	name := st.tmpAgentName
	botID := st.tmpBotID
	secret := st.tmpSecret

	c.setState(userID, MenuNone)

	// Resolve work dir for the new agent.
	wd, _ := c.botPool.ResolveWorkDir("")

	bot, err := c.botPool.Add(botID, name, wd, secret)
	if err != nil {
		return fmt.Sprintf("创建失败：%v\n发送 /start 返回主菜单。", err)
	}

	// Create session and bind bot.
	sess, err := c.sessionMgr.Create(name, agentType, "", 0)
	if err != nil {
		return fmt.Sprintf("创建 Session 失败：%v\n发送 /start 返回主菜单。", err)
	}

	if err := c.botPool.BindToSession(bot.ID, sess.ID); err != nil {
		return fmt.Sprintf("绑定失败：%v\n发送 /start 返回主菜单。", err)
	}

	// Open worker bot WebSocket connection.
	if err := c.gw.OpenWorkerChannel(ctx, bot.ID, bot.BotID, bot.Secret); err != nil {
		c.botPool.Release(bot.ID)
		return fmt.Sprintf("Agent「%s」连接失败：%v", name, err)
	}

	// Send proactive welcome via the worker bot.
	workerCh, ok := c.gw.GetWorkerChannel(bot.ID)
	if !ok {
		c.botPool.Release(bot.ID)
		return "内部错误：找不到 Agent 连接"
	}

	b := c.styler.Bold
	welcome := fmt.Sprintf("你好，我是你的 %s「%s」\n工作目录：%s\n发送 %s 查看可用命令。",
		agentTypeDisplay(agentType), name, wd, b("/help"))
	if err := workerCh.SendMessage(ctx, userID, channel.MessageContent{
		Text:   welcome,
		ChatID: userID,
	}); err != nil {
		log.Printf("[controller] send welcome via bot %s failed: %v", bot.Name, err)
		if isFirstContactErr(err) {
			return fmt.Sprintf("Agent「%s」创建成功！\n\n这是你首次与此 Bot 建立联系，企业微信要求你先主动发起对话。请在企微中搜索 %s 并进入聊天，之后即可正常对话。", name, b(name))
		}
		return fmt.Sprintf("Agent「%s」已创建，但向你发送消息失败：%v\n请尝试在企业微信中搜索 %s 并进入聊天。", name, err, b(name))
	}

	log.Printf("[controller] created agent %s (type: %s, bot: %s, session: %d)", name, agentType, bot.BotID, sess.ID)

	return fmt.Sprintf("Agent「%s」（%s）创建成功！已经在企业微信中给你发了消息，去看看吧。\n\n发送 /start 返回主菜单。", b(name), agentTypeDisplay(agentType))
}

// ============================================================================
// direct /addbot command (kept for power users)
// ============================================================================

func (c *Controller) handleAddBot(userID, text string) string {
	parts := strings.Fields(text)
	if len(parts) < 4 {
		return "参数不足。请使用：/addbot <名称> <BotID> <Secret>\n\n<名称>  给 Agent 起个名字\n<BotID> 企微机器人的 BotID（管理后台 → 应用管理 → AI 机器人 → 详情页）\n<Secret> 同一页面的 Secret\n\n💡 推荐通过主菜单「1. 创建新 Agent」分步骤操作，更友好。"
	}

	name := parts[1]
	botID := parts[2]
	secret := parts[3]

	c.setState(userID, MenuNone)

	wd, _ := c.botPool.ResolveWorkDir("")
	bot, err := c.botPool.Add(botID, name, wd, secret)
	if err != nil {
		return fmt.Sprintf("创建失败：%v", err)
	}

	agentType := "claude-code"
	sess, err := c.sessionMgr.Create(name, agentType, "", 0)
	if err != nil {
		return fmt.Sprintf("创建 Session 失败：%v", err)
	}
	if err := c.botPool.BindToSession(bot.ID, sess.ID); err != nil {
		return fmt.Sprintf("绑定失败：%v", err)
	}

	ctx := context.Background()
	if err := c.gw.OpenWorkerChannel(ctx, bot.ID, bot.BotID, bot.Secret); err != nil {
		c.botPool.Release(bot.ID)
		return fmt.Sprintf("Agent「%s」连接失败：%v", name, err)
	}

	workerCh, ok := c.gw.GetWorkerChannel(bot.ID)
	if !ok {
		c.botPool.Release(bot.ID)
		return "内部错误：找不到 Agent 连接"
	}

	b := c.styler.Bold
	welcome := fmt.Sprintf("你好，我是你的 %s「%s」\n工作目录：%s\n发送 %s 查看可用命令。",
		agentTypeDisplay(agentType), name, wd, b("/help"))
	if err := workerCh.SendMessage(ctx, userID, channel.MessageContent{
		Text:   welcome,
		ChatID: userID,
	}); err != nil {
		log.Printf("[controller] send welcome via bot %s failed: %v", bot.Name, err)
		if isFirstContactErr(err) {
			return fmt.Sprintf("Agent「%s」（%s）创建成功！\n\n这是你首次与此 Bot 建立联系，企业微信要求你先主动发起对话。请在企微中搜索 %s 并进入聊天，之后即可正常对话。", name, agentTypeDisplay(agentType), b(name))
		}
		return fmt.Sprintf("Agent「%s」已创建，但向你发送消息失败：%v", name, err)
	}

	log.Printf("[controller] /addbot: created agent %s (type: %s, bot: %s, session: %d)", name, agentType, bot.BotID, sess.ID)
	return fmt.Sprintf("Agent「%s」（%s）创建成功！已经在企业微信中给你发了消息。", b(name), agentTypeDisplay(agentType))
}

// ============================================================================
// list agents
// ============================================================================

func (c *Controller) handleList(userID string) string {
	sessions, err := c.sessionMgr.ListActive()
	if err != nil {
		return fmt.Sprintf("查询失败：%v", err)
	}

	var valid []session.Session
	for _, s := range sessions {
		if s.BoundBotID > 0 {
			valid = append(valid, s)
		}
	}

	if len(valid) == 0 {
		c.setState(userID, MenuNone)
		return "当前没有 Agent。回复 1 创建一个吧！\n发送 /start 返回主菜单。"
	}

	connStatuses := c.gw.GetAllWorkerStatuses()

	var sb strings.Builder
	sb.WriteString("你的 Agent 列表：\n\n")

	// Cache list items for reconnect feature.
	st := c.getState(userID)
	st.listSessions = make([]listSessionItem, 0, len(valid))

	for i, s := range valid {
		status := "🟢 运行中"
		if s.ProcessStatus == "sleeped" {
			status = "💤 休眠"
		}

		connStatus := "⚪ 未知"
		isConnected := false
		if cs, ok := connStatuses[s.BoundBotID]; ok {
			isConnected = cs.Connected
			if cs.Connected {
				connStatus = "🔗 已连接"
			} else {
				since := time.Since(cs.LastChange)
				if since < time.Minute {
					connStatus = "🔴 已断开"
				} else {
					connStatus = fmt.Sprintf("🔴 已断开 (%s)", formatDurationShort(since))
				}
			}
		}

		st.listSessions = append(st.listSessions, listSessionItem{
			sessionID: s.ID,
			botID:     s.BoundBotID,
			name:      s.Name,
			connected: isConnected,
		})

		bot, _ := c.botPool.GetByID(s.BoundBotID)
		botName := "—"
		botWD := ""
		if bot != nil {
			botName = bot.Name
			botWD = bot.WorkDir
		}

		wd, source := c.botPool.ResolveWorkDir(botWD)

		sb.WriteString(fmt.Sprintf("%d. %s  %s  %s\n", i+1, s.Name, status, connStatus))
		sb.WriteString(fmt.Sprintf("   Bot：%s\n", botName))
		sb.WriteString(fmt.Sprintf("   工作目录：%s（%s）\n\n", wd, source))
	}

	sb.WriteString("💡 引用本条消息回复数字可重连断开的 Agent")

	c.setState(userID, MenuList)
	return c.styler.Box("Agent 列表", sb.String())
}

// isFirstContactErr checks if the error is the WeCom "never contacted" limit (846607).
func isFirstContactErr(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "846607") || strings.Contains(err.Error(), "frequency limit"))
}

func agentTypeDisplay(t string) string {
	switch t {
	case "claude-code":
		return "Claude Code"
	default:
		return t
	}
}

// ============================================================================
// set default work directory (Layer 1)
// ============================================================================

func (c *Controller) enterSetDefaultWorkDir(userID string) string {
	c.setState(userID, MenuSetDefaultWorkDir)

	current, err := c.botPool.DB().GetSetting("default_work_dir")
	if err != nil || current == "" {
		current = "（未设置，使用配置文件或进程目录）"
	}

	return c.styler.Box("默认工作目录", fmt.Sprintf("当前：%s\n\n请输入新路径（必须是存在的目录）：\n（回复 0 返回菜单）", current))
}

func (c *Controller) handleSetDefaultWorkDir(userID, text string) string {
	text = strings.TrimSpace(text)
	if text == "" || text == "0" {
		return c.showMainMenu(userID)
	}

	if !botpool.DirExists(text) {
		return c.styler.Box("默认工作目录", fmt.Sprintf("目录不存在或无法访问：%s\n\n请检查路径后重试。\n回复 0 返回菜单。", text))
	}

	if err := c.botPool.DB().SetSetting("default_work_dir", text); err != nil {
		log.Printf("[controller] set default_work_dir: %v", err)
		return fmt.Sprintf("保存失败：%v\n发送 /start 返回主菜单。", err)
	}

	return c.styler.Box("默认工作目录", fmt.Sprintf("已更新：%s\n\n新建的 Agent 将默认使用此目录，已创建的 Agent 不受影响。\n发送 /start 返回主菜单。", text))
}

// ============================================================================
// state management
// ============================================================================

func (c *Controller) getState(userID string) *userState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.userStates[userID]; ok {
		s.updatedAt = time.Now()
		return s
	}
	s := &userState{state: MenuNone, updatedAt: time.Now()}
	c.userStates[userID] = s
	return s
}

func (c *Controller) setState(userID string, state MenuState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.userStates[userID]; ok {
		s.state = state
		s.updatedAt = time.Now()
	} else {
		c.userStates[userID] = &userState{state: state, updatedAt: time.Now()}
	}
}

// handleListChoice handles a numbered reply to the /list output, triggering
// reconnection for the chosen agent if it is disconnected.
func (c *Controller) handleListChoice(ctx context.Context, userID, text string) string {
	text = strings.TrimSpace(text)
	if text == "0" {
		return c.showMainMenu(userID)
	}

	idx := 0
	if _, err := fmt.Sscanf(text, "%d", &idx); err != nil || idx < 1 {
		return c.styler.Box("Agent 列表", "请回复数字选择要重连的 Agent，或回复 0 返回菜单\n\n💡 引用本条消息后回复数字")
	}

	st := c.getState(userID)
	if idx > len(st.listSessions) {
		return c.styler.Box("Agent 列表", fmt.Sprintf("没有第 %d 个 Agent，请重新选择（1-%d）：\n或回复 0 返回菜单", idx, len(st.listSessions)))
	}

	item := st.listSessions[idx-1]

	// If already connected, no need to reconnect.
	if item.connected {
		return c.styler.Box("Agent 列表", fmt.Sprintf("「%s」已连接，无需重连。\n\n回复其他数字重连其他 Agent，或回复 0 返回菜单。", item.name))
	}

	// Fetch bot credentials.
	bot, err := c.botPool.GetByID(item.botID)
	if err != nil || bot == nil {
		return fmt.Sprintf("获取 Bot 凭证失败：%v\n发送 /start 返回主菜单。", err)
	}

	log.Printf("[controller] reconnecting worker bot %d (%s) for agent %s", item.botID, bot.BotID, item.name)
	if err := c.gw.ReopenWorkerChannel(ctx, item.botID, bot.BotID, bot.Secret); err != nil {
		log.Printf("[controller] reconnect failed: %v", err)
		return fmt.Sprintf("「%s」重连失败：%v\n发送 /start 返回主菜单。", item.name, err)
	}

	// Update cached state so user sees the new status immediately.
	st.listSessions[idx-1].connected = true
	c.setState(userID, MenuList)

	return c.styler.Box("Agent 列表", fmt.Sprintf("「%s」已重连成功！\n\n回复其他数字重连其他 Agent，或回复 0 返回菜单。", item.name))
}

// handleRestart delegates the restart to Claude Code.
// Claude knows the project layout and OS conventions — it runs the restart
// command (make.ps1 restart) while linkcode exits immediately.
func (c *Controller) handleRestart(ctx context.Context, msg channel.Message) string {
	ctrlChan := c.gw.ControlChannel()
	if ctrlChan != nil {
		ctrlChan.SendMessage(ctx, msg.UserID, channel.MessageContent{
			Text:      "正在重启 LinkCode...",
			ReplyToID: msg.ID,
			ChatID:    msg.ChatID,
		})
	}

	log.Printf("[controller] restart: spawning claude to handle restart")
	cmd := exec.Command(c.claudeCodePath, "-p",
		"请重启 linkcode 服务。项目根目录有 make.ps1 脚本，运行 `powershell -File make.ps1 restart` 即可完成重启。重启完成后退出。",
		"--permission-mode", "bypassPermissions",
		"--output-format", "text")

	if err := cmd.Start(); err != nil {
		log.Printf("[controller] restart: spawn claude failed: %v", err)
		return fmt.Sprintf("启动重启进程失败：%v", err)
	}

	log.Printf("[controller] restart: claude pid %d spawned, exiting linkcode", cmd.Process.Pid)
	go cmd.Wait()

	time.Sleep(200 * time.Millisecond)
	os.Exit(0)
	return "" // unreachable
}

// detectMenuStage inspects quoted text to determine which menu the user is replying to.
func detectMenuStage(quotedText string) MenuState {
	switch {
	case strings.Contains(quotedText, "\"1. 创建 Agent\""):
		return MenuMain
	case strings.Contains(quotedText, "Agent 列表"):
		return MenuList
	case strings.Contains(quotedText, "请输入 Agent 名称"):
		return MenuCreateAgentName
	case strings.Contains(quotedText, "请输入企微 BotID"):
		return MenuCreateAgentBotID
	case strings.Contains(quotedText, "请输入 Bot Secret"):
		return MenuCreateAgentSecret
	case strings.Contains(quotedText, "请选择 Agent 类型"):
		return MenuCreateAgentType
	case strings.Contains(quotedText, "设定 Agent 默认工作目录"):
		return MenuSetDefaultWorkDir
	}
	return MenuNone
}

// formatDurationShort returns a compact human-readable duration string.
func formatDurationShort(d time.Duration) string {
	if d < time.Minute {
		return "刚刚"
	}
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%d分钟前", m)
	}
	h := m / 60
	if h < 24 {
		return fmt.Sprintf("%d小时前", h)
	}
	days := h / 24
	return fmt.Sprintf("%d天前", days)
}
