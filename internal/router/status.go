package router

import (
	"sync"

	"linkcode/internal/gateway"
	"linkcode/internal/session"
)

// StatusState represents the current state of a worker bot.
type StatusState int

const (
	StateWaking       StatusState = iota // process starting
	StateThinking                        // agent is producing KindThinking chunks
	StateReplying                        // agent is producing KindText chunks
	StateIdle                            // agent is alive, waiting for input
	StateSleeped                         // session ended or timed out
	StateDizzy                           // error state
	StateCompacting                      // agent is compacting context
	StateReconnecting                    // websocket is reconnecting
)

// StatusEvent carries a state transition for a specific session.
type StatusEvent struct {
	SessionID   int64       // LinkCode session ID
	BotID       string      // platform bot_id
	UserID      string      // platform user_id
	ChatID      string      // chat ID
	SessionName string      // display name
	State       StatusState // new state
}

// StatusManager tracks per-session status state.
// User-visible status is shown as a prefix in the reply stream bubble (router.go).
// This manager exists for future internal state tracking (e.g., admin panel).
type StatusManager struct {
	mu       sync.Mutex
	sessions map[int64]*statusSession
	gw       *gateway.Gateway

	sessionMgr *session.Manager
}

type statusSession struct {
	botID       string
	userID      string
	chatID      string
	sessionName string
}

// NewStatusManager creates a StatusManager.
func NewStatusManager(gw *gateway.Gateway, sessionMgr *session.Manager) *StatusManager {
	return &StatusManager{
		sessions:   make(map[int64]*statusSession),
		gw:         gw,
		sessionMgr: sessionMgr,
	}
}

// Send stores a status event for the session (non-blocking, no side effects).
func (sm *StatusManager) Send(event StatusEvent) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, ok := sm.sessions[event.SessionID]; !ok {
		if event.BotID == "" || event.UserID == "" {
			return
		}
		sm.sessions[event.SessionID] = &statusSession{
			botID:       event.BotID,
			userID:      event.UserID,
			chatID:      event.ChatID,
			sessionName: event.SessionName,
		}
	}
}

// StopSession removes a session from tracking.
func (sm *StatusManager) StopSession(sessionID int64) {
	sm.mu.Lock()
	delete(sm.sessions, sessionID)
	sm.mu.Unlock()
}

// HandleConnectionChange is called when a worker bot WebSocket connects or disconnects.
func (sm *StatusManager) HandleConnectionChange(botInternalID int64, _ string, connected bool) {
	// Connection state is reflected in the next reply stream prefix.
	// No proactive message needed here — it would appear as a separate bubble.
}
