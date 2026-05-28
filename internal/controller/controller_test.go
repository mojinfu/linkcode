package controller

import (
	"context"
	"strings"
	"testing"

	"linkcode/internal/channel"
)

func TestHandleMessage_Start(t *testing.T) {
	ctrl := &Controller{styler: channel.PlainStyler{}, userStates: make(map[string]*userState)}

	ctx := context.Background()
	msg := channel.Message{UserID: "test-user-123", Content: "/start", MsgType: channel.MsgTypeText}
	reply := ctrl.HandleMessage(ctx, msg)

	if !strings.Contains(reply, "创建 Agent") {
		t.Errorf("expected create agent, got: %s", reply)
	}
	t.Logf("Reply:\n%s", reply)
}

func TestHandleMessage_MenuNavigations(t *testing.T) {
	ctrl := &Controller{styler: channel.PlainStyler{}, userStates: make(map[string]*userState)}
	ctx := context.Background()
	uid := "user-1"

	msg := channel.Message{UserID: uid, Content: "/start", MsgType: channel.MsgTypeText}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "创建 Agent") {
		t.Fatal("/start should show menu")
	}

	msg.Content = "1"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请输入 Agent 名称") {
		t.Errorf("option 1: expected name prompt, got: %s", reply)
	}

	msg.Content = "0"
	ctrl.HandleMessage(ctx, msg)

	msg.Content = "/start"
	ctrl.HandleMessage(ctx, msg)
	msg.Content = "99"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请回复数字 1-3") {
		t.Errorf("invalid option: expected 1-3 prompt, got: %s", reply)
	}
}

func TestHandleMessage_CreateAgentFlowCancel(t *testing.T) {
	ctrl := &Controller{styler: channel.PlainStyler{}, userStates: make(map[string]*userState)}
	ctx := context.Background()
	uid := "user-create"

	msg := channel.Message{UserID: uid, Content: "/start", MsgType: channel.MsgTypeText}
	ctrl.HandleMessage(ctx, msg)

	msg.Content = "1"
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请输入 Agent 名称") {
		t.Fatal("expected name prompt")
	}

	msg.Content = "测试Agent"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请输入企微 BotID") {
		t.Fatalf("expected BotID prompt, got: %s", reply)
	}

	msg.Content = "test-bot-id-123"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请输入 Bot Secret") {
		t.Fatalf("expected Secret prompt, got: %s", reply)
	}

	msg.Content = "test-secret-456"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请选择 Agent 类型") {
		t.Fatalf("expected type prompt, got: %s", reply)
	}

	msg.Content = "0"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "创建 Agent") {
		t.Errorf("cancel should return to menu, got: %s", reply)
	}
}

func TestHandleMessage_DirectAddbotFormatError(t *testing.T) {
	ctrl := &Controller{styler: channel.PlainStyler{}, userStates: make(map[string]*userState)}
	ctx := context.Background()
	msg := channel.Message{UserID: "u", Content: "/addbot", MsgType: channel.MsgTypeText}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "参数不足") {
		t.Errorf("expected format error, got: %s", reply)
	}
}

func TestHandleMessage_QuoteMainMenu(t *testing.T) {
	ctrl := &Controller{styler: channel.PlainStyler{}, userStates: make(map[string]*userState)}
	ctx := context.Background()
	uid := "user-quote-1"

	msg := channel.Message{
		UserID:       uid,
		Content:      "1",
		MsgType:      channel.MsgTypeText,
		QuoteContent: "\"1. 创建 Agent\"",
	}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请输入 Agent 名称") {
		t.Errorf("quoting menu + '1': expected name prompt, got: %s", reply)
	}
}

func TestHandleMessage_QuoteUnknown(t *testing.T) {
	ctrl := &Controller{styler: channel.PlainStyler{}, userStates: make(map[string]*userState)}
	ctx := context.Background()
	uid := "user-quote-2"

	msg := channel.Message{
		UserID:       uid,
		Content:      "1",
		MsgType:      channel.MsgTypeText,
		QuoteContent: "这是一条随意的消息",
	}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "创建 Agent") {
		t.Errorf("quoting unknown + '1': expected fallback to menu, got: %s", reply)
	}
}

func TestHandleMessage_NoQuoteStillWorks(t *testing.T) {
	ctrl := &Controller{styler: channel.PlainStyler{}, userStates: make(map[string]*userState)}
	ctx := context.Background()
	uid := "user-noquote"

	msg := channel.Message{UserID: uid, Content: "/start", MsgType: channel.MsgTypeText}
	ctrl.HandleMessage(ctx, msg)

	msg = channel.Message{UserID: uid, Content: "1", MsgType: channel.MsgTypeText}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请输入 Agent 名称") {
		t.Errorf("no quote + '1': expected name prompt, got: %s", reply)
	}
}

func TestDetectMenuStage_AllMenus(t *testing.T) {
	tests := []struct {
		quotedText string
		want       MenuState
	}{
		{"\"1. 创建 Agent\"", MenuMain},
		{"请输入 Agent 名称：", MenuCreateAgentName},
		{"请输入企微 BotID：", MenuCreateAgentBotID},
		{"请输入 Bot Secret：", MenuCreateAgentSecret},
		{"请选择 Agent 类型：\n1. Claude Code", MenuCreateAgentType},
		{"设定 Agent 默认工作目录\n当前：/some/path", MenuSetDefaultWorkDir},
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
	ctrl := &Controller{styler: channel.PlainStyler{}, userStates: make(map[string]*userState)}
	ctx := context.Background()
	msg := channel.Message{UserID: "u", Content: "/help", MsgType: channel.MsgTypeText}
	reply := ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "/start") {
		t.Errorf("expected /start in help, got: %s", reply)
	}
	if !strings.Contains(reply, "/addbot") {
		t.Errorf("expected /addbot in help, got: %s", reply)
	}
}
