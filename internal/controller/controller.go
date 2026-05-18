// Package controller implements the main control bot's menu-based interaction logic.
package controller

import (
	"context"
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

// MenuState tracks where a user is in the menu flow.
type MenuState int

const (
	MenuNone    MenuState = iota
	MenuMain
	MenuCreateAgentType
	MenuCreateAgentName
	MenuEndAgentConfirm
)

type userState struct {
	state        MenuState
	updatedAt    time.Time
	tmpAgentType string
}

// Controller orchestrates the main control bot.
type Controller struct {
	sessionMgr  *session.Manager
	botPool     *botpool.Pool
	agentRunner agent.Runner
	gw          *gateway.Gateway

	mu         sync.Mutex
	userStates map[string]*userState
}

// New creates a new Controller.
func New(sessMgr *session.Manager, pool *botpool.Pool, runner agent.Runner, gw *gateway.Gateway) *Controller {
	return &Controller{
		sessionMgr:  sessMgr,
		botPool:     pool,
		agentRunner: runner,
		gw:          gw,
		userStates:  make(map[string]*userState),
	}
}

// HandleMessage processes an incoming message to the control bot.
// Returns the reply text and optionally a callback for side effects (e.g., worker bot connect).
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
	case strings.HasPrefix(text, "/addbot"):
		return c.handleAddBot(userID, text)
	case strings.HasPrefix(text, "/list"):
		return c.handleList(userID)
	case strings.HasPrefix(text, "/end"):
		return c.handleEnd(userID, text)
	}

	state := c.getState(userID)
	switch state.state {
	case MenuMain:
		return c.handleMainMenuChoice(userID, text)
	case MenuCreateAgentType:
		return c.handleAgentTypeChoice(userID, text)
	case MenuCreateAgentName:
		return c.handleAgentNameInput(ctx, userID, text)
	case MenuEndAgentConfirm:
		return c.handleEndChoice(userID, text)
	default:
		return c.showMainMenu(userID)
	}
}

func (c *Controller) showMainMenu(userID string) string {
	c.setState(userID, MenuMain)
	return `欢迎使用 LinkCode！请选择操作：
1. 创建新 Agent
2. 查看我的 Agent 列表
3. 添加新 Bot（录入凭证到池中）
4. 查看 Bot 池状态
5. 结束 Agent

请回复数字 1-5
💡 引用本条消息后回复数字，选择更准确`
}

func (c *Controller) handleMainMenuChoice(userID, text string) string {
	switch text {
	case "1":
		c.setState(userID, MenuCreateAgentType)
		c.getState(userID).tmpAgentType = ""
		return "请选择 Agent 类型：\n1. Claude Code\n\n请回复数字 1"
	case "2":
		return c.handleList(userID)
	case "3":
		c.setState(userID, MenuNone)
		return "请发送 Bot 的凭证信息，格式为：\n/addbot <Bot名称> <BotID> <Secret>\n示例：/addbot 助手A aibwxxxx xxxxxxxx"
	case "4":
		return c.handleBotPoolStatus()
	case "5":
		return c.handleEndList(userID)
	default:
		return "请回复数字 1-5，或发送 /start 重新开始\n💡 试试引用本条消息后回复数字"
	}
}

func (c *Controller) handleAgentTypeChoice(userID, text string) string {
	switch text {
	case "1":
		c.setState(userID, MenuCreateAgentName)
		c.getState(userID).tmpAgentType = "claude-code"
		return `请为这个 Agent 命名（输入名称或回复"跳过"使用默认名称）`
	default:
		return "请回复数字 1 选择 Claude Code"
	}
}

func (c *Controller) handleAgentNameInput(ctx context.Context, userID, name string) string {
	state := c.getState(userID)
	agentType := state.tmpAgentType
	if name == "跳过" || name == "" {
		if len(userID) > 6 {
			name = fmt.Sprintf("Agent-%s", userID[:6])
		} else {
			name = fmt.Sprintf("Agent-%s", userID)
		}
	}
	c.setState(userID, MenuNone)

	// 1. Create session record.
	sess, err := c.sessionMgr.Create(name, agentType, "", 0)
	if err != nil {
		return fmt.Sprintf("创建 Session 失败：%v", err)
	}

	// 2. Allocate bot from pool.
	bot, err := c.botPool.Allocate(sess.ID)
	if err != nil {
		return fmt.Sprintf("分配 Bot 失败：%v\n请先通过 /addbot 添加可用 Bot。", err)
	}

	// 3. Open worker bot WebSocket connection.
	if err := c.gw.OpenWorkerChannel(ctx, bot.ID, bot.BotID, bot.Secret); err != nil {
		c.botPool.Release(bot.ID)
		return fmt.Sprintf("Bot「%s」连接失败：%v", bot.Name, err)
	}

	// 4. Send proactive welcome via the worker bot and wait for ack.
	workerCh, ok := c.gw.GetWorkerChannel(bot.ID)
	if !ok {
		c.botPool.Release(bot.ID)
		return "内部错误：找不到 Worker Bot 连接"
	}

	welcome := fmt.Sprintf("你好，我是你的 %s「%s」，有什么任务要交给我吗？", agentType, name)
	if err := workerCh.SendMessage(ctx, userID, channel.MessageContent{
		Text:   welcome,
		ChatID: userID,
	}); err != nil {
		log.Printf("[controller] send welcome via bot %s failed: %v", bot.Name, err)
		return fmt.Sprintf("Agent「%s」已创建，但 Bot 向你发送消息失败：%v\n请尝试在企微中搜索「%s」并进入聊天。", name, err, bot.Name)
	}

	log.Printf("[controller] created session %d (%s) with bot %s (platform: %s), welcome acked", sess.ID, name, bot.Name, bot.BotID)

	return fmt.Sprintf("Agent「%s」已创建！Bot「%s」已经给你发了消息，去看看企微的聊天列表吧。", name, bot.Name)
}

func (c *Controller) handleAddBot(userID, text string) string {
	c.setState(userID, MenuNone)

	parts := strings.Fields(text)
	if len(parts) < 4 {
		return "格式错误。请使用：/addbot <Bot名称> <BotID> <Secret>"
	}

	name := parts[1]
	botID := parts[2]
	secret := parts[3]

	_, err := c.botPool.Add(botID, name, secret)
	if err != nil {
		return fmt.Sprintf("添加 Bot 失败：%v", err)
	}

	allBots, _ := c.botPool.List()
	idleCount := 0
	for _, b := range allBots {
		if b.Status == "idle" {
			idleCount++
		}
	}

	return fmt.Sprintf("Bot「%s」已录入池中，当前可用 Bot：%d 个", name, idleCount)
}

func (c *Controller) handleList(userID string) string {
	c.setState(userID, MenuNone)

	sessions, err := c.sessionMgr.ListActive()
	if err != nil {
		return fmt.Sprintf("查询失败：%v", err)
	}

	// Filter: only show sessions with a valid bot binding.
	var valid []session.Session
	for _, s := range sessions {
		if s.BoundBotID > 0 {
			valid = append(valid, s)
		}
	}

	if len(valid) == 0 {
		return "当前没有活跃的 Agent。发送 /start 创建一个吧！"
	}

	var sb strings.Builder
	sb.WriteString("你的活跃 Agent：\n")
	for i, s := range valid {
		status := "运行中"
		if s.ProcessStatus == "sleeped" {
			status = "休眠"
		}
		sb.WriteString(fmt.Sprintf("%d. %s (%s) - %s\n", i+1, s.Name, s.AgentType, status))
	}
	sb.WriteString("\n发送 /start 返回主菜单")
	return sb.String()
}

func (c *Controller) handleEnd(userID, text string) string {
	c.setState(userID, MenuNone)

	parts := strings.Fields(text)
	if len(parts) >= 2 {
		return c.endSessionByRef(parts[1])
	}
	return c.handleEndList(userID)
}

func (c *Controller) handleEndList(userID string) string {
	sessions, err := c.sessionMgr.ListActive()
	if err != nil || len(sessions) == 0 {
		return "当前没有可结束的 Agent。"
	}

	// Filter: only show sessions with a valid bot binding.
	var valid []session.Session
	for _, s := range sessions {
		if s.BoundBotID > 0 {
			valid = append(valid, s)
		}
	}
	if len(valid) == 0 {
		return "当前没有可结束的 Agent。"
	}

	c.setState(userID, MenuEndAgentConfirm)

	var sb strings.Builder
	sb.WriteString("请选择要结束的 Agent（回复数字）：\n")
	for i, s := range valid {
		status := "运行中"
		if s.ProcessStatus == "sleeped" {
			status = "休眠"
		}
		sb.WriteString(fmt.Sprintf("%d. %s (%s) - %s\n", i+1, s.Name, s.AgentType, status))
	}
	sb.WriteString("回复 0 取消\n💡 引用本条消息后回复数字，选择更准确")
	return sb.String()
}

func (c *Controller) handleEndChoice(userID, text string) string {
	c.setState(userID, MenuNone)

	if text == "0" {
		return "已取消。"
	}

	idx, err := strconv.Atoi(text)
	if err != nil || idx < 1 {
		return "请输入有效数字。"
	}

	sessions, err := c.sessionMgr.ListActive()
	if err != nil || idx > len(sessions) {
		return "无效的选择。"
	}

	return c.endSession(&sessions[idx-1])
}

func (c *Controller) endSessionByRef(ref string) string {
	sessions, _ := c.sessionMgr.ListActive()
	for _, s := range sessions {
		if s.Name == ref || strconv.FormatInt(s.ID, 10) == ref {
			return c.endSession(&s)
		}
	}
	return fmt.Sprintf("未找到名为「%s」的 Agent。", ref)
}

func (c *Controller) endSession(sess *session.Session) string {
	// Close worker bot WebSocket.
	c.gw.CloseWorkerChannel(sess.BoundBotID)

	// Release bot back to pool.
	if sess.BoundBotID > 0 {
		if err := c.botPool.Release(sess.BoundBotID); err != nil {
			log.Printf("[controller] release bot %d: %v", sess.BoundBotID, err)
		}
	}

	// Mark session as sleeped.
	if err := c.sessionMgr.MarkSleeped(sess.ID); err != nil {
		log.Printf("[controller] mark sleeped %d: %v", sess.ID, err)
	}

	return fmt.Sprintf("Agent「%s」已结束。Session 记录已保留，Bot 已归还池中。", sess.Name)
}

func (c *Controller) handleBotPoolStatus() string {
	bots, err := c.botPool.List()
	if err != nil {
		return fmt.Sprintf("查询失败：%v", err)
	}

	idle, bound, unavail := 0, 0, 0
	for _, b := range bots {
		switch b.Status {
		case "idle":
			idle++
		case "bound":
			bound++
		case "unavailable":
			unavail++
		}
	}

	return fmt.Sprintf("Bot 池状态：\n总计 %d 个\n空闲：%d\n已绑定：%d\n不可用：%d\n\n发送 /start 返回主菜单",
		len(bots), idle, bound, unavail)
}

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

// detectMenuStage inspects quoted text to determine which menu the user is replying to.
// Returns MenuNone if the quoted text doesn't match any known menu.
func detectMenuStage(quotedText string) MenuState {
	switch {
	case strings.Contains(quotedText, "欢迎使用 LinkCode！请选择操作"):
		return MenuMain
	case strings.Contains(quotedText, "请选择 Agent 类型"):
		return MenuCreateAgentType
	case strings.Contains(quotedText, "请为这个 Agent 命名"):
		return MenuCreateAgentName
	case strings.Contains(quotedText, "请选择要结束的 Agent"):
		return MenuEndAgentConfirm
	}
	return MenuNone
}
