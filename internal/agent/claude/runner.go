// Package claude implements the agent.Runner interface for Claude Code CLI.
package claude

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"linkcode/internal/agent"
	"linkcode/internal/procman"
)

// Runner manages Claude Code sessions via the CLI.
type Runner struct {
	claudePath string

	mu       sync.Mutex
	sessions map[string]*Session // linkCodeSessionID -> running session
}

// NewRunner creates a new Claude Code runner.
func NewRunner(claudePath string) *Runner {
	return &Runner{
		claudePath: claudePath,
		sessions:   make(map[string]*Session),
	}
}

// Name returns the agent type.
func (r *Runner) Name() string { return "claude-code" }

// Start launches a new Claude Code session (no prior session to resume).
// linkCodeSessionID is the internal LinkCode session identifier used as the key.
func (r *Runner) Start(ctx context.Context, linkCodeSessionID, workDir string) (agent.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.sessions[linkCodeSessionID]; ok {
		if existing.process.IsAlive() {
			return existing, agent.ErrBusy
		}
		// Rapid restart protection: if the process died within 30s, delay to
		// avoid repeatedly spawning processes that fail fast.
		if diedAt := existing.process.DiedAt(); !diedAt.IsZero() {
			sinceDeath := time.Since(diedAt)
			if sinceDeath < 30*time.Second {
				log.Printf("[runner] session %s: rapid restart detected (died %v ago), delaying 5s", linkCodeSessionID, sinceDeath)
				time.Sleep(5 * time.Second)
			}
		}
		delete(r.sessions, linkCodeSessionID)
	}

	proc, err := procman.Start(ctx, r.claudePath, workDir, "")
	if err != nil {
		return nil, fmt.Errorf("claude: start: %w", err)
	}

	s := &Session{
		runner:  r,
		id:      linkCodeSessionID,
		process: proc,
	}
	r.sessions[linkCodeSessionID] = s

	return s, nil
}

// Resume resumes an existing Claude Code session using its Claude session ID.
func (r *Runner) Resume(ctx context.Context, linkCodeSessionID, claudeSessionID, workDir string) (agent.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.sessions[linkCodeSessionID]; ok {
		if existing.process.IsAlive() {
			return existing, agent.ErrBusy
		}
		if diedAt := existing.process.DiedAt(); !diedAt.IsZero() {
			sinceDeath := time.Since(diedAt)
			if sinceDeath < 30*time.Second {
				log.Printf("[runner] session %s: rapid restart detected (died %v ago), delaying 5s", linkCodeSessionID, sinceDeath)
				time.Sleep(5 * time.Second)
			}
		}
		delete(r.sessions, linkCodeSessionID)
	}

	proc, err := procman.Start(ctx, r.claudePath, workDir, claudeSessionID)
	if err != nil {
		return nil, fmt.Errorf("claude: resume: %w", err)
	}

	s := &Session{
		runner:  r,
		id:      linkCodeSessionID,
		process: proc,
	}
	r.sessions[linkCodeSessionID] = s

	return s, nil
}

// Interrupt stops the active process for a LinkCode session without removing
// the session record from the database. Returns true if a process was killed.
func (r *Runner) Interrupt(linkCodeSessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[linkCodeSessionID]
	if !ok || !s.process.IsAlive() {
		return false
	}
	s.process.Stop()
	delete(r.sessions, linkCodeSessionID)
	return true
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
// It wraps the output channel so that when the process exits, the session
// is automatically removed from the runner's map (dead session cleanup).
func (s *Session) Send(ctx context.Context, input string) (<-chan agent.OutputChunk, error) {
	ch, err := s.process.Send(ctx, input)
	if err != nil {
		return nil, err
	}

	wrapped := make(chan agent.OutputChunk, 256)
	go func() {
		defer close(wrapped)
		for chunk := range ch {
			wrapped <- chunk
		}
		if !s.process.IsAlive() {
			s.runner.removeSession(s.id)
			log.Printf("[runner] session %s auto-removed (process dead)", s.id)
		}
	}()
	return wrapped, nil
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
