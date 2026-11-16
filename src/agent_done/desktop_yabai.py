"""Experimental macOS/yabai adapter; never guess a target from its title."""

import json
import subprocess

from .process import start_ticks


def command(*args):
    result = subprocess.run(["yabai", "-m", *args], capture_output=True, text=True, timeout=4, check=True)
    return result.stdout


class Yabai:
    name = "yabai"
    ephemeral = False

    def snapshot(self):
        windows = json.loads(command("query", "--windows"))
        return {"backend": self.name,
                "windows": [{"id": w["id"], "pid": w["pid"], "start": start_ticks(w["pid"]),
                             "position": {"output": w.get("display"), "workspace": w.get("space"),
                                          "row": None, "column": None, "floating": w.get("is-floating", False)}}
                            for w in windows],
                "focused": next((w["id"] for w in windows if w.get("has-focus")), None)}

    def activate(self, window_id):
        windows = json.loads(command("query", "--windows"))
        window = next((w for w in windows if w["id"] == window_id), None)
        if window is None:
            raise RuntimeError("yabai 目标窗口已关闭")
        if window.get("is-minimized"):
            command("window", str(window_id), "--deminimize")
        command("window", "--focus", str(window_id))
