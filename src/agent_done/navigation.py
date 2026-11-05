"""Focus the oldest pending conversation using its live window ID."""

import os
import subprocess

from .desktop import niri_query, niri_socket, start_ticks
from .ipc import request


def _alive(view):
    return (start_ticks(view["pid"]) == view["start_ticks"] and
            start_ticks(view["owner_pid"]) == view["owner_start_ticks"])


def jump():
    snapshot = request({"op": "status"})["snapshot"]
    pending = sorted((s for s in snapshot["sessions"] if s["pending"]),
                       key=lambda s: (s["updated"], s["id"]))
    if not pending:
        return None
    socket_path = niri_socket()
    windows = {w["id"] for w in niri_query("windows", socket_path)}
    for session in pending:
        for view in sorted(session["views"], key=lambda v: v["window_id"] or 0):
            if not view["active"] or view["window_id"] not in windows or not _alive(view):
                continue
            try:
                subprocess.run(["niri", "msg", "action", "focus-window", "--id", str(view["window_id"])],
                               env=dict(os.environ, NIRI_SOCKET=socket_path), capture_output=True,
                               text=True, timeout=4, check=True)
            except subprocess.TimeoutExpired as error:
                raise RuntimeError("niri 聚焦窗口超时，提醒仍保留") from error
            except subprocess.CalledProcessError as error:
                # A window can disappear between querying and focusing. Try the
                # next live view, without acknowledging any notification here.
                windows = {w["id"] for w in niri_query("windows", socket_path)}
                if view["window_id"] in windows:
                    raise RuntimeError("niri 无法聚焦目标窗口：" + error.stderr.strip()) from error
                continue
            return {"session": session["id"], "window_id": view["window_id"]}
    return None
