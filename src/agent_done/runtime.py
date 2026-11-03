"""Per-user state service and niri focus observer."""

import asyncio
import fcntl
import json
import logging
import os
import signal

from .desktop import start_ticks, watch_niri
from .paths import control_socket, private_dir, runtime_dir, state_dir
from .state import State

LOG = logging.getLogger(__name__)


class Runtime:
    def __init__(self, state):
        self.state = state
        self.subscribers = set()
        self.clients = set()
        self.previous = None

    def publish(self):
        snapshot = self.state.snapshot()
        encoded = json.dumps(snapshot, sort_keys=True)
        if encoded == self.previous:
            return
        self.previous = encoded
        for queue in self.subscribers:
            if queue.full():
                queue.get_nowait()
            queue.put_nowait(snapshot)

    def dispatch(self, message):
        op = message.get("op")
        s = self.state
        if op == "bind":
            s.bind(message["connection"], message["session"], message["identity"],
                   message.get("title", "")[:512], message.get("cwd", ""))
        elif op == "unbind":
            s.unbind(message["connection"])
        elif op == "deactivate":
            s.deactivate(message["connection"], message["session"])
        elif op == "running":
            s.running(message["session"], message.get("turn", ""))
        elif op == "complete":
            s.complete(message["session"], message["turn"], message.get("status", "completed"))
        elif op == "request_open":
            s.request_open(message["connection"], message["session"], message.get("turn", ""),
                           message["key"], message["kind"])
        elif op == "request_close":
            s.request_close(message["session"], message["key"])
        elif op == "hook":
            s.hook(message["event"])
        elif op == "title":
            s.title(message["session"], message["title"][:512])
        elif op == "remove":
            s.remove_session(message["session"])
        elif op == "ack":
            s.ack(message.get("session"))
        elif op not in ("status", "diagnostics"):
            raise ValueError("unknown operation")
        self.publish()
        result = {"ok": True, "snapshot": s.snapshot()}
        if op == "diagnostics":
            result["hooks"] = s.diagnostics()
            result["subscribers"] = len(self.subscribers)
        return result

    async def client(self, reader, writer):
        self.clients.add(writer)
        queue = None
        disconnected = None
        try:
            line = await asyncio.wait_for(reader.readline(), 5)
            message = json.loads(line)
            if message.get("op") == "subscribe":
                queue = asyncio.Queue(maxsize=1)
                self.subscribers.add(queue)
                queue.put_nowait(self.state.snapshot())
                disconnected = asyncio.create_task(reader.read(1))
                while True:
                    update = asyncio.create_task(queue.get())
                    try:
                        done, _ = await asyncio.wait((update, disconnected), return_when=asyncio.FIRST_COMPLETED)
                        if disconnected in done:
                            break
                        snapshot = update.result()
                    finally:
                        update.cancel()
                        await asyncio.gather(update, return_exceptions=True)
                    writer.write(json.dumps({"ok": True, "snapshot": snapshot}).encode() + b"\n")
                    await asyncio.wait_for(writer.drain(), 5)
            else:
                result = self.dispatch(message)
                writer.write(json.dumps(result).encode() + b"\n")
                await asyncio.wait_for(writer.drain(), 5)
        except (ValueError, KeyError, TypeError):
            writer.write(b'{"ok":false,"error":"invalid request"}\n')
            try:
                await writer.drain()
            except OSError:
                pass
        except (OSError, asyncio.TimeoutError, asyncio.CancelledError):
            pass
        finally:
            if disconnected:
                disconnected.cancel()
                await asyncio.gather(disconnected, return_exceptions=True)
            if queue:
                self.subscribers.discard(queue)
            writer.close()
            await writer.wait_closed()
            self.clients.discard(writer)

    async def prune(self):
        while True:
            for row in self.state.db.execute("SELECT DISTINCT connection,pid,start_ticks,owner_pid,owner_start_ticks FROM views").fetchall():
                if (start_ticks(row["pid"]) != row["start_ticks"] or
                        start_ticks(row["owner_pid"]) != row["owner_start_ticks"]):
                    self.state.unbind(row["connection"])
            self.publish()
            await asyncio.sleep(2)


async def serve():
    private_dir(state_dir())
    private_dir(runtime_dir())
    with (runtime_dir() / "service.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        socket_path = control_socket()
        socket_path.unlink(missing_ok=True)
        state = State(state_dir() / "state.sqlite3")
        runtime = Runtime(state)
        server = await asyncio.start_unix_server(runtime.client, path=str(socket_path), limit=65536)
        socket_path.chmod(0o600)
        stop = asyncio.Event()
        loop = asyncio.get_running_loop()
        for sig in (signal.SIGTERM, signal.SIGINT):
            loop.add_signal_handler(sig, stop.set)
        tasks = [asyncio.create_task(watch_niri(state, runtime.publish)), asyncio.create_task(runtime.prune())]
        try:
            await stop.wait()
        finally:
            # Python 3.13+ waits for client connections in server.wait_closed().
            # Close idle Waybar subscriptions before awaiting server shutdown.
            server.close()
            for writer in list(runtime.clients):
                writer.close()
            for task in tasks:
                task.cancel()
            await asyncio.gather(*tasks, return_exceptions=True)
            await server.wait_closed()
            socket_path.unlink(missing_ok=True)
            state.close()
