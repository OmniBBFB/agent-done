#!/usr/bin/env python3
"""Desktop acceptance probe: real Alacritty, TUI input, normal model and hooks.

Only this probe's own window is driven. The Unix transport adapter adds the
reviewable test hook configuration and observes session identities. It never
injects turn/start, changes model routing, or bypasses hook trust.
"""

import argparse
import errno
import fcntl
import json
import os
from pathlib import Path
import pty
import re
import select
import shlex
import signal
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import termios
import threading
import time
import tty

PROJECT = Path(__file__).resolve().parent.parent
SCRIPT = Path(__file__).resolve()
STATE = PROJECT / ".probe-runtime"
NATIVE = "/usr/local/lib/node_modules/@openai/codex/node_modules/@openai/codex-linux-x64/vendor/x86_64-unknown-linux-musl/bin/codex"
DAEMON_SOCKET = "/home/csc/.codex/app-server-control/app-server-control.sock"
HOOK_COMMAND = shlex.join(["/usr/bin/python3", str(SCRIPT), "--hook"])
HOOKS = {event: [{"hooks": [{"type": "command", "command": HOOK_COMMAND, "timeout": 5}]}]
         for event in ("SessionStart", "Stop")}


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + "." + str(threading.get_ident()) + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2))
    temporary.replace(path)


def append(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        stream.write(json.dumps(value, ensure_ascii=False) + "\n")


def registry(sid, value=None, close_pid=None):
    STATE.mkdir(exist_ok=True)
    with (STATE / "registry.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        path = STATE / "registry.json"
        data = json.loads(path.read_text()) if path.exists() else {}
        if value is not None:
            data[sid] = value
            write_json(path, data)
        if close_pid is not None and data.get(sid, {}).get("tui_pid") == close_pid:
            data[sid]["active"] = False
            write_json(path, data)
        return data.get(sid)


def niri(command, socket_path):
    env = dict(os.environ, NIRI_SOCKET=socket_path)
    result = subprocess.run(["niri", "msg", "--json", command], env=env,
                            capture_output=True, text=True, timeout=4, check=True)
    return json.loads(result.stdout)


def position(window_id, socket_path):
    window = next((w for w in niri("windows", socket_path) if w["id"] == window_id), None)
    if not window:
        return None
    workspace = next((w for w in niri("workspaces", socket_path)
                      if w["id"] == window.get("workspace_id")), {})
    layout = window.get("layout", {}).get("pos_in_scrolling_layout")
    return {"window_id": window_id, "window_pid": window.get("pid"),
            "workspace_id": window.get("workspace_id"), "workspace_index": workspace.get("idx"),
            "output": workspace.get("output"), "column": layout[0] if layout else None,
            "row": layout[1] if layout else None}


def hook():
    data = json.load(sys.stdin)
    sid = data.get("session_id")
    binding = registry(sid) or {}
    row = {"event": data.get("hook_event_name"), "source": data.get("source"),
           "session_id": sid, "hook_pid": os.getpid(), "hook_parent_pid": os.getppid(),
           "binding": binding, "timestamp": time.time()}
    if binding:
        row["current_position"] = position(binding["window_id"], binding["niri_socket"])
        append(Path(binding["artifacts"]) / "hooks.jsonl", row)
    else:
        append(STATE / "unbound-hooks.jsonl", row)
    print("{}")


def read_frame(stream):
    header = stream.read(2)
    if not header:
        return None
    first, second = header
    size = second & 127
    if size == 126:
        size = struct.unpack("!H", stream.read(2))[0]
    elif size == 127:
        size = struct.unpack("!Q", stream.read(8))[0]
    if size > 32 * 1024 * 1024 or not first & 128:
        raise ValueError("unsupported probe WebSocket frame")
    mask = stream.read(4) if second & 128 else b""
    data = stream.read(size)
    if len(data) != size:
        raise EOFError()
    if mask:
        data = bytes(byte ^ mask[i % 4] for i, byte in enumerate(data))
    return first & 15, bool(mask), data


def send_frame(destination, opcode, masked, data):
    size = len(data)
    flag = 128 if masked else 0
    prefix = bytes([128 | opcode])
    if size < 126:
        prefix += bytes([flag | size])
    elif size < 65536:
        prefix += bytes([flag | 126]) + struct.pack("!H", size)
    else:
        prefix += bytes([flag | 127]) + struct.pack("!Q", size)
    if masked:
        mask = os.urandom(4)
        prefix += mask
        data = bytes(byte ^ mask[i % 4] for i, byte in enumerate(data))
    destination.sendall(prefix + data)


def start_adapter(artifacts):
    listener = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    listener.bind(str(artifacts / "proxy.sock"))
    listener.listen(1)

    def serve():
        binding = None
        bound_sid = None
        observed = set()
        try:
            client, _ = listener.accept()
            upstream = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            upstream.connect(DAEMON_SOCKET)
            incoming, outgoing = client.makefile("rb"), upstream.makefile("rb")

            def headers(stream):
                result = []
                while True:
                    line = stream.readline()
                    if not line:
                        raise EOFError()
                    result.append(line)
                    if line == b"\r\n":
                        return result

            upstream.sendall(b"".join(line for line in headers(incoming)
                                      if not line.lower().startswith(b"sec-websocket-extensions:")))
            client.sendall(b"".join(headers(outgoing)))
            pending = {}

            def bind(sid):
                nonlocal binding, bound_sid
                binding = json.loads((artifacts / "launcher.json").read_text())
                binding.update(session_id=sid, active=True)
                bound_sid = sid
                registry(sid, binding)
                write_json(artifacts / "binding.json", binding)
                append(artifacts / "events.jsonl", {"kind": "binding", "binding": binding})

            def forward(stream, destination, is_request):
                try:
                    while (frame := read_frame(stream)) is not None:
                        opcode, masked, data = frame
                        if opcode != 1:
                            send_frame(destination, opcode, masked, data)
                            if opcode == 8:
                                break
                            continue
                        message = json.loads(data)
                        method = message.get("method")
                        if is_request:
                            if "id" in message:
                                pending[message["id"]] = method
                            append(artifacts / "events.jsonl", {"kind": "client_method", "method": method})
                            if method in ("thread/start", "thread/resume"):
                                params = message["params"]
                                ephemeral = params.get("ephemeral") is True
                                append(artifacts / "events.jsonl", {"kind": "session_request", "method": method,
                                       "ephemeral": ephemeral, "service_name": params.get("serviceName"),
                                       "thread_source": params.get("threadSource")})
                                if ephemeral:
                                    pending[message.get("id")] = "ignored_ephemeral"
                                    send_frame(destination, opcode, masked, json.dumps(message).encode())
                                    continue
                                config = params.get("config") or {}
                                for event, groups in HOOKS.items():
                                    config["hooks." + event] = groups
                                params["config"] = config
                                if method == "thread/resume":
                                    bind(params["threadId"])
                        else:
                            if "error" in message:
                                append(artifacts / "events.jsonl", {"kind": "rpc_error", "error": message["error"]})
                            if "id" in message and pending.get(message["id"]) in ("thread/start", "thread/resume"):
                                sid = message.get("result", {}).get("thread", {}).get("id")
                                if sid:
                                    bind(sid)
                                    observed.add(sid)
                            params = message.get("params", {})
                            if method == "item/completed" and params.get("item", {}).get("type") == "agentMessage":
                                append(artifacts / "events.jsonl", {"kind": "assistant", "thread_id": params.get("threadId"),
                                       "turn_id": params.get("turnId"), "item": params["item"]})
                            if method in ("turn/started", "turn/completed", "error"):
                                append(artifacts / "events.jsonl", {"kind": method, "params": params})
                        send_frame(destination, opcode, masked, json.dumps(message).encode())
                except (OSError, ValueError, EOFError) as error:
                    if str(error):
                        append(artifacts / "events.jsonl", {"kind": "adapter_error", "message": str(error)})
                finally:
                    try:
                        destination.shutdown(socket.SHUT_WR)
                    except OSError:
                        pass

            threading.Thread(target=forward, args=(outgoing, client, False), daemon=True).start()
            forward(incoming, upstream, True)
        finally:
            if binding:
                for sid in observed:
                    registry(sid, close_pid=binding["tui_pid"])
            listener.close()

    threading.Thread(target=serve, daemon=True).start()


def terminal(artifacts, resume):
    artifacts.mkdir(parents=True, exist_ok=True)
    socket_path = os.environ["NIRI_SOCKET"]
    alacritty_pid = os.getppid()
    window = None
    for _ in range(60):
        window = next((w for w in niri("windows", socket_path) if w.get("pid") == alacritty_pid), None)
        if window:
            break
        time.sleep(0.1)
    if not window:
        raise RuntimeError("probe terminal window not found")
    metadata = {"window_id": window["id"], "alacritty_pid": alacritty_pid,
                "niri_socket": socket_path, "artifacts": str(artifacts),
                "initial_position": position(window["id"], socket_path)}
    command_value = '[{hooks=[{type="command",command=' + json.dumps(HOOK_COMMAND) + ',timeout=5}]}]'
    args = [NATIVE, "--no-alt-screen", "--remote", "unix://" + str(artifacts / "proxy.sock"), "-C", "/home/csc"]
    for event in HOOKS:
        args += ["-c", "hooks." + event + "=" + command_value]
    if resume:
        args += ["resume", resume]
    # Prepare the listener before the child connects, without forking threads.
    pid, master = pty.fork()
    if pid == 0:
        metadata.update(tui_pid=os.getpid(), tty=os.ttyname(0),
                        proc_start_ticks=Path("/proc/self/stat").read_text().rsplit(")", 1)[1].split()[19])
        write_json(artifacts / "launcher.json", metadata)
        while not (artifacts / "proxy.sock").exists():
            time.sleep(0.02)
        for attempt in range(20):
            try:
                os.execv(NATIVE, args)
            except OSError as error:
                if error.errno != errno.ETXTBSY or attempt == 19:
                    raise
                time.sleep(0.5)
    start_adapter(artifacts)

    def resize(*unused):
        size = fcntl.ioctl(0, termios.TIOCGWINSZ, b"\0" * 8)
        fcntl.ioctl(master, termios.TIOCSWINSZ, size)

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
                            action = json.loads(line)
                            os.write(master, action["data"].encode())
                        offset = stream.tell()
                done, status_value = os.waitpid(pid, os.WNOHANG)
                if done:
                    status = status_value
                    break
    finally:
        termios.tcsetattr(0, termios.TCSADRAIN, previous)
        os.close(master)
        if status is None:
            _, status = os.waitpid(pid, 0)
        for event in lines(artifacts / "events.jsonl"):
            if event.get("kind") == "binding":
                registry(event["binding"]["session_id"], close_pid=pid)
        write_json(artifacts / "terminal-exit.json", {"tui_pid": pid, "exit_code": os.waitstatus_to_exitcode(status)})


def lines(path):
    if not path.exists():
        return []
    return [json.loads(line) for line in path.read_text().splitlines() if line]


def transcript(artifacts):
    path = artifacts / "tui.raw"
    text = path.read_bytes().decode(errors="replace") if path.exists() else ""
    return re.sub(r"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*?(?:\x07|\x1b\\))", "", text)


def input_data(artifacts, text):
    append(artifacts / "input.jsonl", {"data": text})


def submit(artifacts, text):
    input_data(artifacts, "\x1b[200~" + text + "\x1b[201~")
    time.sleep(1)
    input_data(artifacts, "\r")


def wait_for(predicate, artifacts, timeout=90):
    deadline = time.monotonic() + timeout
    trust_confirmed = False
    while time.monotonic() < deadline:
        if predicate():
            return
        text = transcript(artifacts)
        if "Hooks need review" in text and not trust_confirmed:
            # This isolated launch defines only our two already-reviewed hooks.
            input_data(artifacts, "2")
            time.sleep(0.3)
            input_data(artifacts, "\r")
            trust_confirmed = True
            append(artifacts / "driver.jsonl", {"action": "normal_hook_trust_confirmation"})
        if (artifacts / "terminal-exit.json").exists():
            raise RuntimeError("test terminal exited before expected state")
        time.sleep(0.2)
    raise TimeoutError("desktop interaction timed out; inspect " + str(artifacts))


def run(inspect_ui=False):
    STATE.mkdir(exist_ok=True)
    root = Path(tempfile.mkdtemp(prefix="agent-done-normal-"))
    report = {"artifacts": str(root), "input_path": "real Alacritty -> PTY -> Codex TUI",
              "control_rpc_trigger": False, "bypass_hook_trust": False, "model_routing_changed": False,
              "rounds": []}
    sockets = sorted(Path("/run/user/1000").glob("niri.*.sock"))
    socket_path = str(sockets[-1])
    env = dict(os.environ, NIRI_SOCKET=socket_path, XDG_RUNTIME_DIR="/run/user/1000", WAYLAND_DISPLAY="wayland-1")
    original_focus = next((w["id"] for w in niri("windows", socket_path) if w.get("is_focused")), None)
    sessions = set()
    processes = []
    try:
        resume = None
        for index, markers in enumerate((("AGENT_DONE_NORMAL_OK", "AGENT_DONE_SECOND_OK"), ("AGENT_DONE_RESUME_OK",))):
            artifacts = root / ("new" if index == 0 else "resume")
            artifacts.mkdir()
            args = ["alacritty", "--class", "agent-done-normal-test", "--title", "agent-done normal interaction test",
                    "-e", "/usr/bin/python3", str(SCRIPT), "--terminal", str(artifacts)]
            if resume:
                args += ["--resume", resume]
            process = subprocess.Popen(args, env=env, stdout=subprocess.DEVNULL, stderr=(artifacts / "alacritty.log").open("w"))
            processes.append(process)
            print(json.dumps({"stage": "window_opened", "artifacts": str(artifacts), "alacritty_pid": process.pid}), flush=True)
            wait_for(lambda: (artifacts / "binding.json").exists(), artifacts)
            binding = json.loads((artifacts / "binding.json").read_text())
            sessions.add(binding["session_id"])
            if resume and resume != binding["session_id"]:
                raise AssertionError("resume changed the session id")
            report["rounds"].append({"mode": "new" if not resume else "resume", "binding": binding, "replies": []})
            print(json.dumps({"stage": "bound", "binding": binding}), flush=True)
            time.sleep(3)
            if inspect_ui and index == 0:
                submit(artifacts, "/hooks")
                print(json.dumps({"stage": "inspect_hooks_ui", "artifacts": str(artifacts),
                                  "continue_file": str(root / "continue")}), flush=True)
                deadline = time.monotonic() + 180
                while time.monotonic() < deadline and not (root / "continue").exists():
                    time.sleep(0.2)
                if not (root / "continue").exists():
                    raise TimeoutError("hook UI inspection timed out")
            for marker in markers:
                sid = binding["session_id"]
                def main_completions():
                    return [e for e in lines(artifacts / "events.jsonl")
                            if e.get("kind") == "turn/completed" and e.get("params", {}).get("threadId") == sid]
                before = len(main_completions())
                before_stops = sum(h.get("event") == "Stop" and h.get("session_id") == sid
                                   for h in lines(artifacts / "hooks.jsonl"))
                submit(artifacts, "这是 agent-done 的交互连接测试。请只回复 " + marker + "，不要调用工具或读取文件。")
                wait_for(lambda: len(main_completions()) > before, artifacts, timeout=150)
                if main_completions()[-1]["params"]["turn"]["status"] != "completed":
                    raise AssertionError("main conversation turn did not complete successfully")
                replies = [e["item"].get("text", "") for e in lines(artifacts / "events.jsonl")
                           if e.get("kind") == "assistant" and e.get("thread_id") == sid]
                if not any(marker in text for text in replies):
                    raise AssertionError("completed without expected assistant reply: " + marker)
                wait_for(lambda: sum(h.get("event") == "Stop" and h.get("session_id") == sid
                                     for h in lines(artifacts / "hooks.jsonl")) > before_stops, artifacts, timeout=10)
                hooks = lines(artifacts / "hooks.jsonl")
                report["rounds"][-1]["replies"].append(marker)
                print(json.dumps({"stage": "reply_verified", "marker": marker, "hook_count": len(hooks)}), flush=True)
            report["rounds"][-1]["hooks"] = lines(artifacts / "hooks.jsonl")
            resume = binding["session_id"]
            submit(artifacts, "/quit")
            process.wait(timeout=20)
            time.sleep(1)
            report["rounds"][-1]["binding_after_close"] = registry(resume)
            if report["rounds"][-1]["binding_after_close"].get("active"):
                raise AssertionError("closed terminal binding remained active")
            report["rounds"][-1]["window_after_close"] = position(binding["window_id"], socket_path)
            if report["rounds"][-1]["window_after_close"] is not None:
                raise AssertionError("test window remained after normal quit")
        report["passed"] = True
    except Exception as error:
        report["passed"] = False
        report["error"] = str(error)
    finally:
        for process in processes:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()
        # Control RPC is used only for cleanup, never to trigger a test turn.
        from _probe_rpc import ProbeRPC
        for sid in sessions:
            rpc = ProbeRPC()
            try:
                rpc.request("thread/archive", {"threadId": sid})
            finally:
                rpc.close()
        if original_focus:
            subprocess.run(["niri", "msg", "action", "focus-window", "--id", str(original_focus)], env=env, capture_output=True)
    write_json(root / "result.json", report)
    print(json.dumps(report, ensure_ascii=False, indent=2), flush=True)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--terminal", type=Path)
    parser.add_argument("--resume")
    parser.add_argument("--hook", action="store_true")
    parser.add_argument("--inspect-environment", action="store_true")
    parser.add_argument("--inspect-ui", action="store_true")
    args = parser.parse_args()
    if args.inspect_environment:
        print("codex_command", shutil.which("codex"))
        for proc in Path("/proc").iterdir():
            if not proc.name.isdigit():
                continue
            try:
                if proc.joinpath("comm").read_text().strip() == "codex":
                    stat = proc.joinpath("stat").read_text().rsplit(")", 1)[1].split()
                    if int(stat[4]):
                        print(json.dumps({"pid": int(proc.name), "exe": os.readlink(proc / "exe")}))
            except OSError:
                pass
    elif args.hook:
        hook()
    elif args.terminal:
        terminal(args.terminal, args.resume)
    else:
        run(args.inspect_ui)
