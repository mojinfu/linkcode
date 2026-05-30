// Package gateway manages all WeCom WebSocket connections.
// Each Bot (control or worker) gets its own Channel, and the gateway tracks
// which bot is bound to which session for message routing.
package gateway

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"linkcode/internal/channel"
	"linkcode/internal/channel/wecom"
)

// ConnStatus is a snapshot of a worker bot's WebSocket connection state.
type ConnStatus struct {
	Connected  bool
	LastChange time.Time
}

// Gateway manages the lifecycle of all IM bot channels.
type Gateway struct {
	controlChan channel.Channel

	workerMsgHandler    channel.MessageHandler
	workerEventHandler  channel.EventHandler
	connChangeHandler   func(botInternalID int64, platformBotID string, connected bool)

	mu         sync.Mutex
	workerChans map[int64]*workerEntry // botInternalID -> channel
}

type workerEntry struct {
	ch            channel.Channel
	platformBotID string
	connState     connState
}

type connState struct {
	connected  bool
	lastChange time.Time
}

// New creates a new Gateway with the control bot channel.
func New(controlChan channel.Channel) *Gateway {
	return &Gateway{
		controlChan: controlChan,
		workerChans: make(map[int64]*workerEntry),
	}
}

// SetWorkerMessageHandler sets the handler applied to all worker bot channels.
func (g *Gateway) SetWorkerMessageHandler(h channel.MessageHandler) {
	g.workerMsgHandler = h
}

// SetWorkerEventHandler sets the event handler applied to all worker bot channels.
func (g *Gateway) SetWorkerEventHandler(h channel.EventHandler) {
	g.workerEventHandler = h
}

// SetConnectionChangeHandler sets the callback invoked when any worker bot's
// WebSocket connects or disconnects.
func (g *Gateway) SetConnectionChangeHandler(h func(botInternalID int64, platformBotID string, connected bool)) {
	g.connChangeHandler = h
}

// OpenWorkerChannel creates a WebSocket connection for a worker bot.
// botInternalID is the database ID of the bot.
// platformBotID and secret are the WeCom credentials.
func (g *Gateway) OpenWorkerChannel(ctx context.Context, botInternalID int64, platformBotID, secret string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.workerChans[botInternalID]; exists {
		return nil // already open
	}

	ch := wecom.New(platformBotID, secret)

	// Create entry first so the connection-change callback can update it.
	entry := &workerEntry{
		ch:            ch,
		platformBotID: platformBotID,
	}
	g.workerChans[botInternalID] = entry

	// Register handlers BEFORE connecting — the readLoop and connection-change
	// callback both fire during Connect.
	if g.workerMsgHandler != nil {
		ch.OnMessage(g.workerMsgHandler)
	}
	if g.workerEventHandler != nil {
		ch.OnEvent(g.workerEventHandler)
	}
	ch.OnConnectionChange(func(connected bool) {
		g.updateConnState(botInternalID, connected)
		if g.connChangeHandler != nil {
			g.connChangeHandler(botInternalID, platformBotID, connected)
		}
	})

	if err := ch.Connect(ctx); err != nil {
		delete(g.workerChans, botInternalID)
		return fmt.Errorf("gateway: open worker channel for bot %d (%s): %w", botInternalID, platformBotID, err)
	}

	log.Printf("[gateway] worker bot %d (%s) connected", botInternalID, platformBotID)
	return nil
}

// CloseWorkerChannel closes and removes a worker bot channel.
func (g *Gateway) CloseWorkerChannel(botInternalID int64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.workerChans[botInternalID]
	if !ok {
		return
	}
	entry.ch.Close()
	delete(g.workerChans, botInternalID)
	log.Printf("[gateway] worker bot %d (%s) disconnected", botInternalID, entry.platformBotID)
}

// ReopenWorkerChannel closes the existing worker channel (if any) and opens a
// new one with the same credentials. Used by the control bot to recover dead
// worker bot connections.
func (g *Gateway) ReopenWorkerChannel(ctx context.Context, botInternalID int64, platformBotID, secret string) error {
	g.mu.Lock()
	if entry, ok := g.workerChans[botInternalID]; ok {
		entry.ch.Close()
		delete(g.workerChans, botInternalID)
		log.Printf("[gateway] worker bot %d (%s) closed for reopen", botInternalID, entry.platformBotID)
	}
	g.mu.Unlock()

	return g.OpenWorkerChannel(ctx, botInternalID, platformBotID, secret)
}

// GetWorkerChannel returns the channel for a worker bot by internal ID.
func (g *Gateway) GetWorkerChannel(botInternalID int64) (channel.Channel, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	entry, ok := g.workerChans[botInternalID]
	if !ok {
		return nil, false
	}
	return entry.ch, true
}

// GetWorkerByPlatformID returns the worker channel by platform bot_id.
func (g *Gateway) GetWorkerByPlatformID(platformBotID string) (channel.Channel, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, entry := range g.workerChans {
		if entry.platformBotID == platformBotID {
			return entry.ch, true
		}
	}
	return nil, false
}

// ControlChannel returns the control bot channel.
func (g *Gateway) ControlChannel() channel.Channel {
	return g.controlChan
}

// CloseAll closes all worker channels and the control channel.
func (g *Gateway) CloseAll() {
	g.mu.Lock()
	defer g.mu.Unlock()

	for id, entry := range g.workerChans {
		entry.ch.Close()
		delete(g.workerChans, id)
	}

	if g.controlChan != nil {
		g.controlChan.Close()
	}
}

// updateConnState records a WebSocket connection state change for a worker bot.
// Must be called with g.mu held or from a context where it's safe to lock.
func (g *Gateway) updateConnState(botInternalID int64, connected bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.workerChans[botInternalID]
	if !ok {
		return
	}
	entry.connState.connected = connected
	entry.connState.lastChange = time.Now()
}

// GetWorkerConnStatus returns the last known connection state for a worker bot.
// It cross-checks the tracked state against the live channel's IsConnected()
// to guard against missed events.
func (g *Gateway) GetWorkerConnStatus(botInternalID int64) ConnStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	entry, ok := g.workerChans[botInternalID]
	if !ok {
		return ConnStatus{}
	}
	// Cross-check: use the live channel state as ground truth.
	live := entry.ch.IsConnected()
	if live != entry.connState.connected {
		entry.connState.connected = live
		entry.connState.lastChange = time.Now()
	}
	return ConnStatus{
		Connected:  entry.connState.connected,
		LastChange: entry.connState.lastChange,
	}
}

// GetAllWorkerStatuses returns connection status snapshots for all worker bots.
// Keyed by bot internal ID.
func (g *Gateway) GetAllWorkerStatuses() map[int64]ConnStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	result := make(map[int64]ConnStatus, len(g.workerChans))
	for id, entry := range g.workerChans {
		live := entry.ch.IsConnected()
		if live != entry.connState.connected {
			entry.connState.connected = live
			entry.connState.lastChange = time.Now()
		}
		result[id] = ConnStatus{
			Connected:  entry.connState.connected,
			LastChange: entry.connState.lastChange,
		}
	}
	return result
}
