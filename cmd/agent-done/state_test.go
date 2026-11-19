package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func applyEvent(t *testing.T, s *State, r Request) {
	t.Helper()
	if err := s.apply(r); err != nil {
		t.Fatal(err)
	}
}

func readSession(t *testing.T, s *State) Session {
	t.Helper()
	snapshot, err := s.snapshot()
	if err != nil || len(snapshot.Sessions) != 1 {
		t.Fatalf("snapshot = %+v, err = %v", snapshot, err)
	}
	return snapshot.Sessions[0]
}

func TestTurnAndFocusLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	s, err := openState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	window := int64(12)
	applyEvent(t, s, Request{Op: "bind", Connection: "tui", Session: "chat", Identity: &Identity{WindowID: &window}})
	applyEvent(t, s, Request{Op: "running", Session: "chat", Turn: "new"})
	applyEvent(t, s, Request{Op: "request_open", Session: "chat", Connection: "tui", Key: "active", Kind: "approval", Turn: "new"})
	applyEvent(t, s, Request{Op: "request_open", Session: "chat", Connection: "tui", Key: "old", Kind: "question", Turn: "old"})
	applyEvent(t, s, Request{Op: "hook", Event: &Hook{Name: "Interrupt", Session: "chat", Turn: "old"}})
	applyEvent(t, s, Request{Op: "running", Session: "chat", Turn: "new"})
	if row := readSession(t, s); row.TurnID != "new" || row.Phase != "approval" || !row.Pending {
		t.Fatalf("active approval changed: %+v", row)
	}
	if err := s.observe(Observation{Backend: "niri", Windows: []Window{{ID: window}}, Focused: &window}); err != nil {
		t.Fatal(err)
	}
	if row := readSession(t, s); row.Pending || row.Phase != "approval" {
		t.Fatalf("focus resolved the request or left it unread: %+v", row)
	}
	if err := s.observe(Observation{Backend: "niri", Windows: []Window{{ID: window}}}); err != nil {
		t.Fatal(err)
	}
	applyEvent(t, s, Request{Op: "complete", Session: "chat", Turn: "new", Status: "completed"})
	if row := readSession(t, s); !row.Pending || row.Phase != "done" {
		t.Fatalf("completion lost: %+v", row)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	if row := readSession(t, reopened); !row.Pending {
		t.Fatalf("reminder did not survive restart: %+v", row)
	}
}

func TestPreAdapterSQLiteSchemaAndCompletionReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.sqlite3")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema+`
		INSERT INTO sessions(id,turn_id,phase,updated) VALUES ('thread','old','done',0);
		INSERT INTO views(connection,session,window_id,active) VALUES ('tui','thread',42,1);
		INSERT INTO seen(key,created) VALUES ('["thread", "old", "complete"]',?);`, now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := openState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer state.db.Close()
	applyEvent(t, state, Request{Op: "complete", Session: "thread", Turn: "old", Status: "completed"})
	row := readSession(t, state)
	if row.Pending || len(row.Views) != 1 || row.Views[0].Backend != "niri" || *row.Views[0].WindowID != 42 {
		t.Fatalf("historical state changed: %+v", row)
	}
}

func TestWaybarConfigCanBeInstalledTwice(t *testing.T) {
	raw := []byte(`{"modules-right":["clock",], "link":"https://example.invalid", /* backup original */}`)
	initial, err := updateWaybar(raw, "/path with spaces/agent-done")
	if err != nil {
		t.Fatal(err)
	}
	second, err := updateWaybar(initial, "/path with spaces/agent-done")
	if err != nil {
		t.Fatal(err)
	}
	if string(initial) != string(second) {
		t.Fatalf("repeated installation changed the config: %s", second)
	}
}
