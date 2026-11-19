import os
from pathlib import Path
import tempfile
import unittest

from agent_done.state import State


def identity(window, pid=1):
    return {"window_id": window, "pid": pid, "start_ticks": "123"}


class AttentionTests(unittest.TestCase):
    def setUp(self):
        self.state = State(":memory:")

    def tearDown(self):
        self.state.close()

    def rows(self):
        return {row["id"]: row for row in self.state.snapshot()["sessions"]}

    def done(self, sid, turn="t1"):
        self.state.running(sid, turn)
        self.state.complete(sid, turn, "completed")

    def test_two_terminals_count_sessions_and_focus_only_acknowledges_target(self):
        s = self.state
        s.bind("terminal-a", "chat-a", identity(10))
        s.bind("terminal-b", "chat-b", identity(20))
        self.done("chat-a")
        self.done("chat-b")
        self.assertEqual(s.snapshot()["pending_count"], 2)
        s.focus(10)
        self.assertFalse(self.rows()["chat-a"]["pending"])
        self.assertTrue(self.rows()["chat-b"]["pending"])
        self.assertEqual(s.snapshot()["pending_count"], 1)

    def test_focus_does_not_acknowledge_hidden_chat_in_same_terminal(self):
        s = self.state
        s.bind("terminal", "background", identity(10))
        self.done("background")
        s.bind("terminal", "foreground", identity(10))
        s.focus(10)
        self.assertTrue(self.rows()["background"]["pending"])
        s.bind("terminal", "background", identity(10))
        self.assertFalse(self.rows()["background"]["pending"])

    def test_completion_when_already_focused_is_seen(self):
        s = self.state
        s.focus(10)
        s.bind("terminal", "chat", identity(10))
        self.done("chat")
        self.assertEqual(s.snapshot()["pending_count"], 0)
        self.assertEqual(self.rows()["chat"]["phase"], "done")

    def test_duplicate_completion_never_reopens_acknowledged_notification(self):
        s = self.state
        s.bind("terminal", "chat", identity(10))
        self.done("chat")
        s.focus(10)
        s.focus(20)
        s.complete("chat", "t1", "completed")
        self.assertEqual(s.snapshot()["pending_count"], 0)
        s.running("chat", "t2")
        s.complete("chat", "t1", "completed")
        self.assertEqual(self.rows()["chat"]["phase"], "running")
        s.complete("chat", "t2", "completed")
        self.assertEqual(s.snapshot()["pending_count"], 1)

    def test_request_lifecycle_tracks_each_request_without_approving_it(self):
        s = self.state
        s.bind("terminal", "chat", identity(10))
        s.running("chat", "t")
        s.request_open("terminal", "chat", "t", "a1", "approval")
        s.request_open("terminal", "chat", "t", "a2", "approval")
        self.assertEqual(s.snapshot()["pending_count"], 1)
        s.focus(10)
        self.assertEqual(s.snapshot()["pending_count"], 0)
        self.assertEqual(s.db.execute("SELECT COUNT(*) FROM requests").fetchone()[0], 2)
        s.focus(20)
        s.request_open("terminal", "chat", "t", "q", "question")
        self.assertEqual(s.snapshot()["pending_count"], 1)
        s.request_close("chat", "q")
        self.assertEqual(s.snapshot()["pending_count"], 0)  # old approvals were already viewed
        s.request_close("chat", "a1")
        self.assertEqual(self.rows()["chat"]["phase"], "approval")
        s.request_close("chat", "a2")
        self.assertEqual(self.rows()["chat"]["phase"], "running")

    def test_stale_request_and_interrupt_cannot_change_active_turn(self):
        s = self.state
        s.bind("terminal", "chat", identity(10))
        s.running("chat", "new")
        s.request_open("terminal", "chat", "new", "active", "approval")
        s.request_open("terminal", "chat", "old", "stale", "question")
        s.hook({"session_id": "chat", "turn_id": "old", "hook_event_name": "Interrupt"})
        s.running("chat", "new")
        row = self.rows()["chat"]
        self.assertEqual((row["turn_id"], row["phase"], row["pending"]), ("new", "approval", True))
        self.assertEqual(s.db.execute("SELECT key FROM requests").fetchone()[0], "active")
        s.complete("chat", "new", "completed")
        self.assertEqual(self.rows()["chat"]["phase"], "done")

    def test_request_without_turn_keeps_current_turn(self):
        s = self.state
        s.bind("terminal", "chat", identity(10))
        s.running("chat", "current")
        s.request_open("terminal", "chat", "", "q", "question")
        self.assertEqual(self.rows()["chat"]["turn_id"], "current")
        s.complete("chat", "current", "completed")
        self.assertEqual(self.rows()["chat"]["phase"], "done")

    def test_auto_review_hook_and_stop_candidate_do_not_notify_prematurely(self):
        s = self.state
        s.bind("terminal", "chat", identity(10))
        s.running("chat", "t")
        for name in ("PermissionRequest", "Stop"):
            s.hook({"session_id": "chat", "turn_id": "t", "hook_event_name": name})
        self.assertEqual(s.snapshot()["pending_count"], 0)
        s.complete("chat", "t", "completed")
        self.assertEqual(s.snapshot()["pending_count"], 1)

    def test_one_chat_in_two_views_is_counted_once_and_survives_one_close(self):
        s = self.state
        s.bind("a", "chat", identity(10))
        s.bind("b", "chat", identity(20))
        self.done("chat")
        self.assertEqual(s.snapshot()["pending_count"], 1)
        s.unbind("a")
        self.assertEqual(s.snapshot()["pending_count"], 1)
        s.focus(20)
        self.assertEqual(s.snapshot()["pending_count"], 0)
        s.unbind("b")
        self.assertEqual(s.snapshot()["sessions"], [])

    def test_resume_replaces_closed_window_and_location_uses_live_layout(self):
        s = self.state
        s.bind("old", "chat", identity(10, 123))
        self.done("chat")
        s.unbind("old")
        s.bind("new", "chat", identity(11, 124))
        s.observe_desktop({"backend": "niri", "focused": None, "windows": [
            {"id": 11, "position": {"output": "DP-1", "workspace": 4, "row": 2, "column": 3, "floating": False}}]})
        self.done("chat", "t2")
        view = self.rows()["chat"]["views"][0]
        self.assertEqual((view["pid"], view["window_id"]), (124, 11))
        self.assertEqual(view["position"], {"output": "DP-1", "workspace": 4, "row": 2, "column": 3, "floating": False})

    def test_interrupted_turn_clears_waiting_requests_without_completion_notice(self):
        s = self.state
        s.bind("a", "chat", identity(10))
        s.running("chat", "t")
        s.request_open("a", "chat", "t", "q", "question")
        s.complete("chat", "t", "interrupted")
        self.assertEqual(s.snapshot()["pending_count"], 0)
        self.assertEqual(s.db.execute("SELECT COUNT(*) FROM requests").fetchone()[0], 0)

    def test_pending_attention_persists_across_service_restart(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.sqlite3"
            first = State(path)
            first.bind("a", "chat", identity(10))
            first.running("chat", "t")
            first.complete("chat", "t", "completed")
            first.close()
            second = State(path)
            try:
                self.assertEqual(second.snapshot()["pending_count"], 1)
                second.focus(10)
                self.assertEqual(second.snapshot()["pending_count"], 0)
            finally:
                second.close()
