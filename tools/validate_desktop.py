#!/usr/bin/env python3
"""Real-window desktop acceptance with synthetic attention, no model requests.

Creates three temporary Kitty/Alacritty windows and an isolated service from the
current interpreter. This exercises source binding, focus readback and request
viewing, not real agent events, physical shortcuts or cross-Space behavior.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

from agent_done.desktop_niri import niri_socket
from agent_done.ipc import request
from agent_done.process import same_process
from terminal_launcher import desktop_environment, terminal_command


def wait(predicate, timeout=20):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = predicate()
        if result:
            return result
        time.sleep(0.05)
    raise TimeoutError("未在限时内观察到预期窗口/焦点状态")


def status():
    return request({"op": "status"})["snapshot"]


def focus(identity):
    result = request({"op": "focus", "backend": identity["backend"], "window_id": identity["window_id"],
                      "window_pid": identity.get("window_pid"), "window_start": identity.get("window_start")}, timeout=15)
    if not result["requested"]:
        raise RuntimeError("验收窗口已失效")


def run(backend, terminal, output):
    processes, identities = [], []
    service = None
    original = None
    report = {"passed": False, "backend": backend, "terminal": terminal,
              "attention_events": "synthetic approval/question", "physical_shortcut": "not tested",
              "cross_workspace": "not tested", "checks": []}
    report_path = output or Path(__file__).resolve().parent.parent / "outputs" / f"desktop-{backend}" / "result.json"
    if backend == "niri":
        path = niri_socket()
        if not path:
            raise RuntimeError("没有找到真实 niri socket")
        os.environ["NIRI_SOCKET"] = path
    with tempfile.TemporaryDirectory(prefix="ad-", dir="/tmp") as directory:
        # Keep the real graphical runtime; isolate only agent-done's own paths.
        os.environ.update(AGENT_DONE_DESKTOP=backend, AGENT_DONE_STATE_DIR=directory + "/state",
                          AGENT_DONE_RUNTIME_DIR=directory + "/runtime")
        try:
            service = subprocess.Popen([sys.executable, "-m", "agent_done", "serve"],
                                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            wait(lambda: (Path(directory) / "runtime/control.sock").exists())
            desktop = request({"op": "desktop_status"}, timeout=15)["desktop"]
            original = next((w for w in desktop["windows"] if w["id"] == desktop["focused"]), None)
            for index in range(3):
                identity_path = Path(directory) / f"window-{index}.json"
                command = [sys.executable, Path(__file__).with_name("terminal_identity_probe.py"), identity_path]
                env = desktop_environment() if sys.platform.startswith("linux") else dict(os.environ)
                process = subprocess.Popen(terminal_command(terminal, "agent-done-acceptance", "agent-done acceptance", command),
                                           env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                processes.append(process)
                identity = wait(lambda: json.loads(identity_path.read_text())
                                if identity_path.exists() and identity_path.stat().st_size else None)
                if "error" in identity:
                    raise RuntimeError(identity["error"])
                identities.append(identity)
                self_window = next(w for w in request({"op": "desktop_status"}, timeout=15)["desktop"]["windows"]
                                   if w["id"] == identity["window_id"])
                assert self_window["pid"] == identity["window_pid"], "source owner mismatch"
                assert same_process(identity["pid"], identity["start_ticks"]), "source process identity invalid"
            assert len({i["window_id"] for i in identities}) == 3, "source windows are not distinct"
            focus(identities[2])
            wait(lambda: status()["focused_window"] == identities[2]["window_id"])
            for index, kind in enumerate(("approval", "question")):
                sid = f"desktop-test-{index}"
                identity = dict(identities[index], owner_pid=identities[index]["pid"],
                                owner_start_ticks=identities[index]["start_ticks"])
                request({"op": "bind", "connection": sid, "session": sid, "identity": identity})
                request({"op": "request_open", "connection": sid, "session": sid,
                         "turn": "test-turn", "key": sid, "kind": kind})
            assert status()["pending_count"] == 2
            report["checks"].append("distinct source windows and pending_count=2")
            for index, remaining in ((0, 1), (1, 0)):
                result = subprocess.run([sys.executable, "-m", "agent_done", "jump"],
                                        capture_output=True, text=True, check=True, timeout=20)
                report.setdefault("jumps", []).append({"target": index, "stdout": result.stdout,
                                                        "stderr": result.stderr, "snapshot": status()})
                current = wait(lambda: s if (s := status())["pending_count"] == remaining else None)
                assert current["focused_window"] == identities[index]["window_id"]
                session = next(s for s in current["sessions"] if s["id"] == f"desktop-test-{index}")
                assert session["phase"] == ("approval", "question")[index]
                assert not session["pending"]
                assert session["id"] in result.stdout
                report["checks"].append(f"observed target {index}, pending={remaining}, request still waiting")
            previous = status()["focused_window"]
            result = subprocess.run([sys.executable, "-m", "agent_done", "jump"],
                                    capture_output=True, text=True, check=True, timeout=20)
            assert "没有可跳转" in result.stdout
            assert status()["focused_window"] == previous
            report["checks"].append("empty queue leaves focus unchanged")
            report["passed"] = True
        except Exception as error:
            report["error"] = str(error)
            report["identities"] = identities
            try:
                report["failure_snapshot"] = status()
                report["failure_desktop"] = request({"op": "desktop_status"}, timeout=15)["desktop"]
            except (OSError, RuntimeError, ValueError, TimeoutError) as diagnostic_error:
                report["diagnostic_error"] = str(diagnostic_error)
            raise
        finally:
            # Kill only the probe processes we created, never an existing terminal.
            for identity in identities:
                if same_process(identity["pid"], identity["start_ticks"]):
                    try:
                        os.kill(identity["pid"], 15)
                    except ProcessLookupError:
                        pass
            for process in processes:
                if process.poll() is None:
                    process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
            if service and service.poll() is None:
                if original:
                    try:
                        focus({"backend": backend, "window_id": original["id"],
                               "window_pid": original["pid"], "window_start": original["start"]})
                        report["original_focus_restored"] = bool(wait(lambda: status()["focused_window"] == original["id"]))
                    except (OSError, RuntimeError, TimeoutError):
                        report["original_focus_restored"] = False
                else:
                    report["original_focus_restored"] = "unavailable: original window was not registered"
                service.terminate()
                try:
                    service.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    service.kill()
                    service.wait()
                    report["service_forced_shutdown"] = True
            report_path.parent.mkdir(parents=True, exist_ok=True)
            report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"passed": report["passed"], "report": str(report_path)}, ensure_ascii=False))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backend", required=True, choices=("niri", "yabai", "macos"))
    parser.add_argument("--terminal", default="kitty", choices=("kitty", "alacritty"))
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    run(args.backend, args.terminal, args.output)
