package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OmniBBFB/agent-done/internal/remote"
	"github.com/gorilla/websocket"
)

type codexCatalog struct {
	socket        string
	control       bool
	snapshot      func() (*Snapshot, error)
	createSession func(context.Context) (string, error)
}

func liveSession(session Session) bool {
	for _, view := range session.Views {
		if liveView(view) {
			return true
		}
	}
	return false
}

func liveView(view View) bool {
	return view.Active && sameProcess(view.PID, view.StartTicks) && sameProcess(view.OwnerPID, view.OwnerStart)
}

type codexRPC struct {
	conn *websocket.Conn
	next int
}

func (c codexCatalog) connect(ctx context.Context) (*codexRPC, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 3 * time.Second,
		NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", c.socket)
		},
	}
	conn, response, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, errors.New("could not reach local Codex daemon")
	}
	conn.SetReadLimit(16 << 20)
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(8 * time.Second)
	}
	conn.SetReadDeadline(deadline)
	conn.SetWriteDeadline(deadline)
	client := &codexRPC{conn: conn}
	info := map[string]any{"clientInfo": map[string]string{"name": "agent_done_remote", "version": version}}
	if err := client.call("initialize", info, new(json.RawMessage)); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.WriteJSON(map[string]string{"method": "initialized"}); err != nil {
		conn.Close()
		return nil, err
	}
	return client, nil
}

type codexRPCRejection string

func (r codexRPCRejection) Error() string { return "Codex rejected " + string(r) }

func (c *codexRPC) call(method string, params any, result any) error {
	c.next++
	id := c.next
	if err := c.conn.WriteJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	for {
		var message struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if _, raw, err := c.conn.ReadMessage(); err != nil {
			return err
		} else if json.Unmarshal(raw, &message) != nil {
			continue
		}
		if message.ID != id || message.Method != "" {
			continue
		}
		if len(message.Error) != 0 {
			return codexRPCRejection(method)
		}
		return json.Unmarshal(message.Result, result)
	}
}

func (c codexCatalog) Sessions(_ context.Context, cursor string, limit int) (remote.Page[remote.Session], error) {
	page := remote.Page[remote.Session]{Items: []remote.Session{}}
	offset := 0
	if cursor != "" {
		var err error
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			return page, &remote.Problem{Code: "invalid_request"}
		}
	}
	snapshot, err := c.snapshot()
	if err != nil {
		return page, err
	}
	live := 0
	for _, session := range snapshot.Sessions {
		if !liveSession(session) {
			continue
		}
		live++
		if live <= offset {
			continue
		}
		if len(page.Items) == limit {
			next := strconv.Itoa(live - 1)
			page.NextCursor = &next
			break
		}
		state := "idle"
		switch session.Phase {
		case "approval", "question":
			state = "waiting"
		case "running":
			state = "running"
		}
		title, _ := clipText(sessionTitle(session), 512)
		cwd, _ := clipText(session.CWD, 4096)
		controllable := c.control && messageConnection(session) != ""
		page.Items = append(page.Items, remote.Session{
			ID: session.ID, Title: title, CWD: cwd, State: state, Updated: int64(session.Updated * 1000),
			CanSendMessage: controllable, CanAutoApprove: c.control && len(controlConnections(session)) != 0,
		})
	}
	return page, nil
}

type threadItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func clipText(text string, limit int) (string, bool) {
	if len(text) <= limit {
		return text, false
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit], true
}

func (item threadItem) history() (remote.HistoryItem, bool) {
	var role, text string
	switch item.Type {
	case "agentMessage":
		role, text = "assistant", item.Text
	case "userMessage":
		var parts []string
		for _, content := range item.Content {
			if content.Type == "text" {
				parts = append(parts, content.Text)
			}
		}
		role, text = "user", strings.Join(parts, "\n")
	default:
		return remote.HistoryItem{}, false
	}
	if text == "" || item.ID == "" {
		return remote.HistoryItem{}, false
	}
	text, truncated := clipText(text, 4096)
	encoded, _ := json.Marshal(text)
	if len(encoded) > 4096 {
		text, _ = clipText(text, 650)
		truncated = true
	}
	return remote.HistoryItem{ID: item.ID, Role: role, Text: text, Truncated: truncated}, true
}

func (c codexCatalog) History(ctx context.Context, sessionID, cursor string, limit int) (remote.Page[remote.HistoryItem], error) {
	page := remote.Page[remote.HistoryItem]{Items: []remote.HistoryItem{}}
	snapshot, err := c.snapshot()
	if err != nil {
		return page, err
	}
	index := slices.IndexFunc(snapshot.Sessions, func(session Session) bool {
		return session.ID == sessionID && liveSession(session)
	})
	if index < 0 {
		return page, &remote.Problem{Code: "forbidden"}
	}
	for _, connection := range controlConnections(snapshot.Sessions[index]) {
		result, err := callProxy(ctx, connection, proxyRequest{
			Operation: "history", Session: sessionID, Cursor: cursor, Limit: limit,
		})
		if result.History != nil {
			return *result.History, nil
		}
		// Older desktop proxies reject this operation. Keep their existing
		// history path through the shared daemon.
		if result.Problem != nil && result.Problem.Code != "invalid_request" {
			return page, err
		}
	}
	client, err := c.connect(ctx)
	if err != nil {
		return page, err
	}
	defer client.conn.Close()
	params := map[string]any{"threadId": sessionID, "limit": limit, "sortDirection": "desc"}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var response struct {
		Data []struct {
			Item threadItem `json:"item"`
		} `json:"data"`
		NextCursor *string `json:"nextCursor"`
	}
	if err := client.call("thread/items/list", params, &response); err != nil {
		return page, err
	}
	page.NextCursor = response.NextCursor
	for _, entry := range response.Data {
		if item, visible := entry.Item.history(); visible {
			page.Items = append(page.Items, item)
		}
	}
	return page, nil
}
