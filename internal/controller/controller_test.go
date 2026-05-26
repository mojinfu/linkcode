package controller

import (
	"context"
	"strings"
	"testing"

	"linkcode/internal/channel"
)

func TestHandleMessage_Start(t *testing.T) {
	ctrl := &Controller{
		userStates: make(map[string]*userState),
	}

	ctx := context.Background()
	msg := channel.Message{
		UserID:  "test-user-123",
		Content: "/start",
		MsgType: channel.MsgTypeText,
	}

	reply := ctrl.HandleMessage(ctx, msg)

	if !strings.Contains(reply, "欢迎使用 LinkCode") {
		t.Errorf("expected welcome message, got: %s", reply)
	}
	if !strings.Contains(reply, "1. 添加新 Bot") {
		t.Errorf("expected menu option 1, got: %s", reply)
	}
	if !strings.Contains(reply, "4. 结束 Agent") {
		t.Errorf("expected menu option 4, got: %s", reply)
	}

	t.Logf("Reply:\n%s", reply)
}

func TestHandleMessage_MenuNavigations(t *testing.T) {
	ctrl := &Controller{
		userStates: make(map[string]*userState),
	}

	ctx := context.Background()
	uid := "user-1"

	// /start → main menu
	msg := channel.Message{UserID: uid, Content: "/start", MsgType: channel.MsgTypeText}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "欢迎使用 LinkCode") {
		t.Fatal("/start should show menu")
	}

	// Option 1 → addbot instructions
	msg.Content = "1"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "/addbot") {
		t.Errorf("option 1: expected addbot instructions, got: %s", reply)
	}

	// Invalid option
	msg.Content = "/start"
	ctrl.HandleMessage(ctx, msg)
	msg.Content = "99"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请回复数字 1-4") {
		t.Errorf("invalid option: expected prompt for 1-4, got: %s", reply)
	}
}

func TestHandleMessage_DirectAddbotFormatError(t *testing.T) {
	ctrl := &Controller{
		userStates: make(map[string]*userState),
	}

	ctx := context.Background()
	msg := channel.Message{UserID: "u", Content: "/addbot", MsgType: channel.MsgTypeText}

	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "格式错误") {
		t.Errorf("expected format error, got: %s", reply)
	}
	t.Logf("/addbot (bad format): %s", reply)
}

func TestHandleMessage_QuoteMainMenu(t *testing.T) {
	ctrl := &Controller{
		userStates: make(map[string]*userState),
	}

	ctx := context.Background()
	uid := "user-quote-1"

	// Quote main menu text + reply "1" → should show addbot instructions.
	msg := channel.Message{
		UserID:       uid,
		Content:      "1",
		MsgType:      channel.MsgTypeText,
		QuoteContent: "欢迎使用 LinkCode！请选择操作：\n1. 添加新 Bot",
	}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "/addbot") {
		t.Errorf("quoting main menu + '1': expected addbot instructions, got: %s", reply)
	}
}

func TestHandleMessage_QuoteUnknown(t *testing.T) {
	ctrl := &Controller{
		userStates: make(map[string]*userState),
	}

	ctx := context.Background()
	uid := "user-quote-2"

	// Quote unrecognized text + "1" → fallback to state machine (shows main menu).
	msg := channel.Message{
		UserID:       uid,
		Content:      "1",
		MsgType:      channel.MsgTypeText,
		QuoteContent: "这是一条随意的消息，不是菜单",
	}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "欢迎使用 LinkCode") {
		t.Errorf("quoting unknown + '1': expected fallback to main menu, got: %s", reply)
	}
}

func TestHandleMessage_NoQuoteStillWorks(t *testing.T) {
	ctrl := &Controller{
		userStates: make(map[string]*userState),
	}

	ctx := context.Background()
	uid := "user-noquote"

	// /start then "1" without quote → should show addbot instructions.
	msg := channel.Message{UserID: uid, Content: "/start", MsgType: channel.MsgTypeText}
	ctrl.HandleMessage(ctx, msg)

	msg = channel.Message{UserID: uid, Content: "1", MsgType: channel.MsgTypeText}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "/addbot") {
		t.Errorf("no quote + '1': expected addbot instructions, got: %s", reply)
	}
}

func TestHandleMessage_QuoteEndAgentMenu(t *testing.T) {
	// Verify that quoting the end-agent menu text is recognized as MenuEndAgentConfirm.
	quotedText := "请选择要结束的 Agent（回复数字）：\n1. 日志分析员 (claude-code) - 运行中"
	state := detectMenuStage(quotedText)
	if state != MenuEndAgentConfirm {
		t.Errorf("quoting end-agent menu: expected MenuEndAgentConfirm, got %v", state)
	}
	t.Logf("end menu quote detection: state=%v", state)
}

func TestDetectMenuStage_AllMenus(t *testing.T) {
	tests := []struct {
		quotedText string
		want       MenuState
	}{
		{"欢迎使用 LinkCode！请选择操作：\n1. 添加新 Bot", MenuMain},
		{"请选择 Agent 类型：\n1. Claude Code", MenuAddBotAgentType},
		{"请选择要结束的 Agent（回复数字）：\n1. test", MenuEndAgentConfirm},
		{"随机消息", MenuNone},
		{"", MenuNone},
	}
	for _, tt := range tests {
		got := detectMenuStage(tt.quotedText)
		if got != tt.want {
			t.Errorf("detectMenuStage(%q) = %v, want %v", tt.quotedText, got, tt.want)
		}
	}
}

func TestHandleMessage_Help(t *testing.T) {
	ctrl := &Controller{
		userStates: make(map[string]*userState),
	}

	ctx := context.Background()
	msg := channel.Message{UserID: "u", Content: "/help", MsgType: channel.MsgTypeText}

	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "/start") {
		t.Errorf("expected /start in help, got: %s", reply)
	}
	if !strings.Contains(reply, "/addbot") {
		t.Errorf("expected /addbot in help, got: %s", reply)
	}
	t.Logf("/help: %s", reply)
}
