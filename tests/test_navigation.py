"""Navigation uses completion order and live active views; focus acknowledges."""

import subprocess
import unittest
from unittest.mock import patch

from agent_done.navigation import jump
from agent_done.state import State


class NavigationTests(unittest.TestCase):
    def setUp(self):
        self.state = State(":memory:")
        self.socket = patch("agent_done.navigation.niri_socket", return_value="/tmp/niri-test.sock").start()
        patch("agent_done.navigation.start_ticks", return_value="123").start()
        self.query = patch("agent_done.navigation.niri_query", return_value=[{"id": 10}, {"id": 20}]).start()
        self.ipc = patch("agent_done.navigation.request", side_effect=lambda event: {"snapshot": self.state.snapshot()}).start()
        self.run = patch("agent_done.navigation.subprocess.run").start()
        self.addCleanup(patch.stopall)
        self.addCleanup(self.state.close)

    def done(self, sid, window, updated, connection=None):
        self.state.bind(connection or sid, sid, {"window_id": window, "pid": 1, "start_ticks": "123"})
        self.state.running(sid, "turn")
        self.state.complete(sid, "turn", "completed")
        with self.state.db:
            self.state.db.execute("UPDATE sessions SET updated=? WHERE id=?", (updated, sid))

    def test_oldest_completion_first_then_focus_advances_to_next(self):
        # Newest appears first in status; navigation must reverse that order.
        self.done("new", 20, 200)
        self.done("old", 10, 100)
        self.assertEqual(jump(), {"session": "old", "window_id": 10})
        self.assertEqual(self.state.snapshot()["pending_count"], 2)
        self.state.focus(10)
        self.assertEqual(jump(), {"session": "new", "window_id": 20})
        self.state.focus(20)
        self.assertIsNone(jump())
        self.assertEqual(self.run.call_count, 2)
        args, kwargs = self.run.call_args
        self.assertEqual(args[0], ["niri", "msg", "action", "focus-window", "--id", "20"])
        self.assertEqual(kwargs["env"]["NIRI_SOCKET"], "/tmp/niri-test.sock")
        self.assertTrue(all(call.args[0] == {"op": "status"} for call in self.ipc.call_args_list))

    def test_approval_question_error_and_seen_completion_are_not_targets(self):
        for sid, kind in (("a", "approval"), ("q", "question")):
            self.state.bind(sid, sid, {"window_id": 10, "pid": 1, "start_ticks": "123"})
            self.state.request_open(sid, sid, "turn", "request", kind)
        self.done("failed", 10, 1)
        self.state.running("failed", "failed-turn")
        self.state.complete("failed", "failed-turn", "failed")
        self.done("seen", 20, 2)
        self.state.ack("seen")
        self.assertIsNone(jump())
        self.run.assert_not_called()
        self.query.assert_not_called()

    def test_hidden_chat_and_closed_window_are_skipped_without_marking_seen(self):
        self.done("hidden", 10, 1, connection="terminal")
        self.state.bind("terminal", "foreground", {"window_id": 10, "pid": 1, "start_ticks": "123"})
        self.done("closed", 99, 2)
        self.done("available", 20, 3)
        self.assertEqual(jump()["session"], "available")
        self.assertEqual(self.state.snapshot()["pending_count"], 3)

    def test_reused_pid_is_skipped(self):
        self.done("stale", 10, 1)
        self.done("live", 20, 2)
        with self.state.db:
            self.state.db.execute("UPDATE views SET start_ticks='old-process' WHERE session='stale'")
        self.assertEqual(jump()["session"], "live")

    def test_window_disappearing_during_focus_falls_through_to_next(self):
        self.done("vanished", 10, 1)
        self.done("live", 20, 2)
        self.query.side_effect = [[{"id": 10}, {"id": 20}], [{"id": 20}]]
        self.run.side_effect = [subprocess.CalledProcessError(1, "niri", stderr="window disappeared"), None]
        self.assertEqual(jump()["session"], "live")
        self.assertEqual(self.state.snapshot()["pending_count"], 2)

    def test_failed_focus_keeps_notification_unread(self):
        self.done("old", 10, 1)
        self.run.side_effect = subprocess.CalledProcessError(1, "niri", stderr="focus rejected")
        with self.assertRaisesRegex(RuntimeError, "focus rejected"):
            jump()
        self.assertEqual(self.state.snapshot()["pending_count"], 1)
