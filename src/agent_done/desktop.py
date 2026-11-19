"""Desktop interface; adapters own OS handles, runtime owns attention state."""

import asyncio
import os
import subprocess
import sys

from .process import ancestors, start_ticks, codex_pid
# Compatibility for older local diagnostics. Production uses Desktop instead.
from .desktop_niri import niri_socket, niri_query


class WindowIdentificationError(RuntimeError):
    """The desktop was observed, but the source window could not be identified."""


def backend_name():
    explicit = os.environ.get("AGENT_DONE_DESKTOP")
    if explicit:
        if explicit not in ("niri", "yabai", "macos"):
            raise RuntimeError("AGENT_DONE_DESKTOP 必须是 niri、yabai 或 macos")
        return explicit
    if sys.platform == "darwin":
        return "macos"
    if sys.platform.startswith("linux"):
        return "niri"
    raise RuntimeError("当前操作系统尚无桌面适配")


def create_adapter(name=None):
    name = name or backend_name()
    if name == "niri":
        from .desktop_niri import Niri
        return Niri()
    if name in ("macos", "yabai") and sys.platform != "darwin":
        raise RuntimeError(name + " 桌面接入仅在 macOS 可用")
    if name == "yabai":
        from .desktop_yabai import Yabai
        return Yabai()
    if name == "macos":
        from .desktop_macos import MacOS
        return MacOS()
    raise RuntimeError("未知桌面后端：" + name)


def identify(adapter, pid):
    """Focus can only disambiguate windows belonging to a source ancestor."""
    chain = ancestors(pid)
    if hasattr(adapter, "register"):
        adapter.register(chain)
    snapshot = adapter.snapshot()
    for ancestor in chain:
        candidates = [w for w in snapshot["windows"] if w.get("pid") == ancestor]
        if not candidates:
            continue
        if len(candidates) > 1:
            candidates = [w for w in candidates if w["id"] == snapshot["focused"]]
        if len(candidates) != 1 or not candidates[0].get("start"):
            raise WindowIdentificationError("无法唯一识别源终端窗口，请在目标独立窗口中启动")
        window = candidates[0]
        return {"backend": adapter.name, "window_id": window["id"],
                "window_pid": window["pid"], "window_start": window["start"]}
    raise WindowIdentificationError("无法从源进程找到终端窗口，请在本机独立终端窗口内运行")


def desktop_error(error):
    if isinstance(error, subprocess.CalledProcessError):
        return error.stderr.strip() if error.stderr else str(error)
    if isinstance(error, subprocess.TimeoutExpired):
        return "桌面操作超时，提醒仍保留"
    return str(error)


class Desktop:
    """Serialize access to native handles off the event loop in the user service."""
    def __init__(self, adapter=None):
        self.adapter = adapter or create_adapter()
        self.name = self.adapter.name
        self.lock = asyncio.Lock()

    @property
    def ephemeral(self):
        return self.adapter.ephemeral

    async def _call(self, function, *args):
        async with self.lock:
            task = asyncio.create_task(asyncio.to_thread(function, *args))
            try:
                return await asyncio.shield(task)
            except asyncio.CancelledError:
                # A cancelled waiter must not unlock while native code still runs.
                # Preserve cancellation even if the native call fails meanwhile;
                # otherwise watch() treats that error as recoverable and restarts.
                await asyncio.gather(task, return_exceptions=True)
                raise
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                raise RuntimeError(desktop_error(error)) from error

    async def identify(self, pid):
        return await self._call(identify, self.adapter, pid)

    async def snapshot(self):
        return await self._call(self.adapter.snapshot)

    async def activate(self, window_id, window_pid=None, window_start=None):
        def activate():
            snapshot = self.adapter.snapshot()
            window = next((w for w in snapshot["windows"] if w["id"] == window_id), None)
            if window is None:
                return False
            if window_pid is not None and (window.get("pid") != window_pid or
                                           window.get("start") != window_start):
                return False
            self.adapter.activate(window_id)
            return True
        return await self._call(activate)

    async def watch(self, observed, disconnected):
        while True:
            try:
                if hasattr(self.adapter, "events"):
                    async for snapshot in self.adapter.events():
                        observed(snapshot)
                else:
                    while True:
                        observed(await self.snapshot())
                        await asyncio.sleep(0.3)
            except (OSError, RuntimeError, ValueError, KeyError, subprocess.SubprocessError) as error:
                disconnected(desktop_error(error))
            await asyncio.sleep(2)


async def window_identity():
    from .ipc import async_request
    snapshot = (await async_request({"op": "status"}))["snapshot"]
    explicit = os.environ.get("AGENT_DONE_DESKTOP")
    if explicit and snapshot.get("desktop_backend", "niri") != explicit:
        raise RuntimeError("服务使用的桌面后端与 AGENT_DONE_DESKTOP 不一致，请重启服务")
    if "desktop_backend" not in snapshot:
        # Keep an already-running old niri service usable during checkout upgrades.
        if backend_name() != "niri":
            raise RuntimeError("请重启 agent-done 服务以启用桌面适配")
        return await asyncio.to_thread(identify, create_adapter("niri"), os.getpid())
    return (await async_request({"op": "identify", "pid": os.getpid()}, timeout=15))["identity"]
