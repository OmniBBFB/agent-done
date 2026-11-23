package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestLiveSessionWithoutDesktopWindow(t *testing.T) {
	for _, hadWindow := range []bool{false, true} {
		name := "without-window"
		if hadWindow {
			name = "window-disappears"
		}
		t.Run(name, func(t *testing.T) {
			s, err := openState(filepath.Join(t.TempDir(), "state.sqlite3"), "niri")
			if err != nil {
				t.Fatal(err)
			}
			defer s.db.Close()
			pid := os.Getpid()
			identity := Identity{PID: pid, StartTicks: startTicks(pid)}
			if hadWindow {
				window := int64(42)
				identity.WindowID = &window
			}
			applyEvent(t, s, Request{Op: "bind", Connection: "tui", Session: "chat", Identity: &identity})
			applyEvent(t, s, Request{Op: "running", Session: "chat", Turn: "turn"})
			applyEvent(t, s, Request{Op: "request_open", Session: "chat", Connection: "tui", Key: "request", Kind: "question", Turn: "turn"})
			if err := s.observe(Observation{Backend: "niri"}); err != nil {
				t.Fatal(err)
			}
			row := readSession(t, s)
			if !liveSession(row) || !row.Pending || row.Phase != "question" || row.Views[0].WindowID != nil {
				t.Fatalf("window update affected the live session: %+v", row)
			}
			if changed, err := s.prune(); changed || err != nil {
				t.Fatalf("prune live session: changed=%v, err=%v", changed, err)
			}
			if _, err := s.db.Exec("UPDATE views SET owner_start_ticks='stale'"); err != nil {
				t.Fatal(err)
			}
			if changed, err := s.prune(); !changed || err != nil {
				t.Fatalf("prune stale session: changed=%v, err=%v", changed, err)
			}
			snapshot, err := s.snapshot()
			if err != nil || len(snapshot.Sessions) != 0 || snapshot.PendingCount != 0 {
				t.Fatalf("stale session remained visible: %+v, err=%v", snapshot, err)
			}
		})
	}
}

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
	s, err := openState(path, "niri")
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
	reopened, err := openState(path, "niri")
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
	state, err := openState(path, "niri")
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

func TestMacDesktopFocusAndRestart(t *testing.T) {
	for _, backend := range []string{"macos", "yabai"} {
		t.Run(backend, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.sqlite3")
			state, err := openState(path, backend)
			if err != nil {
				t.Fatal(err)
			}
			defer state.db.Close()

			window := Window{ID: 42, PID: 100, Start: "window-start"}
			other := Window{ID: 43, PID: window.PID, Start: window.Start}
			pid := os.Getpid()
			id := Identity{Backend: backend, WindowID: &window.ID, WindowPID: window.PID, WindowStart: window.Start,
				PID: pid, StartTicks: startTicks(pid)}
			observe := func(focused int64) {
				t.Helper()
				if err := state.observe(Observation{Backend: backend, Windows: []Window{window, other}, Focused: &focused}); err != nil {
					t.Fatal(err)
				}
			}
			observe(other.ID)
			applyEvent(t, state, Request{Op: "bind", Connection: "tui", Session: "chat", Identity: &id})
			applyEvent(t, state, Request{Op: "running", Session: "chat", Turn: "first"})
			applyEvent(t, state, Request{Op: "request_open", Session: "chat", Connection: "tui", Key: "allow", Kind: "approval", Turn: "first"})
			observe(other.ID)
			if !readSession(t, state).Pending {
				t.Fatal("another window in the same app cleared the reminder")
			}
			observe(window.ID)
			if row := readSession(t, state); row.Pending || row.Phase != "approval" {
				t.Fatalf("focusing the source window changed more than the reminder: %+v", row)
			}
			snapshot, err := state.snapshot()
			if err != nil || snapshot.NiriConnected || !snapshot.DesktopConnected || snapshot.DesktopBackend != backend {
				t.Fatalf("unexpected desktop status: %+v, %v", snapshot, err)
			}

			if err := state.db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := openState(path, backend)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.db.Close()
			row := readSession(t, reopened)
			if !liveSession(row) || (row.Views[0].WindowID == nil) != (backend == "macos") {
				t.Fatalf("unexpected state after restart: %+v", row)
			}
		})
	}
}
