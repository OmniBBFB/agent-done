"""Transactional attention state. A session is counted once, across all views."""

import json
import sqlite3
import time


class State:
    def __init__(self, database):
        self.db = sqlite3.connect(str(database))
        self.db.row_factory = sqlite3.Row
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.executescript("""
            CREATE TABLE IF NOT EXISTS sessions (
                id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', cwd TEXT NOT NULL DEFAULT '',
                phase TEXT NOT NULL DEFAULT 'idle', turn_id TEXT NOT NULL DEFAULT '',
                pending INTEGER NOT NULL DEFAULT 0, reason TEXT NOT NULL DEFAULT '', updated REAL NOT NULL
            );
            CREATE TABLE IF NOT EXISTS views (
                connection TEXT NOT NULL, session TEXT NOT NULL, window_id INTEGER,
                pid INTEGER, start_ticks TEXT, owner_pid INTEGER, owner_start_ticks TEXT,
                active INTEGER NOT NULL DEFAULT 1,
                PRIMARY KEY(connection,session)
            );
            CREATE TABLE IF NOT EXISTS requests (
                session TEXT NOT NULL, key TEXT NOT NULL, kind TEXT NOT NULL, owner TEXT NOT NULL,
                turn_id TEXT NOT NULL, unread INTEGER NOT NULL, PRIMARY KEY(session,key)
            );
            CREATE TABLE IF NOT EXISTS seen (key TEXT PRIMARY KEY, created REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS hooks (
                id INTEGER PRIMARY KEY, session TEXT NOT NULL, event TEXT NOT NULL,
                turn_id TEXT NOT NULL, created REAL NOT NULL
            );
        """)
        columns = {row[1] for row in self.db.execute("PRAGMA table_info(views)")}
        for name, declaration in (("backend", "TEXT NOT NULL DEFAULT 'niri'"),
                                  ("window_pid", "INTEGER"), ("window_start", "TEXT")):
            if name not in columns:
                self.db.execute(f"ALTER TABLE views ADD COLUMN {name} {declaration}")
        self.db.commit()
        self.focused = None
        self.windows = {}
        self.desktop_backend = "niri"
        self.desktop_connected = False
        self.desktop_error = ""

    def close(self):
        self.db.close()

    def _session(self, sid):
        return self.db.execute("SELECT * FROM sessions WHERE id=?", (sid,)).fetchone()

    def _visible(self, sid):
        if self.focused is None:
            return False
        return self.db.execute("SELECT 1 FROM views WHERE session=? AND active=1 AND window_id=? AND backend=?",
                               (sid, self.focused, self.desktop_backend)).fetchone() is not None

    def _seen(self, key):
        inserted = self.db.execute("INSERT OR IGNORE INTO seen VALUES (?,?)", (key, time.time())).rowcount
        self.db.execute("DELETE FROM seen WHERE created<?", (time.time() - 7 * 86400,))
        return not inserted

    def bind(self, connection, sid, identity, title="", cwd=""):
        with self.db:
            self.db.execute("INSERT OR IGNORE INTO sessions(id,updated) VALUES (?,?)", (sid, time.time()))
            self.db.execute("UPDATE sessions SET title=CASE WHEN ?!='' THEN ? ELSE title END, "
                            "cwd=CASE WHEN ?!='' THEN ? ELSE cwd END WHERE id=?", (title, title, cwd, cwd, sid))
            self.db.execute("UPDATE views SET active=0 WHERE connection=?", (connection,))
            self.db.execute("INSERT INTO views(connection,session,window_id,pid,start_ticks,owner_pid,"
                            "owner_start_ticks,active,backend,window_pid,window_start) "
                            "VALUES (?,?,?,?,?,?,?,1,?,?,?) ON CONFLICT(connection,session) "
                            "DO UPDATE SET window_id=excluded.window_id,pid=excluded.pid,"
                            "start_ticks=excluded.start_ticks,owner_pid=excluded.owner_pid,"
                            "owner_start_ticks=excluded.owner_start_ticks,backend=excluded.backend,"
                            "window_pid=excluded.window_pid,window_start=excluded.window_start,active=1",
                            (connection, sid, identity.get("window_id"), identity.get("pid"), str(identity.get("start_ticks", "")),
                             identity.get("owner_pid", identity.get("pid")),
                             str(identity.get("owner_start_ticks", identity.get("start_ticks", ""))),
                             identity.get("backend", "niri"), identity.get("window_pid"),
                             identity.get("window_start")))
            if self._visible(sid):
                self.db.execute("UPDATE sessions SET pending=0 WHERE id=?", (sid,))
                self.db.execute("UPDATE requests SET unread=0 WHERE session=?", (sid,))

    def deactivate(self, connection, sid):
        with self.db:
            self.db.execute("UPDATE views SET active=0 WHERE connection=? AND session=?", (connection, sid))

    def unbind(self, connection):
        with self.db:
            sessions = [r[0] for r in self.db.execute("SELECT session FROM views WHERE connection=?", (connection,))]
            self.db.execute("DELETE FROM views WHERE connection=?", (connection,))
            self.db.execute("DELETE FROM requests WHERE owner=?", (connection,))
            for sid in sessions:
                if not self.db.execute("SELECT 1 FROM views WHERE session=?", (sid,)).fetchone():
                    self.db.execute("UPDATE sessions SET pending=0,phase='idle' WHERE id=?", (sid,))
                    self.db.execute("DELETE FROM requests WHERE session=?", (sid,))
                else:
                    self._waiting_phase(sid)

    def remove_session(self, sid):
        with self.db:
            for table, column in (("views", "session"), ("requests", "session"), ("sessions", "id")):
                self.db.execute(f"DELETE FROM {table} WHERE {column}=?", (sid,))

    def title(self, sid, title):
        with self.db:
            self.db.execute("UPDATE sessions SET title=? WHERE id=?", (title, sid))

    def running(self, sid, turn):
        with self.db:
            row = self._session(sid)
            if not row:
                return
            if row["turn_id"] == turn and row["phase"] == "running":
                return
            self.db.execute("UPDATE sessions SET turn_id=?,phase='running',pending=0,reason='',updated=? WHERE id=?",
                            (turn, time.time(), sid))
            self.db.execute("DELETE FROM requests WHERE session=? AND turn_id!=?", (sid, turn))

    def complete(self, sid, turn, status):
        with self.db:
            row = self._session(sid)
            if not row or not self.db.execute("SELECT 1 FROM views WHERE session=?", (sid,)).fetchone():
                return
            if self._seen(json.dumps([sid, turn, "complete"])):
                return
            # An old replay must never replace a newer active turn's attention.
            if row["turn_id"] and row["turn_id"] != turn and row["phase"] != "idle":
                return
            reason = "error" if status == "failed" else "done"
            pending = status != "interrupted" and not self._visible(sid)
            self.db.execute("UPDATE sessions SET turn_id=?,phase=?,pending=?,reason=?,updated=? WHERE id=?",
                            (turn, "idle" if status == "interrupted" else reason, int(pending), reason, time.time(), sid))
            self.db.execute("DELETE FROM requests WHERE session=?", (sid,))

    def request_open(self, connection, sid, turn, key, kind):
        with self.db:
            if not self._session(sid):
                return
            inserted = self.db.execute("INSERT OR IGNORE INTO requests VALUES (?,?,?,?,?,?)",
                                       (sid, key, kind, connection, turn, int(not self._visible(sid)))).rowcount
            if not inserted:
                return
            self.db.execute("UPDATE sessions SET turn_id=?,phase=?,pending=?,reason=?,updated=? WHERE id=?",
                            (turn, kind, int(not self._visible(sid)), kind, time.time(), sid))
            self._waiting_phase(sid)

    def _waiting_phase(self, sid):
        row = self.db.execute("SELECT kind FROM requests WHERE session=? ORDER BY kind DESC LIMIT 1", (sid,)).fetchone()
        if row:
            unread = self.db.execute("SELECT MAX(unread) FROM requests WHERE session=?", (sid,)).fetchone()[0]
            self.db.execute("UPDATE sessions SET phase=?,reason=?,pending=? WHERE id=?", (row[0], row[0], unread, sid))
        else:
            self.db.execute("UPDATE sessions SET phase='running',pending=0,reason='' "
                            "WHERE id=? AND phase IN ('approval','question')", (sid,))

    def request_close(self, sid, key):
        with self.db:
            self.db.execute("DELETE FROM requests WHERE session=? AND key=?", (sid, key))
            self._waiting_phase(sid)

    def hook(self, event):
        sid = event.get("session_id", "")
        name = event.get("hook_event_name", "")
        turn = event.get("turn_id", "")
        with self.db:
            self.db.execute("INSERT INTO hooks(session,event,turn_id,created) VALUES (?,?,?,?)",
                            (sid, name, turn, time.time()))
            self.db.execute("DELETE FROM hooks WHERE id NOT IN (SELECT id FROM hooks ORDER BY id DESC LIMIT 2000)")
        if name == "UserPromptSubmit":
            self.running(sid, turn)
        elif name == "Interrupt":
            with self.db:
                self.db.execute("UPDATE sessions SET phase='idle',pending=0 WHERE id=?", (sid,))
                self.db.execute("DELETE FROM requests WHERE session=?", (sid,))
        # Stop is a candidate: other Stop hooks may continue generation. The
        # protocol's turn/completed confirms the actual final result.
        # PermissionRequest may be auto-reviewed; only a real client request
        # creates an actionable approval notification.

    def observe_desktop(self, observation):
        self.desktop_backend = observation["backend"]
        self.desktop_connected = True
        self.desktop_error = ""
        self.windows = {w["id"]: w for w in observation["windows"]}
        # Invalidate stale window ownership before using the observed focus to ack.
        rows = self.db.execute("SELECT * FROM views WHERE backend=? AND active=1",
                               (self.desktop_backend,)).fetchall()
        for row in rows:
            window = self.windows.get(row["window_id"])
            if window is None or (row["window_pid"] is not None and
                                  (window.get("pid") != row["window_pid"] or
                                   window.get("start") != row["window_start"])):
                self.unbind(row["connection"])
        self.focus(observation["focused"])

    def desktop_disconnected(self, reason):
        self.desktop_connected = False
        self.desktop_error = reason
        self.focus(None)

    def focus(self, window_id):
        self.focused = window_id
        with self.db:
            if window_id is not None:
                self.db.execute("UPDATE sessions SET pending=0 WHERE id IN "
                                "(SELECT session FROM views WHERE window_id=? AND backend=? AND active=1)",
                                (window_id, self.desktop_backend))
                self.db.execute("UPDATE requests SET unread=0 WHERE session IN "
                                "(SELECT session FROM views WHERE window_id=? AND backend=? AND active=1)",
                                (window_id, self.desktop_backend))

    def ack(self, sid=None):
        with self.db:
            if sid:
                self.db.execute("UPDATE sessions SET pending=0 WHERE id=?", (sid,))
                self.db.execute("UPDATE requests SET unread=0 WHERE session=?", (sid,))
            else:
                self.db.execute("UPDATE sessions SET pending=0")
                self.db.execute("UPDATE requests SET unread=0")

    def position(self, window_id):
        window = self.windows.get(window_id)
        if window is None:
            return None
        return window.get("position")

    def snapshot(self):
        sessions = []
        for row in self.db.execute("SELECT * FROM sessions WHERE id IN (SELECT session FROM views) ORDER BY updated DESC"):
            item = dict(row)
            item["pending"] = bool(item["pending"])
            item["views"] = []
            for view in self.db.execute("SELECT * FROM views WHERE session=?", (row["id"],)):
                obj = dict(view)
                obj["active"] = bool(obj["active"])
                obj["position"] = self.position(obj["window_id"]) if obj["backend"] == self.desktop_backend else None
                item["views"].append(obj)
            sessions.append(item)
        return {"pending_count": sum(s["pending"] for s in sessions), "sessions": sessions,
                "desktop_backend": self.desktop_backend, "desktop_connected": self.desktop_connected,
                "desktop_error": self.desktop_error,
                "niri_connected": self.desktop_backend == "niri" and self.desktop_connected,
                "focused_window": self.focused}

    def diagnostics(self):
        return [dict(r) for r in self.db.execute("SELECT session,event,turn_id,created FROM hooks ORDER BY id DESC LIMIT 30")]
