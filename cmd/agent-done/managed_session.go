package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/OmniBBFB/agent-done/internal/remote"
)

func (s *Service) createManagedSession(ctx, lifetime context.Context, sessions *sync.WaitGroup, catalog codexCatalog) (string, error) {
	unavailable := &remote.Problem{Code: "unavailable"}
	cwd := homeDir()
	if !filepath.IsAbs(cwd) || ctx.Err() != nil {
		return "", unavailable
	}
	if !exists(catalog.socket) {
		codex, err := exec.LookPath("codex")
		if err != nil || ensureCodexDaemon(ctx, codex, catalog.socket) != nil {
			return "", unavailable
		}
	}
	client, err := catalog.connect(ctx)
	if err != nil {
		return "", unavailable
	}

	pid := os.Getpid()
	identity := Identity{PID: pid, StartTicks: startTicks(pid)}
	emit := func(r Request) {
		if _, err := s.dispatch(lifetime, r); err != nil {
			log.Printf("managed Codex session: %v", err)
		}
	}
	observer := newObserver(func() Identity { return identity }, emit)
	control := newProxyControl(observer, client.conn)
	control.history = &managedHistory{index: make(map[string]int)}
	proxyCtx, cancel := context.WithCancel(lifetime)
	stopControl, err := control.serve(proxyCtx)
	if err != nil {
		cancel()
		client.conn.Close()
		return "", unavailable
	}

	cleanup := func() {
		cancel()
		stopControl()
		client.conn.Close()
	}
	started := false
	defer func() {
		if !started {
			cleanup()
		}
	}()

	var result struct {
		Thread struct{ ID, CWD, Name string }
	}
	params := map[string]any{"cwd": cwd, "config": preservingHooks(nil, observer.command)}
	if err := client.call("thread/start", params, &result); err != nil {
		var rejection codexRPCRejection
		if errors.As(err, &rejection) {
			return "", unavailable
		}
		return "", err
	}
	thread := result.Thread
	if thread.ID == "" || len(thread.ID) > 256 {
		return "", errors.New("managed Codex session did not return a valid thread ID")
	}
	if err := client.conn.SetReadDeadline(time.Time{}); err != nil {
		return "", err
	}
	if err := client.conn.SetWriteDeadline(time.Time{}); err != nil {
		return "", err
	}
	observer.sessions[thread.ID] = true
	if _, err := s.dispatch(lifetime, Request{Op: "bind", Session: thread.ID, Connection: observer.connection,
		Identity: &identity, Title: thread.Name, CWD: thread.CWD}); err != nil {
		emit(Request{Op: "unbind", Connection: observer.connection})
		return "", err
	}
	started = true
	sessions.Go(func() {
		defer emit(Request{Op: "unbind", Connection: observer.connection})
		defer cleanup()
		stop := context.AfterFunc(lifetime, func() { client.conn.Close() })
		defer stop()
		for {
			_, raw, err := client.conn.ReadMessage()
			if err != nil {
				return
			}
			control.server(raw)
			if !observer.tracks(thread.ID) {
				return
			}
		}
	})
	return thread.ID, nil
}

// A managed connection starts with an empty thread. Its notifications supply
// phone history even on Codex versions that do not implement thread/items/list.
type managedHistory struct {
	items []remote.HistoryItem
	index map[string]int
}

func (h *managedHistory) record(value any) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var item threadItem
	if json.Unmarshal(raw, &item) != nil {
		return false
	}
	entry, ok := item.history()
	if !ok {
		return false
	}
	if index, exists := h.index[entry.ID]; exists {
		h.items[index] = entry
		return false
	}
	h.index[entry.ID] = len(h.items)
	h.items = append(h.items, entry)
	return true
}

func (h *managedHistory) page(cursor string, limit int) proxyResult {
	if limit < 1 || limit > 50 {
		return rejected("invalid_request")
	}
	start := len(h.items)
	if cursor != "" {
		var exists bool
		start, exists = h.index[cursor]
		if !exists {
			return rejected("not_found")
		}
	}
	end := max(0, start-limit)
	page := &remote.Page[remote.HistoryItem]{Items: make([]remote.HistoryItem, 0, start-end)}
	for i := start - 1; i >= end; i-- {
		page.Items = append(page.Items, h.items[i])
	}
	if end > 0 {
		cursor := h.items[end].ID
		page.NextCursor = &cursor
	}
	return proxyResult{History: page}
}
