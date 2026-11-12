// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wshub

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"log/slog"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// defaultMaxConcurrentFrames bounds how many inbound frames the hub handles at
// once. A frame handler must be quick — business work is dispatched elsewhere
// — so a small bound is enough, and it is what stops one client from turning a
// burst of frames into an unbounded set of goroutines.
const defaultMaxConcurrentFrames = 64

// Option configures a Hub at construction.
type Option func(*Hub)

// WithAllowedOrigins restricts browser WebSocket connections to the given
// origins. Entries may be full origins ("https://console.example.com") or bare
// hosts ("console.example.com"); an empty list keeps the historical
// allow-all behaviour, so existing deployments are not broken by the upgrade.
// Requests without an Origin header (non-browser clients) are always allowed.
func WithAllowedOrigins(origins []string) Option {
	return func(h *Hub) {
		h.allowedOrigins = make(map[string]struct{}, len(origins))
		for _, origin := range origins {
			origin = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(origin)), "/")
			if origin == "" {
				continue
			}
			h.allowedOrigins[origin] = struct{}{}
			if u, err := url.Parse(origin); err == nil && u.Host != "" {
				h.allowedOrigins[u.Host] = struct{}{}
			}
		}
	}
}

// WithMaxConcurrentFrames bounds the number of inbound frames handled at once.
// A non-positive value keeps the default.
func WithMaxConcurrentFrames(n int) Option {
	return func(h *Hub) {
		if n > 0 {
			h.frameSlots = make(chan struct{}, n)
		}
	}
}

// inboundMessage bundles a frame with the sender's client ID.
type inboundMessage struct {
	clientID string
	frame    ClientFrame
}

// Hub manages all active WebSocket connections and routes inbound frames.
type Hub struct {
	clients    map[string]*Client
	register   chan *Client
	unregister chan *Client
	inbound    chan inboundMessage

	// subscriptions maps convID → set of clientIDs that are watching it.
	subscriptions map[string]map[string]struct{}
	subMu         sync.RWMutex
	clientsMu     sync.RWMutex // protects clients map

	upgrader       websocket.Upgrader
	allowedOrigins map[string]struct{}

	// frameSlots bounds concurrent OnFrame handlers.
	frameSlots chan struct{}

	// OnFrame is called for every frame received from a client.
	// It is set by the handler layer; safe to call concurrently via goroutine.
	OnFrame func(clientID string, frame ClientFrame)
}

func NewHub(opts ...Option) *Hub {
	h := &Hub{
		clients:       make(map[string]*Client),
		register:      make(chan *Client, 64),
		unregister:    make(chan *Client, 64),
		inbound:       make(chan inboundMessage, 256),
		subscriptions: make(map[string]map[string]struct{}),
		frameSlots:    make(chan struct{}, defaultMaxConcurrentFrames),
	}
	for _, opt := range opts {
		opt(h)
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.checkOrigin,
	}
	return h
}

// checkOrigin enforces the configured allowlist for browser clients. A missing
// Origin header is not a browser and is allowed; a configured but non-matching
// origin is refused. With no allowlist configured the historical allow-all
// behaviour is preserved.
func (h *Hub) checkOrigin(r *http.Request) bool {
	origin := strings.TrimSuffix(strings.ToLower(r.Header.Get("Origin")), "/")
	if origin == "" || len(h.allowedOrigins) == 0 {
		return true
	}
	if _, ok := h.allowedOrigins[origin]; ok {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	_, ok := h.allowedOrigins[u.Host]
	return ok
}

// Run is the hub event loop; run in a dedicated goroutine.
func (h *Hub) Run() {
	for {
		select {
		case c := <-h.register:
			h.clientsMu.Lock()
			h.clients[c.id] = c
			h.clientsMu.Unlock()
			slog.Info("WebSocket client connected", "client_id", c.id)
		case c := <-h.unregister:
			h.clientsMu.Lock()
			if _, ok := h.clients[c.id]; ok {
				delete(h.clients, c.id)
				close(c.send)
			}
			h.clientsMu.Unlock()
			h.removeClientSubscriptions(c.id)
			slog.Info("WebSocket client disconnected", "client_id", c.id)
		case message := <-h.inbound:
			if h.OnFrame == nil {
				continue
			}
			// Bounded dispatch: a slow or malicious client must not turn a
			// frame burst into an unbounded set of handler goroutines.
			select {
			case h.frameSlots <- struct{}{}:
				go func(clientID string, frame ClientFrame) {
					defer func() { <-h.frameSlots }()
					defer func() {
						if r := recover(); r != nil {
							slog.Error("OnFrame panicked", "client_id", clientID, "panic", r)
						}
					}()
					h.OnFrame(clientID, frame)
				}(message.clientID, message.frame)
			default:
				slog.Warn("WebSocket frame dropped: handler limit reached", "client_id", message.clientID)
			}
		}
	}
}

// Upgrade upgrades an HTTP request to a WebSocket connection, registers the
// new client with its authenticated identity, and returns its assigned ID.
func (h *Hub) Upgrade(w http.ResponseWriter, r *http.Request, userID, openID string) (string, error) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	c := &Client{id: id, hub: h, conn: conn, send: make(chan []byte, 256), userID: userID, openID: openID}
	h.register <- c
	go c.readPump()
	go c.writePump()
	return id, nil
}

// ClientIdentity returns the authenticated userID and openID of the given
// client, or empty strings if the client is unknown.
func (h *Hub) ClientIdentity(clientID string) (userID, openID string) {
	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()
	if c, ok := h.clients[clientID]; ok {
		return c.userID, c.openID
	}
	return "", ""
}

// Subscribe registers clientID as a listener for messages in convID.
func (h *Hub) Subscribe(clientID, convID string) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	if h.subscriptions[convID] == nil {
		h.subscriptions[convID] = make(map[string]struct{})
	}
	h.subscriptions[convID][clientID] = struct{}{}
}

// Unsubscribe removes clientID from the convID subscription.
func (h *Hub) Unsubscribe(clientID, convID string) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	if subs, ok := h.subscriptions[convID]; ok {
		delete(subs, clientID)
		if len(subs) == 0 {
			delete(h.subscriptions, convID)
		}
	}
}

// removeClientSubscriptions cleans up all subscriptions for a disconnected client.
func (h *Hub) removeClientSubscriptions(clientID string) {
	h.subMu.Lock()
	defer h.subMu.Unlock()
	for convID, subs := range h.subscriptions {
		delete(subs, clientID)
		if len(subs) == 0 {
			delete(h.subscriptions, convID)
		}
	}
}

// SendToConv serializes a ServerFrame as JSON and sends it to all clients
// subscribed to convID.
func (h *Hub) SendToConv(convID string, frame ServerFrame) {
	raw, err := json.Marshal(frame)
	if err != nil {
		slog.Warn("Failed to marshal server frame", "error", err)
		return
	}
	h.subMu.RLock()
	subs := make([]string, 0, len(h.subscriptions[convID]))
	for clientID := range h.subscriptions[convID] {
		subs = append(subs, clientID)
	}
	h.subMu.RUnlock()

	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()
	for _, clientID := range subs {
		c, ok := h.clients[clientID]
		if !ok {
			continue
		}
		select {
		case c.send <- raw:
		default:
			slog.Warn("Client send buffer full, dropping frame", "client_id", clientID)
		}
	}
}

// SendTo serializes a ServerFrame as JSON and sends it to the client with the
// given ID. Non-blocking: drops the frame if the client's send buffer is full.
func (h *Hub) SendTo(clientID string, frame ServerFrame) {
	h.clientsMu.RLock()
	c, ok := h.clients[clientID]
	h.clientsMu.RUnlock()
	if !ok {
		return
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		slog.Warn("Failed to marshal server frame", "error", err)
		return
	}
	select {
	case c.send <- raw:
	default:
		slog.Warn("Client send buffer full, dropping frame", "client_id", clientID)
	}
}

// Broadcast sends a ServerFrame to all connected clients (use SendToConv for
// conversation-scoped delivery).
func (h *Hub) Broadcast(frame ServerFrame) {
	raw, err := json.Marshal(frame)
	if err != nil {
		return
	}
	h.clientsMu.RLock()
	defer h.clientsMu.RUnlock()
	for _, c := range h.clients {
		select {
		case c.send <- raw:
		default:
		}
	}
}
