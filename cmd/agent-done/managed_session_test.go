package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OmniBBFB/agent-done/internal/remote"
	"github.com/gorilla/websocket"
)

func TestPhoneCreatesOneManagedSessionInWorkstationHome(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ad-managed-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("AGENT_DONE_RUNTIME_DIR", dir)
	t.Setenv("HOME", dir)
	socket := filepath.Join(dir, "codex.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	starts := atomic.Int32{}
	upgrader := websocket.Upgrader{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			req := decodeRPC(raw)
			result := map[string]any{}
			switch req["method"] {
			case "initialized":
				continue
			case "initialize":
			case "thread/start":
				starts.Add(1)
				cwd := str(object(req["params"])["cwd"])
				result["thread"] = map[string]any{"id": "created-thread", "cwd": cwd}
			case "turn/start":
				result["turn"] = map[string]string{"id": "new-turn"}
			default:
				t.Errorf("unexpected Codex request: %s", raw)
				return
			}
			if conn.WriteJSON(map[string]any{"id": req["id"], "result": result}) != nil {
				return
			}
			if req["method"] == "turn/start" && conn.WriteMessage(websocket.TextMessage, []byte(
				`{"method":"item/completed","params":{"threadId":"created-thread","item":{"id":"reply","type":"agentMessage","text":"Hello from Codex"}}}`,
			)) != nil {
				return
			}
		}
	})}
	go server.Serve(listener)
	defer server.Close()

	state, err := openState(filepath.Join(dir, "state.sqlite3"), defaultDesktop())
	if err != nil {
		t.Fatal(err)
	}
	defer state.db.Close()
	ledger, err := remote.OpenLedger(state.db)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{state: state, remoteUpdates: make(chan struct{}, 1)}
	ctx, stop := context.WithTimeout(t.Context(), 4*time.Second)
	var managed sync.WaitGroup
	defer func() {
		stop()
		managed.Wait()
	}()
	catalog := codexCatalog{socket: socket, snapshot: s.remoteSnapshot, control: true}
	catalog.createSession = func(request context.Context) (string, error) {
		return s.createManagedSession(request, ctx, &managed, catalog)
	}
	controller := remote.Handler{Catalog: catalog, Controls: catalog, Ledger: ledger}
	viewer := remote.Handler{Catalog: catalog}

	call := func(handler remote.Handler, method string, params map[string]any) (map[string]any, *remote.Problem) {
		t.Helper()
		request, _ := json.Marshal(map[string]any{
			"v": 1, "type": "request", "id": remote.NewID(), "method": method, "params": params,
		})
		requestCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		raw := handler.Handle(requestCtx, "iphone-1", request)
		var reply struct {
			Result map[string]any
			Error  *remote.Problem
		}
		if err := json.Unmarshal(raw, &reply); err != nil {
			t.Fatalf("%s: %s: %v", method, raw, err)
		}
		return reply.Result, reply.Error
	}

	intent := map[string]any{"idempotency_key": remote.NewID(), "expires_at_ms": time.Now().Add(time.Minute).UnixMilli()}
	if _, err := call(viewer, "sessions.create", intent); err == nil || err.Code != "unavailable" {
		t.Fatalf("read-only peer could create: %v", err)
	}
	for _, method := range []string{"sessions.create", "sessions.create.status", "sessions.create"} {
		result, err := call(controller, method, intent)
		if err != nil || result["status"] != "created" || result["session_id"] != "created-thread" {
			t.Fatalf("%s = %v, %v", method, result, err)
		}
	}
	if starts.Load() != 1 {
		t.Fatalf("started %d threads", starts.Load())
	}
	enable := map[string]any{
		"session_id": "created-thread", "enabled": true,
		"idempotency_key": remote.NewID(), "expires_at_ms": time.Now().Add(time.Minute).UnixMilli(),
	}
	disable := map[string]any{
		"session_id": "created-thread", "enabled": false,
		"idempotency_key": remote.NewID(), "expires_at_ms": time.Now().Add(time.Minute).UnixMilli(),
	}
	for _, intent := range []map[string]any{enable, disable, enable} {
		result, problem := call(controller, "sessions.auto_approve.set", intent)
		if problem != nil || result["enabled"] != intent["enabled"] {
			t.Fatalf("mode change = %v, %v", result, problem)
		}
	}
	result, problem := call(controller, "sessions.auto_approve.get", map[string]any{"session_id": "created-thread"})
	if problem != nil || result["enabled"] != false {
		t.Fatalf("replayed enable changed the live mode: %v, %v", result, problem)
	}
	page, err := catalog.Sessions(ctx, "", 20)
	if err != nil || len(page.Items) != 1 || page.Items[0].CWD != dir ||
		!page.Items[0].CanSendMessage || !page.Items[0].CanAutoApprove {
		t.Fatalf("created session = %+v, %v", page, err)
	}
	history, err := catalog.History(ctx, "created-thread", "", 20)
	if err != nil || len(history.Items) != 0 {
		t.Fatalf("empty managed history = %+v, %v", history, err)
	}
	status, err := catalog.Start(ctx, "created-thread", remote.NewID(), "First message", time.Now().Add(time.Minute).UnixMilli())
	if err != nil || status != "accepted" {
		t.Fatalf("turn on managed connection = %s, %v", status, err)
	}
	for deadline := time.Now().Add(time.Second); len(history.Items) == 0 && time.Now().Before(deadline); {
		history, err = catalog.History(ctx, "created-thread", "", 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(history.Items) == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if len(history.Items) != 1 || history.Items[0].Text != "Hello from Codex" {
		t.Fatalf("managed history = %+v", history)
	}
	stop()
	managed.Wait()
	page, err = catalog.Sessions(t.Context(), "", 20)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("managed session survived disconnected service: %+v, %v", page, err)
	}
}
