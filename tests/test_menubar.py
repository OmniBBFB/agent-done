"""Menu bar updates follow service notifications without a polling interval."""

import asyncio
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch

from agent_done.menubar import watch_updates
from agent_done.runtime import Runtime
from agent_done.state import State


class MenuSubscriptionTests(unittest.IsolatedAsyncioTestCase):
    async def test_completion_is_pushed_to_menu_without_polling(self):
        with tempfile.TemporaryDirectory(prefix="ad-menu-", dir="/tmp") as directory:
            path = Path(directory) / "control.sock"
            state = State(":memory:")
            runtime = Runtime(state)
            server = await asyncio.start_unix_server(runtime.client, path=path)
            changed = asyncio.Event()
            stopped = threading.Event()
            updates = []

            def publish(snapshot, message):
                updates.append((snapshot, message))
                changed.set()

            with patch("agent_done.ipc.control_socket", return_value=path):
                task = asyncio.create_task(watch_updates(publish, stopped))
                try:
                    await asyncio.wait_for(changed.wait(), 0.5)
                    self.assertEqual(updates[-1][0]["pending_count"], 0)
                    self.assertEqual(len(runtime.subscribers), 1)
                    changed.clear()
                    runtime.dispatch({"op": "bind", "connection": "probe", "session": "probe",
                                      "identity": {"window_id": 1, "pid": 123}})
                    runtime.dispatch({"op": "complete", "session": "probe", "turn": "turn",
                                      "status": "completed"})
                    await asyncio.wait_for(changed.wait(), 0.5)
                    self.assertEqual(updates[-1][0]["pending_count"], 1)
                    self.assertEqual(updates[-1][1], "")
                finally:
                    stopped.set()
                    task.cancel()
                    await asyncio.gather(task, return_exceptions=True)
                    server.close()
                    await server.wait_closed()
                    state.close()
