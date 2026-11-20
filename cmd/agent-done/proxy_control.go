package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OmniBBFB/agent-done/internal/remote"
	"github.com/gorilla/websocket"
)

type proxyRequest struct {
	Operation   string
	Session     string
	Interaction string
	Reply       remote.InteractionReply
	Intent      string
	Text        string
	Expires     int64
}

type proxyResult struct {
	Cards   []remote.Interaction
	Status  string
	Problem *remote.Problem
}

type proxyCard struct {
	session string
	turn    string
	rpc     any
	card    remote.Interaction
	status  string
}

type proxyTurn struct {
	session string
	result  chan proxyResult
}

type proxyControl struct {
	mu            sync.Mutex
	observer      *Observer
	upstream      *websocket.Conn
	cards         map[string]*proxyCard
	byRPC         map[string]*proxyCard
	active        map[string]string
	desktopStarts map[string]string
	phoneStarts   map[string]string
	phoneCalls    map[string]proxyTurn
}

func newProxyControl(observer *Observer, upstream *websocket.Conn) *proxyControl {
	return &proxyControl{
		observer: observer, upstream: upstream,
		cards: make(map[string]*proxyCard), byRPC: make(map[string]*proxyCard),
		active: make(map[string]string), desktopStarts: make(map[string]string),
		phoneStarts: make(map[string]string), phoneCalls: make(map[string]proxyTurn),
	}
}

func proxyControlSocket(connection string) string {
	return filepath.Join(runtimeDir(), "c-"+connection)
}

func validProxyID(value string) bool {
	return len(value) == 32 && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9')
	}) < 0
}

func (o *Observer) tracks(session string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sessions[session]
}

func decodeRPC(raw []byte) map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var frame map[string]any
	_ = decoder.Decode(&frame)
	return frame
}

func (p *proxyControl) serve(ctx context.Context) func() {
	socket := proxyControlSocket(p.observer.connection)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		log.Printf("local Codex control unavailable: %v", err)
		return func() {}
	}
	if err := os.Chmod(socket, 0600); err != nil {
		listener.Close()
		log.Printf("local Codex control unavailable: %v", err)
		return func() {}
	}
	go func() {
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			go p.handle(ctx, conn)
		}
	}()
	return func() { listener.Close() }
}

func (p *proxyControl) handle(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	raw, err := bufio.NewReaderSize(conn, 192<<10).ReadSlice('\n')
	var request proxyRequest
	if err != nil || json.Unmarshal(raw, &request) != nil || request.Session == "" {
		return
	}
	result := p.dispatch(ctx, request)
	_ = json.NewEncoder(conn).Encode(result)
}

func rejected(code string) proxyResult {
	return proxyResult{Problem: &remote.Problem{Code: code}}
}

func (p *proxyControl) dispatch(ctx context.Context, request proxyRequest) proxyResult {
	if request.Operation == "start" {
		return p.start(ctx, request)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	card := p.cards[request.Interaction]
	if card != nil && card.session != request.Session {
		return rejected("forbidden")
	}
	if request.Operation == "status" && card != nil && card.status != "pending" {
		return proxyResult{Status: card.status}
	}
	if !p.observer.tracks(request.Session) {
		return proxyResult{Status: "expired"}
	}
	switch request.Operation {
	case "list":
		cards := []remote.Interaction{}
		for _, card := range p.cards {
			if card.status == "pending" && card.session == request.Session &&
				(card.card.Expires == 0 || card.card.Expires > time.Now().UnixMilli()) {
				cards = append(cards, card.card)
			}
		}
		return proxyResult{Cards: cards}
	case "status":
		if card == nil {
			return proxyResult{Status: "missing"}
		}
		if card.card.Expires > 0 && card.card.Expires <= time.Now().UnixMilli() {
			return proxyResult{Status: "expired"}
		}
		return proxyResult{Status: card.status}
	case "respond":
		if card == nil {
			return proxyResult{Status: "missing"}
		}
		if card.status != "pending" {
			return proxyResult{Status: card.status}
		}
		if card.card.Expires > 0 && card.card.Expires <= time.Now().UnixMilli() {
			return proxyResult{Status: "expired"}
		}
		result, problem := codexInteractionResult(card.card, request.Reply)
		if problem != nil {
			return proxyResult{Problem: problem}
		}
		if ctx.Err() != nil {
			return rejected("unavailable")
		}
		frame, err := json.Marshal(map[string]any{"id": card.rpc, "result": result})
		if err != nil {
			return rejected("invalid_request")
		}
		card.status = "unknown"
		if err := p.write(frame, websocket.TextMessage); err != nil {
			p.upstream.Close()
			return proxyResult{Status: card.status}
		}
		card.status = "submitted"
		return proxyResult{Status: card.status}
	default:
		return rejected("invalid_request")
	}
}

func (p *proxyControl) write(raw []byte, frame int) error {
	p.upstream.SetWriteDeadline(time.Now().Add(3 * time.Second))
	err := p.upstream.WriteMessage(frame, raw)
	p.upstream.SetWriteDeadline(time.Time{})
	return err
}

func (p *proxyControl) start(ctx context.Context, request proxyRequest) proxyResult {
	p.mu.Lock()
	session := request.Session
	if !p.observer.tracks(session) {
		p.mu.Unlock()
		return rejected("forbidden")
	}
	if p.sessionBusy(session) || ctx.Err() != nil {
		p.mu.Unlock()
		return rejected("busy")
	}
	if request.Expires <= time.Now().UnixMilli() {
		p.mu.Unlock()
		return proxyResult{Status: "expired"}
	}
	id := p.ownPrefix() + remote.NewID()
	result := make(chan proxyResult, 1)
	p.phoneStarts[session] = id
	p.phoneCalls[id] = proxyTurn{session: session, result: result}
	params := map[string]any{
		"threadId": session, "clientUserMessageId": request.Intent,
		"input": []map[string]string{{"type": "text", "text": request.Text}},
	}
	raw, _ := json.Marshal(map[string]any{"id": id, "method": "turn/start", "params": params})
	err := p.write(raw, websocket.TextMessage)
	p.mu.Unlock()
	if err != nil {
		p.upstream.Close()
		return proxyResult{Status: "unknown"}
	}
	select {
	case outcome := <-result:
		return outcome
	case <-ctx.Done():
		return proxyResult{Status: "unknown"}
	}
}

func (p *proxyControl) ownPrefix() string { return "ad/" + p.observer.connection + "/" }

func (p *proxyControl) sessionBusy(session string) bool {
	if p.active[session] != "" || p.phoneStarts[session] != "" {
		return true
	}
	for _, owner := range p.desktopStarts {
		if owner == session {
			return true
		}
	}
	for _, card := range p.byRPC {
		if card.session == session && (card.status == "pending" || card.status == "submitted") {
			return true
		}
	}
	return false
}

// desktop and start/respond share the same mutex and upstream writer.
func (p *proxyControl) desktop(frame int, raw []byte) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	message := decodeRPC(raw)
	method := str(message["method"])
	if id, ok := message["id"]; ok {
		if method == "" {
			if card := p.byRPC[rpcKey(id)]; card != nil {
				if card.status != "pending" {
					return nil, nil
				}
				card.status = "closed"
			}
		} else if method == "turn/start" {
			session := str(object(message["params"])["threadId"])
			if p.phoneStarts[session] != "" {
				result, err := json.Marshal(map[string]any{"id": id, "error": map[string]any{
					"code": -32000, "message": "A phone-originated turn is already starting or running.",
				}})
				return result, err
			}
			p.desktopStarts[rpcKey(id)] = session
		}
	}
	return nil, p.write(p.observer.observe(raw, true), frame)
}

// server returns true only for replies owned by the phone control path.
func (p *proxyControl) server(raw []byte) (owned bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	defer func() {
		if !owned {
			p.observer.observe(raw, false)
		}
	}()
	message := decodeRPC(raw)
	method, params := str(message["method"]), object(message["params"])
	id, hasID := message["id"]
	if method == "" && hasID {
		if key := str(id); strings.HasPrefix(key, p.ownPrefix()) {
			if call, ok := p.phoneCalls[key]; ok {
				delete(p.phoneCalls, key)
				outcome := proxyResult{Status: "unknown"}
				if message["error"] != nil {
					if p.phoneStarts[call.session] == key {
						delete(p.phoneStarts, call.session)
					}
				} else if turn := str(object(object(message["result"])["turn"])["id"]); turn != "" {
					if p.phoneStarts[call.session] == key {
						p.active[call.session] = turn
					}
					outcome.Status = "accepted"
				}
				call.result <- outcome
			}
			return true
		}
		session := p.desktopStarts[rpcKey(id)]
		if session != "" {
			if turn := str(object(object(message["result"])["turn"])["id"]); turn != "" {
				p.active[session] = turn
			}
			delete(p.desktopStarts, rpcKey(id))
		}
		thread := object(object(message["result"])["thread"])
		if sid := str(thread["id"]); sid != "" && str(object(thread["status"])["type"]) == "active" {
			p.active[sid] = "unconfirmed"
		}
		return false
	}
	session := str(params["threadId"])
	if session == "" {
		session = str(params["thread_id"])
	}
	switch method {
	case "turn/started":
		if turn := str(object(params["turn"])["id"]); turn != "" && session != "" {
			p.active[session] = turn
		}
	case "turn/completed":
		turn := str(object(params["turn"])["id"])
		active := p.active[session]
		if active != "" && (active == "unconfirmed" || active == turn) {
			delete(p.active, session)
			delete(p.phoneStarts, session)
		}
		for _, card := range p.byRPC {
			if card.session == session && (card.turn == turn || card.turn == "" && active == turn) {
				card.status = "closed"
			}
		}
	case "thread/closed", "thread/archived", "thread/deleted":
		p.finishTurn(session)
	case "serverRequest/resolved":
		if card := p.byRPC[rpcKey(params["requestId"])]; card != nil {
			card.status = "closed"
		}
	default:
		if hasID && p.observer.tracks(session) {
			key := rpcKey(id)
			if p.byRPC[key] != nil {
				// A repeated request is the same outstanding operation.
				return false
			}
			if card, ok := codexInteraction(method, params, session); ok {
				encoded, err := json.Marshal(card)
				if err != nil || len(encoded) > 32<<10 {
					return false
				}
				turn := str(params["turnId"])
				if turn == "" {
					turn = str(params["turn_id"])
				}
				pending := &proxyCard{session: session, turn: turn, rpc: id, card: card, status: "pending"}
				p.cards[card.ID] = pending
				p.byRPC[key] = pending
			}
		}
	}
	return false
}

func (p *proxyControl) finishTurn(session string) {
	delete(p.active, session)
	delete(p.phoneStarts, session)
	for _, card := range p.byRPC {
		if card.session == session {
			card.status = "closed"
		}
	}
}

func callProxy(ctx context.Context, connection string, request proxyRequest) (proxyResult, error) {
	if !validProxyID(connection) {
		return proxyResult{}, &remote.Problem{Code: "unavailable"}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", proxyControlSocket(connection))
	if err != nil {
		return proxyResult{}, &remote.Problem{Code: "unavailable"}
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(7 * time.Second)
	}
	conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return proxyResult{}, err
	}
	raw, err := bufio.NewReaderSize(conn, 512<<10).ReadSlice('\n')
	if err != nil {
		return proxyResult{}, err
	}
	var result proxyResult
	if json.Unmarshal(raw, &result) != nil {
		return proxyResult{}, errors.New("invalid proxy reply")
	}
	if result.Problem != nil {
		return result, result.Problem
	}
	return result, nil
}
