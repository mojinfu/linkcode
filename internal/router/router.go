// Package router handles bidirectional message forwarding between
// IM channels (user <-> bot) and agent processes.
package router

import (
	"context"
	"fmt"
	"log"

	"linkcode/internal/agent"
	"linkcode/internal/channel"
	"linkcode/internal/gateway"
	"linkcode/internal/session"
)

// Router forwards messages between users and agent processes.
type Router struct {
	sessionMgr  *session.Manager
	agentRunner agent.Runner
	gw          *gateway.Gateway
}

// New creates a new Router.
func New(sessMgr *session.Manager, runner agent.Runner, gw *gateway.Gateway) *Router {
	return &Router{
		sessionMgr:  sessMgr,
		agentRunner: runner,
		gw:          gw,
	}
}

// HandleWorkerEvent handles events from worker bots (enter_chat, etc.).
func (r *Router) HandleWorkerEvent(msg channel.Message) {
	if msg.MsgType != channel.MsgTypeEnterChat {
		return
	}

	// Look up the session for this bot and send welcome.
	sess, err := r.sessionMgr.GetByPlatformBotID(msg.BotID)
	if err != nil || sess == nil {
		return // Bot not bound yet
	}

	ch, ok := r.gw.GetWorkerByPlatformID(msg.BotID)
	if !ok {
		return
	}
	ch.SendMessage(context.Background(), msg.UserID, channel.MessageContent{
		Text: fmt.Sprintf("你好，我是你的 %s「%s」，有什么任务要交给我吗？", sess.AgentType, sess.Name),
	})
}

// HandleWorkerMessage processes a message from a user to a worker bot.
// It looks up the session, routes to the agent process, and streams replies back.
func (r *Router) HandleWorkerMessage(msg channel.Message) {
	log.Printf("[router] msg from %s: %s", msg.UserID, msg.Content)
	ctx := context.Background()

	// Find the session bound to this bot by platform bot_id.
	sess, err := r.sessionMgr.GetByPlatformBotID(msg.BotID)
	if err != nil {
		log.Printf("[router] session not found for bot %s: %v", msg.BotID, err)
		r.sendReply(msg, "找不到对应的 Session，请通过总控 Bot 重新创建。")
		return
	}

	// Process /end as a special command to end the session.
	if msg.Content == "/end" {
		r.sendReply(msg, "会话已结束。Session 记录已保留。")
		return
	}

	// Save user message to history.
	_ = r.sessionMgr.AddMessage(sess.ID, "user", msg.Content, string(msg.MsgType))

	// Resume or start agent process.
	var agentSess agent.Session
	shouldResume := sess.ClaudeSessionID != ""
	if shouldResume {
		agentSess, err = r.agentRunner.Resume(ctx, fmt.Sprintf("%d", sess.ID), sess.ClaudeSessionID)
		if err != nil {
			log.Printf("[router] resume agent: %v", err)
			r.sendReply(msg, "唤醒 Agent 失败，请重试。")
			return
		}
		_ = r.sessionMgr.MarkWaked(sess.ID)
	} else {
		agentSess, err = r.agentRunner.Start(ctx, fmt.Sprintf("%d", sess.ID))
		if err != nil {
			log.Printf("[router] start agent: %v", err)
			r.sendReply(msg, "启动 Agent 失败，请重试。")
			return
		}
		// Save the Claude session ID for future resumes.
		if sid := agentSess.ClaudeSessionID(); sid != "" {
			_ = r.sessionMgr.SetClaudeSessionID(sess.ID, sid)
		}
	}

	_ = r.sessionMgr.Touch(sess.ID)

	// Send input to agent and stream output back.
	outputCh, err := agentSess.Send(ctx, msg.Content)
	if err != nil {
		log.Printf("[router] send to agent: %v", err)
		r.sendReply(msg, "Agent 处理失败，请重试。")
		return
	}

	var fullResponse string
	for chunk := range outputCh {
		switch chunk.Kind {
		case agent.KindError:
			log.Printf("[router] agent error: %s", chunk.Content)
			r.sendReply(msg, chunk.Content)
			return
		case agent.KindText:
			fullResponse += chunk.Content
			r.sendReply(msg, chunk.Content)
		case agent.KindFinal:
			fullResponse += chunk.Content
		case agent.KindThinking, agent.KindToolUse:
			fullResponse += chunk.Content
		}
	}

	// Save agent response to history.
	if fullResponse != "" {
		_ = r.sessionMgr.AddMessage(sess.ID, "agent", fullResponse, "text")
	}
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
