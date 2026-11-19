"""Desktop contracts: source identity, ownership, observations and failure."""

import asyncio
import json
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

from agent_done.desktop import Desktop, identify, backend_name
from agent_done.desktop_macos import MacOS, Accessibility
from agent_done.desktop_niri import Niri
from agent_done.desktop_yabai import Yabai
from agent_done.process import start_ticks, ancestors, same_process
from agent_done.runtime import Runtime
from agent_done.state import State


class FakeAdapter:
    name = "yabai"
    ephemeral = False

    def __init__(self):
        self.windows = [{"id": 1, "pid": 100, "start": "one", "position": None}]
        self.focused = None
        self.activations = []
        self.failure = None

    def snapshot(self):
        if self.failure:
            raise RuntimeError(self.failure)
        return {"backend": self.name, "windows": self.windows, "focused": self.focused}

    def activate(self, target):
        self.activations.append(target)  # Request is deliberately NOT an observation.


class ProcessAndIdentityTests(unittest.TestCase):
    def test_live_process_identity_and_missing_process(self):
        creation = start_ticks(os.getpid())
        self.assertTrue(creation)
        self.assertTrue(same_process(os.getpid(), creation))
        self.assertFalse(same_process(os.getpid(), "different"))
        self.assertFalse(same_process(999999999, None))
        self.assertEqual(ancestors()[0], os.getpid())

    def test_no_source_mapping_from_unrelated_frontmost_window(self):
        adapter = FakeAdapter()
        adapter.focused = 1
        with patch("agent_done.desktop.ancestors", return_value=[200, 201]):
            with self.assertRaisesRegex(RuntimeError, "无法从源进程"):
                identify(adapter, 200)

    def test_shared_process_requires_focused_window_from_same_source(self):
        adapter = FakeAdapter()
        adapter.windows += [{"id": 2, "pid": 100, "start": "one"}, {"id": 3, "pid": 300, "start": "other"}]
        with patch("agent_done.desktop.ancestors", return_value=[200, 100]):
            adapter.focused = 3
            with self.assertRaisesRegex(RuntimeError, "唯一识别"):
                identify(adapter, 200)
            adapter.focused = 2
            self.assertEqual(identify(adapter, 200), {"backend": "yabai", "window_id": 2,
                                                     "window_pid": 100, "window_start": "one"})

    def test_invalid_backend_is_an_error_not_a_silent_fallback(self):
        with patch.dict(os.environ, {"AGENT_DONE_DESKTOP": "unknown"}):
            with self.assertRaises(RuntimeError):
                backend_name()

    def test_macos_process_creation_and_ancestry_use_creation_identity(self):
        class Missing(Exception):
            pass

        def process(pid):
            if pid == 999:
                raise Missing()
            return SimpleNamespace(create_time=lambda: 123.000042,
                                   ppid=lambda: {20: 10, 10: 1}[pid])
        with patch("agent_done.process.sys.platform", "darwin"), \
             patch.dict(sys.modules, {"psutil": SimpleNamespace(Process=process, Error=Missing)}):
            self.assertEqual(ancestors(20), [20, 10])
            self.assertEqual(start_ticks(20), "123.000042")
            self.assertIsNone(start_ticks(999))
            self.assertFalse(same_process(999, None))


class DesktopLifecycleTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.adapter = FakeAdapter()
        self.desktop = Desktop(self.adapter)
        self.state = State(":memory:")
        self.runtime = Runtime(self.state, self.desktop)
        self.identity = {"backend": "yabai", "window_id": 1, "window_pid": 100, "window_start": "one",
                         "pid": 10, "start_ticks": "tui"}
        self.state.bind("view", "chat", self.identity)
        self.state.request_open("view", "chat", "turn", "approval", "approval")

    async def asyncTearDown(self):
        self.state.close()

    async def test_request_success_does_not_ack_but_actual_focus_does(self):
        result = await self.runtime.desktop_request({"op": "focus", **self.identity})
        self.assertTrue(result["requested"])
        self.assertEqual(self.state.snapshot()["pending_count"], 1)
        await self.runtime.desktop_request({"op": "desktop_status"})
        self.assertEqual(self.state.snapshot()["pending_count"], 1)
        self.adapter.focused = 1
        await self.runtime.desktop_request({"op": "desktop_status"})
        self.assertEqual(self.state.snapshot()["pending_count"], 0)
        self.assertEqual(self.state.db.execute("SELECT COUNT(*) FROM requests").fetchone()[0], 1)

    async def test_cancellation_survives_native_permission_error(self):
        started, release = threading.Event(), threading.Event()

        def failing_snapshot():
            started.set()
            release.wait(2)
            raise RuntimeError("permission revoked")

        self.adapter.snapshot = failing_snapshot
        task = asyncio.create_task(self.desktop.snapshot())
        try:
            self.assertTrue(await asyncio.to_thread(started.wait, 2))
            task.cancel()
            await asyncio.sleep(0)
            release.set()
            with self.assertRaises(asyncio.CancelledError):
                await task
        finally:
            release.set()
            await asyncio.gather(task, return_exceptions=True)

    async def test_reused_window_owner_is_rejected_before_focus(self):
        self.adapter.windows[0]["start"] = "replacement"
        result = await self.runtime.desktop_request({"op": "focus", **self.identity})
        self.assertFalse(result["requested"])
        self.assertEqual(self.adapter.activations, [])
        self.assertEqual(self.state.snapshot()["pending_count"], 1)

    async def test_focus_readback_acknowledges_without_waiting_for_watcher(self):
        self.adapter.activate = lambda target: setattr(self.adapter, "focused", target)
        result = await self.runtime.desktop_request({"op": "focus", **self.identity})
        self.assertTrue(result["requested"])
        self.assertEqual(self.state.snapshot()["pending_count"], 0)
        self.assertEqual(self.state.db.execute("SELECT COUNT(*) FROM requests").fetchone()[0], 1)

    async def test_backend_collision_cannot_focus_or_ack_another_view(self):
        with self.assertRaisesRegex(RuntimeError, "不一致"):
            await self.runtime.desktop_request({"op": "focus", **dict(self.identity, backend="niri")})
        self.state.bind("niri-view", "niri-chat", dict(self.identity, backend="niri"))
        self.state.complete("niri-chat", "t", "completed")
        self.adapter.focused = 1
        await self.runtime.desktop_request({"op": "desktop_status"})
        rows = {s["id"]: s for s in self.state.snapshot()["sessions"]}
        self.assertFalse(rows["chat"]["pending"])
        self.assertTrue(rows["niri-chat"]["pending"])

    async def test_query_failure_keeps_binding_and_pending(self):
        self.adapter.failure = "permission revoked"
        with self.assertRaisesRegex(RuntimeError, "permission revoked"):
            await self.runtime.desktop_request({"op": "desktop_status"})
        snapshot = self.state.snapshot()
        self.assertFalse(snapshot["desktop_connected"])
        self.assertEqual(snapshot["pending_count"], 1)
        self.assertTrue(snapshot["sessions"][0]["views"][0]["active"])

    async def test_confirmed_closure_cleans_binding(self):
        self.adapter.windows = []
        await self.runtime.desktop_request({"op": "desktop_status"})
        self.assertEqual(self.state.snapshot()["sessions"], [])

    async def test_real_control_socket_preserves_request_vs_observation(self):
        from agent_done.ipc import async_request
        with tempfile.TemporaryDirectory(prefix="ad-", dir="/tmp") as directory:
            path = Path(directory) / "control.sock"
            server = await asyncio.start_unix_server(self.runtime.client, path=path)
            try:
                with patch("agent_done.ipc.control_socket", return_value=path):
                    self.assertTrue((await async_request({"op": "focus", **self.identity}))["requested"])
                    self.assertEqual((await async_request({"op": "status"}))["snapshot"]["pending_count"], 1)
                    self.adapter.focused = 1
                    self.assertEqual((await async_request({"op": "desktop_status"}))["snapshot"]["pending_count"], 0)
            finally:
                server.close()
                await server.wait_closed()

    async def test_source_registration_goes_through_service_owned_adapter(self):
        with patch("agent_done.desktop.ancestors", return_value=[200, 100]):
            result = await self.runtime.desktop_request({"op": "identify", "pid": 200})
        self.assertEqual(result["identity"]["window_id"], 1)
        self.assertEqual(result["identity"]["window_start"], "one")

    async def test_missing_source_window_keeps_desktop_connection_and_focus(self):
        self.adapter.focused = 1
        await self.runtime.desktop_request({"op": "desktop_status"})
        with patch("agent_done.desktop.ancestors", return_value=[200]):
            with self.assertRaisesRegex(RuntimeError, "无法从源进程"):
                await self.runtime.desktop_request({"op": "identify", "pid": 200})
        snapshot = self.state.snapshot()
        self.assertTrue(snapshot["desktop_connected"])
        self.assertEqual(snapshot["focused_window"], 1)

    async def test_cross_backend_bind_is_rejected(self):
        with self.assertRaisesRegex(RuntimeError, "不一致"):
            self.runtime.dispatch({"op": "bind", "connection": "bad", "session": "bad",
                                   "identity": dict(self.identity, backend="macos")})
        self.assertEqual(len(self.state.snapshot()["sessions"]), 1)


class NativeMacTests(unittest.TestCase):
    def setUp(self):
        self.api = SimpleNamespace(check=lambda: None, equal=lambda a, b: a == b,
                                   windows=lambda pid: ["same title:window1", "same title:window2"] if pid == 100 else [],
                                   focused=lambda: "same title:window2", valid=lambda w: True,
                                   activate=lambda pid, window: None)
        self.backend = MacOS(self.api)
        patch("agent_done.desktop_macos.start_ticks", return_value="start").start()
        patch("agent_done.desktop_macos.same_process", return_value=True).start()
        self.addCleanup(patch.stopall)

    def test_reference_identity_survives_enumeration_without_title_matching(self):
        self.backend.register([200, 100])
        first = self.backend.snapshot()
        second = self.backend.snapshot()
        self.assertEqual(first, second)
        self.assertNotEqual(first["windows"][0]["id"], first["windows"][1]["id"])
        self.assertEqual(first["focused"], first["windows"][1]["id"])
        self.assertIsNone(first["windows"][0]["position"])

    def test_off_space_enumeration_omission_is_not_treated_as_closure(self):
        self.backend.register([100])
        first = self.backend.snapshot()
        self.api.windows = lambda pid: []
        self.assertEqual(self.backend.snapshot(), first)
        self.api.valid = lambda window: False
        self.assertEqual(self.backend.snapshot()["windows"], [])

    def test_new_service_epoch_cannot_restore_or_ack_old_handles(self):
        self.backend.register([100])
        old = self.backend.snapshot()["focused"]
        other = MacOS(self.api)
        other.register([100])
        self.assertNotEqual(old, other.snapshot()["focused"])
        state = State(":memory:")
        try:
            state.bind("view", "chat", {"backend": "macos", "window_id": old, "pid": 10})
            state.complete("chat", "t", "completed")
            Runtime(state, Desktop(other))
            state.observe_desktop(other.snapshot())
            self.assertTrue(state.snapshot()["sessions"][0]["pending"])
            self.assertFalse(state.snapshot()["sessions"][0]["views"][0]["active"])
        finally:
            state.close()

    def test_missing_accessibility_permission_fails_closed(self):
        api = Accessibility.__new__(Accessibility)
        api.ax = SimpleNamespace(AXIsProcessTrusted=lambda: False)
        with self.assertRaisesRegex(RuntimeError, "辅助功能"):
            api.check()


class AdapterParsingTests(unittest.IsolatedAsyncioTestCase):
    async def test_niri_initial_state_waits_for_both_window_and_workspace_sets(self):
        events = [{"WindowsChanged": {"windows": [{"id": 1, "pid": 100, "workspace_id": 5}]}},
                  {"WorkspacesChanged": {"workspaces": [{"id": 5, "idx": 2, "output": "DP-1"}]}},
                  {"WindowFocusChanged": {"id": 1}}, {"WindowClosed": {"id": 1}}]

        async def lines():
            for event in events:
                yield json.dumps(event).encode()
        process = SimpleNamespace(stdout=lines(), returncode=0)
        observations = []
        with patch("agent_done.desktop_niri.niri_socket", return_value="/tmp/niri.sock"), \
             patch("agent_done.desktop_niri.asyncio.create_subprocess_exec", new=AsyncMock(return_value=process)), \
             patch("agent_done.desktop_niri.start_ticks", return_value="start"):
            with self.assertRaisesRegex(RuntimeError, "断开"):
                async for snapshot in Niri().events():
                    observations.append(snapshot)
        self.assertEqual(len(observations), 3)
        self.assertEqual(observations[0]["windows"][0]["position"]["workspace"], 2)
        self.assertEqual(observations[1]["focused"], 1)
        self.assertEqual(observations[2]["windows"], [])

    def test_yabai_snapshot_and_minimized_window_activation(self):
        raw = json.dumps([{"id": 12, "pid": 100, "space": 3, "display": 2,
                           "has-focus": True, "is-minimized": True, "is-floating": False}])
        with patch("agent_done.desktop_yabai.command", return_value=raw) as command, \
             patch("agent_done.desktop_yabai.start_ticks", return_value="start"):
            adapter = Yabai()
            snapshot = adapter.snapshot()
            self.assertEqual(snapshot["focused"], 12)
            self.assertIsNone(snapshot["windows"][0]["position"]["row"])
            adapter.activate(12)
            self.assertEqual(command.call_args_list[-2].args, ("window", "12", "--deminimize"))
            self.assertEqual(command.call_args_list[-1].args, ("window", "--focus", "12"))


class MigrationTests(unittest.TestCase):
    def test_old_views_schema_migrates_without_losing_attention_or_numeric_ids(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.db"
            first = State(path)
            first.bind("a", "chat", {"window_id": 7, "pid": 1, "start_ticks": "old"})
            first.complete("chat", "t", "completed")
            first.close()
            old = sqlite3.connect(path)
            old.execute("ALTER TABLE views DROP COLUMN backend")
            old.execute("ALTER TABLE views DROP COLUMN window_pid")
            old.execute("ALTER TABLE views DROP COLUMN window_start")
            old.commit()
            old.close()
            migrated = State(path)
            try:
                snapshot = migrated.snapshot()
                self.assertEqual(snapshot["pending_count"], 1)
                self.assertEqual(snapshot["sessions"][0]["views"][0]["backend"], "niri")
                self.assertEqual(snapshot["sessions"][0]["views"][0]["window_id"], 7)
            finally:
                migrated.close()
