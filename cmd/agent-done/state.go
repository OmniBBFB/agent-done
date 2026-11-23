package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schema string

type State struct {
	db           *sql.DB
	desktop      Observation
	windows      map[int64]Window
	connected    bool
	desktopError string
}

func openState(path string) (*State, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// The historical Python database has the same schema. Upgrade the three
	// view columns in place when opening a database from before desktop adapters.
	if _, err = db.Exec("PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;" + schema); err == nil {
		err = withTransaction(db, func(tx *sql.Tx) error {
			rows, err := tx.Query("PRAGMA table_info(views)")
			if err != nil {
				return err
			}
			columns := make(map[string]bool)
			for rows.Next() {
				var cid, notnull, primary int
				var name, kind string
				var defaultValue any
				if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &primary); err != nil {
					rows.Close()
					return err
				}
				columns[name] = true
			}
			if err := rows.Err(); err != nil {
				return err
			}
			rows.Close()
			for _, column := range []struct{ name, declaration string }{
				{"backend", "TEXT NOT NULL DEFAULT 'niri'"},
				{"window_pid", "INTEGER"}, {"window_start", "TEXT"},
			} {
				if !columns[column.name] {
					if _, err := tx.Exec("ALTER TABLE views ADD COLUMN " + column.name + " " + column.declaration); err != nil {
						return err
					}
				}
			}
			return execSQL(tx, "UPDATE views SET active=0 WHERE backend!='niri'")
		})
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &State{db: db, desktop: Observation{Backend: "niri"}, windows: make(map[int64]Window)}, nil
}

func withTransaction(db *sql.DB, operation func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := operation(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func execSQL(tx *sql.Tx, query string, args ...any) error {
	_, err := tx.Exec(query, args...)
	return err
}

func now() float64 { return float64(time.Now().UnixNano()) / float64(time.Second) }

func (s *State) visible(tx *sql.Tx, sid string) (bool, error) {
	if s.desktop.Focused == nil {
		return false, nil
	}
	var visible bool
	err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM views WHERE session=? AND active=1 AND window_id=? AND backend='niri')", sid, *s.desktop.Focused).Scan(&visible)
	return visible, err
}

func waitingPhase(tx *sql.Tx, sid string) error {
	var kind sql.NullString
	var unread sql.NullInt64
	if err := tx.QueryRow("SELECT MAX(kind),MAX(unread) FROM requests WHERE session=?", sid).Scan(&kind, &unread); err != nil {
		return err
	}
	if kind.Valid {
		return execSQL(tx, "UPDATE sessions SET phase=?,reason=?,pending=? WHERE id=?", kind.String, kind.String, unread.Int64, sid)
	}
	return execSQL(tx, "UPDATE sessions SET phase='running',pending=0,reason='' WHERE id=? AND phase IN ('approval','question')", sid)
}

func unbind(tx *sql.Tx, connection string) error {
	rows, err := tx.Query("SELECT session FROM views WHERE connection=?", connection)
	if err != nil {
		return err
	}
	var sessions []string
	for rows.Next() {
		var sid string
		if err := rows.Scan(&sid); err != nil {
			rows.Close()
			return err
		}
		sessions = append(sessions, sid)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if err := execSQL(tx, "DELETE FROM views WHERE connection=?", connection); err != nil {
		return err
	}
	if err := execSQL(tx, "DELETE FROM requests WHERE owner=?", connection); err != nil {
		return err
	}
	for _, sid := range sessions {
		var hasViews bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM views WHERE session=?)", sid).Scan(&hasViews); err != nil {
			return err
		}
		if hasViews {
			if err := waitingPhase(tx, sid); err != nil {
				return err
			}
			continue
		}
		if err := execSQL(tx, "UPDATE sessions SET pending=0,phase='idle' WHERE id=?", sid); err != nil {
			return err
		}
		if err := execSQL(tx, "DELETE FROM requests WHERE session=?", sid); err != nil {
			return err
		}
	}
	return nil
}

func ack(tx *sql.Tx, sid string) error {
	if sid == "" {
		if err := execSQL(tx, "UPDATE sessions SET pending=0"); err != nil {
			return err
		}
		return execSQL(tx, "UPDATE requests SET unread=0")
	}
	if err := execSQL(tx, "UPDATE sessions SET pending=0 WHERE id=?", sid); err != nil {
		return err
	}
	return execSQL(tx, "UPDATE requests SET unread=0 WHERE session=?", sid)
}

func shorten(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

func (s *State) bind(tx *sql.Tx, r Request) error {
	if r.Identity == nil || r.Connection == "" || r.Session == "" {
		return errors.New("invalid bind request")
	}
	id := *r.Identity
	if id.Backend != "" && id.Backend != "niri" {
		return errors.New("绑定来源与服务桌面后端不一致")
	}
	if id.OwnerPID == 0 {
		id.OwnerPID, id.OwnerStart = id.PID, id.StartTicks
	}
	if err := execSQL(tx, "INSERT OR IGNORE INTO sessions(id,updated) VALUES (?,?)", r.Session, now()); err != nil {
		return err
	}
	if err := execSQL(tx, "UPDATE sessions SET title=COALESCE(NULLIF(?,''),title),cwd=COALESCE(NULLIF(?,''),cwd) WHERE id=?", shorten(r.Title, 512), r.CWD, r.Session); err != nil {
		return err
	}
	if err := execSQL(tx, "UPDATE views SET active=0 WHERE connection=?", r.Connection); err != nil {
		return err
	}
	if err := execSQL(tx, `INSERT INTO views(connection,session,window_id,pid,start_ticks,owner_pid,owner_start_ticks,active,backend,window_pid,window_start)
		VALUES (?,?,?,?,?,?,?,1,'niri',NULLIF(?,0),NULLIF(?,'')) ON CONFLICT(connection,session) DO UPDATE SET
		window_id=excluded.window_id,pid=excluded.pid,start_ticks=excluded.start_ticks,owner_pid=excluded.owner_pid,
		owner_start_ticks=excluded.owner_start_ticks,active=1,backend='niri',window_pid=excluded.window_pid,window_start=excluded.window_start`,
		r.Connection, r.Session, id.WindowID, id.PID, id.StartTicks, id.OwnerPID, id.OwnerStart, id.WindowPID, id.WindowStart); err != nil {
		return err
	}
	visible, err := s.visible(tx, r.Session)
	if err != nil || !visible {
		return err
	}
	return ack(tx, r.Session)
}

func (s *State) turnEvent(tx *sql.Tx, r Request) error {
	var turn, phase string
	err := tx.QueryRow("SELECT turn_id,phase FROM sessions WHERE id=?", r.Session).Scan(&turn, &phase)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	active := phase == "running" || phase == "approval" || phase == "question"
	different := r.Turn != "" && turn != "" && turn != r.Turn
	switch r.Op {
	case "running":
		if active && !different {
			if r.Turn == "" || turn != "" {
				return nil
			}
			if err := execSQL(tx, "UPDATE sessions SET turn_id=? WHERE id=?", r.Turn, r.Session); err != nil {
				return err
			}
			return execSQL(tx, "UPDATE requests SET turn_id=? WHERE session=? AND turn_id=''", r.Turn, r.Session)
		}
		if err := execSQL(tx, "UPDATE sessions SET turn_id=?,phase='running',pending=0,reason='',updated=? WHERE id=?", r.Turn, now(), r.Session); err != nil {
			return err
		}
		return execSQL(tx, "DELETE FROM requests WHERE session=? AND turn_id!=?", r.Session, r.Turn)
	case "interrupt":
		if different {
			return nil
		}
		if err := execSQL(tx, "UPDATE sessions SET phase='idle',pending=0 WHERE id=?", r.Session); err != nil {
			return err
		}
		return execSQL(tx, "DELETE FROM requests WHERE session=?", r.Session)
	case "complete":
		var hasViews bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM views WHERE session=?)", r.Session).Scan(&hasViews); err != nil || !hasViews {
			return err
		}
		key, err := json.Marshal([]string{r.Session, r.Turn, "complete"})
		if err != nil {
			return err
		}
		// Keep Python's json.dumps layout so completion replays are ignored
		// across upgrades and rollbacks with the same state file.
		var historicalKey strings.Builder
		for i, field := range []string{r.Session, r.Turn, "complete"} {
			if i > 0 {
				historicalKey.WriteString(", ")
			}
			encoded, _ := json.Marshal(field)
			historicalKey.Write(encoded)
		}
		compatibleKey := "[" + historicalKey.String() + "]"
		var seen bool
		if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM seen WHERE key IN (?,?))", string(key), compatibleKey).Scan(&seen); err != nil {
			return err
		}
		if !seen {
			if err := execSQL(tx, "INSERT INTO seen VALUES (?,?)", compatibleKey, now()); err != nil {
				return err
			}
		}
		if err := execSQL(tx, "DELETE FROM seen WHERE created<?", now()-7*86400); err != nil {
			return err
		}
		if seen || (turn != "" && turn != r.Turn && phase != "idle") {
			return nil
		}
		visible, err := s.visible(tx, r.Session)
		if err != nil {
			return err
		}
		reason := "done"
		if r.Status == "failed" {
			reason = "error"
		}
		phase = reason
		if r.Status == "interrupted" {
			phase = "idle"
		}
		if err := execSQL(tx, "UPDATE sessions SET turn_id=?,phase=?,pending=?,reason=?,updated=? WHERE id=?", r.Turn, phase, !visible && r.Status != "interrupted", reason, now(), r.Session); err != nil {
			return err
		}
		return execSQL(tx, "DELETE FROM requests WHERE session=?", r.Session)
	case "request_open":
		if (active && different) || (r.Turn != "" && turn == r.Turn && (phase == "done" || phase == "error")) {
			return nil
		}
		if r.Turn == "" && active {
			r.Turn = turn
		}
		visible, err := s.visible(tx, r.Session)
		if err != nil {
			return err
		}
		result, err := tx.Exec("INSERT OR IGNORE INTO requests VALUES (?,?,?,?,?,?)", r.Session, r.Key, r.Kind, r.Connection, r.Turn, !visible)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil || inserted == 0 {
			return err
		}
		if err := execSQL(tx, "UPDATE sessions SET turn_id=?,updated=? WHERE id=?", r.Turn, now(), r.Session); err != nil {
			return err
		}
		return waitingPhase(tx, r.Session)
	}
	return fmt.Errorf("unknown turn event %q", r.Op)
}

func (s *State) apply(r Request) error {
	return withTransaction(s.db, func(tx *sql.Tx) error {
		switch r.Op {
		case "bind":
			return s.bind(tx, r)
		case "unbind":
			return unbind(tx, r.Connection)
		case "deactivate":
			return execSQL(tx, "UPDATE views SET active=0 WHERE connection=? AND session=?", r.Connection, r.Session)
		case "running", "complete", "request_open":
			return s.turnEvent(tx, r)
		case "request_close":
			if err := execSQL(tx, "DELETE FROM requests WHERE session=? AND key=?", r.Session, r.Key); err != nil {
				return err
			}
			return waitingPhase(tx, r.Session)
		case "title":
			return execSQL(tx, "UPDATE sessions SET title=? WHERE id=?", shorten(r.Title, 512), r.Session)
		case "remove":
			for _, query := range []string{"DELETE FROM views WHERE session=?", "DELETE FROM requests WHERE session=?", "DELETE FROM sessions WHERE id=?"} {
				if err := execSQL(tx, query, r.Session); err != nil {
					return err
				}
			}
			return nil
		case "ack":
			return ack(tx, r.Session)
		case "hook":
			if r.Event == nil {
				return errors.New("missing hook event")
			}
			event := r.Event
			if err := execSQL(tx, "INSERT INTO hooks(session,event,turn_id,created) VALUES (?,?,?,?)", event.Session, event.Name, event.Turn, now()); err != nil {
				return err
			}
			if err := execSQL(tx, "DELETE FROM hooks WHERE id NOT IN (SELECT id FROM hooks ORDER BY id DESC LIMIT 2000)"); err != nil {
				return err
			}
			switch event.Name {
			case "UserPromptSubmit":
				return s.turnEvent(tx, Request{Op: "running", Session: event.Session, Turn: event.Turn})
			case "Interrupt":
				return s.turnEvent(tx, Request{Op: "interrupt", Session: event.Session, Turn: event.Turn})
			}
			return nil
		default:
			return fmt.Errorf("unknown operation %q", r.Op)
		}
	})
}

func (s *State) observe(desktop Observation) error {
	windows := make(map[int64]Window, len(desktop.Windows))
	for _, window := range desktop.Windows {
		windows[window.ID] = window
	}
	err := withTransaction(s.db, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT connection,window_id,window_pid,window_start FROM views WHERE backend='niri' AND active=1 AND window_id IS NOT NULL")
		if err != nil {
			return err
		}
		defer rows.Close()
		stale := map[string]bool{}
		for rows.Next() {
			var conn string
			var id, pid sql.NullInt64
			var start sql.NullString
			if err := rows.Scan(&conn, &id, &pid, &start); err != nil {
				return err
			}
			window, exists := windows[id.Int64]
			if !id.Valid || !exists || (pid.Valid && (int64(window.PID) != pid.Int64 || window.Start != start.String)) {
				stale[conn] = true
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for conn := range stale {
			if err := execSQL(tx, "UPDATE views SET window_id=NULL,window_pid=NULL,window_start=NULL WHERE connection=?", conn); err != nil {
				return err
			}
		}
		if desktop.Focused != nil {
			const visible = " IN (SELECT session FROM views WHERE window_id=? AND backend='niri' AND active=1)"
			if err := execSQL(tx, "UPDATE sessions SET pending=0 WHERE id"+visible, *desktop.Focused); err != nil {
				return err
			}
			return execSQL(tx, "UPDATE requests SET unread=0 WHERE session"+visible, *desktop.Focused)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.desktop, s.windows, s.connected, s.desktopError = desktop, windows, true, ""
	return nil
}

func (s *State) disconnect(err error) {
	s.connected, s.desktopError, s.desktop.Focused = false, err.Error(), nil
}

func (s *State) prune() (bool, error) {
	var changed bool
	err := withTransaction(s.db, func(tx *sql.Tx) error {
		rows, err := tx.Query("SELECT DISTINCT connection,COALESCE(pid,0),COALESCE(start_ticks,''),COALESCE(owner_pid,0),COALESCE(owner_start_ticks,'') FROM views")
		if err != nil {
			return err
		}
		defer rows.Close()
		stale := map[string]bool{}
		for rows.Next() {
			var connection, start, ownerStart string
			var pid, owner int
			if err := rows.Scan(&connection, &pid, &start, &owner, &ownerStart); err != nil {
				return err
			}
			if !sameProcess(pid, start) || !sameProcess(owner, ownerStart) {
				stale[connection] = true
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		for connection := range stale {
			if err := unbind(tx, connection); err != nil {
				return err
			}
		}
		changed = len(stale) != 0
		return nil
	})
	return changed, err
}

func (s *State) snapshot() (*Snapshot, error) {
	result := &Snapshot{
		Sessions: []Session{}, DesktopBackend: "niri", DesktopConnected: s.connected,
		DesktopError: s.desktopError, NiriConnected: s.connected, FocusedWindow: s.desktop.Focused,
	}
	rows, err := s.db.Query("SELECT id,title,cwd,phase,turn_id,pending,reason,updated FROM sessions WHERE id IN (SELECT session FROM views) ORDER BY updated DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]int{}
	for rows.Next() {
		session := Session{Views: []View{}}
		err := rows.Scan(&session.ID, &session.Title, &session.CWD, &session.Phase, &session.TurnID, &session.Pending, &session.Reason, &session.Updated)
		if err != nil {
			return nil, err
		}
		if session.Pending {
			result.PendingCount++
		}
		byID[session.ID] = len(result.Sessions)
		result.Sessions = append(result.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	views, err := s.db.Query(`SELECT connection,session,CASE WHEN typeof(window_id)='integer' THEN window_id END,
		COALESCE(pid,0),COALESCE(start_ticks,''),COALESCE(owner_pid,0),COALESCE(owner_start_ticks,''),
		active,backend,COALESCE(window_pid,0),COALESCE(window_start,'') FROM views`)
	if err != nil {
		return nil, err
	}
	defer views.Close()
	for views.Next() {
		var view View
		err := views.Scan(&view.Connection, &view.Session, &view.WindowID, &view.PID, &view.StartTicks, &view.OwnerPID, &view.OwnerStart, &view.Active, &view.Backend, &view.WindowPID, &view.WindowStart)
		if err != nil {
			return nil, err
		}
		if view.Backend == "niri" && view.WindowID != nil {
			view.Position = s.windows[*view.WindowID].Position
		}
		if index, ok := byID[view.Session]; ok {
			result.Sessions[index].Views = append(result.Sessions[index].Views, view)
		}
	}
	return result, views.Err()
}

func (s *State) diagnostics() (*[]HookRecord, error) {
	rows, err := s.db.Query("SELECT session,event,turn_id,created FROM hooks ORDER BY id DESC LIMIT 30")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []HookRecord{}
	for rows.Next() {
		var record HookRecord
		if err := rows.Scan(&record.Session, &record.Event, &record.TurnID, &record.Created); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return &records, rows.Err()
}
