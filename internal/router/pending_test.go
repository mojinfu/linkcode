package router

import (
	"testing"

	"linkcode/internal/channel"
	"linkcode/internal/pricing"
)

// TestPendingCount_ZeroByDefault verifies that a fresh Router returns 0
// for any session ID — no messages have ever been queued.
func TestPendingCount_ZeroByDefault(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))

	if got := r.PendingCount(1); got != 0 {
		t.Errorf("PendingCount(1) = %d, want 0", got)
	}
	if got := r.PendingCount(999); got != 0 {
		t.Errorf("PendingCount(999) = %d, want 0", got)
	}
}

// TestPendingCount_ReflectsEnqueuedMessages simulates the busy-process path:
// handleLLM → getOrCreateAgentSession returns ErrBusy → enqueueMessage
// appends to pendingMessages. Each new message increments the count.
//
// Real scenario: user sends "帮我写个报告", agent starts thinking.
// Before the response comes, user sends "再帮我分析一下这段代码".
// The second message is queued → PendingCount = 1.
func TestPendingCount_ReflectsEnqueuedMessages(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))
	sid := int64(42)

	// Simulate 1st enqueue (ErrBusy path).
	r.mu.Lock()
	r.pendingMessages[sid] = append(r.pendingMessages[sid], channel.Message{Content: "msg1"})
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 1 {
		t.Errorf("after 1st enqueue: PendingCount = %d, want 1", got)
	}

	// 2nd enqueue.
	r.mu.Lock()
	r.pendingMessages[sid] = append(r.pendingMessages[sid], channel.Message{Content: "msg2"})
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 2 {
		t.Errorf("after 2nd enqueue: PendingCount = %d, want 2", got)
	}

	// 3rd enqueue.
	r.mu.Lock()
	r.pendingMessages[sid] = append(r.pendingMessages[sid], channel.Message{Content: "msg3"})
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 3 {
		t.Errorf("after 3rd enqueue: PendingCount = %d, want 3", got)
	}
}

// TestPendingCount_DrainDecrements simulates drainPendingMessages:
// after streamToUser completes, drainPendingMessages shifts the first
// queued message off the slice and calls handleLLM for it.
//
// Real scenario: agent finishes processing message 1 (queue was [msg2, msg3]).
// drainPendingMessages shifts msg2 off the queue and processes it.
// Queue goes from 2 → 1. Controller /list should show 队列: 1.
func TestPendingCount_DrainDecrements(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))
	sid := int64(7)

	// Start with 3 queued messages (simulating busy agent).
	r.mu.Lock()
	r.pendingMessages[sid] = []channel.Message{
		{Content: "msg1"},
		{Content: "msg2"},
		{Content: "msg3"},
	}
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 3 {
		t.Fatalf("initial: got %d, want 3", got)
	}

	// Drain step 1: shift off first message (simulates drainPendingMessages).
	r.mu.Lock()
	if len(r.pendingMessages[sid]) > 0 {
		r.pendingMessages[sid] = r.pendingMessages[sid][1:]
	}
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 2 {
		t.Errorf("after 1st drain: got %d, want 2", got)
	}

	// Drain step 2.
	r.mu.Lock()
	if len(r.pendingMessages[sid]) > 0 {
		r.pendingMessages[sid] = r.pendingMessages[sid][1:]
	}
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 1 {
		t.Errorf("after 2nd drain: got %d, want 1", got)
	}

	// Drain step 3: last message, queue becomes empty.
	r.mu.Lock()
	if len(r.pendingMessages[sid]) > 0 {
		r.pendingMessages[sid] = r.pendingMessages[sid][1:]
	}
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 0 {
		t.Errorf("after 3rd drain: got %d, want 0", got)
	}
}

// TestPendingCount_NewCommandClears verifies that /new clears the
// pending messages for the old session.
//
// handleNew calls: delete(r.pendingMessages, sess.ID)
// After /new, controller /list should show 队列: 0 for both old and new sessions.
func TestPendingCount_NewCommandClears(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))
	sid := int64(99)

	// Simulate: agent busy, 2 messages queued.
	r.mu.Lock()
	r.pendingMessages[sid] = []channel.Message{
		{Content: "msg1"},
		{Content: "msg2"},
	}
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 2 {
		t.Fatalf("initial: got %d, want 2", got)
	}

	// Simulate handleNew: delete pendingMessages for this session.
	r.mu.Lock()
	delete(r.pendingMessages, sid)
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 0 {
		t.Errorf("after /new clear: got %d, want 0", got)
	}
}

// TestPendingCount_MultipleSessionsIndependent verifies that queue counts
// are tracked independently per session.
//
// Real scenario: 3 worker bots.
// Bot A: 3 messages queued (users are piling on while agent is busy).
// Bot B: idle, no messages.
// Bot C: 1 message queued (just got a message while processing previous).
//
// Controller /list should show: A=3, B=0, C=1.
func TestPendingCount_MultipleSessionsIndependent(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))

	// Session A (bot A busy, 3 queued).
	r.mu.Lock()
	r.pendingMessages[10] = []channel.Message{{}, {}, {}}
	r.mu.Unlock()

	// Session B (bot B idle) — no entry in map.

	// Session C (bot C, 1 queued).
	r.mu.Lock()
	r.pendingMessages[30] = []channel.Message{{}}
	r.mu.Unlock()

	tests := []struct {
		sessionID int64
		desc      string
		want      int
	}{
		{10, "bot A (3 queued)", 3},
		{20, "bot B (idle, no entry)", 0},
		{30, "bot C (1 queued)", 1},
		{999, "non-existent session", 0},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			if got := r.PendingCount(tt.sessionID); got != tt.want {
				t.Errorf("PendingCount(%d) = %d, want %d (%s)", tt.sessionID, got, tt.want, tt.desc)
			}
		})
	}
}

// TestPendingCount_QueueInfoInterface is a compile-time assertion that
// *Router satisfies controller.QueueInfo (PendingCount method).
func TestPendingCount_QueueInfoInterface(t *testing.T) {
	var _ interface{ PendingCount(int64) int } = New(nil, nil, nil, nil, nil, nil, pricing.New(nil))
	// Compiles = *Router satisfies the interface.
}

// TestPendingCount_RealWorldScenarios documents the expected queue count
// for each real-world scenario the user should see in /list.
func TestPendingCount_RealWorldScenarios(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))

	// Scenario 1: worker bot 刚创建，还没有人发消息
	// → queue = 0
	// /list shows: 队列: 0
	sid := int64(1)
	if got := r.PendingCount(sid); got != 0 {
		t.Errorf("scenario 1 (fresh bot): got %d, want 0", got)
	}

	// Scenario 2: 用户发了一条消息，agent 正在处理中（当前消息不计入队列）
	// → queue = 0
	// /list shows: 队列: 0
	// (pendingMessages is empty because the current message is in-flight, not queued)
	if got := r.PendingCount(sid); got != 0 {
		t.Errorf("scenario 2 (1 msg in-flight): got %d, want 0", got)
	}

	// Scenario 3: agent 正在处理第一条消息，用户又发了 2 条
	// → queue = 2 (both new messages are queued)
	r.mu.Lock()
	r.pendingMessages[sid] = []channel.Message{{Content: "msg2"}, {Content: "msg3"}}
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 2 {
		t.Errorf("scenario 3 (1 in-flight + 2 queued): got %d, want 2", got)
	}

	// Scenario 4: agent 处理完第一条，drain 处理第二条（队列从 2 → 1）
	r.mu.Lock()
	r.pendingMessages[sid] = r.pendingMessages[sid][1:]
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 1 {
		t.Errorf("scenario 4 (draining, 1 left): got %d, want 1", got)
	}

	// Scenario 5: agent 处理完最后一条排队的消息
	// → queue = 0
	r.mu.Lock()
	delete(r.pendingMessages, sid)
	r.mu.Unlock()
	if got := r.PendingCount(sid); got != 0 {
		t.Errorf("scenario 5 (all drained): got %d, want 0", got)
	}
}
