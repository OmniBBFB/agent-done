"""Waybar continuous JSON module; all user-provided text is escaped."""

import asyncio
import html
import json
from pathlib import Path

from .codex import ensure_service
from .ipc import snapshots

REASONS = {"done": "已完成", "approval": "待授权", "question": "待回答", "error": "执行失败"}


def location(view):
    position = view.get("position")
    if not position:
        return "位置待更新"
    parts = [str(position.get("output") or "未知显示器"), "工作区 " + str(position.get("workspace") or "?")]
    if position.get("floating"):
        parts.append("浮动窗口")
    elif position.get("row") is not None:
        parts.append(f"第 {position['row']} 行第 {position['column']} 列")
    return " · ".join(parts)


def render(snapshot):
    pending = [s for s in snapshot["sessions"] if s["pending"]]
    count = len(pending)
    rows = [f"待处理 {count} 个 Codex 会话" if count else "没有待处理会话"]
    for session in pending:
        title = session["title"] or Path(session["cwd"]).name or session["id"][:8]
        title = " ".join(title.split())[:100]
        rows.append(f"{title} — {REASONS.get(session['reason'], '待查看')}")
        for view in session["views"]:
            suffix = "" if view["active"] else "（终端内的其他会话）"
            rows.append("  " + location(view) + suffix)
    connected = snapshot.get("desktop_connected", snapshot.get("niri_connected"))
    if not connected:
        rows.append(snapshot.get("desktop_backend", "niri") + " 连接中，位置和查看状态稍后更新")
    return {"text": f"Codex {count}", "tooltip": html.escape("\n".join(rows)),
            "class": ["pending" if count else "idle"] + ([] if connected else ["disconnected"])}


async def stream():
    while True:
        try:
            await ensure_service()
            async for snapshot in snapshots():
                print(json.dumps(render(snapshot), ensure_ascii=False), flush=True)
        except (OSError, ValueError, RuntimeError, asyncio.TimeoutError):
            print(json.dumps({"text": "Codex ?", "tooltip": "提醒服务连接中", "class": "disconnected"}, ensure_ascii=False), flush=True)
        await asyncio.sleep(2)
