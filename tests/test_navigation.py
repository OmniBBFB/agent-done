"""Queue selection uses the control interface; focus observations acknowledge."""

import unittest
from unittest.mock import Mock, patch

from agent_done.navigation import jump
from agent_done.state import State


class NavigationTests(unittest.TestCase):
    def setUp(self):
        self.state = State(":memory:")
        self.windows = [10, 20]
        self.observations = Mock(side_effect=lambda: {"backend": "niri", "windows": [{"id": n} for n in self.windows]})
        self.activate = Mock(return_value=True)
        self.calls = []
        self.ipc = patch("agent_done.navigation.request", side_effect=self.control).start()
        patch("agent_done.navigation.same_process", side_effect=lambda pid, creation: creation == "123").start()
        self.addCleanup(patch.stopall)
        self.addCleanup(self.state.close)

    def control(self, event, timeout=1):
        self.calls.append(event)
        if event["op"] == "desktop_status":
            return {"ok": True, "desktop": self.observations(), "snapshot": self.state.snapshot()}
        if event["op"] == "focus":
            return {"ok": True, "requested": self.activate(event["window_id"])}
        return {"ok": True, "snapshot": self.state.snapshot()}

    def done(self, sid, window, updated, connection=None, backend="niri"):
        self.state.bind(connection or sid, sid, {"backend": backend, "window_id": window, "pid": 1, "start_ticks": "123"})
        self.state.running(sid, "turn")
        self.state.complete(sid, "turn", "completed")
        with self.state.db:
            self.state.db.execute("UPDATE sessions SET updated=? WHERE id=?", (updated, sid))

    def test_oldest_completion_first_then_observed_focus_advances(self):
        self.done("new", 20, 200)
        self.done("old", 10, 100)
        self.assertEqual(jump(), {"session": "old", "window_id": 10})
        self.assertEqual(self.state.snapshot()["pending_count"], 2)
        self.state.focus(10)
        self.assertEqual(jump(), {"session": "new", "window_id": 20})
        self.state.focus(20)
        self.assertIsNone(jump())
        self.assertEqual(self.activate.call_count, 2)
        self.assertTrue(all(c["op"] != "ack" for c in self.calls))

    def test_all_pending_kinds_follow_attention_order(self):
        self.windows = [10, 20, 30, 40]
        self.done("completed", 30, 30)
        self.done("failed", 40, 40)
        self.state.running("failed", "failed-turn")
        self.state.complete("failed", "failed-turn", "failed")
        for sid, kind, window, updated in (("approval", "approval", 10, 10), ("question", "question", 20, 20)):
            self.state.bind(sid, sid, {"window_id": window, "pid": 1, "start_ticks": "123"})
            self.state.request_open(sid, sid, "turn", "request", kind)
            self.state.db.execute("UPDATE sessions SET updated=? WHERE id=?", (updated, sid))
        for remaining, sid, window in ((4, "approval", 10), (3, "question", 20), (2, "completed", 30), (1, "failed", 40)):
            self.assertEqual(jump(), {"session": sid, "window_id": window})
            self.assertEqual(self.state.snapshot()["pending_count"], remaining)
            self.state.focus(window)
        self.assertIsNone(jump())
        self.assertEqual(self.state.db.execute("SELECT COUNT(*) FROM requests").fetchone()[0], 2)

    def test_seen_items_do_not_query_or_activate_desktop(self):
        self.done("seen", 10, 1)
        self.state.ack("seen")
        self.assertIsNone(jump())
        self.observations.assert_not_called()
        self.activate.assert_not_called()

    def test_hidden_closed_and_other_backend_views_are_skipped(self):
        self.done("hidden", 10, 1, connection="terminal")
        self.state.bind("terminal", "foreground", {"window_id": 10, "pid": 1, "start_ticks": "123"})
        self.done("closed", 99, 2)
        self.done("different-backend", 20, 2, backend="yabai")
        self.done("available", 20, 3)
        self.assertEqual(jump()["session"], "available")
        self.assertEqual(self.state.snapshot()["pending_count"], 4)

    def test_reused_pid_is_skipped(self):
        self.done("stale", 10, 1)
        self.done("live", 20, 2)
        self.state.db.execute("UPDATE views SET start_ticks='old-process' WHERE session='stale'")
        self.assertEqual(jump()["session"], "live")

    def test_disappearing_target_falls_through_without_ack(self):
        self.done("vanished", 10, 1)
        self.done("live", 20, 2)
        self.activate.side_effect = [False, True]
        self.assertEqual(jump()["session"], "live")
        self.assertEqual(self.state.snapshot()["pending_count"], 2)

    def test_focus_failure_keeps_pending(self):
        self.done("old", 10, 1)
        self.activate.side_effect = RuntimeError("focus rejected")
        with self.assertRaisesRegex(RuntimeError, "focus rejected"):
            jump()
        self.assertEqual(self.state.snapshot()["pending_count"], 1)

    def test_opaque_macos_target_is_not_coerced_to_integer(self):
        self.state.desktop_backend = "macos"
        self.done("mac", "service-epoch:1", 1, backend="macos")
        self.observations.side_effect = None
        self.observations.return_value = {"backend": "macos", "windows": [{"id": "service-epoch:1"}]}
        self.assertEqual(jump()["window_id"], "service-epoch:1")
        self.assertTrue(self.state.snapshot()["sessions"][0]["pending"])

    def test_numeric_window_order_for_multiple_views_is_preserved(self):
        self.done("chat", 20, 1, connection="second")
        self.state.bind("first", "chat", {"window_id": 3, "pid": 1, "start_ticks": "123"})
        self.windows = [3, 20]
        self.assertEqual(jump()["window_id"], 3)

    def test_old_running_niri_service_uses_local_adapter_without_ack(self):
        self.done("chat", 10, 1)
        snapshot = self.state.snapshot()
        del snapshot["desktop_backend"]
        self.ipc.side_effect = None
        self.ipc.return_value = {"ok": True, "snapshot": snapshot}
        adapter = Mock()
        adapter.name = "niri"
        adapter.snapshot.return_value = {"backend": "niri", "windows": [{"id": 10}]}
        with patch("agent_done.desktop.backend_name", return_value="niri"), \
             patch("agent_done.desktop.create_adapter", return_value=adapter):
            self.assertEqual(jump(), {"session": "chat", "window_id": 10})
        adapter.activate.assert_called_once_with(10)
        self.assertEqual(self.state.snapshot()["pending_count"], 1)
        self.ipc.assert_called_once_with({"op": "status"})
