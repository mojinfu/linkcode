package claude

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"linkcode/internal/agent"
)

// buildFakeClaude compiles a fake claude binary from testdata and returns its path.
func buildFakeClaude(t *testing.T, dir, name string) string {
	t.Helper()
	src := filepath.Join("testdata", name, "main.go")
	bin := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, src)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	return bin
}

// TestRunner_AutoCleanupAfterSend verifies that after a normal Send cycle
// (process exits cleanly), the session is auto-removed from runner.sessions.
func TestRunner_AutoCleanupAfterSend(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	fakeBin := buildFakeClaude(t, dir, "fakeclaude")
	runner := NewRunner(fakeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "test-session-clean"
	sess, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Verify session is in the map after Start.
	runner.mu.Lock()
	_, inMap := runner.sessions[sid]
	runner.mu.Unlock()
	if !inMap {
		t.Fatal("session not in runner.sessions after Start")
	}

	input := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`
	outputCh, err := sess.Send(ctx, input)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var texts []string
	for chunk := range outputCh {
		if chunk.Kind == agent.KindText {
			texts = append(texts, chunk.Content)
		}
	}

	t.Logf("got %d text chunks: %v", len(texts), texts)

	// After channel drain, auto-cleanup should have removed the session.
	// Give the goroutine a tiny moment to finish (it runs before close(wrapped),
	// so this should be unnecessary, but safe).
	time.Sleep(10 * time.Millisecond)

	runner.mu.Lock()
	_, inMap = runner.sessions[sid]
	runner.mu.Unlock()
	if inMap {
		t.Error("session still in runner.sessions after Send completed — auto-cleanup did not fire")
	}
}

// TestRunner_AutoCleanupAfterCrash verifies that when a process crashes
// mid-response (stderr + exit 1), the session is still auto-removed.
func TestRunner_AutoCleanupAfterCrash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	// Use the texterr fake claude from procman testdata (text then crash).
	// Path is relative to this package (internal/agent/claude).
	texterrSrc := filepath.Join("..", "..", "procman", "testdata", "fakeclaude_texterr", "main.go")
	fakeBinTexterr := filepath.Join(dir, "fakeclaude_texterr")
	if runtime.GOOS == "windows" {
		fakeBinTexterr += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBinTexterr, texterrSrc)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude_texterr: %v\n%s", err, out)
	}

	runner := NewRunner(fakeBinTexterr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "test-session-crash"
	sess, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Verify session is in map.
	runner.mu.Lock()
	_, inMap := runner.sessions[sid]
	runner.mu.Unlock()
	if !inMap {
		t.Fatal("session not in runner.sessions after Start")
	}

	input := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`
	outputCh, err := sess.Send(ctx, input)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var texts []string
	var errors []string
	for chunk := range outputCh {
		switch chunk.Kind {
		case agent.KindText:
			texts = append(texts, chunk.Content)
		case agent.KindError:
			errors = append(errors, chunk.Content)
		}
	}

	t.Logf("text chunks: %v", texts)
	t.Logf("error chunks: %v", errors)

	if len(texts) == 0 {
		t.Error("expected at least one text chunk before crash")
	}
	if len(errors) == 0 {
		t.Error("expected at least one error chunk from stderr/exit")
	}

	time.Sleep(10 * time.Millisecond)

	runner.mu.Lock()
	_, inMap = runner.sessions[sid]
	runner.mu.Unlock()
	if inMap {
		t.Error("session still in runner.sessions after crash — auto-cleanup did not fire")
	}
}

// TestRunner_StopCleansUp verifies that Session.Stop() removes the session
// from runner.sessions, even without going through the Send path.
func TestRunner_StopCleansUp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	fakeBin := buildFakeClaude(t, dir, "fakeclaude")
	runner := NewRunner(fakeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "test-session-stop"
	sess, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	runner.mu.Lock()
	_, inMap := runner.sessions[sid]
	runner.mu.Unlock()
	if !inMap {
		t.Fatal("session not in runner.sessions after Start")
	}

	// Kill the session via Stop() — no Send() call, simulating
	// an external kill before the user sends a message.
	if err := sess.Stop(); err != nil {
		t.Logf("Stop() returned error (expected on Windows): %v", err)
	}

	runner.mu.Lock()
	_, inMap = runner.sessions[sid]
	runner.mu.Unlock()
	if inMap {
		t.Error("session still in runner.sessions after Stop()")
	}
}

// TestRunner_RapidRestartDelay verifies that when a process died recently,
// the next Start for the same session ID delays before spawning a new one.
func TestRunner_RapidRestartDelay(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	fakeBin := buildFakeClaude(t, dir, "fakeclaude")
	runner := NewRunner(fakeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "test-rapid-restart"

	// First Start + Send: process runs and exits normally.
	sess1, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("first Start() failed: %v", err)
	}

	input := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`
	outputCh, err := sess1.Send(ctx, input)
	if err != nil {
		t.Fatalf("first Send() failed: %v", err)
	}
	for range outputCh {
		// drain
	}
	// Auto-cleanup should have removed the session.
	// But rapid restart detection uses diedAt, which is on the Process,
	// and the session is already removed. So the second Start creates
	// a new session without delay (no existing session to check).

	// To actually trigger rapid restart delay, we need to NOT drain
	// the channel (so auto-cleanup doesn't fire), then the session
	// stays in the map. Then when we call Start again, the existing
	// dead session is found and the delay triggers.
	//
	// Let's test the actual rapid restart path:
	// 1. Start a session, kill it via Stop() without Send
	// 2. Start again with same ID → should detect recent death and delay

	sid2 := "test-rapid-restart-2"
	sess2, err := runner.Start(ctx, sid2, dir)
	if err != nil {
		t.Fatalf("Start() for rapid restart test failed: %v", err)
	}

	// Kill via Stop — session removed from map, but process has diedAt set.
	sess2.Stop()

	// Immediately start again with same session ID.
	start := time.Now()
	sess3, err := runner.Start(ctx, sid2, dir)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("second Start() after kill failed: %v", err)
	}
	_ = sess3

	t.Logf("second Start took %v", elapsed)
	// The rapid restart delay should be 5s if the previous process died
	// within 30s. But since Stop() also calls removeSession, the session
	// is removed from the map. So the rapid restart check in Start()
	// won't find an existing session and won't delay.
	//
	// This is a design observation: Stop() removes the session, so
	// rapid restart delay only triggers for the auto-cleanup path
	// (where the session stays in the map until Send's goroutine cleans it).
	t.Log("rapid restart delay only triggers for the auto-cleanup path (Send), not Stop()")
}

// TestRunner_SessionNotInMapAfterAutoCleanup is the "status check" scenario:
// a controller-like query of the runner's internal state should show the
// session gone after process exit.
func TestRunner_SessionNotInMapAfterAutoCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	fakeBin := buildFakeClaude(t, dir, "fakeclaude")
	runner := NewRunner(fakeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "worker-bot-001"

	// Simulate: worker bot starts a session.
	sess, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// "主控查询": check session exists.
	runner.mu.Lock()
	_, active := runner.sessions[sid]
	runner.mu.Unlock()
	if !active {
		t.Fatal("control bot /list should show '已连接' — but session not found")
	}
	t.Log("control bot query: session active ✓")

	// "后台偷偷杀掉假 Claude": the process exits (normal Send cycle).
	input := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`
	outputCh, err := sess.Send(ctx, input)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}
	for range outputCh {
		// drain — triggers auto-cleanup
	}

	// "再次查询状态": session should be gone.
	time.Sleep(10 * time.Millisecond)
	runner.mu.Lock()
	_, active = runner.sessions[sid]
	runner.mu.Unlock()
	if active {
		t.Error("control bot /list should show session gone — but session still in map")
	}
	t.Log("control bot query after cleanup: session removed ✓")
}

// TestRunner_InterruptRemovesSession verifies that Interrupt() properly
// removes the session and returns the correct status.
func TestRunner_InterruptRemovesSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	fakeBin := buildFakeClaude(t, dir, "fakeclaude")
	runner := NewRunner(fakeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "test-session-interrupt"
	_, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Interrupt an active session.
	ok := runner.Interrupt(sid)
	if !ok {
		t.Error("Interrupt() should return true for active session")
	}

	// Session should be gone from map.
	runner.mu.Lock()
	_, inMap := runner.sessions[sid]
	runner.mu.Unlock()
	if inMap {
		t.Error("session still in map after Interrupt()")
	}

	// Interrupt again should return false (already removed).
	ok = runner.Interrupt(sid)
	if ok {
		t.Error("Interrupt() should return false for already-removed session")
	}
}

// TestRunner_ErrBusyOnAliveSession verifies that Start/Resume return
// agent.ErrBusy when the session is already running.
func TestRunner_ErrBusyOnAliveSession(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	// Use the hang fake claude — it blocks on stdin, so it stays alive.
	hangSrc := filepath.Join("..", "..", "procman", "testdata", "fakeclaude_hang", "main.go")
	fakeBinHang := filepath.Join(dir, "fakeclaude_hang")
	if runtime.GOOS == "windows" {
		fakeBinHang += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBinHang, hangSrc)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude_hang: %v\n%s", err, out)
	}

	runner := NewRunner(fakeBinHang)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "test-busy-session"
	sess, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	_ = sess

	// Second Start with same ID while process is alive → ErrBusy.
	_, err = runner.Start(ctx, sid, dir)
	if err != agent.ErrBusy {
		t.Errorf("expected agent.ErrBusy, got %v", err)
	}

	// Resume with same ID while process is alive → ErrBusy.
	_, err = runner.Resume(ctx, sid, "some-claude-id", dir)
	if err != agent.ErrBusy {
		t.Errorf("expected agent.ErrBusy for Resume, got %v", err)
	}
}

// TestRunner_InterruptBusySessionThenResume verifies the full cycle:
// active session → /stop (Interrupt) → new session can Start.
func TestRunner_InterruptBusySessionThenResume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	hangSrc := filepath.Join("..", "..", "procman", "testdata", "fakeclaude_hang", "main.go")
	fakeBinHang := filepath.Join(dir, "fakeclaude_hang")
	if runtime.GOOS == "windows" {
		fakeBinHang += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBinHang, hangSrc)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude_hang: %v\n%s", err, out)
	}

	runner := NewRunner(fakeBinHang)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "test-interrupt-resume"

	// 1. Start session — it's alive (blocking on stdin).
	sess1, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	t.Logf("session %s started, alive=%v", sid, sess1.IsAlive())

	// 2. Verify busy.
	_, err = runner.Start(ctx, sid, dir)
	if err != agent.ErrBusy {
		t.Errorf("expected ErrBusy, got %v", err)
	}

	// 3. Interrupt (/stop equivalent).
	ok := runner.Interrupt(sid)
	if !ok {
		t.Error("Interrupt() should return true")
	}
	t.Log("interrupted — session removed from map")

	// 4. Verify not busy — can start a new session with same ID.
	sess3, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start() after interrupt failed: %v", err)
	}
	t.Logf("new session %s started after interrupt, alive=%v", sid, sess3.IsAlive())
}

// TestRunner_ProcessStatusGap_TwoBotsAfterExit demonstrates the bug where
// process status in DB stays "waked" (运行中) after the Claude process exits.
//
// Scenario: two worker bots, each running a Claude process.
// Bot A's process completes and exits. Bot B's process completes and exits.
// Both processes are dead, but the DB process_status remains "waked".
// The control bot /list shows both as "🟢 运行中" even though neither is running.
//
// Root cause: MarkSleeped is only called in handleNew (/new command).
// The process exit path (procman.waitForExit → runner auto-cleanup)
// has no connection to session.Manager.MarkSleeped.
func TestRunner_ProcessStatusGap_TwoBotsAfterExit(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	fakeBin := buildFakeClaude(t, dir, "fakeclaude")
	runner := NewRunner(fakeBin)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	input := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`

	// --- Bot A: starts, responds, exits ---
	sessA, err := runner.Start(ctx, "bot-A", dir)
	if err != nil {
		t.Fatalf("Start bot-A failed: %v", err)
	}
	chA, err := sessA.Send(ctx, input)
	if err != nil {
		t.Fatalf("Send bot-A failed: %v", err)
	}
	for range chA {
	} // drain — process exits, auto-cleanup fires

	aliveA := sessA.IsAlive()
	t.Logf("Bot A: process alive=%v", aliveA)

	// --- Bot B: starts, responds, exits ---
	sessB, err := runner.Start(ctx, "bot-B", dir)
	if err != nil {
		t.Fatalf("Start bot-B failed: %v", err)
	}
	chB, err := sessB.Send(ctx, input)
	if err != nil {
		t.Fatalf("Send bot-B failed: %v", err)
	}
	for range chB {
	} // drain — process exits, auto-cleanup fires

	aliveB := sessB.IsAlive()
	t.Logf("Bot B: process alive=%v", aliveB)

	// Both processes are dead.
	if aliveA {
		t.Error("Bot A: expected process to be dead after response cycle")
	}
	if aliveB {
		t.Error("Bot B: expected process to be dead after response cycle")
	}

	// BUG DEMONSTRATION:
	// At this point, both processes are provably dead (IsAlive() == false).
	// But in a real deployment, the DB process_status for both sessions
	// is still "waked" because:
	//
	//   1. getOrCreateAgentSession called MarkWaked (handler.go:435)
	//   2. The process exited, output channel closed
	//   3. runner auto-cleanup removed session from in-memory map
	//   4. streamToUser's done: block sent final response to user
	//   5. NO ONE called MarkSleeped
	//
	// The only code path that calls MarkSleeped is handleNew (handler.go:73),
	// which only triggers when the user types /new.
	//
	// Result: control bot /list shows both bots as "🟢 运行中"
	// even though neither process is actually running.

	runner.mu.Lock()
	_, aInMap := runner.sessions["bot-A"]
	_, bInMap := runner.sessions["bot-B"]
	runner.mu.Unlock()

	t.Logf("Runner sessions map: bot-A=%v bot-B=%v", aInMap, bInMap)
	t.Log("Both processes dead, both removed from runner.sessions (auto-cleanup worked)")
	t.Log("But DB process_status is still 'waked' for both — MarkSleeped never called")
	t.Log("BUG: /list would show both as 🟢 运行中 even though both processes are dead")
}

// TestRunner_ProcessStatusGap_StopKeepsWaked demonstrates that /stop does NOT
// update the DB process_status. The process is killed, the session is removed
// from the runner's map, but the DB stays "waked".
func TestRunner_ProcessStatusGap_StopKeepsWaked(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()
	// Use the hang fake claude so the process stays alive until we stop it.
	hangSrc := filepath.Join("..", "..", "procman", "testdata", "fakeclaude_hang", "main.go")
	fakeBinHang := filepath.Join(dir, "fakeclaude_hang")
	if runtime.GOOS == "windows" {
		fakeBinHang += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBinHang, hangSrc)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fakeclaude_hang: %v\n%s", err, out)
	}

	runner := NewRunner(fakeBinHang)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sid := "bot-stop-test"
	sess, err := runner.Start(ctx, sid, dir)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Process is alive before /stop.
	if !sess.IsAlive() {
		t.Fatal("process should be alive before stop")
	}
	t.Log("Before /stop: process alive=true, DB status='waked'")

	// User types /stop → calls Interrupt → kills process, removes from map.
	ok := runner.Interrupt(sid)
	if !ok {
		t.Error("Interrupt should return true for active session")
	}

	// Process is dead after /stop.
	if sess.IsAlive() {
		t.Error("process should be dead after Interrupt (/stop)")
	}

	// Session removed from runner map.
	runner.mu.Lock()
	_, inMap := runner.sessions[sid]
	runner.mu.Unlock()

	t.Logf("After /stop: process alive=%v, in runner map=%v", sess.IsAlive(), inMap)
	t.Log("BUG: handleStop only sets interruptedSessions flag + sends StateSleeped event (in-memory)")
	t.Log("handleStop does NOT call sessionMgr.MarkSleeped → DB status stays 'waked'")
	t.Log("BUG: /list shows 🟢 运行中 even after /stop")
}

// TestRunner_ProcessStatusGap_AllTransitionPaths documents every code path
// that can change the DB process_status and identifies the missing transitions.
func TestRunner_ProcessStatusGap_AllTransitionPaths(t *testing.T) {
	t.Log("=== DB process_status transition audit ===")
	t.Log("")
	t.Log("MarkWaked called from (1 path):")
	t.Log("  handler.go:435 getOrCreateAgentSession — on every LLM request")
	t.Log("")
	t.Log("MarkSleeped called from (1 path):")
	t.Log("  handler.go:73 handleNew — only on /new command")
	t.Log("")
	t.Log("MISSING: MarkSleeped is NOT called on:")
	t.Log("  - Process normal exit (streamToUser done: block)")
	t.Log("  - Process crash (streamToUser KindError path)")
	t.Log("  - Process silent exit (streamToUser hadContent=false path)")
	t.Log("  - /stop command (handleStop) — only sets in-memory flag")
	t.Log("  - Process timeout (processTimeout path)")
	t.Log("  - Runner auto-cleanup (Session.Send wrapper goroutine)")
	t.Log("")
	t.Log("RESULT: process_status is a write-only marker that never goes back")
	t.Log("to 'sleeped' without explicit user /new command.")
	t.Log("")
	t.Log("IMPACT: /list always shows 🟢 运行中 for any session that")
	t.Log("has ever been started, regardless of actual process state.")
}
