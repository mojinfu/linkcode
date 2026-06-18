package router

import (
	"testing"

	"linkcode/internal/pricing"
)

func TestEndsWithContinuation(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect bool
	}{
		// 命中：精确短语
		{name: "exact 我接着说", input: "我接着说", expect: true},
		{name: "exact 我来接着说", input: "我来接着说", expect: true},
		{name: "exact 我继续说", input: "我继续说", expect: true},
		{name: "exact 我来继续说", input: "我来继续说", expect: true},
		{name: "exact 我还没说完", input: "我还没说完", expect: true},
		// 命中：短语前有正文，首尾空白被忽略
		{name: "正文后接续说短语", input: "今天做三件事我接着说", expect: true},
		{name: "首尾空白被忽略", input: " 我接着说 ", expect: true},
		{name: "尾部换行被忽略", input: "我接着说\n", expect: true},
		// 不命中：尾部标点（严格匹配）
		{name: "尾部句号不命中", input: "我接着说。", expect: false},
		{name: "尾部逗号不命中", input: "我接着说，", expect: false},
		{name: "我继续说完不误判", input: "我继续说完", expect: false},
		{name: "普通消息不命中", input: "你好", expect: false},
		{name: "空串不命中", input: "", expect: false},
		{name: "缺我字不命中", input: "接着说", expect: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := endsWithContinuation(tt.input); got != tt.expect {
				t.Errorf("endsWithContinuation(%q) = %v, want %v", tt.input, got, tt.expect)
			}
		})
	}
}

// TestCollectFragments_BufferAndJoin 模拟连续口述场景：
// 用户连发两段以续说短语结尾的消息被暂存，第三段不再命中时拼齐发给 Claude。
func TestCollectFragments_BufferAndJoin(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))
	sid := int64(42)

	// 第一段：命中续说 -> 暂存，buffering=true。
	content, buffering := r.collectFragments(sid, "第一段我接着说")
	if buffering != true || content != "" {
		t.Fatalf("1st: got (%q, %v), want (\"\", true)", content, buffering)
	}
	r.mu.Lock()
	n := len(r.pendingFragments[sid])
	r.mu.Unlock()
	if n != 1 {
		t.Errorf("1st: pendingFragments len = %d, want 1", n)
	}

	// 第二段：仍命中 -> 继续暂存。
	content, buffering = r.collectFragments(sid, "第二段我继续说")
	if buffering != true || content != "" {
		t.Fatalf("2nd: got (%q, %v), want (\"\", true)", content, buffering)
	}
	r.mu.Lock()
	n = len(r.pendingFragments[sid])
	r.mu.Unlock()
	if n != 2 {
		t.Errorf("2nd: pendingFragments len = %d, want 2", n)
	}

	// 第三段：不命中 -> 拼齐，buffering=false，暂存清空。
	content, buffering = r.collectFragments(sid, "第三段")
	if buffering != false {
		t.Fatalf("3rd: buffering = true, want false")
	}
	want := "第一段我接着说\n第二段我继续说\n第三段"
	if content != want {
		t.Errorf("3rd: content = %q, want %q", content, want)
	}
	r.mu.Lock()
	_, ok := r.pendingFragments[sid]
	r.mu.Unlock()
	if ok {
		t.Errorf("3rd: pendingFragments should be cleared after join")
	}
}

// TestCollectFragments_NoBufferPassthrough: 无暂存时，非命中消息原样返回。
func TestCollectFragments_NoBufferPassthrough(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))
	content, buffering := r.collectFragments(1, "你好")
	if buffering != false || content != "你好" {
		t.Errorf("got (%q, %v), want (\"你好\", false)", content, buffering)
	}
}

// TestCollectFragments_NewClearsThenFresh: /new 清空暂存后，下一条当全新输入。
func TestCollectFragments_NewClearsThenFresh(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))
	sid := int64(7)

	// 先暂存一条。
	_, buffering := r.collectFragments(sid, "没说完我接着说")
	if buffering != true {
		t.Fatalf("setup: buffering = false, want true")
	}

	// 模拟 handleNew 清理。
	r.mu.Lock()
	delete(r.pendingFragments, sid)
	r.mu.Unlock()

	// 下一条当全新输入。
	content, buffering := r.collectFragments(sid, "新话题")
	if buffering != false || content != "新话题" {
		t.Errorf("after /new: got (%q, %v), want (\"新话题\", false)", content, buffering)
	}
}

// TestCollectFragments_SessionsIndependent: 各 session 暂存互不影响。
func TestCollectFragments_SessionsIndependent(t *testing.T) {
	r := New(nil, nil, nil, nil, nil, nil, pricing.New(nil))

	// session A 暂存一条。
	_, buffering := r.collectFragments(10, "A我接着说")
	if buffering != true {
		t.Fatalf("A: buffering = false, want true")
	}

	// session B 无暂存，非命中原样返回（不受 A 影响）。
	content, buffering := r.collectFragments(20, "B你好")
	if buffering != false || content != "B你好" {
		t.Errorf("B: got (%q, %v), want (\"B你好\", false)", content, buffering)
	}
}
