#!/usr/bin/env python3
"""Exercise a temporary SessionStart hook in a real Codex TUI under a PTY.

An optional test message targets a local rejecting provider, never real inference.
Artifacts stay in /tmp. The existing daemon is used; installed config is unchanged.
"""

import argparse
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import signal
import socket
import struct
import sys
import tempfile
import termios
import time


SCRIPT = Path(__file__).resolve()
NATIVE = "/usr/local/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex"


def workspace(directory):
    return Path((directory / "workspace-path").read_text())


def record_hook(directory):
    payload = json.load(sys.stdin)
    context = json.loads((directory / "launcher.json").read_text())
    context.update({
        "session_id": payload.get("session_id"),
        "event": payload.get("hook_event_name"),
        "source": payload.get("source"),
        "hook_pid": os.getpid(),
        "hook_parent_pid": os.getppid(),
        "hook_stdin": os.readlink("/proc/self/fd/0"),
        "environment_marker": os.environ.get("AGENT_DONE_PROBE_MARKER"),
    })
    (directory / "hook.json").write_text(json.dumps(context, indent=2))
    print(json.dumps({"continue": False, "stopReason": "agent-done launch probe completed"}))


def launch(directory, bypass, hook_source, trigger):
    # exec preserves this PID, so the recorded launcher PID becomes the TUI PID.
    (directory / "launcher.json").write_text(json.dumps({
        "launcher_pid": os.getpid(),
        "tty": os.ttyname(0),
        "marker": directory.name,
        "proc_start_ticks": Path("/proc/self/stat").read_text().rsplit(")", 1)[1].split()[19],
    }))
    command = shlex.join([sys.executable, str(SCRIPT), "--hook", str(directory)])
    hook_value = '[{hooks=[{type="command",command=' + json.dumps(command) + ',timeout=5}]}]'
    args = [NATIVE, "--no-alt-screen", "-C", str(workspace(directory)),
            "--remote", "unix://" + str(directory / "proxy.sock"),
            "-c", "check_for_update_on_startup=false",
            "-c", 'projects.' + json.dumps(str(workspace(directory))) + '.trust_level="trusted"',
            "-c", "shell_environment_policy.set.AGENT_DONE_PROBE_MARKER=" + json.dumps(directory.name)]
    if hook_source == "cli":
        args += ["-c", "hooks.SessionStart=" + hook_value]
    else:
        config = workspace(directory) / ".codex/config.toml"
        config.parent.mkdir()
        config.write_text("[hooks]\nSessionStart=" + hook_value + "\n")
    if bypass:
        args.append("--dangerously-bypass-hook-trust")
    os.environ.update(TERM="xterm-256color", COLORTERM="truecolor")
    os.execv(NATIVE, args)


def exercise(bypass, hook_source, trigger, inject_hook, trigger_via_rpc):
    directory = Path(tempfile.mkdtemp(prefix="agent-done-hook-"))
    target = Path("/home/csc") if hook_source == "cli" else SCRIPT.parent.parent / ".probe-workspaces" / directory.name
    (directory / "workspace-path").write_text(str(target))
    workspace(directory).mkdir(parents=True, exist_ok=True)
    server = None
    provider_requests = []
    rpc_requests = []
    rpc_responses = []
    proxy_errors = []
    import threading

    # Inspect the real TUI's requests; add only a local provider guard when needed.
    listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    listener.bind(str(directory / "proxy.sock"))
    listener.listen(1)

    def proxy():
        try:
            client, _ = listener.accept()
            upstream = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            upstream.connect("/home/csc/.codex/app-server-control/app-server-control.sock")
            client_file = client.makefile("rb")
            upstream_file = upstream.makefile("rb")

            def headers(stream):
                result = []
                while True:
                    line = stream.readline()
                    if not line:
                        raise EOFError()
                    result.append(line)
                    if line == b"\r\n":
                        return result

            # The daemon's Unix transport is WebSocket, not newline JSON.
            # Negotiate no compression to keep this test adapter transparent.
            request_headers = headers(client_file)
            upstream.sendall(b"".join(line for line in request_headers
                                      if not line.lower().startswith(b"sec-websocket-extensions:")))
            client.sendall(b"".join(headers(upstream_file)))
            pending = {}

            def read_frame(stream):
                header = stream.read(2)
                if not header:
                    return None
                if len(header) != 2:
                    raise EOFError()
                first, second = header
                length = second & 127
                if length == 126:
                    length = struct.unpack("!H", stream.read(2))[0]
                elif length == 127:
                    length = struct.unpack("!Q", stream.read(8))[0]
                if length > 32 * 1024 * 1024 or not first & 128:
                    raise ValueError("unsupported probe WebSocket frame")
                mask = stream.read(4) if second & 128 else b""
                payload = stream.read(length)
                if len(payload) != length:
                    raise EOFError()
                if mask:
                    payload = bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
                return first & 15, bool(mask), payload

            def send_frame(destination, opcode, masked, payload):
                length = len(payload)
                flag = 128 if masked else 0
                header = bytes([128 | opcode])
                if length < 126:
                    header += bytes([flag | length])
                elif length < 65536:
                    header += bytes([flag | 126]) + struct.pack("!H", length)
                else:
                    header += bytes([flag | 127]) + struct.pack("!Q", length)
                if masked:
                    mask = os.urandom(4)
                    header += mask
                    payload = bytes(byte ^ mask[i % 4] for i, byte in enumerate(payload))
                destination.sendall(header + payload)

            def forward(source, destination, requests):
                try:
                    while (frame := read_frame(source)) is not None:
                        opcode, masked, payload = frame
                        if opcode != 1:
                            send_frame(destination, opcode, masked, payload)
                            if opcode == 8:
                                break
                            continue
                        message = json.loads(payload)
                        if requests:
                            method = message.get("method")
                            if "id" in message:
                                pending[message["id"]] = method
                            if method in ("thread/start", "thread/resume"):
                                params = message.get("params", {})
                                original = params.get("config") or {}
                                rpc_requests.append({"method": method, "config_keys": list(original),
                                                     "has_hook_override": any(k == "hooks" or k.startswith("hooks.") for k in original)})
                                if trigger:
                                    port = (directory / "provider-port").read_text().strip()
                                    params["config"] = dict(original, **{
                                        "openai_base_url": "http://127.0.0.1:" + port + "/v1",
                                        "chatgpt_base_url": "http://127.0.0.1:" + port,
                                    })
                                if inject_hook:
                                    command = shlex.join([sys.executable, str(SCRIPT), "--hook", str(directory)])
                                    params.setdefault("config", {})["hooks.SessionStart"] = [
                                        {"hooks": [{"type": "command", "command": command, "timeout": 5}]}]
                        elif "id" in message and pending.get(message["id"]) in ("thread/start", "thread/resume"):
                            result = message.get("result", {})
                            rpc_responses.append({"method": pending[message["id"]],
                                                  "thread_id": result.get("thread", {}).get("id"),
                                                  "error": message.get("error")})
                        send_frame(destination, opcode, masked, json.dumps(message).encode())
                except (OSError, ValueError, EOFError) as error:
                    proxy_errors.append(str(error))
                finally:
                    try:
                        destination.shutdown(socket.SHUT_WR)
                    except OSError:
                        pass

            threading.Thread(target=forward, args=(upstream_file, client, False), daemon=True).start()
            forward(client_file, upstream, True)
        except OSError as error:
            proxy_errors.append(str(error))

    threading.Thread(target=proxy, daemon=True).start()
    if trigger:
        import http.server

        class RejectProvider(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                provider_requests.append(self.path)
                self.send_error(503, "agent-done probe: inference disabled")

            def do_GET(self):
                provider_requests.append(self.path)
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.end_headers()
                self.wfile.write(b'{"models":[]}')

            def log_message(self, *args):
                pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), RejectProvider)
        (directory / "provider-port").write_text(str(server.server_port))
        threading.Thread(target=server.serve_forever, daemon=True).start()
    pid, fd = pty.fork()
    if pid == 0:
        args = [sys.executable, str(SCRIPT), "--child", str(directory), "--hook-source", hook_source]
        if bypass:
            args.append("--bypass-hook-trust")
        if trigger:
            args.append("--trigger-turn")
        os.execv(sys.executable, args)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 140, 0, 0))
    buffer = bytearray()
    status = None
    hook_seen = None
    triggered_turn = None

    def drain(timeout):
        if not select.select([fd], [], [], timeout)[0]:
            return
        try:
            data = os.read(fd, 65536)
        except OSError:
            return
        buffer.extend(data)
        if b"\x1b[6n" in data:
            os.write(fd, b"\x1b[1;1R")
        if b"\x1b[c" in data:
            os.write(fd, b"\x1b[?1;2c")
        if b"\x1b[?u" in data:
            os.write(fd, b"\x1b[?0u")

    try:
        deadline = time.monotonic() + 35
        message_sent = False
        ready_at = None
        while time.monotonic() < deadline:
            drain(0.2)
            if rpc_responses and ready_at is None:
                ready_at = time.monotonic()
            if trigger and not message_sent and ready_at is not None and time.monotonic() - ready_at > 3:
                if trigger_via_rpc:
                    from _probe_rpc import ProbeRPC
                    rpc = ProbeRPC()
                    try:
                        response = rpc.request("turn/start", {
                            "threadId": rpc_responses[-1]["thread_id"],
                            "input": [{"type": "text", "text": "agent-done local hook probe"}],
                        })
                        triggered_turn = response["turn"]["id"]
                    finally:
                        rpc.close()
                else:
                    os.write(fd, b"agent-done local hook probe\r")
                message_sent = True
            if (directory / "hook.json").exists():
                hook_seen = json.loads((directory / "hook.json").read_text())
                break
            done, status_value = os.waitpid(pid, os.WNOHANG)
            if done:
                status = status_value
                break
        # Only this probe's own TUI is stopped; the shared daemon stays running.
        if status is None:
            os.write(fd, b"\x03")
            time.sleep(0.2)
            os.write(fd, b"\x03")
            deadline = time.monotonic() + 4
            while time.monotonic() < deadline:
                drain(0.1)
                done, status_value = os.waitpid(pid, os.WNOHANG)
                if done:
                    status = status_value
                    break
            if status is None:
                os.kill(pid, signal.SIGTERM)
                _, status = os.waitpid(pid, 0)
    finally:
        os.close(fd)
        listener.close()
        if server:
            server.shutdown()
    (directory / "tui.raw").write_bytes(buffer)
    clean = re.sub(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*?(?:\x07|\x1b\\))", "", buffer.decode(errors="replace"))
    (directory / "tui.txt").write_text(clean)
    report = {
        "artifacts": str(directory), "hook_source": hook_source,
        "bypass_hook_trust": bypass, "tui_pid": pid,
        "exit_code": os.waitstatus_to_exitcode(status), "hook": hook_seen,
        "local_provider_requests": provider_requests,
        "rpc_requests": rpc_requests, "rpc_responses": rpc_responses, "proxy_errors": proxy_errors,
        "injected_hook_at_protocol_boundary": inject_hook,
        "trigger_via_control_rpc": trigger_via_rpc, "triggered_turn": triggered_turn,
        "hook_related_output": [line[-700:] for line in clean.splitlines()
                                if any(word in line.lower() for word in ("hook", "error", "trust", "unrecognized"))][-12:],
    }
    # Archive only the fresh threads created by this test connection.
    from _probe_rpc import ProbeRPC
    cleanup = []
    for response in rpc_responses:
        if response.get("method") == "thread/start" and response.get("thread_id"):
            rpc = ProbeRPC()
            try:
                rpc.request("thread/archive", {"threadId": response["thread_id"]})
                cleanup.append({"thread_id": response["thread_id"], "archived": True})
            finally:
                rpc.close()
    report["cleanup"] = cleanup
    (directory / "result.json").write_text(json.dumps(report, indent=2))
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--hook", type=Path)
    parser.add_argument("--child", type=Path)
    parser.add_argument("--bypass-hook-trust", action="store_true")
    parser.add_argument("--hook-source", choices=("cli", "file"), default="cli")
    parser.add_argument("--trigger-turn", action="store_true")
    parser.add_argument("--inject-hook", action="store_true")
    parser.add_argument("--trigger-via-rpc", action="store_true")
    args = parser.parse_args()
    if args.hook:
        record_hook(args.hook)
    elif args.child:
        launch(args.child, args.bypass_hook_trust, args.hook_source, args.trigger_turn)
    else:
        exercise(args.bypass_hook_trust, args.hook_source, args.trigger_turn, args.inject_hook, args.trigger_via_rpc)
