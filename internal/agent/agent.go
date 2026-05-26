// Package agent defines the abstraction for AI agent runners.
// Each agent type (Claude Code, Kimi Code, etc.) implements this interface.
package agent

import "context"

// OutputChunk represents a piece of streaming output from an agent.
type OutputChunk struct {
	Content  string
	Kind     OutputKind
	Question *Question // non-nil when Kind == KindQuestion
}

// Question represents a structured AskUserQuestion extracted from a tool_use.
type Question struct {
	ToolUseID string         `json:"toolUseId"`
	Questions []QuestionItem `json:"questions"`
}

// QuestionItem is a single question within an AskUserQuestion tool_use.
type QuestionItem struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multiSelect"`
}

// QuestionOption is one selectable option in a question.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// OutputKind categorizes the type of agent output.
type OutputKind string

const (
	KindText     OutputKind = "text"
	KindThinking OutputKind = "thinking"
	KindToolUse  OutputKind = "tool_use"
	KindFinal    OutputKind = "final"
	KindError    OutputKind = "error"
	KindQuestion OutputKind = "question"
)

// Runner manages the lifecycle of agent sessions.
// Implementations handle starting/resuming the underlying agent process.
type Runner interface {
	// Start creates a new agent session and launches the process.
	// linkCodeSessionID is the internal LinkCode session identifier.
	Start(ctx context.Context, linkCodeSessionID string) (Session, error)

	// Resume resumes an existing session.
	// linkCodeSessionID is the internal LinkCode session identifier.
	// providerSessionID is the agent-provider-specific session ID (e.g. Claude's --session-id).
	Resume(ctx context.Context, linkCodeSessionID, providerSessionID string) (Session, error)

	// Interrupt stops the active process for the given LinkCode session.
	// Returns true if a process was running and has been stopped.
	Interrupt(linkCodeSessionID string) bool

	// Name returns the agent type name (e.g. "claude-code", "kimi-code").
	Name() string
}

// Session represents a running agent session.
// Input is sent via Send, output streams back via a Go channel.
type Session interface {
	// Send sends user input to the agent and returns a channel for streaming output.
	// The channel closes when the agent finishes processing.
	Send(ctx context.Context, input string) (<-chan OutputChunk, error)

	// Stop terminates the agent process. The Session cannot be reused after Stop.
	Stop() error

	// ID returns the session identifier.
	ID() string

	// IsAlive returns whether the underlying process is still running.
	IsAlive() bool

	// ClaudeSessionID returns the Claude-side session identifier (UUID), if any.
	ClaudeSessionID() string


}
