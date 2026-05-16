package procman

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
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
	proc, err := Start(ctx, claudePath, "", "")
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
	proc, err := Start(ctx, claudePath, "", resumeID)
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

	proc, err := Start(ctx, claudePath, "", "")
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer proc.Stop()

	sid := proc.SessionID()
	if sid == "" {
		t.Fatal("SessionID() returned empty")
	}

	outputCh, err := proc.Send(ctx, "Say exactly: hello world")
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
