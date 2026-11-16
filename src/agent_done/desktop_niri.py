"""niri IPC adapter. Observations contain no session or attention state."""

import asyncio
import json
import os
from pathlib import Path
import subprocess

from .process import start_ticks


def niri_socket():
    explicit = os.environ.get("NIRI_SOCKET")
    if explicit:
        return explicit
    runtime = Path(os.environ.get("XDG_RUNTIME_DIR", f"/run/user/{os.getuid()}"))
    candidates = sorted(runtime.glob("niri.*.sock"), key=lambda p: p.stat().st_mtime, reverse=True)
    return str(candidates[0]) if candidates else None


def niri_query(command, socket_path=None):
    path = socket_path or niri_socket()
    if not path:
        raise RuntimeError("没有找到 niri socket")
    result = subprocess.run(["niri", "msg", "--json", command],
                            env=dict(os.environ, NIRI_SOCKET=path), capture_output=True,
                            text=True, timeout=4, check=True)
    return json.loads(result.stdout)


def observation(windows, workspaces):
    workspaces = {w["id"]: w for w in workspaces}
    result = []
    for window in windows:
        workspace = workspaces.get(window.get("workspace_id"), {})
        layout = window.get("layout", {}).get("pos_in_scrolling_layout")
        pid = window.get("pid")
        result.append({"id": window["id"], "pid": pid, "start": start_ticks(pid) if pid else None,
                       "position": {"output": workspace.get("output"),
                                    "workspace": workspace.get("name") or workspace.get("idx"),
                                    "row": layout[1] if layout else None,
                                    "column": layout[0] if layout else None,
                                    "floating": window.get("is_floating", False)}})
    return {"backend": "niri", "windows": result,
            "focused": next((w["id"] for w in windows if w.get("is_focused")), None)}


class Niri:
    name = "niri"
    ephemeral = False

    def snapshot(self):
        return observation(niri_query("windows"), niri_query("workspaces"))

    def activate(self, window_id):
        path = niri_socket()
        subprocess.run(["niri", "msg", "action", "focus-window", "--id", str(window_id)],
                       env=dict(os.environ, NIRI_SOCKET=path), capture_output=True,
                       text=True, timeout=4, check=True)

    async def events(self):
        process = None
        windows, workspaces = {}, {}
        have_windows = have_workspaces = False
        try:
            path = niri_socket()
            if not path:
                raise RuntimeError("没有找到 niri socket")
            process = await asyncio.create_subprocess_exec(
                "niri", "msg", "--json", "event-stream", env=dict(os.environ, NIRI_SOCKET=path),
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL, limit=8 * 1024 * 1024)
            async for line in process.stdout:
                event = json.loads(line)
                if "WindowsChanged" in event:
                    windows = {w["id"]: w for w in event["WindowsChanged"]["windows"]}
                    have_windows = True
                elif "WorkspacesChanged" in event:
                    workspaces = {w["id"]: w for w in event["WorkspacesChanged"]["workspaces"]}
                    have_workspaces = True
                elif "WindowOpenedOrChanged" in event:
                    window = event["WindowOpenedOrChanged"]["window"]
                    windows[window["id"]] = window
                    if window.get("is_focused"):
                        for other in windows.values():
                            other["is_focused"] = other["id"] == window["id"]
                elif "WindowFocusChanged" in event:
                    focused = event["WindowFocusChanged"].get("id")
                    for window in windows.values():
                        window["is_focused"] = window["id"] == focused
                elif "WindowClosed" in event:
                    windows.pop(event["WindowClosed"]["id"], None)
                elif "WindowLayoutsChanged" in event:
                    for window_id, layout in event["WindowLayoutsChanged"]["changes"]:
                        if window_id in windows:
                            windows[window_id]["layout"] = layout
                if have_windows and have_workspaces:
                    yield observation(list(windows.values()), list(workspaces.values()))
            raise RuntimeError("niri 事件流已断开")
        finally:
            if process and process.returncode is None:
                try:
                    process.terminate()
                except ProcessLookupError:
                    pass
                await process.wait()
