// Package procman manages agent subprocess lifecycles.
// It starts, monitors, and stops agent CLI processes (Claude Code, etc.),
// bridging their stdin/stdout to Go channels for the rest of the system.
package procman

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"linkcode/internal/agent"
)

// ansiRE matches ANSI escape sequences (CSI, OSC, and bare ESC sequences).
// CSI: ESC[ + optional parameters + final byte (0x40-0x7E)
// OSC: ESC] + content + BEL (0x07) or ESC\ (ST)
// Bare ESC: ESC followed by a single byte (not [ or ])
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b\][^\x1b]*\x1b\\|\x1b[^\[].`)

// Process wraps a running agent subprocess.
type Process struct {
	cmd              *exec.Cmd
	stdin            io.WriteCloser
	output           chan agent.OutputChunk
	cancel           context.CancelFunc
	mu               sync.Mutex
	alive            bool
	diedAt           time.Time // when the process exited (set in waitForExit)
	sessionID        string    // sessionID passed to procman (for resume)
	claudeSessionID  string // session_id extracted from claude's init message
}

// Start launches a Claude Code subprocess with the given session ID.
// If sessionID is empty, a new session is created with a generated UUID.
// If sessionID is non-empty, it is used with --resume to continue an existing session.
func Start(ctx context.Context, claudePath, workDir, sessionID string) (*Process, error) {
	ctx, cancel := context.WithCancel(ctx)

	args := []string{"-p"}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	} else {
		sessionID = newUUID()
		args = append(args, "--session-id", sessionID)
	}
	args = append(args, "--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions")

	cmd := exec.CommandContext(ctx, claudePath, args...)
	cmd.Env = os.Environ()
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "ANTHROPIC") {
			log.Printf("[procman] env: %s", e)
		}
	}
	if workDir != "" {
		if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
			log.Printf("[procman] workDir %q unavailable (%v), using current directory", workDir, err)
		} else {
			cmd.Dir = workDir
		}
	}

	log.Printf("[procman] starting: %s %s", claudePath, strings.Join(args, " "))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("procman: stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("procman: stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("procman: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("procman: start %s: %w", claudePath, err)
	}

	p := &Process{
		cmd:         cmd,
		stdin:       stdin,
		output:      make(chan agent.OutputChunk, 256),
		cancel:      cancel,
		alive:       true,
		sessionID:   sessionID,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.readOutput(stdout) }()
	go func() { defer wg.Done(); p.readStderr(stderr) }()
	go p.waitForExit(&wg)

	return p, nil
}

// Send writes input to the agent's stdin and closes stdin to signal end of input.
// In -p mode, claude will process the input, output the response, and exit.
// Returns a channel that streams output chunks and closes when the process exits.
func (p *Process) Send(ctx context.Context, input string) (<-chan agent.OutputChunk, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.alive {
		return nil, fmt.Errorf("procman: process not alive")
	}

	_, err := io.WriteString(p.stdin, input+"\n")
	if err != nil {
		return nil, fmt.Errorf("procman: write stdin: %w", err)
	}

	// Close stdin so claude (-p mode) knows input is complete.
	_ = p.stdin.Close()

	return p.output, nil
}

// Stop terminates the process.
func (p *Process) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.alive {
		return nil
	}

	p.alive = false
	p.cancel()

	if err := p.stdin.Close(); err != nil {
		// Ignore close errors.
		_ = err
	}

	timeout := 5 * time.Second
	done := make(chan error, 1)
	go func() {
		done <- p.cmd.Wait()
	}()

	select {
	case <-time.After(timeout):
		if killErr := p.cmd.Process.Kill(); killErr != nil {
			return fmt.Errorf("procman: kill process: %w", killErr)
		}
	case err := <-done:
		if err != nil {
			return fmt.Errorf("procman: wait process: %w", err)
		}
	}

	// Channel closed by readOutput goroutine when process exits.
	return nil
}

// SessionID returns the Claude session identifier (from init message or the passed-in sessionID).
func (p *Process) SessionID() string {
	if p.claudeSessionID != "" {
		return p.claudeSessionID
	}
	return p.sessionID
}

// IsAlive returns whether the process is running.
func (p *Process) IsAlive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.alive
}

// DiedAt returns the time the process exited, or zero if still running.
func (p *Process) DiedAt() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.diedAt
}

// OutputChannel returns the raw output channel for the router to consume.
func (p *Process) OutputChannel() <-chan agent.OutputChunk {
	return p.output
}

func (p *Process) readOutput(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	firstLine := true
	for scanner.Scan() {
		line := scanner.Bytes()

		// Extract session_id from the first init message.
		if firstLine {
			firstLine = false
			if sid := extractSessionID(line); sid != "" {
				p.claudeSessionID = sid
			}
		}

		chunk := parseOutputLine(line)
		select {
		case p.output <- chunk:
		default:
		}
	}

	if err := scanner.Err(); err != nil {
		p.mu.Lock()
		if p.alive {
			select {
			case p.output <- agent.OutputChunk{
				Kind:    agent.KindError,
				Content: fmt.Sprintf("procman: read stdout: %v", err),
			}:
			default:
			}
		}
		p.mu.Unlock()
	}
}

func (p *Process) readStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		select {
		case p.output <- agent.OutputChunk{
			Kind:    agent.KindError,
			Content: "[stderr] " + line,
		}:
		default:
		}
	}
}

func (p *Process) waitForExit(readersDone *sync.WaitGroup) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[procman] waitForExit panic recovered: %v", r)
		}
	}()

	err := p.cmd.Wait()
	readersDone.Wait() // ensure all stdout/stderr is consumed before closing channel

	p.mu.Lock()
	if p.alive && err != nil {
		select {
		case p.output <- agent.OutputChunk{
			Kind:    agent.KindError,
			Content: fmt.Sprintf("process exited: %v", err),
		}:
		default:
		}
	}
	p.alive = false
	p.mu.Unlock()

	close(p.output)
}

// claudeStreamJSON is the JSON format for Claude Code stream-json output.
// Format: {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}}
// or:     {"type":"result","subtype":"success","result":"...","stop_reason":"end_turn"}
// or:     {"type":"system","subtype":"init",...}
type claudeStreamJSON struct {
	Type    string              `json:"type"`
	Subtype string              `json:"subtype"`
	Result  string              `json:"result"`
	Message *claudeStreamMsg    `json:"message"`
}

type claudeStreamMsg struct {
	Content []claudeStreamContent `json:"content"`
}

type claudeStreamContent struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
}

// extractSessionID parses the session_id from a claude init message.
func extractSessionID(line []byte) string {
	var raw struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(line, &raw) == nil && raw.SessionID != "" {
		return raw.SessionID
	}
	return ""
}

// newUUID generates a random v4 UUID string.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// stripANSI removes ANSI escape sequences and isolated control characters from s.
// This prevents terminal formatting codes from appearing as garbled text in IM clients.
func stripANSI(s string) string {
	if s == "" {
		return s
	}
	return ansiRE.ReplaceAllString(s, "")
}

// askUserQuestionInput mirrors the JSON shape of an AskUserQuestion tool_use input.
type askUserQuestionInput struct {
	Questions []agent.QuestionItem `json:"questions"`
}

// parseQuestion extracts a structured Question from an AskUserQuestion tool_use.
func parseQuestion(toolUseID string, raw json.RawMessage) *agent.Question {
	var in askUserQuestionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil
	}
	if len(in.Questions) == 0 {
		return nil
	}
	return &agent.Question{
		ToolUseID: toolUseID,
		Questions: in.Questions,
	}
}

func parseOutputLine(line []byte) agent.OutputChunk {
	var raw claudeStreamJSON
	if err := json.Unmarshal(line, &raw); err != nil {
		return agent.OutputChunk{Kind: agent.KindText, Content: stripANSI(string(line))}
	}

	switch raw.Type {
	case "system":
		return agent.OutputChunk{Kind: agent.KindThinking}
	case "assistant":
		if raw.Message != nil && len(raw.Message.Content) > 0 {
			c := raw.Message.Content[0]
			switch c.Type {
			case "thinking":
				return agent.OutputChunk{Kind: agent.KindThinking, Content: stripANSI(c.Thinking)}
			case "text":
				return agent.OutputChunk{Kind: agent.KindText, Content: stripANSI(c.Text)}
			case "tool_use":
				if c.Name == "AskUserQuestion" {
					if q := parseQuestion(c.ID, c.Input); q != nil {
						return agent.OutputChunk{Kind: agent.KindQuestion, Question: q}
					}
				}
				return agent.OutputChunk{Kind: agent.KindToolUse, Content: stripANSI(c.Text)}
			default:
				return agent.OutputChunk{Kind: agent.KindThinking}
			}
		}
		return agent.OutputChunk{Kind: agent.KindThinking}
	case "result":
		if raw.Subtype == "error" || raw.Subtype == "error_during_execution" {
			return agent.OutputChunk{Kind: agent.KindError, Content: stripANSI(raw.Result)}
		}
		return agent.OutputChunk{Kind: agent.KindFinal, Content: stripANSI(raw.Result)}
	default:
		return agent.OutputChunk{Kind: agent.KindText, Content: stripANSI(raw.Result)}
	}
}
