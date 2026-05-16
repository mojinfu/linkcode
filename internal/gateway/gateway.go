// Package gateway manages all WeCom WebSocket connections.
// Each Bot (control or worker) gets its own Channel, and the gateway tracks
// which bot is bound to which session for message routing.
package gateway

import (
	"context"
	"fmt"
	"log"
	"sync"

	"linkcode/internal/channel"
	"linkcode/internal/channel/wecom"
)

// Gateway manages the lifecycle of all IM bot channels.
type Gateway struct {
	controlChan channel.Channel

	workerMsgHandler   channel.MessageHandler
	workerEventHandler channel.EventHandler

	mu         sync.Mutex
	workerChans map[int64]*workerEntry // botInternalID -> channel
}

type workerEntry struct {
	ch       channel.Channel
	platformBotID string
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
	if err := ch.Connect(ctx); err != nil {
		return fmt.Errorf("gateway: open worker channel for bot %d (%s): %w", botInternalID, platformBotID, err)
	}

	// Attach the shared worker message and event handlers.
	if g.workerMsgHandler != nil {
		ch.OnMessage(g.workerMsgHandler)
	}
	if g.workerEventHandler != nil {
		ch.OnEvent(g.workerEventHandler)
	}

	g.workerChans[botInternalID] = &workerEntry{
		ch:            ch,
		platformBotID: platformBotID,
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
