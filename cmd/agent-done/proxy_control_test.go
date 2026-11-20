package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OmniBBFB/agent-done/internal/remote"
	"github.com/gorilla/websocket"
)

func controlFixture(t *testing.T) (*proxyControl, *websocket.Conn) {
	t.Helper()
	ready := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			ready <- conn
		}
	}))
	t.Cleanup(server.Close)
	caller, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { caller.Close() })
	fakeCodex := <-ready
	t.Cleanup(func() { fakeCodex.Close() })
	fakeCodex.SetReadDeadline(time.Now().Add(3 * time.Second))
	o := newObserver(func() Identity { return Identity{} }, func(Request) {})
	o.sessions["thread"] = true
	o.sessions["other"] = true
	return newProxyControl(o, caller), fakeCodex
}

func TestControlReplyUsesOriginAndOnlyOneSideWins(t *testing.T) {
	p, upstream := controlFixture(t)
	original := []byte(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread","command":"printf ok","cwd":"/work"}}`)
	p.server(original)
	list := p.dispatch(t.Context(), proxyRequest{Operation: "list", Session: "thread"})
	if len(list.Cards) != 1 {
		t.Fatalf("cards = %+v", list)
	}
	card := list.Cards[0]
	request := proxyRequest{Operation: "respond", Session: "other", Interaction: card.ID,
		Reply: remote.InteractionReply{Kind: "approval", Decision: "allow_once"}}
	if got := p.dispatch(t.Context(), request); got.Problem == nil || got.Problem.Code != "forbidden" {
		t.Fatalf("cross-session reply = %+v", got)
	}
	request.Session = "thread"
	reply := make(chan proxyResult, 1)
	desktopErrors := make(chan error, 1)
	var group sync.WaitGroup
	group.Go(func() { reply <- p.dispatch(t.Context(), request) })
	group.Go(func() {
		_, err := p.desktop(websocket.TextMessage, []byte(`{"id":7,"result":{"decision":"decline"}}`))
		desktopErrors <- err
	})
	group.Wait()
	if err := <-desktopErrors; err != nil {
		t.Fatal(err)
	}
	dispatched := <-reply
	if dispatched.Status != "submitted" && dispatched.Status != "closed" {
		t.Fatalf("mobile result = %+v", dispatched)
	}
	var frame struct {
		ID     int
		Result map[string]string
	}
	if err := upstream.ReadJSON(&frame); err != nil || frame.ID != 7 {
		t.Fatalf("original RPC reply: %+v, %v", frame, err)
	}
	expected := "decline"
	if dispatched.Status == "submitted" {
		expected = "accept"
	}
	if frame.Result["decision"] != expected {
		t.Fatalf("winner changed: %+v", frame)
	}

	p.server(original)
	p.server([]byte(`{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":7}}`))
	if replay := p.dispatch(t.Context(), request); replay.Status != "closed" {
		t.Fatalf("replay = %+v", replay)
	}
	upstream.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if _, _, err := upstream.ReadMessage(); err == nil {
		t.Fatal("forwarded a duplicate RPC")
	}
}

func TestControlSendsNewTurnAndWaitsForCodexReceipt(t *testing.T) {
	p, upstream := controlFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	request := proxyRequest{Operation: "start", Session: "thread", Intent: remote.NewID(),
		Text: "Explain the test result", Expires: time.Now().Add(time.Minute).UnixMilli()}
	result := make(chan proxyResult, 1)
	go func() { result <- p.dispatch(ctx, request) }()
	var turn struct {
		ID     string
		Method string
		Params struct {
			ThreadID            string
			ClientUserMessageID string
			Input               []map[string]string
		}
	}
	if err := upstream.ReadJSON(&turn); err != nil || turn.Method != "turn/start" ||
		turn.Params.ThreadID != "thread" || turn.Params.ClientUserMessageID != request.Intent ||
		len(turn.Params.Input) != 1 || turn.Params.Input[0]["text"] != request.Text {
		t.Fatalf("start: %+v, %v", turn, err)
	}
	select {
	case outcome := <-result:
		t.Fatalf("reported receipt early: %+v", outcome)
	default:
	}
	p.server([]byte(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"older"}}}`))
	if collision := p.dispatch(ctx, request); collision.Problem == nil || collision.Problem.Code != "busy" {
		t.Fatalf("second turn: %+v", collision)
	}
	desktop, err := p.desktop(websocket.TextMessage,
		[]byte(`{"id":9,"method":"turn/start","params":{"threadId":"thread","input":[]}}`))
	if err != nil || !strings.Contains(string(desktop), `"error"`) {
		t.Fatalf("desktop collision: %s %v", desktop, err)
	}
	response, _ := json.Marshal(map[string]any{"id": turn.ID, "result": map[string]any{"turn": map[string]string{"id": "new"}}})
	if !p.server(response) {
		t.Fatal("exposed phone-only RPC receipt to desktop")
	}
	if outcome := <-result; outcome.Status != "accepted" {
		t.Fatalf("receipt: %+v", outcome)
	}
	p.server([]byte(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"new"}}}`))
	if p.sessionBusy("thread") {
		t.Fatal("completed turn kept the session busy")
	}
}

func TestControlOnlyAllowsMatchingOptionsAndCompleteApprovalDetails(t *testing.T) {
	question, ok := codexInteraction("item/tool/requestUserInput", decodeRPC([]byte(
		`{"isBlocking":true,"questions":[{"id":"q1","question":"Which?", "options":[{"label":"A"}]}]}`)), "thread")
	if !ok {
		t.Fatal("valid blocking question omitted")
	}
	response, problem := codexInteractionResult(question, remote.InteractionReply{Kind: "answer", Answers: map[string][]string{"q1": {"A"}}})
	encoded, _ := json.Marshal(response)
	if problem != nil || string(encoded) != `{"answers":{"q1":{"answers":["A"]}}}` {
		t.Fatalf("response = %s, %v", encoded, problem)
	}
	if _, problem := codexInteractionResult(question, remote.InteractionReply{Kind: "answer", Answers: map[string][]string{"q1": {"free text"}}}); problem == nil {
		t.Fatal("accepted a non-option response")
	}
	if _, ok := codexInteraction("item/tool/requestUserInput", decodeRPC([]byte(
		`{"questions":[{"id":"secret","question":"Password", "isSecret":true}]}`)), "thread"); ok {
		t.Fatal("exposed a secret question")
	}
	file, ok := codexInteraction("item/fileChange/requestApproval", map[string]any{}, "thread")
	if !ok {
		t.Fatal("file request should offer a denial")
	}
	if _, problem := codexInteractionResult(file, remote.InteractionReply{Kind: "approval", Decision: "allow_once"}); problem == nil {
		t.Fatal("allowed an operation without a full preview")
	}
	if _, problem := codexInteractionResult(file, remote.InteractionReply{Kind: "approval", Decision: "deny"}); problem != nil {
		t.Fatal("did not allow rejecting the request")
	}
}

func TestControlHandlerRoutesThroughLinuxProxySocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("workstation identity uses Linux /proc")
	}
	dir, err := os.MkdirTemp("/tmp", "ad-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("AGENT_DONE_RUNTIME_DIR", dir)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()

	p, fakeCodex := controlFixture(t)
	closeProxy := p.serve(ctx)
	defer closeProxy()
	other, unwanted := controlFixture(t)
	closeOther := other.serve(ctx)
	defer closeOther()

	pid := os.Getpid()
	start := startTicks(pid)
	identity := Identity{PID: pid, StartTicks: start, OwnerPID: pid, OwnerStart: start}
	catalog := codexCatalog{control: true, snapshot: func() (*Snapshot, error) {
		return &Snapshot{Sessions: []Session{
			{ID: "thread", Phase: "idle", Views: []View{{Active: true, Identity: identity, Connection: p.observer.connection}}},
			{ID: "other", Phase: "idle", Views: []View{{Active: true, Identity: identity, Connection: other.observer.connection}}},
		}}, nil
	}}
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ledger, err := remote.OpenLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	handler := remote.Handler{Catalog: catalog, Controls: catalog, Ledger: ledger}

	call := func(method string, params map[string]any) []byte {
		request, _ := json.Marshal(map[string]any{"v": 1, "type": "request", "id": remote.NewID(), "method": method, "params": params})
		return handler.Handle(ctx, "iphone-1", request)
	}
	if raw := call("sessions.list", map[string]any{}); !strings.Contains(string(raw), `"can_send_message":true`) {
		t.Fatalf("capability missing: %s", raw)
	}
	p.server([]byte(`{"id":901,"method":"item/tool/requestUserInput","params":{"threadId":"thread","turnId":"old","isBlocking":true,"questions":[{"id":"test","question":"Which?","options":[{"label":"A"}]}]}}`))
	list := call("interactions.snapshot", map[string]any{"session_id": "thread"})
	var listing struct {
		Result remote.Page[remote.Interaction]
	}
	if err := json.Unmarshal(list, &listing); err != nil || len(listing.Result.Items) != 1 {
		t.Fatalf("snapshot: %s %v", list, err)
	}
	card := listing.Result.Items[0]
	intent := remote.NewID()
	reply := call("interaction.respond", map[string]any{"session_id": "thread", "interaction_id": card.ID, "idempotency_key": intent,
		"response": map[string]any{"kind": "answer", "answers": map[string][]string{"test": {"A"}}}})
	if !strings.Contains(string(reply), `"status":"submitted"`) {
		t.Fatalf("reply: %s", reply)
	}
	_, answer, err := fakeCodex.ReadMessage()
	if err != nil || !strings.Contains(string(answer), `"id":901`) ||
		!strings.Contains(string(answer), `"test":{"answers":["A"]}`) {
		t.Fatalf("wrong origin response: %s %v", answer, err)
	}
	p.server([]byte(`{"method":"serverRequest/resolved","params":{"threadId":"thread","requestId":901}}`))
	status := call("interaction.status", map[string]any{"session_id": "thread", "interaction_id": card.ID, "idempotency_key": intent})
	if !strings.Contains(string(status), `"status":"closed"`) {
		t.Fatalf("status: %s", status)
	}

	params := map[string]any{"session_id": "thread", "idempotency_key": remote.NewID(),
		"expires_at_ms": time.Now().Add(time.Minute).UnixMilli(), "text": "Report the test result"}
	response := make(chan []byte, 1)
	go func() { response <- call("messages.send", params) }()
	_, turn, err := fakeCodex.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	parsed := decodeRPC(turn)
	if str(parsed["method"]) != "turn/start" || str(object(parsed["params"])["threadId"]) != "thread" {
		t.Fatalf("unexpected start: %s", turn)
	}
	result, _ := json.Marshal(map[string]any{"id": parsed["id"], "result": map[string]any{"turn": map[string]string{"id": "new"}}})
	p.server(result)
	if got := <-response; !strings.Contains(string(got), `"status":"accepted"`) {
		t.Fatalf("send: %s", got)
	}
	if got := call("messages.status", params); !strings.Contains(string(got), `"status":"accepted"`) {
		t.Fatalf("query: %s", got)
	}

	unwanted.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := unwanted.ReadMessage(); err == nil {
		t.Fatal("operation reached a different Codex connection")
	}
}
