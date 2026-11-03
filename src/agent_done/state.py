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
        self.focused = None
        self.windows = {}
        self.workspaces = {}
        self.niri_connected = False

    def close(self):
        self.db.close()

    def _session(self, sid):
        return self.db.execute("SELECT * FROM sessions WHERE id=?", (sid,)).fetchone()

    def _visible(self, sid):
        if self.focused is None:
            return False
        return self.db.execute("SELECT 1 FROM views WHERE session=? AND active=1 AND window_id=?",
                               (sid, self.focused)).fetchone() is not None

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
            self.db.execute("INSERT INTO views VALUES (?,?,?,?,?,?,?,1) ON CONFLICT(connection,session) "
                            "DO UPDATE SET window_id=excluded.window_id,pid=excluded.pid,"
                            "start_ticks=excluded.start_ticks,owner_pid=excluded.owner_pid,"
                            "owner_start_ticks=excluded.owner_start_ticks,active=1",
                            (connection, sid, identity.get("window_id"), identity.get("pid"), str(identity.get("start_ticks", "")),
                             identity.get("owner_pid", identity.get("pid")),
                             str(identity.get("owner_start_ticks", identity.get("start_ticks", "")))))
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

    def focus(self, window_id):
        self.focused = window_id
        with self.db:
            if window_id is not None:
                self.db.execute("UPDATE sessions SET pending=0 WHERE id IN "
                                "(SELECT session FROM views WHERE window_id=? AND active=1)", (window_id,))
                self.db.execute("UPDATE requests SET unread=0 WHERE session IN "
                                "(SELECT session FROM views WHERE window_id=? AND active=1)", (window_id,))

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
        workspace = self.workspaces.get(window.get("workspace_id"), {})
        layout = window.get("layout", {}).get("pos_in_scrolling_layout")
        return {"output": workspace.get("output"), "workspace": workspace.get("name") or workspace.get("idx"),
                "row": layout[1] if layout else None, "column": layout[0] if layout else None,
                "floating": window.get("is_floating", False)}

    def snapshot(self):
        sessions = []
        for row in self.db.execute("SELECT * FROM sessions WHERE id IN (SELECT session FROM views) ORDER BY updated DESC"):
            item = dict(row)
            item["pending"] = bool(item["pending"])
            item["views"] = []
            for view in self.db.execute("SELECT * FROM views WHERE session=?", (row["id"],)):
                obj = dict(view)
                obj["active"] = bool(obj["active"])
                obj["position"] = self.position(obj["window_id"])
                item["views"].append(obj)
            sessions.append(item)
        return {"pending_count": sum(s["pending"] for s in sessions), "sessions": sessions,
                "niri_connected": self.niri_connected, "focused_window": self.focused}

    def diagnostics(self):
        return [dict(r) for r in self.db.execute("SELECT session,event,turn_id,created FROM hooks ORDER BY id DESC LIMIT 30")]
