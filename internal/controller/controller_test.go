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
	if !strings.Contains(reply, "1. 创建新 Agent") {
		t.Errorf("expected menu option 1, got: %s", reply)
	}
	if !strings.Contains(reply, "5. 结束 Agent") {
		t.Errorf("expected menu option 5, got: %s", reply)
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

	// Option 1 → agent type selection
	msg.Content = "1"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请选择 Agent 类型") {
		t.Errorf("option 1: expected agent type selection, got: %s", reply)
	}

	// Send /start again to reset → main menu
	msg.Content = "/start"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "欢迎使用 LinkCode") {
		t.Fatal("second /start should show menu")
	}

	// Option 3 → add bot instructions
	msg.Content = "3"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "格式") {
		t.Errorf("option 3: expected addbot format hint, got: %s", reply)
	}
	t.Logf("option 3: %s", reply)

	// Invalid option
	msg.Content = "/start"
	ctrl.HandleMessage(ctx, msg)
	msg.Content = "99"
	reply = ctrl.HandleMessage(ctx, msg)
	if !strings.Contains(reply, "请回复数字 1-5") {
		t.Errorf("invalid option: expected prompt for 1-5, got: %s", reply)
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
