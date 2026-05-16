// Package claude implements the agent.Runner interface for Claude Code CLI.
package claude

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"linkcode/internal/agent"
	"linkcode/internal/procman"
)

// ErrSessionExists is returned when trying to start a session that is already running.
var ErrSessionExists = errors.New("claude: session already running")

// Runner manages Claude Code sessions via the CLI.
type Runner struct {
	claudePath string
	workDir    string

	mu       sync.Mutex
	sessions map[string]*Session // linkCodeSessionID -> running session
}

// NewRunner creates a new Claude Code runner.
func NewRunner(claudePath, workDir string) *Runner {
	return &Runner{
		claudePath: claudePath,
		workDir:    workDir,
		sessions:   make(map[string]*Session),
	}
}

// Name returns the agent type.
func (r *Runner) Name() string { return "claude-code" }

// Start launches a new Claude Code session (no prior session to resume).
// linkCodeSessionID is the internal LinkCode session identifier used as the key.
func (r *Runner) Start(ctx context.Context, linkCodeSessionID string) (agent.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.sessions[linkCodeSessionID]; ok {
		if existing.process.IsAlive() {
			return existing, ErrSessionExists
		}
		delete(r.sessions, linkCodeSessionID)
	}

	proc, err := procman.Start(ctx, r.claudePath, r.workDir, "")
	if err != nil {
		return nil, fmt.Errorf("claude: start: %w", err)
	}

	s := &Session{
		id:      linkCodeSessionID,
		process: proc,
	}
	r.sessions[linkCodeSessionID] = s

	return s, nil
}

// Resume resumes an existing Claude Code session using its Claude session ID.
func (r *Runner) Resume(ctx context.Context, linkCodeSessionID, claudeSessionID string) (agent.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.sessions[linkCodeSessionID]; ok {
		if existing.process.IsAlive() {
			return existing, ErrSessionExists
		}
		delete(r.sessions, linkCodeSessionID)
	}

	proc, err := procman.Start(ctx, r.claudePath, r.workDir, claudeSessionID)
	if err != nil {
		return nil, fmt.Errorf("claude: resume: %w", err)
	}

	s := &Session{
		id:      linkCodeSessionID,
		process: proc,
	}
	r.sessions[linkCodeSessionID] = s

	return s, nil
}

// removeSession is called by Session.Stop to clean up the map entry.
func (r *Runner) removeSession(id string) {
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

// Session wraps a Claude Code process and implements agent.Session.
type Session struct {
	runner  *Runner
	id      string
	process *procman.Process
}

// Send sends input to the Claude Code process.
func (s *Session) Send(ctx context.Context, input string) (<-chan agent.OutputChunk, error) {
	return s.process.Send(ctx, input)
}

// Stop terminates the Claude Code process.
func (s *Session) Stop() error {
	s.runner.removeSession(s.id)
	return s.process.Stop()
}

// ID returns the LinkCode session identifier.
func (s *Session) ID() string {
	return s.id
}

// IsAlive returns whether the underlying process is running.
func (s *Session) IsAlive() bool {
	return s.process.IsAlive()
}

// ClaudeSessionID returns the Claude-side session identifier (UUID).
func (s *Session) ClaudeSessionID() string {
	return s.process.SessionID()
}
