#!/usr/bin/env python3
"""Real-desktop acceptance helpers for the installed production launcher.

The bridge forwards terminal keyboard input and terminal replies through a PTY.
Automation writes only the same terminal input, never turn/start RPC calls.
Raw test terminal output stays in a private temporary directory.
"""

import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import select
import signal
import subprocess
import sys
import termios
import time
import tty

from normal_interaction_probe import append, input_data, submit, transcript, write_json
from terminal_launcher import desktop_environment, terminal_command

ROOT = Path(__file__).resolve().parent.parent
BIN = Path.home() / ".local/bin/agent-done"
PYTHON = ROOT / ".venv/bin/python"


def terminal(artifacts, resume=None):
    args = [str(BIN), "codex", "--no-alt-screen", "-C", str(Path.home())]
    if resume:
        args += ["resume", resume]
    else:
        args += ["-a", "on-request", "-c", 'approvals_reviewer="user"']
    pid, master = pty.fork()
    if not pid:
        write_json(artifacts / "launcher.json", {"owner_pid": os.getpid(), "tty": os.ttyname(0)})
        os.execv(str(BIN), args)

    def resize(*unused):
        fcntl.ioctl(master, termios.TIOCSWINSZ, fcntl.ioctl(0, termios.TIOCGWINSZ, b"\0" * 8))

    resize()
    signal.signal(signal.SIGWINCH, resize)
    previous = termios.tcgetattr(0)
    tty.setraw(0)
    offset = 0
    status = None
    try:
        with (artifacts / "tui.raw").open("ab", buffering=0) as log:
            while True:
                ready, _, _ = select.select([0, master], [], [], 0.1)
                if 0 in ready:
                    data = os.read(0, 65536)
                    if data:
                        os.write(master, data)
                if master in ready:
                    try:
                        data = os.read(master, 65536)
                    except OSError:
                        break
                    if not data:
                        break
                    log.write(data)
                    os.write(1, data)
                controls = artifacts / "input.jsonl"
                if controls.exists():
                    with controls.open() as stream:
                        stream.seek(offset)
                        for line in stream:
                            os.write(master, json.loads(line)["data"].encode())
                        offset = stream.tell()
                done, status = os.waitpid(pid, os.WNOHANG)
                if done:
                    break
                status = None
    finally:
        termios.tcsetattr(0, termios.TCSADRAIN, previous)
        os.close(master)
        if status is None:
            _, status = os.waitpid(pid, 0)
        write_json(artifacts / "terminal-exit.json", {"exit_code": os.waitstatus_to_exitcode(status)})


def start(artifacts, resume=None, terminal_name='kitty'):
    artifacts.mkdir(parents=True, mode=0o700)
    args = terminal_command(terminal_name, 'agent-done-acceptance', 'agent-done acceptance',
                            [PYTHON, Path(__file__).resolve(), 'terminal', artifacts])
    if resume:
        args += ["--resume", resume]
    env = desktop_environment()
    with (artifacts / f"{terminal_name}.log").open("w") as log:
        process = subprocess.Popen(args, env=env, stdout=subprocess.DEVNULL, stderr=log, start_new_session=True)
    write_json(artifacts / "window.json", {"terminal": terminal_name, "terminal_pid": process.pid})
    print(json.dumps({"artifacts": str(artifacts), "terminal": terminal_name, "terminal_pid": process.pid}), flush=True)


def status(artifacts):
    from agent_done.ipc import request
    snapshot = request({"op": "diagnostics"})
    owner = json.loads((artifacts / "launcher.json").read_text())["owner_pid"]
    sessions = [s for s in snapshot["snapshot"]["sessions"]
                if any(v["owner_pid"] == owner for v in s["views"])]
    return {"pending_count": snapshot["snapshot"]["pending_count"],
            "focused_window": snapshot["snapshot"]["focused_window"],
            "niri_connected": snapshot["snapshot"]["niri_connected"],
            "subscribers": snapshot.get("subscribers"), "sessions": sessions,
            "hooks": [h for h in snapshot["hooks"] if h["session"] in {s["id"] for s in sessions}]}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("start", "terminal", "send", "key", "read", "status"))
    parser.add_argument("artifacts", type=Path)
    parser.add_argument("text", nargs="?")
    parser.add_argument("--resume")
    parser.add_argument("--terminal", choices=('kitty', 'alacritty'), default='kitty')
    args = parser.parse_args()
    if args.action == "terminal":
        terminal(args.artifacts, args.resume)
    elif args.action == "start":
        start(args.artifacts, args.resume, args.terminal)
    elif args.action == "send":
        submit(args.artifacts, args.text)
    elif args.action == "key":
        input_data(args.artifacts, args.text)
    elif args.action == "read":
        print(transcript(args.artifacts)[-10000:])
    elif args.action == "status":
        print(json.dumps(status(args.artifacts), ensure_ascii=False, indent=2))
