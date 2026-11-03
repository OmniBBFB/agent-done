"""niri IPC and Linux process identity discovery."""

import asyncio
import json
import os
from pathlib import Path
import subprocess


def start_ticks(pid):
    try:
        return (Path("/proc") / str(pid) / "stat").read_text().rsplit(")", 1)[1].split()[19]
    except (OSError, IndexError):
        return None


def codex_pid(launcher_pid):
    pending = [launcher_pid]
    while pending:
        pid = pending.pop(0)
        proc = Path("/proc") / str(pid)
        try:
            if proc.joinpath("comm").read_text().strip() == "codex":
                return pid
            children = proc / "task" / str(pid) / "children"
            pending.extend(int(x) for x in children.read_text().split())
        except OSError:
            pass
    return launcher_pid


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


def window_identity():
    socket_path = niri_socket()
    windows = niri_query("windows", socket_path)
    ancestors = []
    pid = os.getpid()
    while pid > 1 and pid not in ancestors:
        ancestors.append(pid)
        try:
            stat = (Path("/proc") / str(pid) / "stat").read_text().rsplit(")", 1)[1].split()
            pid = int(stat[1])
        except (OSError, ValueError):
            break
    for ancestor in ancestors:
        candidates = [w for w in windows if w.get("pid") == ancestor]
        if len(candidates) == 1:
            return {"window_id": candidates[0]["id"], "niri_socket": socket_path}
        if candidates:
            focused = [w for w in candidates if w.get("is_focused")]
            if len(focused) == 1:
                return {"window_id": focused[0]["id"], "niri_socket": socket_path}
            raise RuntimeError("同一终端进程有多个窗口，请在目标窗口内启动")
    raise RuntimeError("无法从当前进程找到 niri 终端窗口，请在本机终端内运行")


async def watch_niri(state, changed):
    while True:
        process = None
        try:
            path = niri_socket()
            if not path:
                raise RuntimeError("niri is not running")
            process = await asyncio.create_subprocess_exec(
                "niri", "msg", "--json", "event-stream", env=dict(os.environ, NIRI_SOCKET=path),
                stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.DEVNULL, limit=8 * 1024 * 1024)
            async for line in process.stdout:
                event = json.loads(line)
                state.niri_connected = True
                if "WindowsChanged" in event:
                    windows = event["WindowsChanged"]["windows"]
                    state.windows = {w["id"]: w for w in windows}
                    state.focus(next((w["id"] for w in windows if w.get("is_focused")), None))
                    for row in state.db.execute("SELECT DISTINCT connection,window_id FROM views").fetchall():
                        if row["window_id"] not in state.windows:
                            state.unbind(row["connection"])
                elif "WorkspacesChanged" in event:
                    state.workspaces = {w["id"]: w for w in event["WorkspacesChanged"]["workspaces"]}
                elif "WindowOpenedOrChanged" in event:
                    window = event["WindowOpenedOrChanged"]["window"]
                    state.windows[window["id"]] = window
                    if window.get("is_focused"):
                        state.focus(window["id"])
                elif "WindowFocusChanged" in event:
                    state.focus(event["WindowFocusChanged"].get("id"))
                elif "WindowClosed" in event:
                    window_id = event["WindowClosed"]["id"]
                    state.windows.pop(window_id, None)
                    for row in state.db.execute("SELECT DISTINCT connection FROM views WHERE window_id=?", (window_id,)).fetchall():
                        state.unbind(row["connection"])
                elif "WindowLayoutsChanged" in event:
                    for window_id, layout in event["WindowLayoutsChanged"]["changes"]:
                        if window_id in state.windows:
                            state.windows[window_id]["layout"] = layout
                changed()
        except (OSError, RuntimeError, ValueError, KeyError):
            pass
        finally:
            state.niri_connected = False
            state.focus(None)
            changed()
            if process and process.returncode is None:
                try:
                    process.terminate()
                except ProcessLookupError:
                    pass
                await process.wait()
        await asyncio.sleep(2)
