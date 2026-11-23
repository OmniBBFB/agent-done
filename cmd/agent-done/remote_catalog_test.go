package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OmniBBFB/agent-done/internal/remote"
	"github.com/gorilla/websocket"
)

func TestRemoteCatalogListsLiveSessionsAndReadsTheirHistory(t *testing.T) {
	pid := os.Getpid()
	start := startTicks(pid)
	active := View{Identity: Identity{PID: pid, StartTicks: start, OwnerPID: pid, OwnerStart: start}, Active: true}
	inactive := active
	inactive.Active = false
	stale := active
	stale.OwnerStart = "stale"
	snapshot := &Snapshot{Sessions: []Session{
		{ID: "known", CWD: "/workspace/one", Updated: 100, Phase: "question", Views: []View{active}},
		{ID: "inactive", CWD: "/workspace/one", Views: []View{inactive}},
		{ID: "other", CWD: "/workspace/two", Views: []View{active}},
		{ID: "private", CWD: "/workspace/one", Views: []View{stale}},
		{ID: "archived", CWD: "/workspace/two"},
	}}
	shortDir, err := os.MkdirTemp("/tmp", "ad-catalog-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(shortDir)
	socket := filepath.Join(shortDir, "codex.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	methods := make(chan string, 16)
	upgrader := websocket.Upgrader{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		for {
			var req struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if ws.ReadJSON(&req) != nil {
				return
			}
			methods <- req.Method
			if req.ID == nil {
				continue
			}
			var result any
			switch req.Method {
			case "initialize":
				result = map[string]any{}
			case "thread/items/list":
				result = json.RawMessage(`{"data":[{"item":{"id":"a","type":"agentMessage","text":"visible"}},{"item":{"id":"b","type":"commandExecution","text":"hidden"}}],"nextCursor":"older"}`)
			default:
				return
			}
			if ws.WriteJSON(map[string]any{"id": req.ID, "result": result}) != nil {
				return
			}
		}
	})}
	go server.Serve(listener)
	defer server.Close()
	catalog := codexCatalog{socket: socket, snapshot: func() (*Snapshot, error) { return snapshot, nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	page, err := catalog.Sessions(ctx, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "known" ||
		page.Items[0].Updated != 100000 || page.Items[0].State != "waiting" || page.NextCursor == nil {
		t.Fatalf("unexpected listing: %+v %v", page, err)
	}
	second, err := catalog.Sessions(ctx, *page.NextCursor, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "other" || second.NextCursor != nil {
		t.Fatalf("unexpected second page: %+v %v", second, err)
	}
	history, err := catalog.History(ctx, "known", "", 20)
	if err != nil || !reflect.DeepEqual(history.Items, []remote.HistoryItem{
		{ID: "a", Role: "assistant", Text: "visible"},
	}) || history.NextCursor == nil || *history.NextCursor != "older" {
		t.Fatalf("unexpected history: %+v %v", history, err)
	}
	// A previously listed session must lose history access when its view closes.
	snapshot.Sessions[0].Views = nil
	for _, id := range []string{"known", "private", "inactive", "archived"} {
		if _, err := catalog.History(ctx, id, "", 20); err == nil || err.Error() != "forbidden" {
			t.Fatalf("non-live history was not rejected for %s: %v", id, err)
		}
	}
	var actual []string
	for len(methods) > 0 {
		actual = append(actual, <-methods)
	}
	if want := []string{
		"initialize", "initialized", "thread/items/list",
	}; !reflect.DeepEqual(actual, want) {
		t.Fatalf("unexpected Codex traffic: %v", actual)
	}
}
