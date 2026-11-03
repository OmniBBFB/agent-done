#!/usr/bin/env python3
"""Test jump with real niri windows and an isolated attention service.

Completion events are fixtures, not real Codex generations. The installed CLI,
niri focus action and focus-driven acknowledgement are exercised end to end.
The production service/database and users' notifications are never modified.
"""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

from agent_done.desktop import niri_query, niri_socket, start_ticks
from agent_done.ipc import request

BIN = str(Path.home() / ".local/bin/agent-done")


def wait(predicate, timeout=8):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.02)
    raise TimeoutError("expected desktop state did not arrive")


def action(*args):
    subprocess.run(["niri", "msg", "action", *args], capture_output=True, text=True, check=True, timeout=4)


def snapshot():
    return request({"op": "status"})["snapshot"]


def run():
    os.environ["NIRI_SOCKET"] = niri_socket()
    original = next(w for w in niri_query("windows") if w.get("is_focused"))
    workspaces = niri_query("workspaces")
    output = next(w["output"] for w in workspaces if w["id"] == original["workspace_id"])
    target_workspace = max(w["idx"] for w in workspaces if w["output"] == output)
    processes = []
    service = None
    report = {"passed": False, "completion_events": "synthetic fixtures", "checks": []}
    report_path = Path(__file__).resolve().parent.parent / "outputs/jump-2026-11-03/result.json"
    with tempfile.TemporaryDirectory(prefix="agent-done-jump-") as directory:
        os.environ.update(XDG_RUNTIME_DIR=directory, AGENT_DONE_STATE_DIR=directory + "/state")
        try:
            service = subprocess.Popen([BIN, "serve"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            wait(lambda: (Path(directory) / "agent-done/control.sock").exists())
            wait(lambda: snapshot()["niri_connected"])
            for index in range(2):
                action("focus-window", "--id", str(original["id"]))
                # Alacritty still connects to the real desktop's Wayland socket.
                env = dict(os.environ, XDG_RUNTIME_DIR=f"/run/user/{os.getuid()}", WAYLAND_DISPLAY="wayland-1")
                process = subprocess.Popen(["alacritty", "--class", "agent-done-jump-test",
                                            "--title", "agent-done jump test", "-e", "/usr/bin/sleep", "120"],
                                           env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                processes.append(process)
                window = wait(lambda: next((w for w in niri_query("windows") if w.get("pid") == process.pid), None))
                action("move-window-to-workspace", "--window-id", str(window["id"]), "--focus", "false", str(target_workspace))
                sid = f"jump-test-{index + 1}"
                identity = {"window_id": window["id"], "pid": process.pid, "start_ticks": start_ticks(process.pid),
                            "owner_pid": process.pid, "owner_start_ticks": start_ticks(process.pid)}
                request({"op": "bind", "connection": sid, "session": sid, "identity": identity})
            action("focus-window", "--id", str(original["id"]))
            wait(lambda: snapshot()["focused_window"] == original["id"])
            for index in range(2):
                sid = f"jump-test-{index + 1}"
                request({"op": "running", "session": sid, "turn": "test-turn"})
                request({"op": "complete", "session": sid, "turn": "test-turn", "status": "completed"})
                time.sleep(0.02)
            before = snapshot()
            assert before["pending_count"] == 2
            report["checks"].append({"before": before})
            for index, expected_count in ((1, 1), (2, 0)):
                expected = next(s for s in before["sessions"] if s["id"] == f"jump-test-{index}")
                completed = subprocess.run([BIN, "jump"], capture_output=True, text=True, check=True, timeout=5)
                after = wait(lambda: s if (s := snapshot())["pending_count"] == expected_count else None)
                window_id = expected["views"][0]["window_id"]
                assert after["focused_window"] == window_id
                assert next(w["id"] for w in niri_query("windows") if w.get("is_focused")) == window_id
                assert expected["id"] in completed.stdout
                report["checks"].append({"cli": completed.stdout.strip(), "after": after})
            previous = snapshot()["focused_window"]
            completed = subprocess.run([BIN, "jump"], capture_output=True, text=True, check=True, timeout=5)
            assert "没有可跳转" in completed.stdout
            assert snapshot()["focused_window"] == previous
            report["checks"].append({"empty_queue": completed.stdout.strip(), "focus_unchanged": True})
            report["passed"] = True
        except Exception as error:
            report["error"] = str(error)
            raise
        finally:
            for process in processes:
                if process.poll() is None:
                    process.terminate()
                    process.wait(timeout=5)
            if service and service.poll() is None:
                service.terminate()
                service.wait(timeout=5)
            action("focus-window", "--id", str(original["id"]))
            report["test_windows_closed"] = all(p.poll() is not None for p in processes)
            report["original_focus_restored"] = original["id"]
            report_path.parent.mkdir(parents=True, exist_ok=True)
            report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"passed": report["passed"], "checks": len(report["checks"]), "report": str(report_path)}), flush=True)


if __name__ == "__main__":
    run()
