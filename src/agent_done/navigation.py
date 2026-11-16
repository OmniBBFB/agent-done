"""Choose pending conversations; only observed focus acknowledges attention."""

import asyncio

from .ipc import request
from .process import same_process


def _alive(view):
    return (same_process(view["pid"], view["start_ticks"]) and
            same_process(view["owner_pid"], view["owner_start_ticks"]))


def _pending(snapshot):
    return sorted((s for s in snapshot["sessions"] if s["pending"]),
                  key=lambda s: (s["updated"], s["id"]))


def jump():
    snapshot = request({"op": "status"})["snapshot"]
    if not _pending(snapshot):
        return None
    legacy = "desktop_backend" not in snapshot
    if legacy:
        # Existing Linux service can keep running while the checkout is upgraded.
        from .desktop import Desktop, backend_name, create_adapter
        if backend_name() != "niri":
            raise RuntimeError("请重启 agent-done 服务以启用桌面适配")
        desktop = Desktop(create_adapter("niri"))
        observation = asyncio.run(desktop.snapshot())
    else:
        response = request({"op": "desktop_status"}, timeout=15)
        observation, snapshot = response["desktop"], response["snapshot"]
    backend = observation["backend"]
    windows = {w["id"]: w for w in observation["windows"]}
    for session in _pending(snapshot):
        for view in sorted(session["views"], key=lambda v: (0, v["window_id"])
                           if isinstance(v["window_id"], int) else (1, str(v["window_id"]))):
            if (not view["active"] or view.get("backend", "niri") != backend or
                    view["window_id"] not in windows or not _alive(view)):
                continue
            if legacy:
                requested = asyncio.run(desktop.activate(view["window_id"]))
            else:
                try:
                    requested = request({"op": "focus", "backend": backend, "window_id": view["window_id"],
                                         "window_pid": view.get("window_pid"),
                                         "window_start": view.get("window_start")}, timeout=15)["requested"]
                except RuntimeError:
                    # Disappearance is a recoverable race, a rejected focus isn't.
                    response = request({"op": "desktop_status"}, timeout=15)
                    if view["window_id"] in {w["id"] for w in response["desktop"]["windows"]}:
                        raise
                    continue
            if requested:
                return {"session": session["id"], "window_id": view["window_id"]}
    return None
