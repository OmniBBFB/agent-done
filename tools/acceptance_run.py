#!/usr/bin/env python3
"""Drive the installed launcher through real TUI input, with assertions.

Run on the host desktop. Only the explicitly named test terminal is driven.
Control RPC is used for read-only hook inspection, never generating turns.
"""

import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import time

from acceptance import status, submit, input_data, transcript, write_json, start
from _probe_rpc import ProbeRPC
from agent_done.codex import hook_command
from agent_done.desktop import niri_query, niri_socket
from agent_done.ipc import request
from agent_done.waybar import render


def wait(predicate, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = predicate()
        if result:
            return result
        time.sleep(0.2)
    raise TimeoutError("expected desktop state did not arrive")


def focus(window_id):
    subprocess.run(["niri", "msg", "action", "focus-window", "--id", str(window_id)],
                   check=True, capture_output=True)
    wait(lambda: request({"op": "status"})["snapshot"]["focused_window"] == window_id, 5)


def session(artifacts):
    rows = status(artifacts)["sessions"]
    return rows[0] if rows else None


def record(artifacts, name, value):
    path = artifacts.parent / "checks.json"
    checks = json.loads(path.read_text()) if path.exists() else {}
    checks[name] = value
    write_json(path, checks)
    print(json.dumps({"check": name, "result": value}, ensure_ascii=False), flush=True)


def trust(artifacts):
    sid = session(artifacts)["id"]
    rpc = ProbeRPC()
    try:
        data = rpc.request("hooks/list", {"threadId": sid})
    finally:
        rpc.close()
    hooks = [h for entry in data["data"] for h in entry["hooks"]]
    pending = [h for h in hooks if h["trustStatus"] in ("untrusted", "modified")]
    def ours(hook):
        argv = shlex.split(hook.get("command", ""))
        return (len(argv) == 4 and argv[1:] == ["-m", "agent_done", "hook"] and
                Path(argv[0]).parent == Path(hook_command().split()[0]).parent and
                Path(argv[0]).name in ("python", "python3"))
    if any(not ours(h) for h in pending):
        raise RuntimeError("unrelated untrusted hook; inspect individually instead of trusting all")
    if pending:
        submit(artifacts, "/hooks")
        time.sleep(1)
        input_data(artifacts, "t")
        time.sleep(1)
        input_data(artifacts, "\x1b")
    record(artifacts, "normal_hook_review", {"command": hook_command(), "reviewed": len(pending)})


def complete(artifacts, original):
    row = session(artifacts)
    old_turn = row["turn_id"]
    submit(artifacts, "这是 agent-done 正式版本的桌面验收。请只回复 AGENT_DONE_PRODUCTION_OK，不调用工具、不读取文件。")
    focus(original)
    result = wait(lambda: (s if s["turn_id"] != old_turn and s["phase"] == "done" and s["pending"] else None)
                  if (s := session(artifacts)) else None)
    assert "AGENT_DONE_PRODUCTION_OK" in transcript(artifacts)
    snapshot = request({"op": "status"})["snapshot"]
    output = render(snapshot)
    assert output["text"] == "Codex " + str(snapshot["pending_count"])
    assert result["views"][0]["position"]["row"] is not None
    record(artifacts, "completion_unread", {"session": result, "waybar": output,
                                           "subscribers": status(artifacts)["subscribers"]})
    focus(result["views"][0]["window_id"])
    assert not session(artifacts)["pending"]
    record(artifacts, "completion_focus_ack", session(artifacts))


def approval(artifacts, original):
    submit(artifacts, "这是 agent-done 对真实人工授权界面的验收。请使用 shell 工具运行唯一一条无副作用命令 printf AGENT_DONE_PERMISSION_OK，并显式设置 sandbox_permissions=require_escalated，justification 为‘agent-done 人工授权验收’，不要设置 prefix_rule，不要用其他方式运行它。需要用户确认时请等待。完成后只回复验收完成。")
    focus(original)
    row = wait(lambda: s if (s := session(artifacts))["phase"] == "approval" and s["pending"] else None)
    record(artifacts, "approval_unread", row)
    focus(row["views"][0]["window_id"])
    row = session(artifacts)
    assert row["phase"] == "approval" and not row["pending"]
    time.sleep(1)
    assert session(artifacts)["phase"] == "approval"
    record(artifacts, "approval_focus_does_not_approve", row)
    # Native TUI's default is the one-time approval; never persistent approval.
    text = transcript(artifacts)[-6000:]
    if "AGENT_DONE_PERMISSION_OK" not in text or "1." not in text:
        raise RuntimeError("inspect native approval UI before approving")
    input_data(artifacts, "\r")
    focus(original)
    row = wait(lambda: s if (s := session(artifacts))["phase"] == "done" else None)
    assert "AGENT_DONE_PERMISSION_OK" in transcript(artifacts)
    record(artifacts, "approval_resolved_by_native_ui", row)
    focus(row["views"][0]["window_id"])


def question(artifacts, original):
    submit(artifacts, "/plan")
    time.sleep(1)
    submit(artifacts, "这是 agent-done 结构化提问提醒验收。不要调用 shell 或读取文件。请立即调用 request_user_input 工具向用户提一个选择题：‘选择验收选项’，两个选项为‘A（推荐）’和‘B’，然后等待回答。回答后只给出一句计划。")
    focus(original)
    row = wait(lambda: s if (s := session(artifacts))["phase"] == "question" and s["pending"] else None)
    record(artifacts, "structured_question_unread", row)
    focus(row["views"][0]["window_id"])
    row = session(artifacts)
    assert row["phase"] == "question" and not row["pending"]
    time.sleep(1)
    assert session(artifacts)["phase"] == "question"
    record(artifacts, "question_focus_does_not_answer", row)


def rest(artifacts, original):
    old = session(artifacts)
    if old is None:
        old = json.loads((artifacts.parent / "checks.json").read_text())["question_answered_in_native_ui"]
    sid = old["id"]
    if not (artifacts / "terminal-exit.json").exists():
        assert old["phase"] == "question"
        input_data(artifacts, "\r")
        row = wait(lambda: s if (s := session(artifacts))["phase"] == "done" else None)
        record(artifacts, "question_answered_in_native_ui", row)
        time.sleep(1)
        if "Implement this plan?" in transcript(artifacts)[-3000:]:
            input_data(artifacts, "\x1b")
            time.sleep(1)
        submit(artifacts, "/quit")
        wait(lambda: (artifacts / "terminal-exit.json").exists(), 15)
    wait(lambda: session(artifacts) is None, 5)
    record(artifacts, "normal_exit_clears_binding", True)
    resumed = artifacts.parent / "resume"
    if resumed.exists():
        resumed = artifacts.parent / ("resume-" + str(int(time.time())))
    start(resumed, sid)
    wait(lambda: (resumed / "launcher.json").exists(), 10)
    row = wait(lambda: session(resumed), 20)
    assert row["id"] == sid
    assert row["views"][0]["pid"] != old["views"][0]["pid"]
    assert row["views"][0]["window_id"] != old["views"][0]["window_id"]
    record(resumed, "resume_same_session_new_identity", row)
    trust(resumed)
    second = artifacts.parent / "second"
    start(second)
    wait(lambda: (second / "launcher.json").exists(), 10)
    other = wait(lambda: session(second), 20)
    assert other["id"] != sid
    parallel(artifacts, resumed, second, original)


def parallel(artifacts, resumed, second, original):
    sid = session(resumed)["id"]
    other = session(second)
    # Keep test windows outside the user's working workspace while generating.
    for terminal in (resumed, second):
        window_id = session(terminal)["views"][0]["window_id"]
        subprocess.run(["niri", "msg", "action", "move-window-to-workspace", "--window-id", str(window_id),
                        "--focus", "false", "2"], check=True, capture_output=True)
    focus(original)
    for terminal in (resumed, second):
        submit(terminal, "agent-done 多窗口验收：只回复 AGENT_DONE_PARALLEL_OK，不调用工具或读取文件。")
    rows = wait(lambda: [a, b] if (a := session(resumed))["pending"] and (b := session(second))["pending"] else None)
    assert request({"op": "status"})["snapshot"]["pending_count"] == 2
    record(artifacts, "two_real_sessions_count_two", rows)
    focus(rows[0]["views"][0]["window_id"])
    assert not session(resumed)["pending"] and session(second)["pending"]
    assert request({"op": "status"})["snapshot"]["pending_count"] == 1
    record(artifacts, "focus_acknowledges_only_one", True)
    window = session(second)["views"][0]["window_id"]
    before = session(second)["views"][0]["position"]
    target = 2 if before["workspace"] != 2 else 1
    subprocess.run(["niri", "msg", "action", "move-window-to-workspace", "--window-id", str(window), str(target)],
                   check=True, capture_output=True)
    row = wait(lambda: s if (s := session(second))["views"][0]["position"]["workspace"] == target else None, 5)
    record(artifacts, "live_position_after_move", {"before": before, "after": row})
    # Close exactly the second test's Alacritty to exercise abrupt view cleanup.
    pid = json.loads((second / "window.json").read_text())["alacritty_pid"]
    os.kill(pid, 15)
    wait(lambda: session(second) is None, 10)
    assert request({"op": "status"})["snapshot"]["pending_count"] == 0
    record(artifacts, "abrupt_window_close_clears_count", True)
    submit(resumed, "/quit")
    wait(lambda: (resumed / "terminal-exit.json").exists(), 15)
    wait(lambda: session(resumed) is None, 5)
    # Archive only our acceptance threads. Never use RPC to drive a model turn.
    rpc = ProbeRPC()
    try:
        for test_sid in (sid, other["id"]):
            rpc.request("thread/archive", {"threadId": test_sid})
    finally:
        rpc.close()
    focus(original)
    record(artifacts, "test_cleanup", request({"op": "diagnostics"}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("step", choices=("complete", "approval", "question", "rest", "parallel"))
    parser.add_argument("artifacts", type=Path)
    parser.add_argument("--original", type=int, default=202)
    args = parser.parse_args()
    os.environ.setdefault("XDG_RUNTIME_DIR", f"/run/user/{os.getuid()}")
    os.environ.setdefault("NIRI_SOCKET", niri_socket())
    if args.step == "complete":
        trust(args.artifacts)
        complete(args.artifacts, args.original)
    elif args.step == "approval":
        approval(args.artifacts, args.original)
    elif args.step == "question":
        question(args.artifacts, args.original)
    elif args.step == "rest":
        rest(args.artifacts, args.original)
    elif args.step == "parallel":
        resumed = sorted(args.artifacts.parent.glob("resume-*"))[-1]
        parallel(args.artifacts, resumed, args.artifacts.parent / "second", args.original)
