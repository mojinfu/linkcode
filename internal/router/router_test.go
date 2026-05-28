package router

import (
	"testing"

	"linkcode/internal/agent"
	"linkcode/internal/channel"
	"linkcode/internal/channel/wecom"
)

func TestBuildQuotePrefix(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		n      int
		expect string
	}{
		{
			name:   "short message",
			input:  "hello",
			n:      30,
			expect: "> hello\n",
		},
		{
			name:   "long message truncated",
			input:  "这是一条很长的消息用来测试截断功能的正确性",
			n:      10,
			expect: "> 这是一条很长的消息用...\n",
		},
		{
			name:   "exactly n chars",
			input:  "12345",
			n:      5,
			expect: "> 12345\n",
		},
		{
			name:   "empty message",
			input:  "",
			n:      30,
			expect: "> \n",
		},
		{
			name:   "emoji in message",
			input:  "hello 😀 world!!!",
			n:      10,
			expect: "> hello 😀 wo...\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quotePrefix(wecom.WeComStyler{}, tt.input, tt.n)
			if got != tt.expect {
				t.Errorf("quotePrefix(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.expect)
			}
		})
	}
}

func TestResolveAnswer(t *testing.T) {
	opts := []agent.QuestionOption{
		{Label: "A", Description: "first"},
		{Label: "B", Description: "second"},
		{Label: "C", Description: "third"},
	}
	qi := agent.QuestionItem{Options: opts}

	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{name: "single number", input: "1", expect: "A"},
		{name: "single number 2", input: "2", expect: "B"},
		{name: "out of range", input: "5", expect: "5"},
		{name: "plain text", input: "hello", expect: "hello"},
		{name: "multi-select comma", input: "1,3", expect: "A, C"},
		{name: "multi-select spaces", input: "1, 2 , 3", expect: "A, B, C"},
		{name: "zero not valid", input: "0", expect: "0"},
		{name: "negative", input: "-1", expect: "-1"},
		{name: "empty", input: "", expect: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAnswer(qi, tt.input)
			if got != tt.expect {
				t.Errorf("resolveAnswer(%q) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}

func TestFormatAnswerText(t *testing.T) {
	q := &agent.Question{
		ToolUseID: "tool_123",
		Questions: []agent.QuestionItem{
			{
				Question: "Which model?",
				Options: []agent.QuestionOption{
					{Label: "Opus", Description: "best"},
					{Label: "Sonnet", Description: "fast"},
				},
			},
		},
	}

	got := formatAnswerText(q, "2")
	expect := "[用户回答]\n问题: Which model?\n回答: Sonnet"
	if got != expect {
		t.Errorf("formatAnswerText = %q, want %q", got, expect)
	}
}

func TestFormatAnswerTextMultiSelect(t *testing.T) {
	q := &agent.Question{
		ToolUseID: "tool_456",
		Questions: []agent.QuestionItem{
			{
				Question:    "Pick options",
				MultiSelect: true,
				Options: []agent.QuestionOption{
					{Label: "Red"},
					{Label: "Green"},
					{Label: "Blue"},
				},
			},
		},
	}

	got := formatAnswerText(q, "1,3")
	expect := "[用户回答]\n问题: Pick options\n回答: Red, Blue"
	if got != expect {
		t.Errorf("formatAnswerText = %q, want %q", got, expect)
	}
}

func TestBuildTextInput(t *testing.T) {
	got := buildTextInput("hello world")
	if len(got) == 0 {
		t.Error("buildTextInput returned empty string")
	}
	// Verify it contains the input text.
	if !contains(got, "hello world") {
		t.Errorf("buildTextInput(%q) = %q, expected to contain input", "hello world", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestSpinPrefix(t *testing.T) {
	runes := []rune("⣷⣯⣟⡿")
	name := "test"

	got := spinPrefix(runes, 0, 0, name)
	if len(got) == 0 {
		t.Error("spinPrefix returned empty")
	}
}

// -- classifyMessage tests --

func TestClassifyVoiceMessage(t *testing.T) {
	msg := channel.Message{MsgType: "voice", Content: ""}
	if got := classifyMessage(msg); got != msgKindVoice {
		t.Errorf("classifyMessage(voice) = %d, want msgKindVoice", got)
	}
}

func TestClassifyCommandMessage(t *testing.T) {
	tests := []string{"/stop", "/end", "/list", "/help", "/start"}
	for _, cmd := range tests {
		t.Run(cmd, func(t *testing.T) {
			msg := channel.Message{MsgType: "text", Content: cmd}
			if got := classifyMessage(msg); got != msgKindCommand {
				t.Errorf("classifyMessage(%q) = %d, want msgKindCommand", cmd, got)
			}
		})
	}
}

func TestClassifyTextMessage(t *testing.T) {
	tests := []string{"hello", "帮我分析一下", "123", "", " what", "/", "/ "}
	for _, content := range tests {
		t.Run(content, func(t *testing.T) {
			msg := channel.Message{MsgType: "text", Content: content}
			if got := classifyMessage(msg); got != msgKindText {
				t.Errorf("classifyMessage(%q) = %d, want msgKindText", content, got)
			}
		})
	}
}

func TestClassifyVoiceTakesPriority(t *testing.T) {
	// Voice + /stop content: voice classification wins.
	msg := channel.Message{MsgType: "voice", Content: "/stop"}
	if got := classifyMessage(msg); got != msgKindVoice {
		t.Errorf("classifyMessage(voice+cmd) = %d, want msgKindVoice", got)
	}
}

// -- parseCommand tests --

func TestParseCommand(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
	}{
		{name: "stop", input: "/stop", expect: "/stop"},
		{name: "end", input: "/end", expect: "/end"},
		{name: "list", input: "/list", expect: "/list"},
		{name: "help", input: "/help", expect: "/help"},
		{name: "not command", input: "hello", expect: ""},
		{name: "empty", input: "", expect: ""},
		{name: "bare slash", input: "/", expect: ""},
		{name: "space after slash", input: "/ notacommand", expect: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCommand(tt.input); got != tt.expect {
				t.Errorf("parseCommand(%q) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}
