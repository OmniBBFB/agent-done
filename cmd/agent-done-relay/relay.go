package main

import (
	"crypto/sha256"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	subprotocol     = "agent-done.relay.v1"
	maxPayload      = 256 << 10
	maxFrame        = (maxPayload+2)/3*4 + 1024
	writeTimeout    = 3 * time.Second
	heartbeat       = 20 * time.Second
	heartbeatWait   = 60 * time.Second
	framesPerSecond = 30
)

type incoming struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	To      string `json:"to"`
	Payload []byte `json:"payload,omitempty"`
}

type outgoing struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Device  string `json:"device,omitempty"`
	From    string `json:"from,omitempty"`
	Payload []byte `json:"payload,omitempty"`
	Online  *bool  `json:"online,omitempty"`
	Status  string `json:"status,omitempty"`
}

type peer struct {
	device device
	ws     *websocket.Conn
	writes sync.Mutex
}

func (p *peer) write(message outgoing) error {
	if err := p.ws.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return p.ws.WriteJSON(message)
}

func (p *peer) control(message outgoing) error {
	p.writes.Lock()
	defer p.writes.Unlock()
	return p.write(message)
}

type relay struct {
	mu       sync.Mutex
	registry registry
	peers    map[string]*peer
	closed   bool
	upgrader websocket.Upgrader
	log      *log.Logger
}

func newRelay(devices registry, logger *log.Logger) *relay {
	return &relay{
		registry: devices, peers: make(map[string]*peer), log: logger,
		upgrader: websocket.Upgrader{
			Subprotocols:     []string{subprotocol},
			HandshakeTimeout: writeTimeout,
			// Native clients use Authorization, with no browser Origin.
			CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" },
		},
	}
}

func (r *relay) authenticate(header string) (device, bool) {
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || len(token) != 43 {
		return device{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.registry.byToken[sha256.Sum256([]byte(token))]
	return d, ok && !r.closed
}

func (r *relay) attach(p *peer) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, ok := r.registry.byID[p.device.id]
	if r.closed || !ok || current.digest != p.device.digest || current.role != p.device.role {
		return false
	}
	previous := r.peers[current.id]
	r.peers[current.id] = p
	if previous != nil {
		previous.ws.Close()
	}
	r.log.Printf("device=%s connected", current.id)
	return true
}

func (r *relay) detach(p *peer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers[p.device.id] == p {
		delete(r.peers, p.device.id)
		r.log.Printf("device=%s disconnected", p.device.id)
	}
}

func (r *relay) reload(next registry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry = next
	for id, peer := range r.peers {
		d, ok := next.byID[id]
		if !ok || d.digest != peer.device.digest || d.role != peer.device.role {
			delete(r.peers, id)
			peer.ws.Close()
			r.log.Printf("device=%s credential revoked", id)
		}
	}
}

func (r *relay) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	for _, peer := range r.peers {
		peer.ws.Close()
	}
	clear(r.peers)
}

func (r *relay) target(sender *peer, targetID string) (*peer, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers[sender.device.id] != sender {
		return nil, false
	}
	source := r.registry.byID[sender.device.id]
	target, ok := r.registry.byID[targetID]
	if !ok || source.role == target.role || !source.allow[targetID] || !target.allow[source.id] {
		return nil, false
	}
	return r.peers[target.id], true
}

func (r *relay) forward(source *peer, request incoming) string {
	target, allowed := r.target(source, request.To)
	if !allowed {
		return "forbidden"
	}
	if target == nil {
		return "offline"
	}
	if !target.writes.TryLock() {
		return "busy"
	}
	defer target.writes.Unlock()
	// Recheck identity and route so a reconnect or registry update during
	// contention can't redirect a frame to a different connection.
	current, allowed := r.target(source, request.To)
	if !allowed {
		return "forbidden"
	}
	if current != target {
		return "offline"
	}
	if err := target.write(outgoing{Type: "delivery", From: source.device.id, Payload: request.Payload}); err != nil {
		target.ws.Close()
		return "delivery_unknown"
	}
	return "forwarded"
}

func (r *relay) handle(p *peer, frame int, data []byte) error {
	var request incoming
	if frame != websocket.TextMessage || decode(data, &request) != nil || !identifier.MatchString(request.ID) || !identifier.MatchString(request.To) {
		r.log.Printf("device=%s result=invalid_request", p.device.id)
		return p.control(outgoing{Type: "result", Status: "invalid_request"})
	}
	response := outgoing{Type: "result", ID: request.ID}
	switch request.Type {
	case "send":
		switch {
		case len(request.Payload) == 0:
			response.Status = "invalid_request"
		case len(request.Payload) > maxPayload:
			response.Status = "too_large"
		default:
			response.Status = r.forward(p, request)
		}
	case "presence":
		if len(request.Payload) != 0 {
			response.Status = "invalid_request"
		} else if target, allowed := r.target(p, request.To); allowed {
			online := target != nil
			response.Status, response.Online = "ok", &online
		} else {
			response.Status = "forbidden"
		}
	default:
		response.Status = "invalid_request"
	}
	if response.Status != "ok" && response.Status != "forwarded" {
		r.log.Printf("device=%s operation=%q result=%s", p.device.id, logOperation(request.Type), response.Status)
	}
	return p.control(response)
}

func logOperation(operation string) string {
	if operation == "send" || operation == "presence" {
		return operation
	}
	return "invalid"
}

func (r *relay) websocket(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	d, ok := r.authenticate(req.Header.Get("Authorization"))
	if !ok {
		r.log.Print("connection rejected: unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if !slices.Contains(websocket.Subprotocols(req), subprotocol) {
		http.Error(w, "unsupported relay protocol", http.StatusBadRequest)
		return
	}
	ws, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	p := &peer{device: d, ws: ws}
	p.writes.Lock()
	if !r.attach(p) {
		p.writes.Unlock()
		return
	}
	defer r.detach(p)
	err = p.write(outgoing{Type: "ready", Device: d.id})
	p.writes.Unlock()
	if err != nil {
		return
	}

	ws.SetReadLimit(maxFrame)
	ws.SetReadDeadline(time.Now().Add(heartbeatWait))
	ws.SetPongHandler(func(string) error {
		return ws.SetReadDeadline(time.Now().Add(heartbeatWait))
	})
	done := make(chan struct{})
	defer close(done)
	go ping(ws, done)
	windowStart, frames := time.Now(), 0
	for {
		frame, data, err := ws.ReadMessage()
		if err != nil {
			if errors.Is(err, websocket.ErrReadLimit) {
				r.log.Printf("device=%s result=frame_too_large", d.id)
			}
			return
		}
		if time.Since(windowStart) >= time.Second {
			windowStart, frames = time.Now(), 0
		}
		frames++
		if frames > framesPerSecond {
			r.log.Printf("device=%s result=rate_limited", d.id)
			p.control(outgoing{Type: "result", Status: "rate_limited"})
			return
		}
		if err := r.handle(p, frame, data); err != nil {
			return
		}
	}
}

func ping(ws *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				ws.Close()
				return
			}
		}
	}
}
