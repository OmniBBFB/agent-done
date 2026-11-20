package remote

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type testControls struct {
	Controls
	starts  atomic.Int32
	entered chan struct{}
	resume  chan struct{}
}

func (c *testControls) Start(ctx context.Context, session, intent, text string, expires int64) (string, error) {
	c.starts.Add(1)
	close(c.entered)
	select {
	case <-c.resume:
		return "accepted", nil
	case <-ctx.Done():
		return "unknown", ctx.Err()
	}
}

func TestMessageIntentSurvivesConcurrentReplayAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intent.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	ledger, err := OpenLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	controls := &testControls{entered: make(chan struct{}), resume: make(chan struct{})}
	handler := Handler{Controls: controls, Ledger: ledger}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	op := operation{Session: "thread", Key: NewID(), Expires: time.Now().Add(time.Minute).UnixMilli(), Text: "original"}
	request, _ := json.Marshal(op)
	first := make(chan string, 1)
	go func() {
		result, problem := handler.control(ctx, "phone", "messages.send", request)
		if problem != nil {
			first <- problem.Code
			return
		}
		first <- result.(map[string]string)["status"]
	}()
	select {
	case <-controls.entered:
	case <-ctx.Done():
		t.Fatal("did not reach Codex")
	}
	assertControl := func(peer, method string, op operation, status string) {
		t.Helper()
		params, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		result, problem := handler.control(ctx, peer, method, params)
		if problem != nil {
			t.Fatalf("result: %v", problem)
		}
		if got := result.(map[string]string)["status"]; got != status {
			t.Fatalf("%s = %s, want %s", method, got, status)
		}
	}
	assertControl("phone", "messages.send", op, "unknown")
	assertControl("phone-2", "messages.status", op, "not_received")
	close(controls.resume)
	if got := <-first; got != "accepted" {
		t.Fatalf("receipt = %s", got)
	}
	assertControl("phone", "messages.status", op, "accepted")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	handler.Ledger, err = OpenLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	assertControl("phone", "messages.send", op, "accepted")
	if controls.starts.Load() != 1 {
		t.Fatalf("started %d turns", controls.starts.Load())
	}
	op.Text = "different payload"
	altered, _ := json.Marshal(op)
	if _, problem := handler.control(ctx, "phone", "messages.send", altered); problem == nil || problem.Code != "conflict" {
		t.Fatalf("changed intent=%v", problem)
	}
	op.Key = NewID()
	op.Expires = time.Now().Add(-time.Second).UnixMilli()
	assertControl("phone", "messages.send", op, "expired")
}

type uncertainReplyControls struct {
	Controls
	status string
}

func (c *uncertainReplyControls) Respond(context.Context, string, string, InteractionReply) (string, error) {
	return "unknown", nil
}

func (c *uncertainReplyControls) InteractionStatus(context.Context, string, string) (string, error) {
	return c.status, nil
}

func TestUncertainReplyCanBeConfirmedClosedWithoutResubmitting(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ledger, err := OpenLedger(db)
	if err != nil {
		t.Fatal(err)
	}
	controls := &uncertainReplyControls{status: "pending"}
	handler := Handler{Controls: controls, Ledger: ledger}
	params, err := json.Marshal(operation{
		Session: "thread", Target: NewID(), Key: NewID(),
		Reply: InteractionReply{Kind: "approval", Decision: "deny"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assert := func(method, expected string) {
		t.Helper()
		result, problem := handler.control(t.Context(), "phone", method, params)
		if problem != nil || result.(map[string]string)["status"] != expected {
			t.Fatalf("%s = %v, %v", method, result, problem)
		}
	}
	assert("interaction.respond", "unknown")
	assert("interaction.status", "unknown")
	controls.status = "closed"
	assert("interaction.status", "closed")
}
