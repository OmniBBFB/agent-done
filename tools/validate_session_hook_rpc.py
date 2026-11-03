#!/usr/bin/env python3
"""Verify per-thread hook arguments against the existing daemon, then clean up."""

import http.server
import json
import os
from pathlib import Path
import shlex
import sys
import tempfile
import threading
import time

from _probe_rpc import ProbeRPC


def main():
    directory = Path(tempfile.mkdtemp(prefix="agent-done-rpc-hook-"))
    context = {"launcher_pid": os.getpid(), "tty": os.readlink("/proc/self/fd/0"),
               "marker": directory.name, "identity_kind": "RPC probe process, not a desktop TUI"}
    (directory / "launcher.json").write_text(json.dumps(context))
    requests = []

    class Guard(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            requests.append({"method": "GET", "path": self.path})
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"models":[]}')

        def do_POST(self):
            requests.append({"method": "POST", "path": self.path})
            self.send_error(503, "probe: inference disabled")

        def log_message(self, *args):
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Guard)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    recorder = Path(__file__).with_name("validate_launch_hook.py")
    command = shlex.join([sys.executable, str(recorder), "--hook", str(directory)])
    config = {
        "openai_base_url": f"http://127.0.0.1:{server.server_port}/v1",
        "chatgpt_base_url": f"http://127.0.0.1:{server.server_port}",
        "hooks.SessionStart": [{"hooks": [{"type": "command", "command": command, "timeout": 5}]}],
        "bypass_hook_trust": True,
    }
    rpc = ProbeRPC()
    tid = None
    turn_id = None
    report = {"artifacts": str(directory), "probe_process_pid": os.getpid()}
    try:
        response = rpc.request("thread/start", {"cwd": "/home/csc", "config": config,
                                                 "modelProvider": "openai", "historyMode": "legacy"})
        tid = response["thread"]["id"]
        report["thread_id"] = tid
        hooks = rpc.request("hooks/list", {"threadId": tid})
        (directory / "hooks-list.json").write_text(json.dumps(hooks, indent=2))
        report["hook_present_on_daemon"] = str(recorder) in json.dumps(hooks)
        if report["hook_present_on_daemon"]:
            response = rpc.request("turn/start", {"threadId": tid,
                "input": [{"type": "text", "text": "agent-done local hook transport probe"}]})
            turn_id = response["turn"]["id"]
            report["turn_id"] = turn_id
            deadline = time.monotonic() + 12
            while time.monotonic() < deadline and not (directory / "hook.json").exists():
                time.sleep(0.1)
            if (directory / "hook.json").exists():
                report["hook"] = json.loads((directory / "hook.json").read_text())
    except Exception as error:
        report["error"] = str(error)
    finally:
        if tid:
            if turn_id:
                try:
                    rpc.request("turn/interrupt", {"threadId": tid, "turnId": turn_id})
                    report["interrupt_requested"] = True
                except Exception as error:
                    report["interrupt_result"] = str(error)
            try:
                rpc.request("thread/archive", {"threadId": tid})
                report["test_thread_archived"] = True
            except Exception as error:
                report["cleanup_error"] = str(error)
        rpc.close()
        server.shutdown()
    report["local_provider_requests"] = requests
    (directory / "result.json").write_text(json.dumps(report, indent=2))
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
