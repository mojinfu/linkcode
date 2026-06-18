package procman

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"linkcode/internal/agent"
)

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"plain text", "hello world", "hello world"},
		{"chinese text", "你好世界", "你好世界"},
		{"markdown formatting", "**bold** and *italic*", "**bold** and *italic*"},
		{"CSI color code", "\x1b[90mgray text\x1b[0m", "gray text"},
		{"CSI multi-param", "\x1b[1;32mbold green\x1b[0m", "bold green"},
		{"bare ESC sequence", "\x1b[2Jcleared", "cleared"},
		{"mixed ANSI and text", "normal \x1b[1mbold\x1b[0m normal", "normal bold normal"},
		{"code block with backticks", "```go\nfmt.Println(\"hi\")\n```", "```go\nfmt.Println(\"hi\")\n```"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripANSI(tt.input)
			if got != tt.want {
				t.Errorf("stripANSI(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// buildJSONLine constructs a valid JSON line with the given content fields.
// The text is JSON-escaped so that control characters (like ESC) are properly encoded.
func buildJSONLine(contentType, text string) []byte {
	textJSON, _ := json.Marshal(text)
	// json.Marshal returns "text", so we strip the quotes for embedding.
	textVal := string(textJSON)

	line := `{"type":"assistant","message":{"id":"abc","type":"message","role":"assistant","content":[{"type":"` + contentType + `","` + contentType + `":` + textVal + `}]}}`
	return []byte(line)
}

func TestParseOutputLine_StripsANSIFromText(t *testing.T) {
	// Simulate Claude output where text content contains ANSI escape codes.
	// json.Unmarshal decodes JSON-escaped ESC bytes, then stripANSI removes them.
	input := "normal \x1b[90mthinking...\x1b[0m response"
	line := buildJSONLine("text", input)
	chunk := parseOutputLine(line)
	if chunk.Kind != "text" {
		t.Fatalf("expected KindText, got %s", chunk.Kind)
	}
	if chunk.Content != "normal thinking... response" {
		t.Errorf("ANSI not stripped: got %q", chunk.Content)
	}
}

func TestParseOutputLine_StripsANSIFromThinking(t *testing.T) {
	input := "\x1b[90manalyzing...\x1b[0m"
	line := buildJSONLine("thinking", input)
	chunk := parseOutputLine(line)
	if chunk.Kind != "thinking" {
		t.Fatalf("expected KindThinking, got %s", chunk.Kind)
	}
	if chunk.Content != "analyzing..." {
		t.Errorf("ANSI not stripped from thinking content: got %q", chunk.Content)
	}
}

func TestParseOutputLine_StripsANSIFromError(t *testing.T) {
	contentJSON, _ := json.Marshal("\x1b[31mError: something went wrong\x1b[0m")
	line := `{"type":"result","subtype":"error","result":` + string(contentJSON) + `}`
	chunk := parseOutputLine([]byte(line))
	if chunk.Kind != "error" {
		t.Fatalf("expected KindError, got %s", chunk.Kind)
	}
	if chunk.Content != "Error: something went wrong" {
		t.Errorf("ANSI not stripped from error content: got %q", chunk.Content)
	}
}

func TestParseOutputLine_StripsANSIFromNonJSON(t *testing.T) {
	// When JSON parsing fails, the raw line is used as KindText.
	// ANSI stripping still applies to this fallback path.
	rawLine := "\x1b[90mnot valid json at all\x1b[0m"
	chunk := parseOutputLine([]byte(rawLine))
	if chunk.Kind != "text" {
		t.Fatalf("expected KindText, got %s", chunk.Kind)
	}
	if chunk.Content != "not valid json at all" {
		t.Errorf("ANSI not stripped from non-JSON line: got %q", chunk.Content)
	}
}

func TestExtractSessionID(t *testing.T) {
	// Simulates the first init line from Claude's stream-json output.
	initLine := `{"type":"system","subtype":"init","session_id":"a1b2c3d4-e5f6-7890-abcd-ef1234567890","claude_code_version":"2.1.142"}`
	sid := extractSessionID([]byte(initLine))
	if sid != "a1b2c3d4-e5f6-7890-abcd-ef1234567890" {
		t.Errorf("expected session_id from init line, got %q", sid)
	}

	// Non-init lines should return empty.
	assistantLine := `{"type":"assistant","message":{"id":"abc","type":"message","role":"assistant","content":[{"type":"text","text":"hello"}]}}`
	sid = extractSessionID([]byte(assistantLine))
	if sid != "" {
		t.Errorf("expected empty session_id from non-init line, got %q", sid)
	}

	// Garbage input should return empty.
	sid = extractSessionID([]byte("not json"))
	if sid != "" {
		t.Errorf("expected empty session_id from garbage, got %q", sid)
	}
}

func TestSessionID_NewSession_ReturnsUUIDImmediately(t *testing.T) {
	// Simulate a process started with an empty sessionID (new session).
	// The fix ensures p.sessionID is set to the generated UUID.
	p := &Process{
		sessionID: "deadbeef-dead-beef-dead-beefdeadbeef",
		alive:     true,
		cmd:       exec.Command("true"), // dummy
	}

	sid := p.SessionID()
	if sid != "deadbeef-dead-beef-dead-beefdeadbeef" {
		t.Errorf("expected generated UUID, got %q", sid)
	}
}

func TestSessionID_ResumeSession_ReturnsPassedIDImmediately(t *testing.T) {
	// Simulate a process started with a non-empty sessionID (resume).
	p := &Process{
		sessionID: "resume-session-uuid-12345",
		alive:     true,
		cmd:       exec.Command("true"),
	}

	sid := p.SessionID()
	if sid != "resume-session-uuid-12345" {
		t.Errorf("expected resume session UUID, got %q", sid)
	}
}

func TestSessionID_ClaudeSessionIDTakesPrecedence(t *testing.T) {
	// After readOutput extracts the session_id from the init line,
	// claudeSessionID should take precedence over sessionID.
	p := &Process{
		sessionID:       "generated-uuid",
		claudeSessionID: "extracted-from-init-uuid",
		alive:           true,
		cmd:             exec.Command("true"),
	}

	sid := p.SessionID()
	if sid != "extracted-from-init-uuid" {
		t.Errorf("expected claudeSessionID to take precedence, got %q", sid)
	}
}

// waitClaudePath tries to find the claude binary; returns empty if not found.
func findClaudePath() string {
	for _, p := range []string{
		"/Users/mojinfu/apps/claude/bin/claude",
		"/usr/local/bin/claude",
	} {
		if _, err := exec.LookPath(p); err == nil {
			return p
		}
	}
	// Try generic PATH lookup.
	if p, err := exec.LookPath("claude"); err == nil {
		return p
	}
	return ""
}

func TestStart_NewSession_SessionIDIsNonEmpty(t *testing.T) {
	claudePath := findClaudePath()
	if claudePath == "" {
		t.Skip("claude binary not found, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Start a new session (empty sessionID).
	proc, err := Start(ctx, claudePath, "", "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer proc.Stop()

	// Immediately after Start returns, SessionID() must be non-empty.
	// This is the core fix: without it, SessionID() returns "" due to a race condition.
	sid := proc.SessionID()
	if sid == "" {
		t.Fatal("SessionID() returned empty string immediately after Start() — race condition not fixed")
	}

	// Verify it looks like a UUID (contains hyphens).
	if !strings.Contains(sid, "-") {
		t.Errorf("SessionID() = %q, expected a UUID-like value", sid)
	}

	t.Logf("SessionID immediately available: %s", sid)
}

func TestStart_ResumeSession_SessionIDReturnsPassedID(t *testing.T) {
	claudePath := findClaudePath()
	if claudePath == "" {
		t.Skip("claude binary not found, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resumeID := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	proc, err := Start(ctx, claudePath, "", resumeID, nil)
	if err != nil {
		t.Fatalf("Start() with resume ID failed: %v", err)
	}
	defer proc.Stop()

	sid := proc.SessionID()
	if sid != resumeID {
		t.Errorf("SessionID() = %q, expected resume ID %q", sid, resumeID)
	}
}

func TestStart_SendAndReceive(t *testing.T) {
	claudePath := findClaudePath()
	if claudePath == "" {
		t.Skip("claude binary not found, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	proc, err := Start(ctx, claudePath, "", "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer proc.Stop()

	sid := proc.SessionID()
	if sid == "" {
		t.Fatal("SessionID() returned empty")
	}

	// Build a stream-json user message (now required by --input-format stream-json).
	inputJSON := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Say exactly: hello world"}]}}`
	outputCh, err := proc.Send(ctx, inputJSON)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var gotText bool
	for chunk := range outputCh {
		if chunk.Kind == "error" {
			t.Logf("agent error: %s", chunk.Content)
		}
		if chunk.Kind == "text" && strings.Contains(strings.ToLower(chunk.Content), "hello world") {
			gotText = true
		}
	}

	if !gotText {
		t.Error("did not get expected 'hello world' response from agent")
	}
}

func TestParseQuestion(t *testing.T) {
	// Simulate an AskUserQuestion tool_use input JSON.
	input := json.RawMessage(`{"questions":[{"question":"Which color?","header":"Color","options":[{"label":"Red","description":"Warm color"},{"label":"Blue","description":"Cool color"}],"multiSelect":false}]}`)
	q := parseQuestion("call_00_test123", input)
	if q == nil {
		t.Fatal("parseQuestion returned nil")
	}
	if q.ToolUseID != "call_00_test123" {
		t.Errorf("ToolUseID = %q, want call_00_test123", q.ToolUseID)
	}
	if len(q.Questions) != 1 {
		t.Fatalf("len(Questions) = %d, want 1", len(q.Questions))
	}
	if q.Questions[0].Header != "Color" {
		t.Errorf("Header = %q, want Color", q.Questions[0].Header)
	}
	if len(q.Questions[0].Options) != 2 {
		t.Errorf("len(Options) = %d, want 2", len(q.Questions[0].Options))
	}
}

func TestParseQuestion_InvalidInput(t *testing.T) {
	if q := parseQuestion("id", json.RawMessage(`not json`)); q != nil {
		t.Error("expected nil for invalid JSON")
	}
	if q := parseQuestion("id", json.RawMessage(`{"other":"field"}`)); q != nil {
		t.Error("expected nil for non-question JSON")
	}
}

func TestParseOutputLine_KindQuestion(t *testing.T) {
	// Build a stream-json line with an AskUserQuestion tool_use.
	input := json.RawMessage(`{"questions":[{"question":"Pick one","header":"Choice","options":[{"label":"A","description":""}],"multiSelect":false}]}`)
	inputJSON, _ := json.Marshal(input)
	line := `{"type":"assistant","message":{"id":"abc","type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_00_q1","name":"AskUserQuestion","input":` + string(inputJSON) + `}]}}`

	chunk := parseOutputLine([]byte(line))
	if chunk.Kind != "question" {
		t.Fatalf("expected KindQuestion, got %s", chunk.Kind)
	}
	if chunk.Question == nil {
		t.Fatal("Question is nil")
	}
	if chunk.Question.ToolUseID != "call_00_q1" {
		t.Errorf("ToolUseID = %q", chunk.Question.ToolUseID)
	}
}

func TestParseOutputLine_NonAskUserQuestion_ToolUse(t *testing.T) {
	// A Write tool_use should still be KindToolUse, not KindQuestion.
	input := json.RawMessage(`{"file_path":"/tmp/test.txt","content":"hello"}`)
	inputJSON, _ := json.Marshal(input)
	line := `{"type":"assistant","message":{"id":"abc","type":"message","role":"assistant","content":[{"type":"tool_use","id":"call_00_w1","name":"Write","input":` + string(inputJSON) + `}]}}`

	chunk := parseOutputLine([]byte(line))
	if chunk.Kind != "tool_use" {
		t.Fatalf("expected KindToolUse, got %s", chunk.Kind)
	}
}

// TestStart_ErrorThenSuccess verifies that procman correctly surfaces process
// errors (stderr + exit code) and that a subsequent Start of the same logical
// session succeeds. It uses a compiled fake Claude binary:
//
//	first run  → writes to stderr, outputs error result, exits 1
//	second run → outputs valid stream-json (init + assistant text + result)
//
// This mirrors the real-world scenario where a user sends two messages to the
// same worker bot: the first triggers an agent crash, the second should complete.
func TestStart_ErrorThenSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	// Build fake Claude binary from testdata/fakeclaude/main.go.
	fakeBin := filepath.Join(dir, "fakeclaude")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude: %v\n%s", err, out)
	}

	inputJSON := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`

	// ---- First run: expect errors surfaced via output channel ----
	ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel1()

	proc1, err := Start(ctx1, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("first Start() failed: %v", err)
	}

	outputCh1, err := proc1.Send(ctx1, inputJSON)
	if err != nil {
		t.Fatalf("first Send() failed: %v", err)
	}

	var errors1 []string
	for chunk := range outputCh1 {
		if chunk.Kind == agent.KindError {
			errors1 = append(errors1, chunk.Content)
		}
	}

	if len(errors1) == 0 {
		t.Error("first run: expected at least one error chunk (stderr + exit error), got none")
	}
	if proc1.IsAlive() {
		t.Error("first run: process should be dead after error exit")
	}
	t.Logf("first run errors: %v", errors1)

	// ---- Second run: expect normal stream-json output ----
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	proc2, err := Start(ctx2, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("second Start() failed: %v", err)
	}

	outputCh2, err := proc2.Send(ctx2, inputJSON)
	if err != nil {
		t.Fatalf("second Send() failed: %v", err)
	}

	var errors2 []string
	var gotHello bool
	for chunk := range outputCh2 {
		if chunk.Kind == agent.KindError {
			errors2 = append(errors2, chunk.Content)
		}
		if chunk.Kind == agent.KindText && strings.Contains(chunk.Content, "hello from fake claude") {
			gotHello = true
		}
	}

	if len(errors2) > 0 {
		t.Errorf("second run: expected no errors, got %v", errors2)
	}
	if !gotHello {
		t.Error("second run: expected 'hello from fake claude' in text output")
	}
}

// TestStart_NonJSONOutput verifies that procman handles non-JSON garbage output
// (e.g. a panic dump instead of stream-json) gracefully:
//   - each non-JSON line becomes a KindText chunk (fallback path)
//   - stderr lines are captured as KindError
//   - exit error is captured as KindError
//   - a subsequent Start works normally
func TestStart_NonJSONOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	fakeBin := filepath.Join(dir, "fakeclaude_garbage")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_garbage/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude garbage: %v\n%s", err, out)
	}

	inputJSON := `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`

	// ---- First run: garbage output ----
	ctx1, cancel1 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel1()

	proc1, err := Start(ctx1, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("first Start() failed: %v", err)
	}

	outputCh1, err := proc1.Send(ctx1, inputJSON)
	if err != nil {
		t.Fatalf("first Send() failed: %v", err)
	}

	var texts1 []string
	var errors1 []string
	for chunk := range outputCh1 {
		switch chunk.Kind {
		case agent.KindError:
			errors1 = append(errors1, chunk.Content)
		case agent.KindText:
			texts1 = append(texts1, chunk.Content)
		}
	}

	// Should have stderr + exit error at minimum.
	if len(errors1) == 0 {
		t.Error("first run: expected at least one error chunk (stderr + exit error), got none")
	}
	// Should have non-JSON lines delivered as text (the panic dump).
	if len(texts1) == 0 {
		t.Error("first run: expected non-JSON lines delivered as text chunks, got none")
	}
	t.Logf("first run: %d errors, %d text chunks", len(errors1), len(texts1))
	for _, e := range errors1 {
		t.Logf("  error: %s", e)
	}
	for _, txt := range texts1 {
		t.Logf("  text: %s", txt)
	}

	// ---- Second run: normal output ----
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	proc2, err := Start(ctx2, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("second Start() failed: %v", err)
	}

	outputCh2, err := proc2.Send(ctx2, inputJSON)
	if err != nil {
		t.Fatalf("second Send() failed: %v", err)
	}

	var gotRecovered bool
	for chunk := range outputCh2 {
		if chunk.Kind == agent.KindError {
			t.Errorf("second run: unexpected error: %s", chunk.Content)
		}
		if chunk.Kind == agent.KindText && strings.Contains(chunk.Content, "recovered and working") {
			gotRecovered = true
		}
	}

	if !gotRecovered {
		t.Error("second run: expected 'recovered and working' in text output")
	}
}

// TestStart_SilentKill verifies procman behavior when a process is killed
// externally without ever writing to stdout. This mirrors the "silent crash"
// scenario where Claude's process dies before producing any output.
//
// The fake Claude blocks on stdin read (no stdout output). The test uses
// a short context timeout to kill it, then observes what the output channel
// delivers. We use context cancellation instead of proc.Stop() to avoid the
// double cmd.Wait() race between waitForExit and Stop.
func TestStart_SilentKill(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	fakeBin := filepath.Join(dir, "fakeclaude_hang")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_hang/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude silent: %v\n%s", err, out)
	}

	// Short context that auto-kills the process after 2 seconds.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	proc, err := Start(ctx, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Send closes stdin, which unblocks the fake claude's stdin-read loop.
	outputCh, err := proc.Send(ctx, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}`)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var chunks []agent.OutputChunk
	for chunk := range outputCh {
		chunks = append(chunks, chunk)
	}

	t.Logf("silent kill produced %d chunks:", len(chunks))
	for _, c := range chunks {
		t.Logf("  kind=%s content=%s", c.Kind, c.Content)
	}

	// After stdin close, the hang process exits cleanly (code 0).
	// No stdout output → no KindText or KindFinal chunks.
	// No stderr output → no stderr error chunks.
	// Exit code 0 → no "process exited" error in waitForExit.
	//
	// Result: 0 chunks. This is the "hadContent=false" scenario that
	// handler.go's streamToUser must detect as silent close.
	if len(chunks) > 0 {
		for _, c := range chunks {
			t.Logf("unexpected chunk: kind=%s content=%s", c.Kind, c.Content)
		}
	}
	t.Logf("chunk count: %d (0 = silent close → handler shows [💫] 无响应)", len(chunks))
}

// TestStart_ChannelCloseRace verifies the WaitGroup coordination fix:
// when a process closes stdout before actually exiting, the exit error
// must still be delivered to the output channel before it's closed.
//
// Without the fix (defer close in readOutput), the channel would close
// before waitForExit gets to send the exit error.
func TestStart_ChannelCloseRace(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	fakeBin := filepath.Join(dir, "fakeclaude_race")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_race/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude race: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	proc, err := Start(ctx, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	outputCh, err := proc.Send(ctx, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var chunks []agent.OutputChunk
	for chunk := range outputCh {
		chunks = append(chunks, chunk)
	}

	t.Logf("channel close race produced %d chunks:", len(chunks))
	for _, c := range chunks {
		t.Logf("  kind=%s content=%s", c.Kind, c.Content)
	}

	// The key assertion: the exit error must be present.
	// Without the WaitGroup fix, this chunk is lost because readOutput's
	// defer close(p.output) closes the channel before waitForExit can send.
	var hasExitError bool
	var hasInit bool
	for _, c := range chunks {
		if c.Kind == agent.KindError && strings.Contains(c.Content, "process exited") {
			hasExitError = true
		}
		if c.Kind == agent.KindThinking {
			hasInit = true
		}
	}
	if !hasInit {
		t.Log("no init system message (expected: stdout closed before init parse)")
	}
	if !hasExitError {
		t.Error("exit error was lost — channel close race bug may still be present")
	}
}

// TestStart_TextThenError verifies that when a process outputs valid text
// and then crashes, both KindText and KindError chunks are delivered.
func TestStart_TextThenError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	fakeBin := filepath.Join(dir, "fakeclaude_texterr")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_texterr/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude texterr: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proc, err := Start(ctx, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	outputCh, err := proc.Send(ctx, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
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
		t.Error("expected at least one KindText chunk with partial response, got none")
	}
	if len(errors) == 0 {
		t.Error("expected at least one KindError chunk (stderr + exit error), got none")
	}
}

// TestStart_SlowStream verifies that procman delivers chunks in order
// when the process outputs with delays between lines.
func TestStart_SlowStream(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	fakeBin := filepath.Join(dir, "fakeclaude_slow")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_slow/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude slow: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	proc, err := Start(ctx, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	outputCh, err := proc.Send(ctx, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var texts []string
	for chunk := range outputCh {
		if chunk.Kind == agent.KindText {
			texts = append(texts, chunk.Content)
		}
	}

	t.Logf("got %d text chunks", len(texts))
	for i, txt := range texts {
		t.Logf("  [%d] %s", i, txt)
	}

	if len(texts) < 5 {
		t.Errorf("expected at least 5 text chunks, got %d", len(texts))
	}
	for i := 1; i <= len(texts); i++ {
		expected := fmt.Sprintf("chunk %d of 5", i)
		found := false
		for _, txt := range texts {
			if strings.Contains(txt, expected) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing expected chunk: %q", expected)
		}
	}
}

// TestStart_StderrOnly verifies that when a process only writes to stderr
// and then exits, all stderr lines are captured as KindError chunks.
func TestStart_StderrOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	fakeBin := filepath.Join(dir, "fakeclaude_stderronly")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_stderronly/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude stderronly: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proc, err := Start(ctx, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	outputCh, err := proc.Send(ctx, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var errors []string
	var texts []string
	for chunk := range outputCh {
		switch chunk.Kind {
		case agent.KindError:
			errors = append(errors, chunk.Content)
		case agent.KindText:
			texts = append(texts, chunk.Content)
		}
	}

	t.Logf("stderr-only: %d errors, %d texts", len(errors), len(texts))
	for _, e := range errors {
		t.Logf("  error: %s", e)
	}

	if len(errors) == 0 {
		t.Error("expected at least one KindError from stderr lines")
	}
	// No stdout → no KindText chunks (except possibly the raw exit error).
	if len(texts) > 0 {
		t.Logf("unexpected text chunks (may be exit error fallback): %v", texts)
	}
}

// TestStart_InstantExitNoOutput verifies procman behavior when a process
// exits immediately with code 1 and produces zero stdout/stderr.
func TestStart_InstantExitNoOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	fakeBin := filepath.Join(dir, "fakeclaude_instantexit")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_instantexit/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude instantexit: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	proc, err := Start(ctx, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	outputCh, err := proc.Send(ctx, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var chunks []agent.OutputChunk
	for chunk := range outputCh {
		chunks = append(chunks, chunk)
	}

	t.Logf("instant exit produced %d chunks:", len(chunks))
	for _, c := range chunks {
		t.Logf("  kind=%s content=%s", c.Kind, c.Content)
	}

	// The only chunk should be the exit error from waitForExit.
	if len(chunks) == 0 {
		t.Error("expected at least the exit error chunk, got nothing — channel may have closed silently")
	}
	var hasExitError bool
	for _, c := range chunks {
		if c.Kind == agent.KindError && strings.Contains(c.Content, "process exited") {
			hasExitError = true
		}
	}
	if !hasExitError {
		t.Error("expected exit error chunk, not found")
	}
}

// TestStart_LargeOutput verifies that procman handles a large number of
// output lines without blocking or dropping chunks.
func TestStart_LargeOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	dir := t.TempDir()

	fakeBin := filepath.Join(dir, "fakeclaude_large")
	if runtime.GOOS == "windows" {
		fakeBin += ".exe"
	}
	build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_large/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake claude large: %v\n%s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	proc, err := Start(ctx, fakeBin, dir, "", nil)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	outputCh, err := proc.Send(ctx, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
	if err != nil {
		t.Fatalf("Send() failed: %v", err)
	}

	var textCount, errorCount, finalCount int
	for chunk := range outputCh {
		switch chunk.Kind {
		case agent.KindText:
			textCount++
		case agent.KindError:
			errorCount++
		case agent.KindFinal:
			finalCount++
		}
	}

	t.Logf("large output: %d text, %d errors, %d final", textCount, errorCount, finalCount)

	if textCount < 600 {
		t.Errorf("expected at least 600 text chunks, got %d", textCount)
	}
	if finalCount != 1 {
		t.Errorf("expected exactly 1 final chunk, got %d", finalCount)
	}
	if errorCount > 0 {
		t.Errorf("expected 0 errors, got %d", errorCount)
	}
}

	// TestStop_ClosesOutputChannel reproduces the /stop-not-working bug:
	// when a process is streaming output (user sent a message, agent is thinking),
	// and Stop() is called from another goroutine (user sent /stop),
	// the output channel must close promptly so streamToUser can exit.
	//
	// The bug: procman.Stop() and waitForExit() both call cmd.Wait() on the same
	// process. On Windows, Go's os/exec explicitly documents that multiple
	// simultaneous Wait calls are not supported and may cause undefined behavior.
	// On all platforms, the double-Wait creates a race where:
	//   1. Stop()'s Wait() consumes the process state
	//   2. waitForExit's Wait() blocks indefinitely (already consumed)
	//   3. close(p.output) is never called
	//   4. streamToUser spins forever — user sees elapsed time keep increasing
	func TestStop_ClosesOutputChannel(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping integration test in short mode")
		}

		dir := t.TempDir()

		fakeBin := filepath.Join(dir, "fakeclaude_stoppable")
		if runtime.GOOS == "windows" {
			fakeBin += ".exe"
		}
		build := exec.Command("go", "build", "-o", fakeBin, "testdata/fakeclaude_stoppable/main.go")
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build fake claude stoppable: %v\n%s", err, out)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		proc, err := Start(ctx, fakeBin, dir, "", nil)
		if err != nil {
			t.Fatalf("Start() failed: %v", err)
		}

		outputCh, err := proc.Send(ctx, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`)
		if err != nil {
			t.Fatalf("Send() failed: %v", err)
		}

		// Consume a few chunks to confirm the process is alive and streaming.
		var firstChunks []agent.OutputChunk
		timeout := time.After(3 * time.Second)
		for i := 0; i < 3; {
			select {
			case chunk, ok := <-outputCh:
				if !ok {
					t.Fatal("output channel closed before we could call Stop() — process died too early")
				}
				if chunk.Kind == agent.KindText {
					firstChunks = append(firstChunks, chunk)
					i++
				}
			case <-timeout:
				t.Fatal("timed out waiting for initial chunks — process not streaming?")
			}
		}
		t.Logf("got %d initial chunks, process is alive=%v", len(firstChunks), proc.IsAlive())

		if !proc.IsAlive() {
			t.Fatal("process died before Stop() — cannot reproduce the bug")
		}

		// Simulate /stop: call Stop() from this goroutine while the output
		// consumer (below) is still reading.
		stopDone := make(chan error, 1)
		go func() {
			stopDone <- proc.Stop()
		}()

		// The output channel MUST close within a reasonable time after Stop().
		// If the double-Wait bug exists, waitForExit's cmd.Wait() hangs and
		// close(p.output) is never called — the test will time out here.
		channelClosed := make(chan struct{})
		go func() {
			for range outputCh {
				// drain remaining chunks
			}
			close(channelClosed)
		}()

		select {
		case <-channelClosed:
			t.Log("output channel closed promptly after Stop() — no bug")
		case <-time.After(8 * time.Second):
			// The double-Wait bug: waitForExit's cmd.Wait() is stuck because
			// Stop() already consumed the process exit state.
			t.Error("BUG REPRODUCED: output channel did NOT close within 8s of Stop()")
			t.Error("This means streamToUser would spin forever with increasing elapsed time.")
			t.Error("Root cause: double cmd.Wait() in Stop() + waitForExit().")
			t.Error("On Windows this is explicitly documented as undefined behavior.")
		}

		// Verify Stop() itself returns.
		select {
		case err := <-stopDone:
			if err != nil {
				t.Logf("Stop() returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Stop() itself timed out after 5s")
		}

		if proc.IsAlive() {
			t.Error("process still marked alive after Stop() returned")
		}
	}
