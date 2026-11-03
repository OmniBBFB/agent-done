"""Exercise real service connections and graceful process shutdown."""

import asyncio
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

from agent_done.runtime import Runtime
from agent_done.state import State


class SubscriberTests(unittest.IsolatedAsyncioTestCase):
    async def test_disconnected_idle_subscriber_is_released(self):
        with tempfile.TemporaryDirectory() as directory:
            state = State(Path(directory) / "state.sqlite3")
            runtime = Runtime(state)
            server = await asyncio.start_unix_server(runtime.client, path=directory + "/control.sock")
            try:
                reader, writer = await asyncio.open_unix_connection(directory + "/control.sock")
                writer.write(b'{"op":"subscribe"}\n')
                await writer.drain()
                self.assertTrue(json.loads(await reader.readline())["ok"])
                self.assertEqual(len(runtime.subscribers), 1)
                writer.close()
                await writer.wait_closed()
                for _ in range(100):
                    if not runtime.subscribers:
                        break
                    await asyncio.sleep(0.01)
                self.assertEqual(len(runtime.subscribers), 0)
            finally:
                server.close()
                await server.wait_closed()
                state.close()


class ServiceLifecycleTests(unittest.TestCase):
    def test_service_exits_promptly_with_idle_subscription(self):
        import socket
        for _ in range(3):
            with tempfile.TemporaryDirectory() as directory:
                env = dict(os.environ, XDG_RUNTIME_DIR=directory, AGENT_DONE_STATE_DIR=directory + "/state")
                process = subprocess.Popen([sys.executable, "-m", "agent_done", "serve"], env=env,
                                           start_new_session=True, stderr=subprocess.PIPE)
                client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
                try:
                    path = Path(directory) / "agent-done/control.sock"
                    deadline = time.monotonic() + 4
                    while not path.exists() and time.monotonic() < deadline:
                        time.sleep(0.02)
                    client.connect(str(path))
                    client.sendall(b'{"op":"subscribe"}\n')
                    client.recv(65536)
                    os.killpg(process.pid, signal.SIGTERM)
                    self.assertEqual(process.wait(timeout=3), 0)
                finally:
                    client.close()
                    if process.poll() is None:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait()
                    process.stderr.close()
